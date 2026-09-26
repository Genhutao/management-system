package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 留痕详情的核心风险是"按 id 换楼栋"：宿管端命中的是 Casbin 的 /api/v1/dorm/* 通配策略，
// 列表接口的楼栋过滤在 URL 上根本不成立，所以详情必须自己按同一规则过滤，
// 且越权与不存在要长得一模一样，否则详情接口会变成记录探测面。

func setupInspectionDB(t *testing.T) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.InspectionPhoto{}, &model.InspectionSubject{}, &model.DeductionRecord{}); err != nil {
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

func seedInspection(t *testing.T, building string, fields ...map[string]interface{}) uint {
	t.Helper()
	record := model.InspectionPhoto{
		Building: building, RoomNumber: "302", ManagerName: "刘阿姨",
		ImageURL: "/uploads/fake.jpg", AIStatus: "real", Category: "违规电器",
	}
	for _, extra := range fields {
		raw, err := json.Marshal(extra)
		if err != nil {
			t.Fatalf("构造留痕字段失败: %v", err)
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatalf("套用留痕字段失败: %v", err)
		}
	}
	if err := repository.DB.Create(&record).Error; err != nil {
		t.Fatalf("写入留痕记录失败: %v", err)
	}
	return record.ID
}

func inspectionDetailContext(role, building, idParam string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	// URL 固定：id 里的空白等字符不可能出现在解析后的 URL 上，拼进去只会让 httptest.NewRequest 先 panic。
	// 处理器只读 c.Param，所以非法值由 Params 注入才能真正走到判定分支。
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/dorm/inspections/1", nil)
	c.Params = gin.Params{{Key: "id", Value: idParam}}
	c.Set("role", role)
	c.Set("building", building)
	return c, w
}

func decodeDetail(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v / %s", err, w.Body.String())
	}
	return body
}

func TestInspectionDetailConfinesDormManagerToOwnBuilding(t *testing.T) {
	setupInspectionDB(t)
	own := seedInspection(t, "1号楼")
	other := seedInspection(t, "2号楼")
	dorm := &DormController{}

	c, w := inspectionDetailContext(model.RoleDormManager, "1号楼", itoa(own))
	dorm.GetInspectionDetail(c)
	if w.Code != http.StatusOK {
		t.Fatalf("本楼栋记录应可读，实际 %d: %s", w.Code, w.Body.String())
	}

	c, w = inspectionDetailContext(model.RoleDormManager, "1号楼", itoa(other))
	dorm.GetInspectionDetail(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨栋读取必须 404，实际 %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "2号楼") {
		t.Fatalf("越权响应泄漏了别栋数据: %s", w.Body.String())
	}
}

// 详情接口的 404 不能区分"没有这条"和"不是你的"，否则宿管可以用 id 枚举别栋留痕量。
func TestInspectionDetailDoesNotLeakExistence(t *testing.T) {
	setupInspectionDB(t)
	other := seedInspection(t, "2号楼")
	dorm := &DormController{}

	absentCtx, absentW := inspectionDetailContext(model.RoleDormManager, "1号楼", "4242")
	dorm.GetInspectionDetail(absentCtx)

	foreignCtx, foreignW := inspectionDetailContext(model.RoleDormManager, "1号楼", itoa(other))
	dorm.GetInspectionDetail(foreignCtx)

	if absentW.Code != http.StatusNotFound || foreignW.Code != http.StatusNotFound {
		t.Fatalf("两种情况都必须 404，实际 %d / %d", absentW.Code, foreignW.Code)
	}
	if absentW.Body.String() != foreignW.Body.String() {
		t.Fatalf("不存在与越权的响应体必须逐字一致，实际 %q vs %q", absentW.Body.String(), foreignW.Body.String())
	}
	if strings.TrimSpace(foreignW.Body.String()) == "" {
		t.Fatal("越权响应不应为空体")
	}
}

func TestInspectionDetailFullBuildingAndTechAdminIgnoreScope(t *testing.T) {
	setupInspectionDB(t)
	other := seedInspection(t, "2号楼")
	dorm := &DormController{}

	c, w := inspectionDetailContext(model.RoleDormManager, "全楼", itoa(other))
	dorm.GetInspectionDetail(c)
	if w.Code != http.StatusOK {
		t.Fatalf("全楼宿管可读任意楼栋，实际 %d: %s", w.Code, w.Body.String())
	}

	c, w = inspectionDetailContext(model.RoleTechAdmin, "", itoa(other))
	dorm.GetInspectionDetail(c)
	if w.Code != http.StatusOK {
		t.Fatalf("技术维护组可读任意楼栋，实际 %d: %s", w.Code, w.Body.String())
	}
}

func TestInspectionDetailRejectsInvalidID(t *testing.T) {
	setupInspectionDB(t)
	own := seedInspection(t, "1号楼")
	dorm := &DormController{}

	for _, raw := range []string{"abc", "0", "-3", "", "  ", "1e99999", itoa(own) + "x"} {
		c, w := inspectionDetailContext(model.RoleDormManager, "1号楼", raw)
		dorm.GetInspectionDetail(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("id %q 应判 400，实际 %d: %s", raw, w.Code, w.Body.String())
		}
	}
}

func TestInspectionDetailCarriesSubjectsAndLinkedDeductions(t *testing.T) {
	setupInspectionDB(t)
	record := seedInspection(t, "1号楼")
	// 按 甲乙丙 顺序写入，详情应按 id 正序原样回带，界面顺序才与纸条抄录一致。
	for _, name := range []string{"甲同学", "乙同学", "丙同学"} {
		subject := model.InspectionSubject{InspectionID: record, RawName: name, MatchStatus: "matched", ClassName: "高一(3)班"}
		if err := repository.DB.Create(&subject).Error; err != nil {
			t.Fatalf("写入名单条目失败: %v", err)
		}
	}
	// 无关记录的名条目不得串进来
	noise := model.InspectionSubject{InspectionID: record + 99, RawName: "隔壁栋", MatchStatus: "unmatched"}
	if err := repository.DB.Create(&noise).Error; err != nil {
		t.Fatalf("写入干扰数据失败: %v", err)
	}

	confirmed := model.DeductionRecord{
		Building: "1号楼", Floor: "3F", RoomNumber: "302", StudentName: "甲同学", ClassName: "高一(3)班",
		Category: "违规电器", DeductPoints: 5, Reason: "热杯", InspectorName: "李四",
		SourceInspectionID: record, Status: "confirmed",
	}
	revoked := confirmed
	revoked.ID = 0
	revoked.StudentName = "乙同学"
	revoked.Status = "revoked"
	revoked.RevokeReason = "误记"
	for _, item := range []*model.DeductionRecord{&confirmed, &revoked} {
		if err := repository.DB.Create(item).Error; err != nil {
			t.Fatalf("写入打表记录失败: %v", err)
		}
	}
	// 手工录入(无来源留痕)的扣分不得出现在详情里
	stray := model.DeductionRecord{
		Building: "1号楼", Floor: "3F", RoomNumber: "302", StudentName: "无关同学", ClassName: "高一(9)班",
		Category: "迟到", DeductPoints: 1, Reason: "早自习", InspectorName: "李四", Status: "confirmed",
	}
	if err := repository.DB.Create(&stray).Error; err != nil {
		t.Fatalf("写入干扰打表记录失败: %v", err)
	}

	c, w := inspectionDetailContext(model.RoleDormManager, "1号楼", itoa(record))
	dorm := &DormController{}
	dorm.GetInspectionDetail(c)
	if w.Code != http.StatusOK {
		t.Fatalf("详情应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	body := decodeDetail(t, w)

	if total, _ := body["subject_total"].(float64); total != 3 {
		t.Fatalf("名单条数应为 3，实际 %v", body["subject_total"])
	}
	subjects, _ := body["subjects"].([]interface{})
	if len(subjects) != 3 {
		t.Fatalf("名单应回带 3 条，实际 %d", len(subjects))
	}
	names := []string{}
	for _, item := range subjects {
		row, _ := item.(map[string]interface{})
		name, _ := row["raw_name"].(string)
		names = append(names, name)
	}
	if strings.Join(names, ",") != "甲同学,乙同学,丙同学" {
		t.Fatalf("名单应按 id 正序回带，实际 %v", names)
	}

	linked, _ := body["linked_deductions"].([]interface{})
	if len(linked) != 2 {
		t.Fatalf("已关联打表应为 2 条（含撤销），实际 %d: %s", len(linked), w.Body.String())
	}
	statuses := map[string]string{}
	for _, item := range linked {
		brief, _ := item.(map[string]interface{})
		name, _ := brief["student_name"].(string)
		statuses[name] = brief["status"].(string)
		if _, ok := brief["reason"]; ok {
			t.Fatalf("打表简述不应回带事由等冗余字段: %v", brief)
		}
	}
	if statuses["甲同学"] != "confirmed" || statuses["乙同学"] != "revoked" {
		t.Fatalf("打表状态回带错误: %v", statuses)
	}
	if _, ok := statuses["无关同学"]; ok {
		t.Fatal("无来源留痕的手工扣分不得串入详情")
	}
}

// structured_json 是文本 AI 的产物，历史数据里既有空串也有半截 JSON：
// 解析不出来只能回 null，不能把脏数据原样透给界面，也不能让整个详情接口 500。
func TestInspectionDetailStructuredJSON(t *testing.T) {
	cases := map[string]struct {
		raw      string
		wantNull bool
	}{
		"合法归纳":    {raw: `{"category":"违规电器","deduct_points":5,"summary":"寝室使用热杯"}`, wantNull: false},
		"半截 JSON": {raw: `{"category":"违规电器","summ`, wantNull: true},
		"空串":      {raw: ``, wantNull: true},
		"纯空白":     {raw: "   \n\t ", wantNull: true},
		"JSON 数组": {raw: `[1,2,3]`, wantNull: true},
	}
	dorm := &DormController{}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setupInspectionDB(t)
			id := seedInspection(t, "1号楼", map[string]interface{}{"structured_json": tc.raw})
			c, w := inspectionDetailContext(model.RoleDormManager, "1号楼", itoa(id))
			dorm.GetInspectionDetail(c)
			if w.Code != http.StatusOK {
				t.Fatalf("结构化字段脏不应影响详情可读，实际 %d: %s", w.Code, w.Body.String())
			}
			body := decodeDetail(t, w)
			structured := body["structured"]
			if tc.wantNull {
				if structured != nil {
					t.Fatalf("脏结构化数据应回 null，实际 %v", structured)
				}
				return
			}
			got, _ := structured.(map[string]interface{})
			if got["summary"] != "寝室使用热杯" {
				t.Fatalf("合法结构化数据应原样回带，实际 %v", structured)
			}
		})
	}
}

func itoa(id uint) string {
	return strconv.FormatUint(uint64(id), 10)
}
