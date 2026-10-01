package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 三个"高危（确认可利用）"的回归测试：
//  1. 公开答题路由上的 SQL 条件注入（200/404 布尔预言机）
//  2. member 可篡改全校 AI 模型定价
//  3. 福利网关无角色门 → 改 BaseURL 后真实密钥被发往攻击者地址
//
// 共同根因是同一类：来自 URL 的 id 未经收敛就进了 GORM 条件，以及
// "Casbin 按 role 放行整个前缀、handler 里却不再区分谁在调用"。

func setupGateDB(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.ExamPaper{}, &model.Question{},
		&model.WelfareModelPricing{}, &model.TechWelfareGateway{}, &model.OperationLog{},
		&model.RewardItem{}, &model.RewardOrder{}, &model.LeaveRequest{},
	); err != nil {
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

func seedUser(t *testing.T, id uint, username, role, dept string) {
	t.Helper()
	now := time.Now()
	if err := repository.DB.Create(&model.User{
		ID: id, Username: username, RealName: "测试-" + username, Role: role,
		Department: dept, PasswordChangedAt: &now,
	}).Error; err != nil {
		t.Fatalf("预置账号失败: %v", err)
	}
}

// callHandler 用给定的登录身份与路径参数调用一个 handler。
func callHandler(t *testing.T, h gin.HandlerFunc, userID uint, realName, pathIDValue string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// 路径参数不拼进 URL：注入载荷里带空格会让 NewRequest 报 malformed HTTP version。
	// handler 只读 c.Param，所以这里固定 URL，参数一律走 c.Params。
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/anything/fixed", nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		req = httptest.NewRequest(http.MethodPost, "/api/v1/anything/fixed", strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
	}
	c.Request = req
	if userID > 0 {
		c.Set("user_id", userID)
		c.Set("real_name", realName)
	}
	c.Params = gin.Params{{Key: "id", Value: pathIDValue}}
	h(c)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v / %q", err, rec.Body.String())
	}
	return out
}

// 1. 公开路由上的注入：路径里的 id 不再是数字就必须被挡在 SQL 之前。
//
// GORM 的 First(&x, v) 在 v 是"非数字字符串"时会把它当原生 SQL 片段拼进 WHERE，
// 于是 /papers/1 OR 1=1 能改写查询条件；这条路由不要求登录，200/404 就是布尔预言机。
func TestPublicExamRouteRejectsSQLPayloads(t *testing.T) {
	setupGateDB(t)
	if err := repository.DB.Create(&model.ExamPaper{ID: 1, Title: "素养测验", IsPublished: true}).Error; err != nil {
		t.Fatalf("预置试卷失败: %v", err)
	}
	if err := repository.DB.Create(&model.ExamPaper{ID: 2, Title: "未发布试卷", IsPublished: false}).Error; err != nil {
		t.Fatalf("预置试卷失败: %v", err)
	}

	exc := &ExamController{}

	// 正常主键仍然可用
	if rec := callHandler(t, exc.GetPaperDetail, 0, "", "2", nil); rec.Code != http.StatusOK {
		t.Fatalf("按 id 取未发布卷应 200，实际 %d", rec.Code)
	}
	if rec := callHandler(t, exc.GetPaperDetail, 0, "", "999", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的 id 应 404，实际 %d", rec.Code)
	}

	payloads := []string{
		"1 OR 1=1",
		"1+OR+1=1",
		"1) OR (1=1",
		"1 AND (SELECT COUNT(*) FROM users) > 0",
		"0",
		"-1",
		"abc",
		"1;DROP TABLE exam_papers--",
	}
	for _, p := range payloads {
		rec := callHandler(t, exc.GetPaperDetail, 0, "", p, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("注入载荷 %q 应 400，实际 %d / %s", p, rec.Code, rec.Body.String())
		}
		if !strings.Contains(decodeErr(t, rec)["error"].(string), "正整数") {
			t.Fatalf("错误要说明参数口径，实际 %v", decodeErr(t, rec)["error"])
		}
	}

	// 提交接口同样不接受非数字 id
	for _, p := range []string{"1 OR 1=1", "abc"} {
		rec := callHandler(t, exc.SubmitPaper, 0, "", p, map[string]any{
			"applicant_name": "张三", "applicant_phone": "13800000000", "answers": map[string]string{},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("SubmitPaper 对 %q 应 400，实际 %d", p, rec.Code)
		}
	}

	// 注入没有真的改动数据
	var papers []model.ExamPaper
	repository.DB.Find(&papers)
	if len(papers) != 2 {
		t.Fatalf("试卷表应仍是 2 条，实际 %d", len(papers))
	}
}

// 2. 模型定价：member 不得写；部长只能设本部门的价，且不能按 ID 顶掉别部的行。
func TestModelPricingRoleAndRowGate(t *testing.T) {
	setupGateDB(t)
	seedUser(t, 1, "member_a", model.RoleMember, "纪检部")
	seedUser(t, 2, "minister_jj", model.RoleMinister, "纪检部")
	seedUser(t, 3, "minister_xc", model.RoleMinister, "宣传部")

	if err := repository.DB.Create(&model.WelfareModelPricing{
		ID: 10, ModelKey: "deepseek-chat", Department: "纪检部",
		PointsCost: 10, CallsGranted: 10, CostPerCall: 1,
	}).Error; err != nil {
		t.Fatalf("预置定价失败: %v", err)
	}

	wc := &WelfareController{}
	body := func(m map[string]any) map[string]any { return m }

	// 部员调用被拒
	if rec := callHandler(t, wc.SaveModelPricing, 1, "测试-member_a", "0", body(map[string]any{
		"model_key": "gpt-4o", "points_cost": 1, "calls_granted": 999999,
	})); rec.Code != http.StatusForbidden {
		t.Fatalf("member 设定价应 403，实际 %d / %s", rec.Code, rec.Body.String())
	}

	// 宣传部部长想改纪检部那行 → 拒
	if rec := callHandler(t, wc.SaveModelPricing, 3, "测试-minister_xc", "0", body(map[string]any{
		"id": 10, "model_key": "deepseek-chat", "points_cost": 1, "calls_granted": 10,
	})); rec.Code != http.StatusForbidden {
		t.Fatalf("跨部门顶掉他人定价应 403，实际 %d / %s", rec.Code, rec.Body.String())
	}

	// 纪检部部长自己改：请求体里的 department 不采信，数值被封顶
	if rec := callHandler(t, wc.SaveModelPricing, 2, "测试-minister_jj", "0", body(map[string]any{
		"id": 10, "model_key": "deepseek-chat", "department": "宣传部",
		"points_cost": 1, "calls_granted": 1 << 30,
	})); rec.Code != http.StatusOK {
		t.Fatalf("本部门改本部门定价应 200，实际 %d / %s", rec.Code, rec.Body.String())
	}
	var stored model.WelfareModelPricing
	if err := repository.DB.First(&stored, "id = ?", 10).Error; err != nil {
		t.Fatalf("读回定价失败: %v", err)
	}
	if stored.Department != "纪检部" {
		t.Fatalf("department 不得由请求体改写，实际 %q", stored.Department)
	}
	if stored.CallsGranted > 100000 {
		t.Fatalf("calls_granted 应被封顶，实际 %d", stored.CallsGranted)
	}

	// 新建时同样落在自己的部门
	if rec := callHandler(t, wc.SaveModelPricing, 3, "测试-minister_xc", "0", body(map[string]any{
		"model_key": "gpt-4o-mini", "department": "纪检部", "points_cost": 5, "calls_granted": 5,
	})); rec.Code != http.StatusOK {
		t.Fatalf("宣传部新建定价应 200，实际 %d / %s", rec.Code, rec.Body.String())
	}
	var created model.WelfareModelPricing
	if err := repository.DB.Where("model_key = ?", "gpt-4o-mini").First(&created).Error; err != nil {
		t.Fatalf("新建的定价没落库: %v", err)
	}
	if created.Department != "宣传部" {
		t.Fatalf("新建定价应落在操作者部门，实际 %q", created.Department)
	}
}

// 3. 福利网关：member 不得写；OwnerID=0 的历史共享行收归技术维护组。
//
//	网关的 BaseURL 决定服务器把库里那把真实密钥以 Bearer 发往何处，
//	"改地址 → 触发一次探测"就是完整的密钥外泄链。
func TestGatewayRoleGateAndSharedRow(t *testing.T) {
	setupGateDB(t)
	seedUser(t, 1, "member_a", model.RoleMember, "纪检部")
	seedUser(t, 2, "minister_jj", model.RoleMinister, "纪检部")
	seedUser(t, 3, "tech_root", model.RoleTechAdmin, "技术组")

	// 用 map 建一行 OwnerID=0 的历史共享网关，绕开密钥封装钩子
	if err := repository.DB.Table("tech_welfare_gateways").Create(map[string]any{
		"owner_id": 0, "owner_name": "", "gateway_name": "历史共享网关",
		"base_url": "https://api.example.com/v1", "api_key": "", "default_model": "gpt-4o-mini",
		"point_cost_per_call": 2, "is_active": true,
		"created_at": time.Now(), "updated_at": time.Now(),
	}).Error; err != nil {
		t.Fatalf("预置共享网关失败: %v", err)
	}

	wc := &WelfareController{}
	newGateway := map[string]any{
		"gateway_name": "我的网关", "base_url": "https://evil.example.com/v1", "api_key": "sk-test",
	}

	// 部员连建都不行
	if rec := callHandler(t, wc.SaveGateway, 1, "测试-member_a", "0", newGateway); rec.Code != http.StatusForbidden {
		t.Fatalf("member 建网关应 403，实际 %d / %s", rec.Code, rec.Body.String())
	}

	// 部长改共享行（OwnerID=0）也不行——这正是把别人密钥发去自己地址的那一步
	if rec := callHandler(t, wc.SaveGateway, 2, "测试-minister_jj", "0", map[string]any{
		"id": 1, "gateway_name": "被改名的共享网关", "base_url": "https://evil.example.com/v1",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("部长改共享网关应 403，实际 %d / %s", rec.Code, rec.Body.String())
	}
	var names []string
	repository.DB.Table("tech_welfare_gateways").Where("id = ?", 1).Pluck("gateway_name", &names)
	if len(names) != 1 || names[0] != "历史共享网关" {
		t.Fatalf("共享网关不应被改动，实际 gateway_name=%v", names)
	}

	// 技术维护组不被权限挡下（validatePublicURL 会做真实 DNS 解析，
	// 这里只断言"不是 403"，避免把网络可用性绑进测试）
	if rec := callHandler(t, wc.SaveGateway, 3, "测试-tech_root", "0", map[string]any{
		"id": 1, "gateway_name": "技术组接管", "base_url": "https://api.openai.com/v1",
	}); rec.Code == http.StatusForbidden {
		t.Fatalf("技术维护组改共享网关不应被拒，实际 %d / %s", rec.Code, rec.Body.String())
	}

	// 探测同样要认归属：非所有者且非技术维护组不得触发
	if rec := callHandler(t, wc.ProbeModels, 2, "测试-minister_jj", "1", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("探测他人网关应 403，实际 %d", rec.Code)
	}
	// 路径 id 不合法时在查库之前就被挡下
	if rec := callHandler(t, wc.ProbeModels, 3, "测试-tech_root", "1 OR 1=1", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("非数字网关 id 应 400，实际 %d", rec.Code)
	}
}

// 4. 路径参数收敛 helper 本身的口径。
func TestPathIDHelper(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		raw  string
		want uint
		ok   bool
	}{
		{"12", 12, true},
		{" 12 ", 12, true},
		{"0", 0, false},
		{"-3", 0, false},
		{"1.5", 0, false},
		{"1e3", 0, false},
		{"012", 12, true},
		{"1 OR 1=1", 0, false},
		{"", 0, false},
		{strconv.Itoa(1 << 40), 1 << 40, true},
	} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: tc.raw}}
		got, ok := pathID(c, "id")
		if got != tc.want || ok != tc.ok {
			t.Fatalf("pathID(%q) = %d/%v，期望 %d/%v", tc.raw, got, ok, tc.want, tc.ok)
		}
		if !tc.ok && rec.Code != http.StatusBadRequest {
			t.Fatalf("非法参数应已写出 400，实际 %d", rec.Code)
		}
	}
}
