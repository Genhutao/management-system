package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 发送是站内信唯一会写库的入口，也是矩阵唯一真正生效的地方：
// 选人列表只是"少给你看几个人"，拦不住直接 POST 一个别人账号的 id。

func setupMessageSendDB(t *testing.T) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Message{}, &model.OperationLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	prev := repository.DB
	repository.DB = db
	t.Cleanup(func() {
		repository.DB = prev
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
}

func seedMessageUser(t *testing.T, username, realName, role, dept, building, status string) uint {
	t.Helper()
	user := model.User{
		Username: username, RealName: realName, Role: role,
		Department: dept, Building: building, Status: status, Position: model.PositionMember,
		Phone: "13800001111",
	}
	if err := repository.DB.Create(&user).Error; err != nil {
		t.Fatalf("写入账号失败: %v", err)
	}
	return user.ID
}

func sendRequest(uid uint, payload map[string]interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/messages", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", uid)
	return c, w
}

func decodeSend(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v / %s", err, w.Body.String())
	}
	return body
}

func countMessages(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := repository.DB.Model(&model.Message{}).Count(&n).Error; err != nil {
		t.Fatalf("统计站内信条数失败: %v", err)
	}
	return n
}

// 选人列表严格等于矩阵：自己、跨部门、停用、别的角色都不该出现，且不带手机号。
func TestContactsFollowMatrix(t *testing.T) {
	setupMessageSendDB(t)
	me := seedMessageUser(t, "m1", "部员甲", model.RoleMember, "纪检部", "", "active")
	seedMessageUser(t, "m2", "部员乙", model.RoleMember, "纪检部", "", "active")
	seedMessageUser(t, "m3", "部长丙", model.RoleMinister, "纪检部", "", "active")
	seedMessageUser(t, "m4", "别部门员", model.RoleMember, "组织部", "", "active")
	seedMessageUser(t, "m5", "停用部长", model.RoleMinister, "纪检部", "", "disabled")
	seedMessageUser(t, "m6", "宿管丁", model.RoleDormManager, "", "7号楼", "active")

	ctrl := &MessageController{}
	c, w := messageRequest(me, http.MethodGet, "/api/v1/messages/contacts", "")
	ctrl.Contacts(c)
	if w.Code != http.StatusOK {
		t.Fatalf("选人列表应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	body := decodeSend(t, w)
	if body["can_send"] != true {
		t.Errorf("有可发对象时 can_send 应为 true，实际 %v", body["can_send"])
	}
	items, _ := body["items"].([]interface{})
	got := map[string]bool{}
	for _, item := range items {
		row, _ := item.(map[string]interface{})
		name, _ := row["real_name"].(string)
		got[name] = true
		if _, ok := row["phone"]; ok {
			t.Fatalf("选人列表回带了 phone 字段: %v", row)
		}
	}
	want := map[string]bool{"部员乙": true, "部长丙": true}
	if len(got) != len(want) {
		t.Fatalf("可发对象应恰为 %v，实际 %v", want, got)
	}
	for name := range want {
		if !got[name] {
			t.Errorf("缺少可发对象 %q，实际 %v", name, got)
		}
	}
	if strings.Contains(w.Body.String(), "13800001111") {
		t.Errorf("响应里出现了手机号")
	}
	// 自己不参与（矩阵拒自发），前端也就不会在列表里看到"发给自己"
	if _, ok := got["部员甲"]; ok {
		t.Error("选人列表不应包含自己")
	}
}

// 查看下载岗只收不发：拿到 200 + 空列表 + can_send=false，而不是一个让人误判系统坏了的 403。
func TestContactsForReceiveOnlyRole(t *testing.T) {
	setupMessageSendDB(t)
	viewer := seedMessageUser(t, "v1", "查看岗戊", model.RoleViewerExport, "", "", "active")
	seedMessageUser(t, "v2", "部长己", model.RoleMinister, "纪检部", "", "active")

	ctrl := &MessageController{}
	c, w := messageRequest(viewer, http.MethodGet, "/api/v1/messages/contacts", "")
	ctrl.Contacts(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200 空列表，实际 %d: %s", w.Code, w.Body.String())
	}
	body := decodeSend(t, w)
	if body["can_send"] != false {
		t.Errorf("只收不发的身份 can_send 应为 false，实际 %v", body["can_send"])
	}
	items, _ := body["items"].([]interface{})
	if len(items) != 0 {
		t.Errorf("只收不发的身份不该有可发对象，实际 %v", items)
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("空列表必须编码成 []，实际 %s", w.Body.String())
	}
}

// 发给两个人就落两行，各自的收件人快照独立，且都是未读。
func TestSendFansOutOneRowPerRecipient(t *testing.T) {
	setupMessageSendDB(t)
	from := seedMessageUser(t, "s1", "部长甲", model.RoleMinister, "纪检部", "", "active")
	to1 := seedMessageUser(t, "s2", "部员乙", model.RoleMember, "纪检部", "", "active")
	to2 := seedMessageUser(t, "s3", "部员丙", model.RoleMember, "组织部", "", "active")

	ctrl := &MessageController{}
	c, w := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{to1, to2}, "title": "查寝时段调整", "body": "今晚改为 19:30，请通知本部人员",
	})
	ctrl.Send(c)
	if w.Code != http.StatusOK {
		t.Fatalf("部长发全校应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := decodeSend(t, w)
	if body["sent"] != float64(2) {
		t.Fatalf("sent 应为 2，实际 %v", body["sent"])
	}
	if n := countMessages(t); n != 2 {
		t.Fatalf("应落 2 行，实际 %d", n)
	}

	var rows []model.Message
	repository.DB.Order("recipient_id asc").Find(&rows)
	if rows[0].RecipientID != to1 || rows[1].RecipientID != to2 {
		t.Fatalf("收件人不符: %+v", rows)
	}
	for _, row := range rows {
		if row.SenderName != "部长甲" || row.RecipientName == "" {
			t.Errorf("姓名快照缺失: %+v", row)
		}
		if row.Kind != model.MessageKindHuman {
			t.Errorf("落库 kind 应为 human，实际 %q", row.Kind)
		}
		if row.ReadAt != nil {
			t.Errorf("新信应为未读，实际 %v", *row.ReadAt)
		}
		if row.Title != "查寝时段调整" {
			t.Errorf("标题未原样落库: %q", row.Title)
		}
	}
}

// 混进一个发不了的对象就整封不发。半途成功的"已发送"是谎话。
func TestSendIsAllOrNothing(t *testing.T) {
	setupMessageSendDB(t)
	from := seedMessageUser(t, "a1", "部员甲", model.RoleMember, "纪检部", "", "active")
	ok := seedMessageUser(t, "a2", "部长乙", model.RoleMinister, "纪检部", "", "active")
	forbidden := seedMessageUser(t, "a3", "别部同志", model.RoleMember, "组织部", "", "active")

	ctrl := &MessageController{}
	c, w := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{ok, forbidden}, "title": "测试", "body": "正文",
	})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("含不可发对象应整封拒发，实际 %d: %s", w.Code, w.Body.String())
	}
	if n := countMessages(t); n != 0 {
		t.Fatalf("被拒时不得留下任何半封信，实际库里 %d 条", n)
	}
	body := decodeSend(t, w)
	rejected, _ := body["rejected_recipient_ids"].([]interface{})
	if len(rejected) != 1 || rejected[0] != float64(forbidden) {
		t.Errorf("应只回带被拒的那个 id，实际 %v", rejected)
	}

	// 审计也不该留下"发过但失败"的记录（业务未发生）
	var logs int64
	repository.DB.Model(&model.OperationLog{}).Where("action = ?", "message.send").Count(&logs)
	if logs != 0 {
		t.Errorf("发信失败不应写成功留痕，实际 %d 条", logs)
	}
}

// "不存在"与"你发不了"必须给同一句话，否则这个接口能枚举全校账号编号。
func TestSendDoesNotLeakAccountExistence(t *testing.T) {
	setupMessageSendDB(t)
	from := seedMessageUser(t, "e1", "部员甲", model.RoleMember, "纪检部", "", "active")
	dormOther := seedMessageUser(t, "e2", "宿管乙", model.RoleDormManager, "", "7号楼", "active")

	ctrl := &MessageController{}
	cAbsent, wAbsent := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{987654}, "title": "测试", "body": "正文",
	})
	ctrl.Send(cAbsent)

	cForbidden, wForbidden := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{dormOther}, "title": "测试", "body": "正文",
	})
	ctrl.Send(cForbidden)

	if wAbsent.Code != http.StatusBadRequest || wForbidden.Code != http.StatusBadRequest {
		t.Fatalf("两种情况都应 400，实际 %d / %d", wAbsent.Code, wForbidden.Code)
	}
	msgAbsent, _ := decodeSend(t, wAbsent)["error"].(string)
	msgForbidden, _ := decodeSend(t, wForbidden)["error"].(string)
	if msgAbsent != msgForbidden {
		t.Fatalf("不存在与不可发的报文必须一致，实际 %q vs %q", msgAbsent, msgForbidden)
	}
	if strings.Contains(msgAbsent, "不存在") || strings.Contains(msgAbsent, "没有权限") {
		t.Errorf("报文中出现了区分两种情况的措辞: %q", msgAbsent)
	}
}

// kind 由服务端写：客户端提交 system_cc 一律拒绝，否则谁都能自造一条"系统抄送"。
func TestSendRejectsClientSuppliedKind(t *testing.T) {
	setupMessageSendDB(t)
	from := seedMessageUser(t, "k1", "部长甲", model.RoleMinister, "纪检部", "", "active")
	to := seedMessageUser(t, "k2", "部员乙", model.RoleMember, "纪检部", "", "active")

	ctrl := &MessageController{}
	c, w := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{to}, "title": "标题", "body": "正文", "kind": model.MessageKindSystemCC,
	})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("客户端指定 kind 应被拒，实际 %d: %s", w.Code, w.Body.String())
	}
	if n := countMessages(t); n != 0 {
		t.Fatalf("被拒的请求不该落库，实际 %d 条", n)
	}

	// 显式提交 human 是合法的（与缺省等价），别把兼容写法判成错误
	c, w = sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{to}, "title": "标题", "body": "正文", "kind": model.MessageKindHuman,
	})
	ctrl.Send(c)
	if w.Code != http.StatusOK {
		t.Fatalf("kind=human 应可发送，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 标题/正文的空值与长度：长度按字符数算，中文一个字不该按三字节计。
func TestSendValidatesTitleAndBody(t *testing.T) {
	setupMessageSendDB(t)
	from := seedMessageUser(t, "l1", "部长甲", model.RoleMinister, "纪检部", "", "active")
	to := seedMessageUser(t, "l2", "部员乙", model.RoleMember, "纪检部", "", "active")
	ctrl := &MessageController{}

	cases := []struct {
		name  string
		title string
		body  string
	}{
		{"空标题", "", "正文"},
		{"纯空白标题", "   \t ", "正文"},
		{"超长标题", strings.Repeat("违", messageTitleMaxRunes+1), "正文"},
		{"空正文", "标题", ""},
		{"纯空白正文", "标题", "  \n "},
		{"超长正文", "标题", strings.Repeat("字", messageBodyMaxRunes+1)},
	}
	for _, tc := range cases {
		c, w := sendRequest(from, map[string]interface{}{
			"recipient_ids": []uint{to}, "title": tc.title, "body": tc.body,
		})
		ctrl.Send(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s 应判 400，实际 %d: %s", tc.name, w.Code, w.Body.String())
		}
	}
	if n := countMessages(t); n != 0 {
		t.Fatalf("校验失败的一律不该落库，实际 %d 条", n)
	}

	// 边界：恰好 40 个汉字的标题必须通过（曾经按字节卡会把正常话判超长）
	c, w := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{to}, "title": strings.Repeat("违", messageTitleMaxRunes), "body": "正文",
	})
	ctrl.Send(c)
	if w.Code != http.StatusOK {
		t.Fatalf("恰好到字数上限的标题应可发送，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 收件人重复提交只发一份；0 占位值忽略；只有 0 等同没选人。
func TestSendDedupesAndIgnoresZeroIDs(t *testing.T) {
	setupMessageSendDB(t)
	from := seedMessageUser(t, "d1", "部长甲", model.RoleMinister, "纪检部", "", "active")
	to := seedMessageUser(t, "d2", "部员乙", model.RoleMember, "纪检部", "", "active")
	ctrl := &MessageController{}

	c, w := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{to, to, 0, to}, "title": "标题", "body": "正文",
	})
	ctrl.Send(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if n := countMessages(t); n != 1 {
		t.Fatalf("重复收件人应只落一行，实际 %d", n)
	}

	c, w = sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{0}, "title": "标题", "body": "正文",
	})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("只剩占位值应判 400，实际 %d: %s", w.Code, w.Body.String())
	}

	c, w = sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{from}, "title": "标题", "body": "正文",
	})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("不能发给自己，实际 %d: %s", w.Code, w.Body.String())
	}

	c, w = sendRequest(from, map[string]interface{}{"title": "标题", "body": "正文"})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺收件人应 400，实际 %d: %s", w.Code, w.Body.String())
	}

	// 超出单次上限：按"群发性通知应走公告"引导，而不是默默发一半
	many := make([]uint, 0, messageRecipientsMaxPerSend+1)
	for i := 0; i <= messageRecipientsMaxPerSend; i++ {
		many = append(many, uint(10000+i))
	}
	c, w = sendRequest(from, map[string]interface{}{"recipient_ids": many, "title": "标题", "body": "正文"})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("超过单次人数上限应 400，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 留痕记"谁发给谁、标题是什么"，不记正文：正文里常有具体学生与事件。
func TestSendWritesAuditWithoutBody(t *testing.T) {
	setupMessageSendDB(t)
	from := seedMessageUser(t, "g1", "部长甲", model.RoleMinister, "纪检部", "", "active")
	to1 := seedMessageUser(t, "g2", "部员乙", model.RoleMember, "纪检部", "", "active")
	to2 := seedMessageUser(t, "g3", "部员丙", model.RoleMinister, "技术组", "", "active")

	ctrl := &MessageController{}
	c, w := sendRequest(from, map[string]interface{}{
		"recipient_ids": []uint{to1, to2}, "title": "违纪名单核对", "body": "张三 高一(3)班 扣 5 分，明细见附件",
	})
	ctrl.Send(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", w.Code, w.Body.String())
	}

	var log model.OperationLog
	if err := repository.DB.Where("action = ?", "message.send").First(&log).Error; err != nil {
		t.Fatalf("发信应写审计留痕: %v", err)
	}
	if log.OperatorName != "部长甲" {
		t.Errorf("留痕应记操作人姓名快照，实际 %q", log.OperatorName)
	}
	for _, want := range []string{"部员乙", "部员丙", "违纪名单核对"} {
		if !strings.Contains(log.Detail, want) {
			t.Errorf("留痕缺少 %q，实际 detail=%q", want, log.Detail)
		}
	}
	if strings.Contains(log.Detail, "张三") || strings.Contains(log.Detail, "附件") {
		t.Errorf("留痕不该复制正文内容，实际 detail=%q", log.Detail)
	}
	if !strings.Contains(log.Detail, "收件人 2 人") {
		t.Errorf("留痕应记收件人数量，实际 detail=%q", log.Detail)
	}
}

// 宿管这条路：能发给任意部长（不限部门），发不到部员。
func TestSendDormManagerToMinisters(t *testing.T) {
	setupMessageSendDB(t)
	dorm := seedMessageUser(t, "h1", "宿管甲", model.RoleDormManager, "", "7号楼", "active")
	minister := seedMessageUser(t, "h2", "部长乙", model.RoleMinister, "组织部", "", "active")
	member := seedMessageUser(t, "h3", "部员丙", model.RoleMember, "纪检部", "", "active")
	peer := seedMessageUser(t, "h4", "宿管丁", model.RoleDormManager, "", "西区7号楼", "active")
	otherPeer := seedMessageUser(t, "h5", "宿管戊", model.RoleDormManager, "", "12号楼", "active")

	ctrl := &MessageController{}
	c, w := sendRequest(dorm, map[string]interface{}{
		"recipient_ids": []uint{minister, peer}, "title": "热水管漏水", "body": "请知悉",
	})
	ctrl.Send(c)
	if w.Code != http.StatusOK {
		t.Fatalf("宿管发部长+同栋宿管应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if n := countMessages(t); n != 2 {
		t.Fatalf("应落 2 行，实际 %d", n)
	}

	c, w = sendRequest(dorm, map[string]interface{}{"recipient_ids": []uint{member}, "title": "标题", "body": "正文"})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("宿管发部员应被拒，实际 %d: %s", w.Code, w.Body.String())
	}

	c, w = sendRequest(dorm, map[string]interface{}{"recipient_ids": []uint{otherPeer}, "title": "标题", "body": "正文"})
	ctrl.Send(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("宿管发别栋宿管应被拒，实际 %d: %s", w.Code, w.Body.String())
	}
}
