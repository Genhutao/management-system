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

// 总览页唯一的硬约束是"不借总览扩权"：每张卡片必须落在该角色本来就能读到的接口范围内。
// 因此这里的断言主要不是数字对不对，而是**谁的卡片里出现了什么、不该出现的有没有出现**。

func setupDashboardDB(t *testing.T) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Student{}, &model.DeductionRecord{}, &model.InspectionPhoto{},
		&model.LeaveRequest{}, &model.MemberScoreLog{}, &model.RewardOrder{},
		&model.RecruitmentApplication{}, &model.ScheduleShift{}, &model.AIConfig{},
		&model.Message{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	prev := repository.DB
	repository.DB = db
	t.Cleanup(func() {
		repository.DB = prev
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
}

func seedDashboardUser(t *testing.T, u model.User) model.User {
	t.Helper()
	if err := repository.DB.Create(&u).Error; err != nil {
		t.Fatalf("写入用户失败: %v", err)
	}
	return u
}

// runSummary 只塞 user_id，故意把 role 塞成别的值，
// 以证明 handler 读的是库里的最新身份，不是旧会话声明。
func runSummary(t *testing.T, uid uint) (int, map[string]interface{}) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil)
	c.Set("user_id", uid)
	c.Set("role", "tech_admin")

	(&DashboardController{}).Summary(c)

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v / %s", err, w.Body.String())
	}
	return w.Code, body
}

func cardKeys(t *testing.T, body map[string]interface{}) map[string]DashboardCard {
	t.Helper()
	out := map[string]DashboardCard{}
	raw, _ := json.Marshal(body["cards"])
	var cards []DashboardCard
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatalf("cards 解析失败: %v", err)
	}
	for _, cd := range cards {
		out[cd.Key] = cd
	}
	return out
}

func TestDormSummaryHasNoSchoolWideDeductionData(t *testing.T) {
	setupDashboardDB(t)
	dorm := seedDashboardUser(t, model.User{
		Username: "dorm_a", RealName: "宿管甲", Role: model.RoleDormManager,
		Building: "7号楼", Floor: "全楼", Status: "active",
	})

	// 别人的上报、全校的违纪，都不该出现在宿管总览里
	repository.DB.Create(&model.InspectionPhoto{DormManagerID: 999, ManagerName: "宿管乙", Building: "8号楼", ImageURL: "/uploads/x.jpg", Status: "uploaded"})
	repository.DB.Create(&model.InspectionPhoto{DormManagerID: dorm.ID, ManagerName: dorm.RealName, Building: "7号楼", ImageURL: "/uploads/y.jpg", Status: "converted"})
	repository.DB.Create(&model.DeductionRecord{
		Building: "7号楼", Floor: "3F", RoomNumber: "301", StudentName: "学生甲", ClassName: "高一(1)班",
		Category: "违规电器", DeductPoints: 5, Reason: "测试", InspectorName: "副部长", Status: "confirmed",
	})

	code, body := runSummary(t, dorm.ID)
	if code != http.StatusOK {
		t.Fatalf("宿管应 200，实际 %d: %v", code, body)
	}
	cards := cardKeys(t, body)
	if _, leaked := cards["deduct"]; leaked {
		t.Errorf("宿管总览出现了违纪卡片，越权")
	}
	for _, k := range []string{"score", "leave", "reward", "members"} {
		if _, leaked := cards[k]; leaked {
			t.Errorf("宿管总览出现了部员/部长专属卡片 %s", k)
		}
	}
	if got := cards["week"].Value; got != 1 {
		t.Errorf("本周已上报应为 1（只算本人），实际 %d", got)
	}
	if got := cards["scope"].Text; got != "7号楼 全楼" {
		t.Errorf("负责范围应为「7号楼 全楼」，实际 %q", got)
	}
	// 待转扣分的上报只能给宿管看数字：宿管的 Casbin 没有 /deductions，
	// 这张卡一旦可点，就是把人送去一个只会弹"权限拦截"的面板。
	if got := cards["pending"].Tab; got != "" {
		t.Errorf("宿管的待处理上报卡不该有跳转目标，实际 tab=%q", got)
	}
	// 今天 0 条上报要有提醒，但不能替宿管判断"该不该交"
	if notices := body["notices"]; notices == nil {
		t.Errorf("今天没上报时应有一条提醒")
	}
}

func TestMemberSummaryGatesDeductionCardByAuthority(t *testing.T) {
	setupDashboardDB(t)
	plain := seedDashboardUser(t, model.User{
		Username: "mem_a", RealName: "部员甲", Role: model.RoleMember,
		Department: "纪检部", Position: model.PositionMember, TotalScore: 98, Status: "active",
	})
	vice := seedDashboardUser(t, model.User{
		Username: "mem_b", RealName: "副部长乙", Role: model.RoleMember,
		Department: "组织部 · 技术组", Position: model.PositionVice, TotalScore: 105, Status: "active",
	})

	weekStart, _ := weekBounds()
	repository.DB.Create(&model.DeductionRecord{
		Building: "1号楼", Floor: "2F", RoomNumber: "202", StudentName: "学生乙", ClassName: "高一(2)班",
		Category: "内务卫生", DeductPoints: 2, Reason: "本周该副部长的打表", InspectorName: vice.RealName,
		InspectorID: vice.ID, Status: "confirmed", CreatedAt: weekStart.Add(24 * time.Hour),
	})
	// 打表记录被撤销后不该再计入"本周我打表"
	repository.DB.Create(&model.DeductionRecord{
		Building: "1号楼", Floor: "2F", RoomNumber: "203", StudentName: "学生丙", ClassName: "高一(3)班",
		Category: "喧哗", DeductPoints: 1, Reason: "已撤销的打表", InspectorName: vice.RealName,
		InspectorID: vice.ID, Status: "revoked", CreatedAt: weekStart.Add(24 * time.Hour),
	})

	_, plainBody := runSummary(t, plain.ID)
	if _, has := cardKeys(t, plainBody)["deduct"]; has {
		t.Errorf("普通部员没有打表权，总览不该出现打表卡片")
	}
	if sc := cardKeys(t, plainBody)["score"].Value; sc != 98 {
		t.Errorf("积分应读库里的 98，实际 %d", sc)
	}

	_, viceBody := runSummary(t, vice.ID)
	cards := cardKeys(t, viceBody)
	if _, has := cards["deduct"]; !has {
		t.Fatalf("副部长应有打表卡片")
	}
	if cards["deduct"].Value != 1 {
		t.Errorf("本周打表应为 1 条（撤销的那条不计入），实际 %d", cards["deduct"].Value)
	}
}

func TestMinisterPendingLeavesIsSchoolWideToMatchList(t *testing.T) {
	setupDashboardDB(t)
	minister := seedDashboardUser(t, model.User{
		Username: "mini_a", RealName: "部长甲", Role: model.RoleMinister,
		Department: "纪检部", Position: model.PositionMinister, Status: "active",
	})
	// GET /minister/leaves 本来就不按部门过滤，卡片必须与它一致，否则数字与点进去的列表打架
	repository.DB.Create(&model.LeaveRequest{MemberID: 1, MemberName: "外部部员", Reason: "测试", Status: "pending"})
	repository.DB.Create(&model.LeaveRequest{MemberID: 2, MemberName: "本部部员", Reason: "测试", Status: "pending"})
	repository.DB.Create(&model.LeaveRequest{MemberID: 3, MemberName: "已批部员", Reason: "测试", Status: "approved"})

	_, body := runSummary(t, minister.ID)
	cards := cardKeys(t, body)
	if cards["leave"].Value != 2 {
		t.Errorf("待审批请假应为全校 pending 共 2 条，实际 %d", cards["leave"].Value)
	}
	if !strings.Contains(cards["leave"].Hint, "全校") {
		t.Errorf("卡片要写明口径是全校，避免被当成本部门数，实际 hint %q", cards["leave"].Hint)
	}
	if len(body["notices"].([]interface{})) == 0 {
		t.Errorf("有待审批时应有提醒")
	}
}

func TestViewerSummaryCannotSeeMemberScores(t *testing.T) {
	setupDashboardDB(t)
	viewer := seedDashboardUser(t, model.User{
		Username: "view_a", RealName: "查看岗", Role: model.RoleViewerExport, Status: "active",
	})
	repository.DB.Create(&model.MemberScoreLog{MemberID: 1, MemberName: "部员甲", ChangeType: "attendance_ok", ScoreChange: 5, Reason: "核销"})

	_, body := runSummary(t, viewer.ID)
	cards := cardKeys(t, body)
	for _, k := range []string{"score", "duty", "leave", "reward", "users", "papers"} {
		if _, leaked := cards[k]; leaked {
			t.Errorf("查看下载岗不该看到卡片 %s（其 Casbin 无对应读权限）", k)
		}
	}
	if _, has := cards["deducts"]; !has {
		t.Errorf("查看岗可读违纪列表，应有违纪卡片")
	}
}

func TestSummaryUsesFreshRoleNotStaleClaims(t *testing.T) {
	setupDashboardDB(t)
	dorm := seedDashboardUser(t, model.User{
		Username: "dorm_b", RealName: "宿管乙", Role: model.RoleDormManager, Building: "9号楼", Status: "active",
	})
	// 会话声明里写着 tech_admin（runSummary 故意这么塞），库里是宿管 —— 必须按库算
	code, body := runSummary(t, dorm.ID)
	if code != http.StatusOK || body["role"] != model.RoleDormManager {
		t.Fatalf("应按库里的角色返回，实际 code=%d role=%v", code, body["role"])
	}
	if _, leaked := cardKeys(t, body)["users"]; leaked {
		t.Errorf("旧声明里的技术组卡片不该出现")
	}
}

func TestUnknownRoleIsRejected(t *testing.T) {
	setupDashboardDB(t)
	stranger := seedDashboardUser(t, model.User{Username: "x", RealName: "未知", Role: "parent", Status: "active"})

	code, body := runSummary(t, stranger.ID)
	if code != http.StatusForbidden {
		t.Fatalf("未知角色应 403，实际 %d", code)
	}
	if _, has := body["cards"]; has {
		t.Errorf("403 时不该带出卡片数据")
	}
}

// 空列表要序列化成 []，不是 null：前端直接遍历它。
// 这与名册识别那次 preview_sample 崩在 .map 上是同一个失效模式。
func TestEmptyCardsSerializeAsArrayNotNull(t *testing.T) {
	setupDashboardDB(t)
	viewer := seedDashboardUser(t, model.User{Username: "v2", RealName: "查看岗乙", Role: model.RoleViewerExport, Status: "active"})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/summary", nil)
	c.Set("user_id", viewer.ID)
	(&DashboardController{}).Summary(c)

	got := w.Body.String()
	if !strings.Contains(got, `"cards":[`) {
		t.Errorf("无数据时 cards 不该是 null，实际响应：%s", got)
	}
	if strings.Contains(got, `"cards":null`) || strings.Contains(got, `"notices":null`) {
		t.Errorf("出现了 null 列表，前端会当场崩：%s", got)
	}
}
