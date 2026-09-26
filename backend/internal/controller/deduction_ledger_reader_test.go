package controller

import (
	"bytes"
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

// 台账导出的读者闸门单独测：分页列表对全体登录用户开放（部员端要看违纪公示），
// 收紧的只是"一次请求带走全校明细"的 CSV 通道，这条边界只有测出来才不会漂移。

const csvBOM = "\xEF\xBB\xBF"

func setupLedgerDB(t *testing.T) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.DeductionRecord{}, &model.OperationLog{}); err != nil {
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

func seedLedgerUser(t *testing.T, username, role, department, position, status string) uint {
	t.Helper()
	u := model.User{
		Username: username, RealName: "测试读者", Role: role,
		Department: department, Position: position, Status: status,
	}
	if err := repository.DB.Create(&u).Error; err != nil {
		t.Fatalf("写入账号 %s 失败: %v", username, err)
	}
	return u.ID
}

func ledgerContext(userID uint, query string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/deductions/export-csv"+query, nil)
	c.Set("user_id", userID)
	return c, w
}

func TestLedgerReaderAllowsDesignatedReaders(t *testing.T) {
	setupLedgerDB(t)
	cases := map[string][3]string{
		"档案导出岗":      {model.RoleViewerExport, "", model.PositionMember},
		"部长":         {model.RoleMinister, "纪检部", model.PositionMinister},
		"技术维护组":      {model.RoleTechAdmin, "组织部 · 技术组", model.PositionMinister},
		"持打表权的副部长":   {model.RoleMember, "组织部 · 技术组", model.PositionVice},
		"无打表权的部长但同部": {model.RoleMinister, "组织部 · 技术组", model.PositionMinister},
	}
	for name, spec := range cases {
		userID := seedLedgerUser(t, "allow_"+name, spec[0], spec[1], spec[2], "active")
		c, w := ledgerContext(userID, "")
		got, ok := requireDeductionLedgerReader(c)
		if !ok {
			t.Fatalf("%s 应放行，实际被拒: %s", name, w.Body.String())
		}
		if got.ID != userID {
			t.Fatalf("%s 应返回数据库最新身份，实际 id=%d", name, got.ID)
		}
		if w.Body.Len() != 0 {
			t.Fatalf("%s 放行时不得提前写出响应体，实际: %q", name, w.Body.String())
		}
	}
}

func TestLedgerReaderRejectsEveryoneElse(t *testing.T) {
	setupLedgerDB(t)
	cases := map[string][3]string{
		"普通部员":    {model.RoleMember, "纪检部", model.PositionMember},
		"技术部部员":   {model.RoleMember, "组织部 · 技术组", model.PositionMember},
		"技术部正部长岗": {model.RoleMember, "组织部 · 技术组", model.PositionMinister},
		"宿管":      {model.RoleDormManager, "西区12号楼", model.PositionMember},
	}
	for name, spec := range cases {
		userID := seedLedgerUser(t, "deny_"+name, spec[0], spec[1], spec[2], "active")
		c, w := ledgerContext(userID, "")
		if _, ok := requireDeductionLedgerReader(c); ok {
			t.Fatalf("%s 不得放行整表导出", name)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s 应返 403，实际 %d: %s", name, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "违纪台账导出仅限") {
			t.Fatalf("%s 的 403 应说明可读范围，实际: %s", name, w.Body.String())
		}
	}
}

func TestLedgerReaderRejectsDisabledAndMissingAccount(t *testing.T) {
	setupLedgerDB(t)
	// 停用：即使职务仍满足打表口径也不能导出
	disabledVice := seedLedgerUser(t, "disabled_vice", model.RoleMember, "组织部 · 技术组", model.PositionVice, "disabled")
	c, w := ledgerContext(disabledVice, "")
	if _, ok := requireDeductionLedgerReader(c); ok {
		t.Fatalf("停用账号不得放行")
	}
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "已被停用") {
		t.Fatalf("停用账号应 403 并说明原因，实际 %d: %s", w.Code, w.Body.String())
	}

	// 身份缺失（Cookie 有效但账号已不存在）不得当成"有权限"
	c2, w2 := ledgerContext(99999, "")
	if _, ok := requireDeductionLedgerReader(c2); ok {
		t.Fatalf("账号不存在时不得放行")
	}
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("账号不存在应 401，实际 %d: %s", w2.Code, w2.Body.String())
	}
}

func TestLedgerReaderFollowsLivePromotionAndRevocation(t *testing.T) {
	setupLedgerDB(t)
	userID := seedLedgerUser(t, "flip_role", model.RoleMember, "组织部 · 技术组", model.PositionMember, "active")

	c, _ := ledgerContext(userID, "")
	if _, ok := requireDeductionLedgerReader(c); ok {
		t.Fatalf("任命前不得放行")
	}
	if err := repository.DB.Model(&model.User{}).Where("id = ?", userID).
		Update("position", model.PositionVice).Error; err != nil {
		t.Fatalf("任命失败: %v", err)
	}
	c, _ = ledgerContext(userID, "")
	if _, ok := requireDeductionLedgerReader(c); !ok {
		t.Fatalf("任命为副部长后应立即放行（不依赖重新登录）")
	}
	if err := repository.DB.Model(&model.User{}).Where("id = ?", userID).
		Update("position", model.PositionMember).Error; err != nil {
		t.Fatalf("免职失败: %v", err)
	}
	c, w := ledgerContext(userID, "")
	if _, ok := requireDeductionLedgerReader(c); ok {
		t.Fatalf("免职后应立即回收，实际仍放行")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("免职后应 403，实际 %d", w.Code)
	}
}

func seedLedgerRows(t *testing.T) {
	t.Helper()
	rows := []model.DeductionRecord{
		{StudentID: 1, Building: "西区12号楼", Floor: "3F", RoomNumber: "302", StudentName: "测试学生甲", ClassName: "高一(3)班", Category: "晚归", DeductPoints: 2, Reason: "逾期归寝", InspectorName: "记录人", Status: "confirmed"},
		{StudentID: 2, Building: "西区12号楼", Floor: "4F", RoomNumber: "411", StudentName: "测试学生乙", ClassName: "高二(2)班", Category: "大功率电器", DeductPoints: 5, Reason: "使用违规电器", InspectorName: "记录人", Status: "confirmed"},
		{StudentID: 0, Building: "东区4号楼", Floor: "1F", RoomNumber: "105", StudentName: "测试学生丙", ClassName: "高三(1)班", Category: "脏乱差", DeductPoints: 1, Reason: "内务不合格", InspectorName: "记录人", Status: "revoked"},
	}
	if err := repository.DB.Create(&rows).Error; err != nil {
		t.Fatalf("写入台账失败: %v", err)
	}
}

func TestExportDeductionsCSVEnforcesReaderGate(t *testing.T) {
	setupLedgerDB(t)
	seedLedgerRows(t)
	dc := &DeductionController{}

	memberID := seedLedgerUser(t, "export_member", model.RoleMember, "纪检部", model.PositionMember, "active")
	c, w := ledgerContext(memberID, "")
	dc.ExportDeductionsCSV(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("部员整表导出应 403，实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "测试学生甲") {
		t.Fatalf("被拒响应不得夹带台账内容: %s", w.Body.String())
	}
	if !strings.HasPrefix(w.Body.String(), "{") {
		t.Fatalf("403 应返回 JSON 说明供前端提示，实际: %s", w.Body.String())
	}

	viewerID := seedLedgerUser(t, "export_viewer", model.RoleViewerExport, "", model.PositionMember, "active")
	c, w = ledgerContext(viewerID, "")
	dc.ExportDeductionsCSV(c)
	if w.Code != http.StatusOK {
		t.Fatalf("档案导出岗应可导出，实际 %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("成功响应应为 CSV，实际 Content-Type=%q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("应带附件下载头，实际 %q", cd)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, csvBOM) {
		t.Fatalf("应保留 UTF-8 BOM 以免 Excel 乱码")
	}
	for _, name := range []string{"测试学生甲", "测试学生乙", "测试学生丙"} {
		if !strings.Contains(body, name) {
			t.Fatalf("整表导出应含 %s（撤销记录也要在台账里可追溯）", name)
		}
	}

	// 筛选条件仍生效：只导出撤销记录时不应出现未撤销的两行
	c, w = ledgerContext(viewerID, "?status=revoked")
	dc.ExportDeductionsCSV(c)
	body = w.Body.String()
	if !strings.Contains(body, "测试学生丙") || strings.Contains(body, "测试学生甲") {
		t.Fatalf("status=revoked 筛选未生效")
	}
}

func TestExportDeductionsCSVDoesNotEmitPartialFileOnQueryFailure(t *testing.T) {
	setupLedgerDB(t)
	seedLedgerRows(t)
	viewerID := seedLedgerUser(t, "broken_viewer", model.RoleViewerExport, "", model.PositionMember, "active")

	// 表没了 → 查库必然失败。此前错误被忽略，使用者会拿到一份只有表头的"空台账"。
	if err := repository.DB.Migrator().DropTable(&model.DeductionRecord{}); err != nil {
		t.Fatalf("制造查库失败前提失败: %v", err)
	}

	c, w := ledgerContext(viewerID, "")
	(&DeductionController{}).ExportDeductionsCSV(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("查库失败应 500，实际 %d: %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(csvBOM)) {
		t.Fatalf("失败时不得写出任何 CSV 字节")
	}
	if !strings.Contains(w.Body.String(), "导出未执行") {
		t.Fatalf("失败响应应明确说明导出未执行，实际: %s", w.Body.String())
	}
}
