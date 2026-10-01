package controller

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service/rosterparse"
)

// 名单导入的控制器侧口径：字节读取的失败必须如实透出、缺字段的行绝不能进 all_parsed、
// 逐列下拉记住的映射必须能在下一次导入同一张表时自动套用。
// 解析细节由 internal/service/rosterparse 自己覆盖，这里只测接线。

type previewPayloadShape struct {
	TotalRecognized int                  `json:"total_recognized"`
	BlockedCount    int                  `json:"blocked_count"`
	GradeStats      map[string]int       `json:"grade_stats"`
	PreviewSample   []rosterparse.Record `json:"preview_sample"`
	AllParsed       []rosterparse.Record `json:"all_parsed"`
	Issues          []rosterparse.Issue  `json:"issues"`
	MappingSource   string               `json:"mapping_source"`
	Fingerprint     string               `json:"fingerprint"`
	FieldOptions    []struct {
		Value    string `json:"value"`
		Label    string `json:"label"`
		Required bool   `json:"required"`
	} `json:"field_options"`
	Error string `json:"error"`
}

func newMultipartParseRequest(t *testing.T, filename string, content []byte, form map[string]string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if filename != "" {
		part, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("构造 multipart 失败: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("写入文件内容失败: %v", err)
		}
	}
	for k, v := range form {
		if err := w.WriteField(k, v); err != nil {
			t.Fatalf("写入表单字段失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/parse-preview", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	return rec, c
}

func newJSONParseRequest(t *testing.T, payload map[string]any) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化请求体失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/students/parse-preview", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	return rec, c
}

func decodePreview(t *testing.T, rec *httptest.ResponseRecorder) previewPayloadShape {
	t.Helper()
	var payload previewPayloadShape
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v / body=%q", err, rec.Body.String())
	}
	return payload
}

func setupRosterDB(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Student{}, &model.RosterColumnMapping{}, &model.OperationLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if err := db.Create(&model.User{ID: 1, Username: "tech_admin", RealName: "测试员-技术组", Role: model.RoleTechAdmin}).Error; err != nil {
		t.Fatalf("预置账号失败: %v", err)
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

// 空文件不能被当成"没有上传文件"，否则最终报"请提供文件或粘贴文本"，把人引到错误方向。
func TestParseAndPreviewRejectsEmptyUpload(t *testing.T) {
	rec, c := newMultipartParseRequest(t, "roster.csv", nil, nil)
	new(StudentController).ParseAndPreview(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空文件必须 400，实际 %d / %s", rec.Code, rec.Body.String())
	}
	payload := decodePreview(t, rec)
	if !strings.Contains(payload.Error, "roster.csv") {
		t.Fatalf("错误要点名是哪个文件，实际 %q", payload.Error)
	}
}

// 超过体积上限要直接拒绝，不做半份解析。
func TestParseAndPreviewRejectsOversizeUpload(t *testing.T) {
	oversize := bytes.Repeat([]byte("1号楼,301,李华,高一(1)班\n"), maxRosterBytes/16+64)
	rec, c := newMultipartParseRequest(t, "huge.csv", oversize, nil)
	new(StudentController).ParseAndPreview(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("超大文件必须 400，实际 %d", rec.Code)
	}
	if payload := decodePreview(t, rec); !strings.Contains(payload.Error, "上限") {
		t.Fatalf("错误要说明是体积上限，实际 %q", payload.Error)
	}
}

// 正常上传要能走通，且响应保留既有四个键。
func TestParseAndPreviewParsesNormalUpload(t *testing.T) {
	setupRosterDB(t)
	content := []byte("楼栋\t寝室\t姓名\t班级\t性别\n1号楼\t301\t李华\t高一(1)班\t男\n2号楼\t405\t王芳\t高二(3)班\t女\n")
	rec, c := newMultipartParseRequest(t, "roster.tsv", content, nil)
	new(StudentController).ParseAndPreview(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("正常名单应 200，实际 %d / %s", rec.Code, rec.Body.String())
	}
	payload := decodePreview(t, rec)
	if payload.TotalRecognized != 2 {
		t.Fatalf("应识别 2 人，实际 %d", payload.TotalRecognized)
	}
	if len(payload.AllParsed) != 2 || payload.AllParsed[0].Gender != "男" {
		t.Fatalf("all_parsed 结构与取值异常: %+v", payload.AllParsed)
	}
	if payload.MappingSource != "auto" {
		t.Fatalf("未记住过的表应报 auto，实际 %q", payload.MappingSource)
	}
	if payload.Fingerprint == "" {
		t.Fatal("必须返回表头指纹，否则前端无法保存列映射")
	}
	if len(payload.FieldOptions) != len(rosterparse.SupportedFields) {
		t.Fatalf("字段下拉选项应与内核一致，实际 %d", len(payload.FieldOptions))
	}
}

// 缺字段的行绝不进 all_parsed——"确认入库"提交的就是这个数组。
func TestParseAndPreviewKeepsBlockedRowsOutOfImport(t *testing.T) {
	setupRosterDB(t)
	text := "1号楼 301 李华 高一(1)班\n302 张明\n"
	rec, c := newJSONParseRequest(t, map[string]any{"raw_text": text})
	new(StudentController).ParseAndPreview(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d / %s", rec.Code, rec.Body.String())
	}
	payload := decodePreview(t, rec)
	if payload.TotalRecognized != 1 || payload.BlockedCount != 1 {
		t.Fatalf("应 1 条可入库 + 1 条待补，实际 %+v", payload)
	}
	for _, r := range payload.AllParsed {
		if r.Blocked() {
			t.Fatalf("待补的行混进了 all_parsed: %+v", r)
		}
	}
	var hasMissing bool
	for _, is := range payload.Issues {
		if is.Code == rosterparse.CodeMissingField {
			hasMissing = true
		}
	}
	if !hasMissing {
		t.Fatalf("必须返回逐行缺字段诊断，实际 %+v", payload.Issues)
	}
}

// 记住列映射 → 下次导入同一张表自动套用。
// 用的是自动识别认不出表头的那类表：这正是"记住"最有价值的场景。
func TestColumnMappingSavedThenReused(t *testing.T) {
	setupRosterDB(t)
	const sample = "列1\t列2\t列3\t列4\nN\t101\t杨健\t高三(1)班\n"

	rec, c := newJSONParseRequest(t, map[string]any{"raw_text": sample})
	new(StudentController).ParseAndPreview(c)
	first := decodePreview(t, rec)
	if first.MappingSource != "auto" {
		t.Fatalf("首次解析应报 auto，实际 %q", first.MappingSource)
	}
	if first.TotalRecognized != 0 || first.BlockedCount != 1 {
		t.Fatalf("未指定列序时该行应因缺楼栋被拦下，实际 %+v", first)
	}
	if first.Fingerprint == "" {
		t.Fatal("自动识别失败也必须有指纹，否则记不住映射")
	}

	// 保存逐列下拉指定的结果
	saveReq := map[string]any{
		"fingerprint":   first.Fingerprint,
		"header_labels": []string{"列1", "列2", "列3", "列4"},
		"header_line":   1,
		"column_map": map[string]int{
			"building": 0, "room_number": 1, "real_name": 2, "class_name": 3,
		},
	}
	raw, _ := json.Marshal(saveReq)
	saveHTTP := httptest.NewRequest(http.MethodPost, "/api/v1/students/column-mapping", bytes.NewReader(raw))
	saveHTTP.Header.Set("Content-Type", "application/json")
	saveRec := httptest.NewRecorder()
	saveCtx, _ := gin.CreateTestContext(saveRec)
	saveCtx.Request = saveHTTP
	saveCtx.Set("user_id", uint(1))
	new(StudentController).SaveColumnMapping(saveCtx)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("保存列映射应 200，实际 %d / %s", saveRec.Code, saveRec.Body.String())
	}

	// 再导同一张表：不带 column_map，应自动套用
	rec2, c2 := newJSONParseRequest(t, map[string]any{"raw_text": sample})
	new(StudentController).ParseAndPreview(c2)
	second := decodePreview(t, rec2)
	if second.MappingSource != "saved" {
		t.Fatalf("第二次解析应报 saved，实际 %q", second.MappingSource)
	}
	if second.TotalRecognized != 1 || second.BlockedCount != 0 {
		t.Fatalf("套用映射后该行应可入库，实际 %+v", second)
	}
	if got := second.AllParsed[0].Building; got != "N号楼" {
		t.Fatalf("楼栋应按指定列取值并归一，实际 %q", got)
	}

	var stored model.RosterColumnMapping
	if err := repository.DB.Where("fingerprint = ?", first.Fingerprint).First(&stored).Error; err != nil {
		t.Fatalf("映射应已落库: %v", err)
	}
	if stored.UseCount < 1 {
		t.Fatalf("套用次数应被记录，实际 %d", stored.UseCount)
	}
}

// 指纹不合法或映射为空时不得落库，否则会记住一条永远命中不了或命中了也没用的行。
func TestSaveColumnMappingRejectsJunk(t *testing.T) {
	setupRosterDB(t)

	post := func(payload map[string]any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/students/column-mapping", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = req
		c.Set("user_id", uint(1))
		new(StudentController).SaveColumnMapping(c)
		return rec
	}

	if code := post(map[string]any{"fingerprint": "abc", "column_map": map[string]int{"real_name": 0}}).Code; code != http.StatusBadRequest {
		t.Fatalf("短指纹必须 400，实际 %d", code)
	}
	if code := post(map[string]any{"fingerprint": strings.Repeat("g", 64), "column_map": map[string]int{"real_name": 0}}).Code; code != http.StatusBadRequest {
		t.Fatalf("非十六进制指纹必须 400，实际 %d", code)
	}
	if code := post(map[string]any{"fingerprint": strings.Repeat("a", 64), "column_map": map[string]int{"real_name": -1}}).Code; code != http.StatusBadRequest {
		t.Fatalf("全是不导入的映射必须 400，实际 %d", code)
	}
	var count int64
	repository.DB.Model(&model.RosterColumnMapping{}).Count(&count)
	if count != 0 {
		t.Fatalf("非法请求不应留下任何记录，实际 %d 条", count)
	}
}
