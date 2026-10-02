package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 站内信读路径的核心风险只有一个：Casbin 给的是路径级策略，"哪几行"必须在 SQL 里收窄。
// 所以这里的断言集中在"别人的信读不到、别人的已读改不动、以及改不动的那条不能告诉你是谁"。

func setupMessageDB(t *testing.T) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Message{}); err != nil {
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

// seedMessage 直接落库（发送接口是 A2 的内容，这里不依赖它）。
func seedMessage(t *testing.T, senderID, recipientID uint, title string, readAt *time.Time) uint {
	t.Helper()
	msg := model.Message{
		Kind:     model.MessageKindHuman,
		SenderID: senderID, SenderName: "发件人",
		RecipientID: recipientID, RecipientName: "收件人",
		Title: title, Body: title + " 的正文", ReadAt: readAt,
		CreatedAt: time.Now(),
	}
	if err := repository.DB.Create(&msg).Error; err != nil {
		t.Fatalf("写入站内信失败: %v", err)
	}
	return msg.ID
}

func messageRequest(uid uint, method, target, idParam string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	if idParam != "" {
		c.Params = gin.Params{{Key: "id", Value: idParam}}
	}
	c.Set("user_id", uid)
	return c, w
}

func decodeMessages(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v / %s", err, w.Body.String())
	}
	return body
}

// 收件箱只能看到 recipient_id 是自己的行，发件箱只能看到 sender_id 是自己的行。
func TestMessageListConfinesToOwnMailbox(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	seedMessage(t, 2, 1, "发给我的", nil)
	seedMessage(t, 1, 3, "我发出的", nil)
	seedMessage(t, 2, 3, "与我无关", nil)

	c, w := messageRequest(1, http.MethodGet, "/api/v1/messages?box=in", "")
	ctrl.List(c)
	if w.Code != http.StatusOK {
		t.Fatalf("收件箱应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if titles := messageTitles(t, w); strings.Join(titles, ",") != "发给我的" {
		t.Fatalf("收件箱串进了别人的信: %v", titles)
	}
	if got := decodeMessages(t, w)["total"]; got != float64(1) {
		t.Errorf("收件箱 total 应为 1，实际 %v", got)
	}
	inboxBody := w.Body.String()

	c, w = messageRequest(1, http.MethodGet, "/api/v1/messages?box=sent", "")
	ctrl.List(c)
	if titles := messageTitles(t, w); strings.Join(titles, ",") != "我发出的" {
		t.Fatalf("发件箱应按 sender_id 收窄: %v", titles)
	}

	// 两个盒子都不该把第三人之间的信漏出来
	if strings.Contains(inboxBody, "与我无关") || strings.Contains(w.Body.String(), "与我无关") {
		t.Fatalf("响应泄漏了他人之间的站内信: %s | %s", inboxBody, w.Body.String())
	}
}

func messageTitles(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	items, _ := decodeMessages(t, w)["items"].([]interface{})
	titles := []string{}
	for _, item := range items {
		row, _ := item.(map[string]interface{})
		title, _ := row["title"].(string)
		titles = append(titles, title)
	}
	return titles
}

// box 只认 in / sent（大小写与首尾空格容忍，归一后再判）。
// 把未知取值悄悄当成收件箱，界面点"我发出的信"会显示收到的信，所以未知值一律 400。
func TestMessageListRejectsUnknownBox(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	for _, raw := range []string{"all", "inbox", "sent2", "0", "recieve"} {
		c, w := messageRequest(1, http.MethodGet, "/api/v1/messages?box="+raw, "")
		ctrl.List(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("box=%q 应判 400，实际 %d: %s", raw, w.Code, w.Body.String())
		}
	}

	// 归一化必须真的生效，否则前端换个写法就凭空 400
	for _, raw := range []string{"in", "IN", "Sent", " sent "} {
		c, w := messageRequest(1, http.MethodGet, "/api/v1/messages?box="+strings.TrimSpace(raw), "")
		ctrl.List(c)
		if w.Code != http.StatusOK {
			t.Errorf("box=%q 归一后应可识别，实际 %d: %s", raw, w.Code, w.Body.String())
		}
	}

	// 不传 box 时默认收件箱，这是前端的调用形态
	c, w := messageRequest(1, http.MethodGet, "/api/v1/messages", "")
	ctrl.List(c)
	if w.Code != http.StatusOK || decodeMessages(t, w)["box"] != "in" {
		t.Fatalf("缺省应为收件箱，实际 %d: %s", w.Code, w.Body.String())
	}
}

// 只看未读：勾掉复选框必须真的取消筛选，所以 unread_only=0 不能算真。
// 发件箱不按未读筛——那一列是收件人的状态，不是发件人的。
func TestMessageUnreadOnlyFilter(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	seedMessage(t, 2, 1, "我的未读", nil)
	seedMessage(t, 2, 1, "我的已读", anHourAgo())
	seedMessage(t, 1, 2, "我发出的-对方未读", nil)
	seedMessage(t, 1, 3, "我发出的-对方已读", anHourAgo())

	c, w := messageRequest(1, http.MethodGet, "/api/v1/messages?unread_only=1", "")
	ctrl.List(c)
	if titles := messageTitles(t, w); strings.Join(titles, ",") != "我的未读" {
		t.Fatalf("unread_only=1 应只回本人的未读，实际 %v", titles)
	}
	if got := decodeMessages(t, w)["total"]; got != float64(1) {
		t.Errorf("筛选后的 total 应随之收窄为 1，实际 %v", got)
	}
	if got := decodeMessages(t, w)["unread_only"]; got != true {
		t.Errorf("响应应回显筛选状态供界面核对，实际 %v", got)
	}

	c, w = messageRequest(1, http.MethodGet, "/api/v1/messages?box=sent&unread_only=1", "")
	ctrl.List(c)
	if n := len(messageTitles(t, w)); n != 2 {
		t.Fatalf("发件箱不该按收件人的已读状态筛，实际回了 %d 条: %s", n, w.Body.String())
	}
	if got := decodeMessages(t, w)["unread_only"]; got != false {
		t.Errorf("发件箱的 unread_only 必须回显 false，实际 %v", got)
	}

	for _, raw := range []string{"0", "false", "", "yes", "2"} {
		c, w = messageRequest(1, http.MethodGet, "/api/v1/messages?unread_only="+raw, "")
		ctrl.List(c)
		if n := len(messageTitles(t, w)); n != 2 {
			t.Errorf("unread_only=%q 不该生效（只认 1 与 true，大小写不敏感），实际回了 %d 条", raw, n)
		}
	}
}

func anHourAgo() *time.Time {
	v := time.Now().Add(-time.Hour)
	return &v
}

// 空收件箱必须是 []，不能是 null —— 这条在总览接口上栽过一次。
func TestMessageListEmptyInboxIsArray(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	c, w := messageRequest(99, http.MethodGet, "/api/v1/messages", "")
	ctrl.List(c)
	if w.Code != http.StatusOK {
		t.Fatalf("空收件箱也应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"items":[]`) {
		t.Fatalf("空列表必须编码成 []，实际响应: %s", body)
	}
	if strings.Contains(body, "null") {
		t.Fatalf("响应里不该出现 null 列表: %s", body)
	}
}

// 分页参数收口：非法值退回默认，超大 page_size 不能原样进 SQL。
func TestMessageListPaginationClamped(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	for _, title := range []string{"第一封", "第二封", "第三封"} {
		seedMessage(t, 2, 1, title, nil)
	}

	c, w := messageRequest(1, http.MethodGet, "/api/v1/messages?page=-3&page_size=abc", "")
	ctrl.List(c)
	body := decodeMessages(t, w)
	if body["page"] != float64(1) || body["page_size"] != float64(messageDefaultPageSize) {
		t.Errorf("非法分页参数应退回 1/%d，实际 %v / %v", messageDefaultPageSize, body["page"], body["page_size"])
	}

	c, w = messageRequest(1, http.MethodGet, "/api/v1/messages?page_size=99999", "")
	ctrl.List(c)
	if got := decodeMessages(t, w)["page_size"]; got != float64(messageMaxPageSize) {
		t.Errorf("page_size 应被压到上限 %d，实际 %v", messageMaxPageSize, got)
	}

	// 翻第二页应拿到最早那封（列表按 id 倒序）
	c, w = messageRequest(1, http.MethodGet, "/api/v1/messages?page=2&page_size=2", "")
	ctrl.List(c)
	if titles := messageTitles(t, w); strings.Join(titles, ",") != "第一封" {
		t.Fatalf("第二页内容错误: %v", titles)
	}
}

func TestMessageUnreadCountOnlyOwnUnread(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	seedMessage(t, 2, 1, "我的未读一", nil)
	seedMessage(t, 2, 1, "我的未读二", nil)
	seedMessage(t, 2, 1, "我已读过", func() *time.Time { v := time.Now(); return &v }())
	seedMessage(t, 2, 3, "别人的未读", nil)

	c, w := messageRequest(1, http.MethodGet, "/api/v1/messages/unread-count", "")
	ctrl.UnreadCount(c)
	if w.Code != http.StatusOK {
		t.Fatalf("未读数应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if got := decodeMessages(t, w)["unread"]; got != float64(2) {
		t.Fatalf("未读数应为 2（本人的、且未读的），实际 %v: %s", got, w.Body.String())
	}

	// 没有任何未读时要回 0 并把字段写出来，不能省字段让界面显示成空白
	c, w = messageRequest(9, http.MethodGet, "/api/v1/messages/unread-count", "")
	ctrl.UnreadCount(c)
	if w.Code != http.StatusOK {
		t.Fatalf("无未读也应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if got := decodeMessages(t, w)["unread"]; got != float64(0) {
		t.Errorf("无未读时应回 0，实际 %v", got)
	}
	if !strings.Contains(w.Body.String(), `"unread":0`) {
		t.Errorf("响应应显式带 \"unread\":0，实际 %s", w.Body.String())
	}
}

// 清未读：不存在与不是我的必须长得一模一样，否则这里变成"全校谁收到过信"的探测器。
func TestMarkReadMergesAbsentAndForeign(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	foreign := seedMessage(t, 2, 3, "第三人之间的信", nil)

	cAbsent, wAbsent := messageRequest(1, http.MethodPut, "/api/v1/messages/4242/read", "4242")
	ctrl.MarkRead(cAbsent)

	cForeign, wForeign := messageRequest(1, http.MethodPut, "/api/v1/messages/"+itoa(foreign)+"/read", itoa(foreign))
	ctrl.MarkRead(cForeign)

	if wAbsent.Code != http.StatusNotFound || wForeign.Code != http.StatusNotFound {
		t.Fatalf("两种情况都必须 404，实际 %d / %d", wAbsent.Code, wForeign.Code)
	}
	if wAbsent.Body.String() != wForeign.Body.String() {
		t.Fatalf("不存在与越权的响应体必须逐字一致，实际 %q vs %q", wAbsent.Body.String(), wForeign.Body.String())
	}
	if strings.Contains(wForeign.Body.String(), "第三人") || strings.TrimSpace(wForeign.Body.String()) == "" {
		t.Fatalf("越权响应不得带出内容，也不得是空体: %s", wForeign.Body.String())
	}

	// 越权调用不能顺手把别人的信标成已读
	var check model.Message
	if err := repository.DB.First(&check, foreign).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if check.ReadAt != nil {
		t.Fatal("非收件人的调用改动了他人消息的已读状态")
	}
}

// 已读时间只在"未读→已读"那一次写；重复点不覆盖，也不报错。
func TestMarkReadIsIdempotentAndOwned(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	own := seedMessage(t, 2, 1, "发给我的信", nil)

	c, w := messageRequest(1, http.MethodPut, "/api/v1/messages/1/read", itoa(own))
	ctrl.MarkRead(c)
	if w.Code != http.StatusOK {
		t.Fatalf("收件人清自己未读应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	first := decodeMessages(t, w)
	if first["already_read"] != false {
		t.Errorf("首次清未读应标 already_read=false，实际 %v", first["already_read"])
	}
	readAtRaw, _ := first["read_at"].(string)
	if strings.TrimSpace(readAtRaw) == "" || readAtRaw == "null" {
		t.Fatalf("首次清未读应回带已读时间，实际 %v", first["read_at"])
	}

	time.Sleep(15 * time.Millisecond) // 让"第二次的时间"在毫秒级上可区分
	c, w = messageRequest(1, http.MethodPut, "/api/v1/messages/1/read", itoa(own))
	ctrl.MarkRead(c)
	second := decodeMessages(t, w)
	if second["already_read"] != true {
		t.Errorf("重复清未读应标 already_read=true，实际 %v", second["already_read"])
	}
	if second["read_at"] != readAtRaw {
		t.Errorf("重复清未读覆盖了首次已读时间: %v -> %v", readAtRaw, second["read_at"])
	}

	// 发件人拿自己发出去那条的 id，改不动收件人的已读状态
	c, w = messageRequest(2, http.MethodPut, "/api/v1/messages/1/read", itoa(own))
	ctrl.MarkRead(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("发件人不得清收件人的未读，实际 %d: %s", w.Code, w.Body.String())
	}

	var check model.Message
	if err := repository.DB.First(&check, own).Error; err != nil {
		t.Fatalf("回读失败: %v", err)
	}
	if check.ReadAt == nil {
		t.Fatal("收件人已读状态被发件人的调用弄丢了")
	}
}

func TestMarkReadRejectsInvalidID(t *testing.T) {
	setupMessageDB(t)
	ctrl := &MessageController{}
	for _, raw := range []string{"abc", "0", "-3", "", "  ", "1e99999", "7x"} {
		c, w := messageRequest(1, http.MethodPut, "/api/v1/messages/x/read", raw)
		ctrl.MarkRead(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("id %q 应判 400，实际 %d: %s", raw, w.Code, w.Body.String())
		}
	}
}
