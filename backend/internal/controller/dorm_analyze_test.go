package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 宿管端流式识别的断言重点不是"识别得准不准"，而是三件事：
// 越权与不存在必须长得一模一样；失败绝不能把记录推进成已识别；
// 以及模型没给推理链时要有如实的提示，而不是拿步骤日志冒充思考过程。

const onePixelPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

func setupAnalyzeDB(t *testing.T) uint {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.InspectionPhoto{}, &model.InspectionSubject{}, &model.AIConfig{}, &model.Student{}, &model.DeductionRecord{}, &model.OperationLog{}); err != nil {
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

	// 限流是进程级全局表，用例之间必须互相隔离
	analysisGate.mu.Lock()
	analysisGate.seen = map[uint]analyzeQuota{}
	analysisGate.mu.Unlock()

	dorm := model.User{Username: "dorm_stream", RealName: "宿管甲", Role: model.RoleDormManager, Building: "7号楼", Status: "active"}
	if err := db.Create(&dorm).Error; err != nil {
		t.Fatalf("写入宿管失败: %v", err)
	}
	return dorm.ID
}

// fakeUpstream 一个 OpenAI 兼容的假上游：带 image_url 的请求按视觉阶段答，其余按文本阶段答。
// 两个阶段的答案都能单独配，方便构造"有推理链""没有推理链""直接报错"三种情况。
type fakeUpstream struct {
	server   *httptest.Server
	vision   string
	text     string
	calls    int32
	status   int
	errBody  string
	baseURL  string
	requests *[]string
}

func (f *fakeUpstream) handler(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&f.calls, 1)
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	if f.requests != nil {
		*f.requests = append(*f.requests, body)
	}
	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
		fmt.Fprint(w, f.errBody)
		return
	}
	answer := f.text
	if strings.Contains(body, "image_url") {
		answer = f.vision
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, answer)
}

func newFakeUpstream(t *testing.T, vision, text string) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{vision: vision, text: text, requests: &[]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handler))
	f.baseURL = f.server.URL + "/chat/completions"
	t.Cleanup(f.server.Close)
	return f
}

func seedEngines(t *testing.T, url string) {
	t.Helper()
	repository.DB.Create(&model.AIConfig{ConfigKey: "vision_engine", DisplayName: "视觉", Endpoint: url, APIKey: "sk-v", ModelName: "v", SystemPrompt: "识别巡查图片", IsEnabled: true})
	repository.DB.Create(&model.AIConfig{ConfigKey: "text_engine", DisplayName: "文本", Endpoint: url, APIKey: "sk-t", ModelName: "t", SystemPrompt: "归纳结构化结论", IsEnabled: true})
}

func seedStreamInspection(t *testing.T, uid uint, status string) uint {
	t.Helper()
	rec := model.InspectionPhoto{
		DormManagerID: uid, ManagerName: "宿管甲", Building: "7号楼", RoomNumber: "701",
		ImageURL: onePixelPNG, PhotoType: "violation", ReportKind: "photo", Status: status, AIStatus: aiStatusPending,
	}
	if err := repository.DB.Create(&rec).Error; err != nil {
		t.Fatalf("写入上报记录失败: %v", err)
	}
	return rec.ID
}

func runAnalyze(t *testing.T, uid, id uint, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/dorm/inspections/analyze", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(id)}}
	c.Set("user_id", uid)
	(&DormController{}).AnalyzeInspection(c)
	return w
}

func loadInspection(t *testing.T, id uint) model.InspectionPhoto {
	t.Helper()
	var rec model.InspectionPhoto
	if err := repository.DB.First(&rec, id).Error; err != nil {
		t.Fatalf("读回上报记录失败: %v", err)
	}
	return rec
}

func sseEventBody(recorder *httptest.ResponseRecorder, event string) string {
	all := sseEventBodies(recorder, event)
	if len(all) == 0 {
		return ""
	}
	return all[0]
}

// sseEventBodies 同名事件可能有多条（失败时先推上游原文，再推带 retryable 的收尾），
// 断言"有没有某个字段"要看全量，不能只取第一条。
func sseEventBodies(recorder *httptest.ResponseRecorder, event string) []string {
	block := "event: " + event + "\ndata: "
	out := []string{}
	rest := recorder.Body.String()
	for {
		i := strings.Index(rest, block)
		if i < 0 {
			return out
		}
		rest = rest[i+len(block):]
		payload := strings.SplitN(rest, "\n\n", 2)[0]
		out = append(out, payload)
		if len(rest) == len(payload) {
			return out
		}
		rest = rest[len(payload):]
	}
}

func TestAnalyzeStreamsReasoningAndPersistsRealResult(t *testing.T) {
	uid := setupAnalyzeDB(t)
	fake := newFakeUpstream(t,
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先看有没有发热源\"}}]}\ndata: {\"choices\":[{\"delta\":{\"content\":\"图片里有插座发黑\"}}]}\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"category\\\":\\\"违规电器\\\",\\\"severity\\\":\\\"high\\\",\\\"deduct_points\\\":5,\\\"summary\\\":\\\"插座发黑\\\",\\\"action_advice\\\":\\\"没收并通知班主任\\\"}\"}}]}\ndata: [DONE]\n\n")
	seedEngines(t, fake.baseURL)
	id := seedStreamInspection(t, uid, "uploaded")

	w := runAnalyze(t, uid, id, `{"room_number":"701"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("响应必须是事件流，实际 %q", ct)
	}
	if !strings.Contains(w.Body.String(), "先看有没有发热源") {
		t.Errorf("视觉阶段的推理原文没推给浏览器：%s", w.Body.String())
	}
	if !strings.Contains(sseEventBody(w, "result"), "没收并通知班主任") {
		t.Errorf("result 事件里该带上处理建议：%s", sseEventBody(w, "result"))
	}
	// gin.H 序列化按字母序排，不能拿写死的键顺序去匹配整段 JSON
	visionDone := false
	for _, e := range sseEventBodies(w, "step") {
		if strings.Contains(e, `"key":"vision"`) && strings.Contains(e, `"status":"done"`) {
			visionDone = true
			if !strings.Contains(e, `"ms":`) {
				t.Errorf("步骤事件要带耗时（ms），实际 %s", e)
			}
		}
	}
	if !visionDone {
		t.Errorf("该有视觉阶段完成事件（带耗时），实际事件：%v", sseEventBodies(w, "step"))
	}

	rec := loadInspection(t, id)
	if rec.AIStatus != "real" || rec.Status != "ai_analyzed" {
		t.Errorf("两个阶段都成功才应记 real/ai_analyzed，实际 %s / %s", rec.AIStatus, rec.Status)
	}
	var structured map[string]interface{}
	if err := json.Unmarshal([]byte(rec.StructuredJSON), &structured); err != nil {
		t.Fatalf("结构化结论没入库: %v", err)
	}
	if structured["action_advice"] != "没收并通知班主任" {
		t.Errorf("处置建议没入库：%v", structured["action_advice"])
	}

	// 已有信息要以请求为准：宿管现场改了寝室号，送模型的就该是新值
	if !strings.Contains((*fake.requests)[0], "701") {
		t.Errorf("视觉请求里该带上本次提交的寝室号")
	}
}

// 模型不给推理链时，要有一条如实说明，且推理链为空不能被前端当成"识别失败"。
func TestAnalyzeNotifiesWhenModelGivesNoReasoning(t *testing.T) {
	uid := setupAnalyzeDB(t)
	fake := newFakeUpstream(t,
		"data: {\"choices\":[{\"delta\":{\"content\":\"插座发黑\"}}]}\ndata: [DONE]\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"category\\\":\\\"违规电器\\\",\\\"severity\\\":\\\"medium\\\",\\\"deduct_points\\\":2,\\\"summary\\\":\\\"发黑\\\",\\\"action_advice\\\":\\\"\\\"}\"}}]}\ndata: [DONE]\n\n")
	seedEngines(t, fake.baseURL)
	id := seedStreamInspection(t, uid, "uploaded")

	w := runAnalyze(t, uid, id, "{}")
	if !strings.Contains(w.Body.String(), "没有输出推理过程") {
		t.Errorf("引擎不吐推理链时应如实提示，不能拿步骤冒充：%s", w.Body.String())
	}
	if rec := loadInspection(t, id); rec.AIStatus != "real" {
		t.Errorf("没有推理链不影响识别成功，实际 %s", rec.AIStatus)
	}
	if !strings.Contains(sseEventBody(w, "result"), `"reasoning_used":false`) {
		t.Errorf("result 要告诉前端这次没拿到推理链：%s", sseEventBody(w, "result"))
	}
}

func TestAnalyzeForbiddenOnOtherManagersRecordAndMissingShareSame404(t *testing.T) {
	uid := setupAnalyzeDB(t)
	// 999 是"另一位宿管"，名下有一条不属于自己的记录
	repository.DB.Create(&model.User{ID: 999, Username: "dorm_other", RealName: "宿管乙", Role: model.RoleDormManager, Status: "active"})
	otherID := seedStreamInspection(t, 999, "uploaded")

	wOther := runAnalyze(t, uid, otherID, "{}")
	wMissing := runAnalyze(t, uid, 424242, "{}")

	if wOther.Code != http.StatusNotFound || wMissing.Code != http.StatusNotFound {
		t.Fatalf("越权与不存在都该 404，实际 %d / %d", wOther.Code, wMissing.Code)
	}
	// 报文逐字相同，否则这个接口会变成"全校哪间寝室出过事"的探测器
	if wOther.Body.String() != wMissing.Body.String() {
		t.Errorf("两种情况的报文必须一字不差，实际 %q vs %q", wOther.Body.String(), wMissing.Body.String())
	}
}

func TestAnalyzeConflictWhenAlreadyConverted(t *testing.T) {
	uid := setupAnalyzeDB(t)
	fake := newFakeUpstream(t, "data: [DONE]\n\n", "data: [DONE]\n\n")
	seedEngines(t, fake.baseURL)
	id := seedStreamInspection(t, uid, "converted")
	repository.DB.Create(&model.InspectionSubject{InspectionID: id, RawName: "学生甲", ConvertedDeductionID: 7})

	w := runAnalyze(t, uid, id, "{}")
	if w.Code != http.StatusConflict {
		t.Fatalf("已转扣分的记录不该再重跑识别，实际 %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "event:") {
		t.Errorf("409 时不应已经开始事件流：%s", w.Body.String())
	}
	if n := atomic.LoadInt32(&fake.calls); n != 0 {
		t.Errorf("409 之前就应挡住，一次模型都不该调用，实际 %d 次", n)
	}
}

// 失败绝不能把记录推进成"已识别"，否则一次跑挂就把没核验的结论洗白。
func TestAnalyzeUpstreamFailureKeepsRecordUnverified(t *testing.T) {
	uid := setupAnalyzeDB(t)
	fake := newFakeUpstream(t, "", "")
	fake.status = http.StatusInternalServerError
	fake.errBody = `{"error":{"message":"upstream blew up"}}`
	seedEngines(t, fake.baseURL)
	id := seedStreamInspection(t, uid, "uploaded")

	w := runAnalyze(t, uid, id, "{}")
	if !strings.Contains(w.Body.String(), "upstream blew up") {
		t.Errorf("上游报错要原样带到界面上：%s", w.Body.String())
	}
	if !strings.Contains(strings.Join(sseEventBodies(w, "error"), "|"), `"retryable":true`) {
		t.Errorf("调用失败应标成可重试：%v", sseEventBodies(w, "error"))
	}
	rec := loadInspection(t, id)
	if rec.AIStatus != "failed" {
		t.Errorf("应记 failed，实际 %s", rec.AIStatus)
	}
	if rec.Status != "uploaded" {
		t.Errorf("失败后 status 必须留在 uploaded，实际 %s", rec.Status)
	}
	if rec.Category != "" || rec.DeductPoints != 0 {
		t.Errorf("失败时不得写入任何结论字段，实际 %q / %d", rec.Category, rec.DeductPoints)
	}
}

// 引擎没配是运维状态，不是故障：记 disabled，且告诉宿管重试没用。
func TestAnalyzeWithoutEnginesReportsDisabled(t *testing.T) {
	uid := setupAnalyzeDB(t)
	id := seedStreamInspection(t, uid, "uploaded")

	w := runAnalyze(t, uid, id, "{}")
	if !strings.Contains(w.Body.String(), "AI 引擎未配置") {
		t.Errorf("未配置要说清楚：%s", w.Body.String())
	}
	if strings.Contains(strings.Join(sseEventBodies(w, "error"), "|"), `"retryable":true`) {
		t.Errorf("没配引擎时重试无意义，不该标可重试：%v", sseEventBodies(w, "error"))
	}
	if rec := loadInspection(t, id); rec.AIStatus != "disabled" {
		t.Errorf("未配置应记 disabled 而不是 failed，实际 %s", rec.AIStatus)
	}
}

func TestAnalyzeRateLimitedPerAccount(t *testing.T) {
	uid := setupAnalyzeDB(t)
	newFakeUpstream(t, "data: [DONE]\n\n", "data: [DONE]\n\n")
	// 指向一个不会通的端点也没关系：限流在调用之前就拦下了
	seedEngines(t, "http://127.0.0.1:1/chat/completions")
	id := seedStreamInspection(t, uid, "uploaded")

	first := runAnalyze(t, uid, id, "{}")
	if first.Code != http.StatusOK {
		t.Fatalf("第一次应放行，实际 %d", first.Code)
	}
	second := runAnalyze(t, uid, id, "{}")
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("60 秒内重复发起应被限流，实际 %d: %s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "间隔") {
		t.Errorf("限流提示要说清要等多久：%s", second.Body.String())
	}
	if loadInspection(t, id).AIStatus == "real" {
		t.Errorf("被限流的那次不该已经把记录标成已识别")
	}
}

func TestAnalyzeRejectsRecordWithoutImage(t *testing.T) {
	uid := setupAnalyzeDB(t)
	rec := model.InspectionPhoto{DormManagerID: uid, ManagerName: "宿管甲", Building: "7号楼", RoomNumber: "702", ReportKind: "text", NoteText: "纯文本申报", Status: "uploaded", AIStatus: aiStatusPending}
	repository.DB.Create(&rec)

	w := runAnalyze(t, uid, rec.ID, "{}")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "没有原图") {
		t.Fatalf("无图记录应拒绝发起识别，实际 %d: %s", w.Code, w.Body.String())
	}
}

// runUpload 用 multipart 发一次上传，与前端真实请求同形。
// 传 imgName 非空时会附一张 1 像素 PNG —— 只有带图才会走到视觉模型那一步。
// handler 把图存到相对路径 ./uploads，所以这里先 chdir 到临时目录，免得在仓库里拉出脏目录。
func runUpload(t *testing.T, uid uint, form map[string]string, imgName string) *httptest.ResponseRecorder {
	t.Helper()
	t.Chdir(t.TempDir())

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range form {
		_ = mw.WriteField(k, v)
	}
	if imgName != "" {
		// CreateFormFile 会把分块的 Content-Type 写成 application/octet-stream，
		// 而上传校验只放行 image/*，所以这里手动造分块并声明 image/png。
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename=%q`, imgName))
		h.Set("Content-Type", "image/png")
		part, err := mw.CreatePart(h)
		if err != nil {
			t.Fatalf("构造图片分块失败: %v", err)
		}
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(onePixelPNG, "data:image/png;base64,"))
		_, _ = part.Write(raw)
	}
	mw.Close()

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/dorm/upload-photo", &body)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	c.Set("user_id", uid)
	c.Set("real_name", "宿管甲")
	c.Set("building", "7号楼")
	(&DormController{}).UploadPhoto(c)
	return w
}

// 上传带 analyze=false：只存档。pending 既不是 disabled（引擎没配）也不是 failed（跑挂了），
// 混用会让宿管端把一个正常的"还没点识别"显示成出错。
func TestUploadWithAnalyzeFalseOnlyStores(t *testing.T) {
	uid := setupAnalyzeDB(t)
	fake := newFakeUpstream(t, "data: [DONE]\n\n", "data: [DONE]\n\n")
	seedEngines(t, fake.baseURL)

	w := runUpload(t, uid, map[string]string{
		"room_number": "705", "photo_type": "violation", "report_kind": "photo",
		"note_text": "705 寝室插座发黑", "analyze": "false",
	}, "shot.png")
	if w.Code != http.StatusOK {
		t.Fatalf("上传应 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if n := atomic.LoadInt32(&fake.calls); n != 0 {
		t.Errorf("analyze=false 时一次模型都不该调用，实际 %d 次", n)
	}
	var rec model.InspectionPhoto
	if err := repository.DB.Order("id desc").First(&rec).Error; err != nil {
		t.Fatalf("记录没落库: %v", err)
	}
	if rec.AIStatus != aiStatusPending || rec.Status != "uploaded" {
		t.Errorf("应记 pending/uploaded，实际 %s / %s", rec.AIStatus, rec.Status)
	}
	if rec.Category != "" || rec.DeductPoints != 0 {
		t.Errorf("没识别就不该有任何结论字段，实际 %q / %d", rec.Category, rec.DeductPoints)
	}
}

// 缺省行为必须原样保留：安卓 APK 走的就是上传即识别这条老路径。
func TestUploadDefaultStillRunsPipeline(t *testing.T) {
	uid := setupAnalyzeDB(t)
	fake := newFakeUpstream(t, "data: [DONE]\n\n", "data: [DONE]\n\n")
	seedEngines(t, fake.baseURL)

	runUpload(t, uid, map[string]string{
		"room_number": "706", "photo_type": "violation", "report_kind": "photo", "note_text": "插座发黑",
	}, "shot.png")
	if n := atomic.LoadInt32(&fake.calls); n == 0 {
		t.Errorf("不带 analyze=false 时仍应调用模型，否则安卓端拿不到任何识别结果")
	}
}
