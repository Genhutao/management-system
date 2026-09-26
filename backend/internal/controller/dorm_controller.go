package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
	"xgh-system/pkg/ai"
)

type DormController struct{}

// GetTodayTasks 宿管工作台核心：工作时间自动推送当前监督与查寝任务
func (d *DormController) GetTodayTasks(c *gin.Context) {
	managerBuilding, _ := c.Get("building")
	buildingStr := managerBuilding.(string)
	todayStr := time.Now().Format("2006-01-02")

	// 查找今日对应楼栋的排班记录
	var shifts []model.ScheduleShift
	query := repository.DB.Where("date = ?", todayStr)
	if buildingStr != "" && buildingStr != "全楼" {
		query = query.Where("building LIKE ?", "%"+buildingStr+"%")
	}
	query.Find(&shifts)

	// 判断当前是否在工作时间区间（如 18:30 - 22:30 或午检时段）
	currentHour := time.Now().Hour()
	isWorkTime := (currentHour >= 12 && currentHour <= 13) || (currentHour >= 18 && currentHour <= 22)

	// 构造待办卡片推送
	type PushCard struct {
		ID          uint   `json:"id"`
		Title       string `json:"title"`
		Period      string `json:"period"`
		Building    string `json:"building"`
		DutyMembers string `json:"duty_members"`
		IsWorkTime  bool   `json:"is_work_time"`
		Priority    string `json:"priority"` // urgent, normal
		ActionType  string `json:"action_type"` // take_photo_supervise, take_photo_check
		PromptText  string `json:"prompt_text"`
	}

	var pushCards []PushCard
	for _, s := range shifts {
		priority := "normal"
		prompt := "今日已安排巡检部员上岗，请宿管协同开门并核验身份"
		if isWorkTime {
			priority = "urgent"
			prompt = fmt.Sprintf("【当前工作时间提醒】部员 %s 正在本楼巡查，请点击拍摄监督照记录履职情况！", s.MemberNames)
		}
		pushCards = append(pushCards, PushCard{
			ID:          s.ID,
			Title:       "晚查寝与宿舍安全巡查监督",
			Period:      s.ShiftPeriod,
			Building:    s.Building,
			DutyMembers: s.MemberNames,
			IsWorkTime:  isWorkTime,
			Priority:    priority,
			ActionType:  "take_photo_supervise",
			PromptText:  prompt,
		})
	}

	// 如果今日没有预先排班，生成一个通用的快捷拍照上报卡片
	if len(pushCards) == 0 {
		pushCards = append(pushCards, PushCard{
			ID:          0,
			Title:       "日常宿舍安全与卫生快速巡检",
			Period:      "全天随时响应",
			Building:    buildingStr,
			DutyMembers: "本楼值班宿管",
			IsWorkTime:  isWorkTime,
			Priority:    "normal",
			ActionType:  "take_photo_check",
			PromptText:  "发现违规电器、私拉电线、垃圾杂物，请随时点击大按钮拍照上传 AI 自动分析入库",
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"is_in_work_time": isWorkTime,
		"current_time":    time.Now().Format("15:04:05"),
		"cards":           pushCards,
		"building":        buildingStr,
	})
}

// UploadPhoto 宿管上报：现场实拍 / 记名纸条 / 纯文本三轨，附带照片时触发双 AI 流水线
func (d *DormController) UploadPhoto(c *gin.Context) {
	userID := c.GetUint("user_id")
	realName, _ := c.Get("real_name")
	building, _ := c.Get("building")

	roomNumber := strings.TrimSpace(c.PostForm("room_number"))
	photoType := c.PostForm("photo_type") // sanitation, violation, duty_supervise
	if photoType == "" {
		photoType = "violation"
	}
	targetBuilding := strings.TrimSpace(c.PostForm("building"))
	if targetBuilding == "" {
		targetBuilding, _ = building.(string)
	}

	noteText := normalizeNoteText(c.PostForm("note_text"))
	submittedNames := c.PostForm("subject_names") // 换行或顿号分隔，留空则自动从 note_text 拆分

	file, fileErr := c.FormFile("image")
	hasImage := fileErr == nil
	if hasImage {
		// D-4 上传限制：必须为图片且不超过 8MB
		if file.Size > 8<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "图片超过 8MB 上限，请压缩后重拍"})
			return
		}
		if ct := file.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "留痕材料必须为图片文件"})
			return
		}
	}
	reportKind := normalizeReportKind(c.PostForm("report_kind"), hasImage, noteText)

	// 实拍与纸条都必须留原图；只有宿管明确以纯文本申报时才允许无图
	if !hasImage && reportKind != "text" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未接收到有效的现场原图：实拍与记名纸条两种上报都必须附照片"})
		return
	}

	imageURL := ""
	if hasImage {
		uploadDir := "./uploads"
		if err := os.MkdirAll(uploadDir, 0755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "上传目录初始化失败: " + err.Error()})
			return
		}
		filename := fmt.Sprintf("%s_%s", uuid.New().String()[:8], filepath.Base(file.Filename))
		savePath := filepath.Join(uploadDir, filename)
		if err := c.SaveUploadedFile(file, savePath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "现场照片保存失败: " + err.Error()})
			return
		}
		imageURL = "/uploads/" + filename
	}

	if roomNumber == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写寝室号，名单需要与宿位名册核对"})
		return
	}

	// 1. 读取技术维护组配置的 AI 引擎
	var visionCfg model.AIConfig
	var textCfg model.AIConfig
	repository.DB.Where("config_key = ?", "vision_engine").First(&visionCfg)
	repository.DB.Where("config_key = ?", "text_engine").First(&textCfg)

	// 2. 有原图才触发 AI；无图或调用失败时 structured 为 nil，禁止写入任何模拟结论
	rawVision, structured, aiStatus, aiErr := ai.ProcessMultimodalAndText(imageURL, photoType, roomNumber, &visionCfg, &textCfg)

	// 3. 拆名册：显式名单优先，否则从申报正文里拆分
	names := splitNames(subjectSubmittedNamesOr(submittedNames, noteText))

	record := model.InspectionPhoto{
		DormManagerID:  userID,
		ManagerName:    toString(realName),
		Building:       targetBuilding,
		RoomNumber:     roomNumber,
		ImageURL:       imageURL,
		PhotoType:      photoType,
		ReportKind:     reportKind,
		NoteText:       noteText,
		AIStatus:       aiStatus,
		VisionAIOutput: rawVision,
		Status:         "uploaded",
		CreatedAt:      time.Now(),
	}
	if structured != nil {
		structuredJSONBytes, _ := json.Marshal(structured)
		record.StructuredJSON = string(structuredJSONBytes)
		record.Category = structured.Category
		record.DeductPoints = structured.DeductPoints
		record.Severity = structured.Severity
		record.Status = "ai_analyzed"
		now := time.Now()
		record.ProcessedAt = &now
	}

	matchedCount := 0
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		for _, rawName := range names {
			studentID, className, matchStatus, matchNote := service.MatchSubjectInRoom(targetBuilding, roomNumber, rawName)
			if matchStatus == service.MatchMatched {
				matchedCount++
			}
			subject := model.InspectionSubject{
				InspectionID: record.ID,
				RawName:      rawName,
				StudentID:    studentID,
				ClassName:    className,
				MatchStatus:  matchStatus,
				MatchNote:    matchNote,
				CreatedAt:    time.Now(),
			}
			if err := tx.Create(&subject).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "留痕记录入库失败: " + err.Error()})
		return
	}

	aiMessage := "现场照片已留痕入库。"
	if aiErr != nil {
		aiMessage = fmt.Sprintf("现场照片已留痕入库；%s，需技术部副部长人工看图核对。", aiErr.Error())
	} else if structured != nil {
		aiMessage = "图片上传成功，AI 双引擎已完成识别与结构化归纳。"
	}

	c.JSON(http.StatusOK, gin.H{
		"message":           aiMessage,
		"record":            record,
		"ai_status":         aiStatus,
		"vision_analysis":   rawVision,
		"structured_result": structured,
		"subject_total":     len(names),
		"subject_matched":   matchedCount,
		"subject_unmatched": len(names) - matchedCount,
	})
}

// InspectionCorrection 宿管人工纠正 AI 识别结论的入参。
// 全部用指针：只有浏览器确实提交了该字段才参与改写，未提交的字段保持原值。
type InspectionCorrection struct {
	VisionAnalysis *string `json:"vision_analysis"`
	Category       *string `json:"category"`
	Severity       *string `json:"severity"`
	DeductPoints   *int    `json:"deduct_points"`
	Summary        *string `json:"summary"`
	ActionAdvice   *string `json:"action_advice"`
	Reason         string  `json:"reason"`
}

var allowedSeverity = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}

// CorrectInspectionAnalysis 现场巡查隐患 / 上工监督拍照在 AI 完成识别后，由宿管人工纠正结论。
//
// 三条不可让的边界：
//  1. 只有上报者本人（技术维护组例外）能改，且已转打表的记录一律拒绝——改判要走撤销打表流程；
//  2. ai_status 一个字都不动。它记录的是"模型是否真的跑成功"，人工改写结论不得冒充模型结论，
//     否则打表侧的 aiVerified 判定就被绕过了；
//  3. 每次纠正都在 review_note 追加带时间与差异的留痕，并写审计。
func (d *DormController) CorrectInspectionAnalysis(c *gin.Context) {
	userID := c.GetUint("user_id")
	role := ""
	if r, ok := c.Get("role"); ok {
		role, _ = r.(string)
	}
	realName, _ := c.Get("real_name")

	var targetID uint
	if _, err := fmt.Sscanf(c.Param("id"), "%d", &targetID); err != nil || targetID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "记录编号无效"})
		return
	}

	var record model.InspectionPhoto
	if err := repository.DB.First(&record, targetID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "留痕记录不存在"})
		return
	}
	if role != model.RoleTechAdmin && record.DormManagerID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能纠正本人上报的留痕记录；他人记录请走复核流程"})
		return
	}
	if record.Status == "converted" {
		c.JSON(http.StatusConflict, gin.H{"error": "该上报已转打表，不能在此改判。请先按流程撤销打表记录，再回来纠正。"})
		return
	}

	var req InspectionCorrection
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式错误: " + err.Error()})
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "必须填写纠正理由（将随记录永久留痕）"})
		return
	}
	if len([]rune(reason)) > 500 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "纠正理由不能超过 500 字"})
		return
	}

	// structured_json 与散列字段必须同步，否则打表面板读到的与详情页展示的会是两份结论
	var structured ai.StructuredDeductResult
	if strings.TrimSpace(record.StructuredJSON) != "" {
		_ = json.Unmarshal([]byte(record.StructuredJSON), &structured)
	}

	var changes []string
	applyStr := func(label, newVal string, dst *string, maxRunes int) {
		if strings.TrimSpace(newVal) == "" {
			return
		}
		if len([]rune(newVal)) > maxRunes {
			newVal = string([]rune(newVal)[:maxRunes])
		}
		if *dst != newVal {
			changes = append(changes, fmt.Sprintf("%s：%s → %s", label, orNone(*dst), newVal))
			*dst = newVal
		}
	}

	if req.VisionAnalysis != nil {
		applyStr("现场描述", strings.TrimSpace(*req.VisionAnalysis), &record.VisionAIOutput, 4000)
	}
	if req.Category != nil {
		applyStr("隐患类别", strings.TrimSpace(*req.Category), &structured.Category, 64)
		record.Category = structured.Category
	}
	if req.Severity != nil {
		sev := strings.ToLower(strings.TrimSpace(*req.Severity))
		if sev != "" && !allowedSeverity[sev] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "严重度只允许 low / medium / high / critical"})
			return
		}
		applyStr("严重度", sev, &structured.Severity, 16)
		record.Severity = structured.Severity
	}
	if req.DeductPoints != nil {
		if *req.DeductPoints < 0 || *req.DeductPoints > 30 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "建议扣分只允许 0~30 分"})
			return
		}
		if record.DeductPoints != *req.DeductPoints {
			changes = append(changes, fmt.Sprintf("建议扣分：%d → %d", record.DeductPoints, *req.DeductPoints))
			record.DeductPoints = *req.DeductPoints
			structured.DeductPoints = *req.DeductPoints
		}
	}
	if req.Summary != nil {
		applyStr("归纳摘要", strings.TrimSpace(*req.Summary), &structured.Summary, 2000)
	}
	if req.ActionAdvice != nil {
		applyStr("处置建议", strings.TrimSpace(*req.ActionAdvice), &structured.ActionAdvice, 1000)
	}

	if len(changes) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有检测到任何改动：请先修改识别结论再提交"})
		return
	}

	structuredJSONBytes, _ := json.Marshal(&structured)
	record.StructuredJSON = string(structuredJSONBytes)
	record.Status = "manual_corrected"
	now := time.Now()
	record.ProcessedAt = &now
	record.ReviewNote = appendCorrectionNote(record.ReviewNote, toString(realName), reason, changes)

	if err := repository.DB.Save(&record).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "纠正结果保存失败: " + err.Error()})
		return
	}

	operator, _ := operatorFromContext(c)
	logOperationAs(c, operator, "dorm.inspection.correct", "inspection_photo", record.ID,
		fmt.Sprintf("人工纠正识别结论：%s（理由：%s）", strings.Join(changes, "；"), reason))

	c.JSON(http.StatusOK, gin.H{
		"message":           "识别结论已按宿管人工纠正结果入库，后续打表以本条为准。",
		"record":            record,
		"structured_result": structured,
		"changes":           changes,
		"ai_status":         record.AIStatus,
	})
}

// appendCorrectionNote 把一次纠正追加到复核留痕尾部，保留此前所有历史留痕。
func appendCorrectionNote(existing, operatorName, reason string, changes []string) string {
	line := fmt.Sprintf("[%s] %s 人工纠正： %s（理由：%s）",
		time.Now().Format("2006-01-02 15:04"), orNone(operatorName), strings.Join(changes, "；"), reason)
	if strings.TrimSpace(existing) == "" {
		return line
	}
	return existing + "\n" + line
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（空）"
	}
	return s
}

// GetInspections 宿管查看所辖楼栋历史上传的扣分/违规照片瀑布流
func (d *DormController) GetInspections(c *gin.Context) {
	buildingVal, _ := c.Get("building")
	roleVal, _ := c.Get("role")
	buildingStr := buildingVal.(string)
	roleStr := roleVal.(string)

	var list []model.InspectionPhoto
	query := repository.DB.Order("created_at desc")

	// 宿管仅查看本楼栋
	if roleStr == model.RoleDormManager && buildingStr != "" && buildingStr != "全楼" {
		query = query.Where("building LIKE ?", "%"+buildingStr+"%")
	}

	// 支持筛选
	if cat := c.Query("category"); cat != "" {
		query = query.Where("category = ?", cat)
	}
	if sev := c.Query("severity"); sev != "" {
		query = query.Where("severity = ?", sev)
	}

	query.Limit(50).Find(&list)

	c.JSON(http.StatusOK, gin.H{
		"total": len(list),
		"items": list,
	})
}

// LinkedDeductionBrief 留痕详情里回带的打表关联，只给核对所需的最小字段。
type LinkedDeductionBrief struct {
	ID           uint   `json:"id"`
	StudentName  string `json:"student_name"`
	ClassName    string `json:"class_name"`
	Category     string `json:"category"`
	DeductPoints int    `json:"deduct_points"`
	Status       string `json:"status"`
}

// GetInspectionDetail 单条拍照留痕的详情：记录本体 + 记名名单 + 已关联的打表扣分。
//
// 楼栋范围判定必须与 GetInspections 逐字一致。宿管端命中的是 Casbin 的
// `/api/v1/dorm/*` 通配策略，"列表只按本楼栋取"并不构成服务端约束——
// 若详情接口不按同一规则过滤，任何宿管都能靠换 id 读到别栋的违纪明细。
func (d *DormController) GetInspectionDetail(c *gin.Context) {
	buildingStr, _ := c.Get("building")
	roleVal, _ := c.Get("role")
	roleStr, _ := roleVal.(string)
	buildingName, _ := buildingStr.(string)

	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "记录编号无效"})
		return
	}

	query := repository.DB.Where("id = ?", id)
	if roleStr == model.RoleDormManager && buildingName != "" && buildingName != "全楼" {
		query = query.Where("building LIKE ?", "%"+buildingName+"%")
	}

	var record model.InspectionPhoto
	if findErr := query.First(&record).Error; findErr != nil {
		// 不存在与越权合并成同一个 404：否则"这个 id 存在但不给你看"会变成记录探测面
		c.JSON(http.StatusNotFound, gin.H{"error": "该留痕记录不存在，或不属于您所负责的楼栋"})
		return
	}

	var subjects []model.InspectionSubject
	repository.DB.Where("inspection_id = ?", record.ID).Order("id asc").Find(&subjects)

	var linked []model.DeductionRecord
	repository.DB.Where("source_inspection_id = ?", record.ID).Order("id asc").Find(&linked)
	briefs := make([]LinkedDeductionBrief, 0, len(linked))
	for _, item := range linked {
		briefs = append(briefs, LinkedDeductionBrief{
			ID: item.ID, StudentName: item.StudentName, ClassName: item.ClassName,
			Category: item.Category, DeductPoints: item.DeductPoints, Status: item.Status,
		})
	}

	// structured_json 是文本 AI 的归纳产物，历史数据里可能是空串或半截 JSON：
	// 解析不出来就返回 null，由界面显示"无结构化结论"，不把脏数据原样甩给前端。
	var structured map[string]interface{}
	if strings.TrimSpace(record.StructuredJSON) != "" {
		if jsonErr := json.Unmarshal([]byte(record.StructuredJSON), &structured); jsonErr != nil {
			structured = nil
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"record":            record,
		"subjects":          subjects,
		"subject_total":     len(subjects),
		"linked_deductions": briefs,
		"structured":        structured,
	})
}

// GetCurrentSlotNotice 宿管工作台核心：根据当前服务器时间动态匹配时段任务并提醒提交指定资料
func (d *DormController) GetCurrentSlotNotice(c *gin.Context) {
	building, _ := c.Get("building")
	buildingStr, _ := building.(string)
	now := time.Now()
	nowMinute := now.Hour()*60 + now.Minute()
	weekday := now.Weekday() // 0 is Sunday, 6 is Saturday
	isWeekend := (weekday == time.Saturday || weekday == time.Sunday)

	var allSlots []model.DormTaskSlotConfig
	repository.DB.Where("is_enabled = ?", true).Order("sort_order asc, id asc").Find(&allSlots)

	type MatchedSlotInfo struct {
		Config             model.DormTaskSlotConfig `json:"config"`
		IsActiveNow        bool                     `json:"is_active_now"`
		RemainingMinutes   int                      `json:"remaining_minutes"`
		TodaySubmittedCount int64                   `json:"today_submitted_count"`
		HasSubmitted       bool                     `json:"has_submitted"`
	}

	parseHM := func(hm string) int {
		var h, m int
		fmt.Sscanf(hm, "%d:%d", &h, &m)
		return h*60 + m
	}

	// 统计今日对应时段或楼栋提交的巡检存证照片数
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todayEnd := todayStart.Add(24 * time.Hour)

	var activeSlot *MatchedSlotInfo
	var nextSlot *MatchedSlotInfo
	var upcomingSlots []MatchedSlotInfo

	for _, s := range allSlots {
		// 适用周期与今天不匹配的时段根本不生效（周末专属、单双周均在此过滤）
		if !model.PeriodTypeMatches(s.PeriodType, now) {
			continue
		}

		startMin := parseHM(s.StartTime)
		endMin := parseHM(s.EndTime)

		isActive := false
		remMin := 0

		if startMin <= endMin {
			// 普通日间时段，例如 06:30 ~ 08:30
			if nowMinute >= startMin && nowMinute <= endMin {
				isActive = true
				remMin = endMin - nowMinute
			}
		} else {
			// 跨午夜时段，例如 22:40 ~ 01:30
			if nowMinute >= startMin || nowMinute <= endMin {
				isActive = true
				if nowMinute >= startMin {
					remMin = (1440 - nowMinute) + endMin
				} else {
					remMin = endMin - nowMinute
				}
			}
		}

		// 检查今日宿管是否已提交该分类或照片
		var count int64
		photoQuery := repository.DB.Model(&model.InspectionPhoto{}).
			Where("created_at >= ? AND created_at < ?", todayStart, todayEnd)
		if buildingStr != "" && buildingStr != "全楼" {
			photoQuery = photoQuery.Where("building LIKE ?", "%"+buildingStr+"%")
		}
		if s.TargetPhotoType != "" {
			photoQuery = photoQuery.Where("photo_type = ?", s.TargetPhotoType)
		}
		photoQuery.Count(&count)

		info := MatchedSlotInfo{
			Config:              s,
			IsActiveNow:         isActive,
			RemainingMinutes:    remMin,
			TodaySubmittedCount: count,
			HasSubmitted:        count > 0,
		}

		if isActive && activeSlot == nil {
			activeSlot = &info
		} else {
			upcomingSlots = append(upcomingSlots, info)
		}
	}

	// 如果当前不在任何时段内，挑选下一个即将到来的时段
	if activeSlot == nil && len(upcomingSlots) > 0 {
		nextSlot = &upcomingSlots[0]
	}

	c.JSON(http.StatusOK, gin.H{
		"server_time":   now.Format("2006-01-02 15:04:05"),
		"active_slot":   activeSlot,
		"next_slot":     nextSlot,
		"all_rules":     allSlots,
		"building":      buildingStr,
		"is_weekend":    isWeekend,
	})
}
