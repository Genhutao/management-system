package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
		// 判断周期类型是否匹配当前星期
		if s.PeriodType == "weekdays" && isWeekend {
			continue
		}
		if s.PeriodType == "weekends" && !isWeekend {
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
