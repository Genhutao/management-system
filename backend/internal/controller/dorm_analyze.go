package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/ai"
)

// 宿管端识别：由前端带着"图片 + 寝室号 + 已填信息"主动发起，过程用 SSE 一步步推回浏览器。
//
// 为什么不再放在 upload-photo 里同步跑：那条路径要 HTTP 请求一直挂着等两个模型跑完，
// 宿管在手机上看到的是长时间无响应，失败也分不清卡在哪一步。拆开后上传就是上传，
// 识别是可重试、可中途关页面、能看到每步耗时与推理原文的独立动作。

// aiStatusPending 表示这条上报只完成了存档、还没发起识别。
// 必须与 pkg/ai 的 disabled（引擎没配）、failed（跑挂了）、unknown（迁移前的历史行）区分开，
// 否则宿管端会把"还没点识别"显示成"出错了"。
const aiStatusPending = "pending"

// analyzeMinInterval 同一账号两次识别的最小间隔。这是新增的高成本端点（每次真打两个模型），
// 公网部署下没有节流等于任人拿组织的密钥刷 token。
const analyzeMinInterval = 60 * time.Second

// analyzeDailyCap 单账号每日识别次数上限。
const analyzeDailyCap = 200

type analyzeGate struct {
	mu   sync.Mutex
	seen map[uint]analyzeQuota
}

type analyzeQuota struct {
	last  time.Time
	day   string
	count int
}

var analysisGate = analyzeGate{seen: map[uint]analyzeQuota{}}

// reserve 判定该账号现在能不能发起一次识别。返回的 reason 要能直接给宿管看。
// 失败的那次同样计入频次：上游报错也是真花了请求。
func (g *analyzeGate) reserve(uid uint, now time.Time) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	today := now.Format("2006-01-02")
	q := g.seen[uid]
	if q.day != today {
		q = analyzeQuota{day: today}
	}
	if q.count >= analyzeDailyCap {
		g.seen[uid] = q
		return false, fmt.Sprintf("今天已识别 %d 次，达到上限；需要更多请联系技术维护组", analyzeDailyCap)
	}
	if !q.last.IsZero() && now.Sub(q.last) < analyzeMinInterval {
		wait := int((analyzeMinInterval - now.Sub(q.last)).Seconds()) + 1
		g.seen[uid] = q
		return false, fmt.Sprintf("两次识别之间请间隔 %d 秒", wait)
	}
	q.last = now
	q.count++
	g.seen[uid] = q
	return true, ""
}

// sseStart 把响应切成事件流。X-Accel-Buffering 必须显式关：
// 否则 nginx 会把整个流攒到最后一次下发，宿管看到的仍然是"转圈很久然后一起蹦出来"。
func sseStart(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()
}

func sseSend(c *gin.Context, event string, data interface{}) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}

// AnalyzeInspection 对一条已上传的留痕记录发起识别，流式返回过程。
// 权限沿用人工纠正那套：只能是自己上报的记录；已转扣分的不予重跑；
// 调用失败一律如实记 failed，绝不因为"跑过一次"就把结论洗成 AI 已核验。
func (d *DormController) AnalyzeInspection(c *gin.Context) {
	uid := c.GetUint("user_id")

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err == nil && id == 0 {
		err = errors.New("id 不得为 0")
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "上报编号不对"})
		return
	}

	var record model.InspectionPhoto
	// "不存在"与"不是你的记录"合并成同一个 404、报文逐字相同，
	// 否则这个接口就变成全校留痕的存在性探测器。
	if e := repository.DB.First(&record, id).Error; e != nil || record.DormManagerID != uid {
		c.JSON(http.StatusNotFound, gin.H{"error": "上报记录不存在"})
		return
	}

	var convertedCount int64
	repository.DB.Model(&model.InspectionSubject{}).
		Where("inspection_id = ? AND converted_deduction_id > 0", record.ID).Count(&convertedCount)
	if record.Status == "converted" || convertedCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该上报已转入打表，不再重跑识别"})
		return
	}
	if strings.TrimSpace(record.ImageURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这条上报没有原图，无法发起识别"})
		return
	}

	if allowed, reason := analysisGate.reserve(uid, time.Now()); !allowed {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": reason})
		return
	}

	// 已有信息以这次请求为准：宿管在现场改了寝室号或补了备注，识别就该按新的来
	var req struct {
		RoomNumber string `json:"room_number"`
		Building   string `json:"building"`
		PhotoType  string `json:"photo_type"`
		NoteText   string `json:"note_text"`
	}
	_ = c.ShouldBindJSON(&req)
	if room := strings.TrimSpace(req.RoomNumber); room != "" {
		record.RoomNumber = room
	}
	if b := strings.TrimSpace(req.Building); b != "" {
		record.Building = b
	}
	if pt := strings.TrimSpace(req.PhotoType); pt != "" {
		record.PhotoType = pt
	}
	if note := normalizeNoteText(strings.TrimSpace(req.NoteText)); note != "" {
		record.NoteText = note
	}

	var visionCfg, textCfg model.AIConfig
	repository.DB.Where("config_key = ?", "vision_engine").First(&visionCfg)
	repository.DB.Where("config_key = ?", "text_engine").First(&textCfg)

	sseStart(c)
	step := func(key, label, status string, ms int64, detail string) {
		_ = sseSend(c, "step", gin.H{"key": key, "label": label, "status": status, "ms": ms, "detail": detail})
	}

	// 1. 视觉阶段：图片 + 寝室号 + 宿管备注一起送，识别才不是凭空猜
	step("vision", "识别图片内容", "start", 0, "")
	visionInstr := fmt.Sprintf("请分析这张宿管上传的巡查图片（类型：%s，寝室：%s）。请详细提取翻译识别出的安全隐患或上工情况。",
		record.PhotoType, record.RoomNumber)
	if strings.TrimSpace(record.NoteText) != "" {
		visionInstr += "\n宿管随图附上的说明：" + record.NoteText
	}

	visionStart := time.Now()
	visionOut, visionReasoned, err := streamStage(c, "vision", func(emit func(ai.StreamEvent) error) (*ai.StreamOutcome, error) {
		return ai.StreamVisionWith(&visionCfg, record.ImageURL, visionInstr, emit)
	})
	visionMS := time.Since(visionStart).Milliseconds()
	if err != nil {
		d.finishAnalyzeFailed(c, &record, "vision", "识别图片内容", err, step)
		return
	}
	step("vision", "识别图片内容", "done", visionMS, clipRunes(visionOut, 80))

	// 2. 文本归纳阶段
	step("text", "归纳成结构化结论", "start", 0, "")
	textInstr := textCfg.SystemPrompt + "\n必须只输出JSON格式：" +
		`{"category":"...","severity":"low/medium/high/critical","deduct_points":0,"summary":"...","action_advice":"..."}` +
		"\naction_advice 是给部员和宿管的处理建议；现场信息不足以判断怎么处置时，该项返回空字符串，不要编。"

	textStart := time.Now()
	structuredRaw, textReasoned, err := streamStage(c, "text", func(emit func(ai.StreamEvent) error) (*ai.StreamOutcome, error) {
		return ai.StreamTextWith(&textCfg, textInstr, visionOut, emit)
	})
	textMS := time.Since(textStart).Milliseconds()
	if err != nil {
		d.finishAnalyzeFailed(c, &record, "text", "归纳成结构化结论", err, step)
		return
	}

	var structured ai.StructuredDeductResult
	if e := json.Unmarshal([]byte(structuredRaw), &structured); e != nil {
		d.finishAnalyzeFailed(c, &record, "text", "归纳成结构化结论",
			fmt.Errorf("模型返回的内容没法解析成结论：%s", clipRunes(structuredRaw, 120)), step)
		return
	}

	// 3. 两个阶段都成功才允许写 ai_status=real，并把结论落库
	structuredJSON, _ := json.Marshal(structured)
	record.StructuredJSON = string(structuredJSON)
	record.Category = structured.Category
	record.DeductPoints = structured.DeductPoints
	record.Severity = structured.Severity
	record.VisionAIOutput = visionOut
	record.AIStatus = ai.StatusReal
	record.Status = "ai_analyzed"
	record.Building = strings.TrimSpace(record.Building)
	now := time.Now()
	record.ProcessedAt = &now
	if e := repository.DB.Save(&record).Error; e != nil {
		_ = sseSend(c, "error", gin.H{"text": "识别结果保存失败，请重试"})
		return
	}

	step("text", "归纳成结构化结论", "done", textMS, clipRunes(structuredRaw, 80))

	// 推理链是模型给的，不是我们拼的。没收到过就必须让界面如实说"这个引擎不输出推理链"，
	// 拿步骤日志冒充思考过程是这轮最不能出错的地方。
	if !visionReasoned && !textReasoned {
		_ = sseSend(c, "notice", gin.H{"text": "当前引擎没有输出推理过程，只能看到各步耗时。要真·思考链需要换支持深度思考输出的模型。"})
	}

	_ = sseSend(c, "result", gin.H{
		"inspection_id":  record.ID,
		"category":       structured.Category,
		"severity":       structured.Severity,
		"deduct_points":  structured.DeductPoints,
		"summary":        structured.Summary,
		"action_advice":  structured.ActionAdvice,
		"tags":           structured.Tags,
		"ai_status":      record.AIStatus,
		"room_number":    record.RoomNumber,
		"building":       record.Building,
		"reasoning_used": visionReasoned || textReasoned,
	})
	_ = sseSend(c, "done", gin.H{"total_ms": visionMS + textMS, "ai_status": record.AIStatus})
}

// streamStage 跑一个流式阶段：推理与正文增量实时转成事件推给浏览器，返回汇总文本与该阶段是否收到过推理。
func streamStage(c *gin.Context, stage string, call func(func(ai.StreamEvent) error) (*ai.StreamOutcome, error)) (string, bool, error) {
	out, err := call(func(e ai.StreamEvent) error {
		switch e.Type {
		case ai.EventTypeReasoning:
			return sseSend(c, "reasoning", gin.H{"stage": stage, "text": e.Text})
		case ai.EventTypeContent:
			return sseSend(c, "output", gin.H{"stage": stage, "text": e.Text})
		case ai.EventTypeError:
			return sseSend(c, "error", gin.H{"stage": stage, "text": e.Text})
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	text := out.Content
	if strings.TrimSpace(text) == "" {
		text = out.Reasoning
	}
	return text, out.ReasoningSeen, nil
}

// finishAnalyzeFailed 失败收尾：status 一定留在 uploaded ——
// "跑过一次但没成功"和"已核验"在数据上必须是两回事，否则重跑一次失败调用就能把结论洗白。
// 未配置与调用失败要分开记：前者是运维状态（写 disabled），后者是故障（写 failed），
// 混成一个 failed 会让技术组去查一个根本不存在的故障。
func (d *DormController) finishAnalyzeFailed(c *gin.Context, record *model.InspectionPhoto, key, label string, cause error, step func(string, string, string, int64, string)) {
	notConfigured := errors.Is(cause, ai.ErrNotConfigured)
	status := ai.StatusFailed
	if notConfigured {
		status = ai.StatusDisabled
	}
	repository.DB.Model(&model.InspectionPhoto{}).Where("id = ?", record.ID).Updates(map[string]interface{}{
		"ai_status": status,
		"status":    "uploaded",
	})
	record.AIStatus = status
	step(key, label, "error", 0, clipRunes(cause.Error(), 120))

	msg := cause.Error()
	if notConfigured {
		msg = "AI 引擎未配置，本次没有进行任何识别。请联系技术维护组。"
	}
	_ = sseSend(c, "error", gin.H{"text": msg, "retryable": !notConfigured})
	_ = sseSend(c, "done", gin.H{"ai_status": status})
}

func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
