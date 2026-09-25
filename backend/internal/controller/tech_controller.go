package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/ai"
)

type TechController struct{}

type TestAIRequest struct {
	ConfigKey string `json:"config_key" binding:"required"` // vision_engine, text_engine
	InputText string `json:"input_text"`
	ImageURL  string `json:"image_url"`
	PhotoType string `json:"photo_type"`
}

type AddRosterRequest struct {
	RealName string `json:"real_name" binding:"required"`
	Phone    string `json:"phone" binding:"required"`
	Building string `json:"building" binding:"required"`
	Floor    string `json:"floor"`
}

// GetAIConfigs 技术维护组读取当前所有 AI 引擎配置
func (tc *TechController) GetAIConfigs(c *gin.Context) {
	var configs []model.AIConfig
	repository.DB.Find(&configs)
	c.JSON(http.StatusOK, gin.H{
		"total": len(configs),
		"items": configs,
	})
}

// UpdateAIConfig 技术维护组修改 AI 模型、Prompt 提示词、端点与 Key
func (tc *TechController) UpdateAIConfig(c *gin.Context) {
	configID := c.Param("id")

	var existing model.AIConfig
	if err := repository.DB.First(&existing, configID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "AI 配置项不存在"})
		return
	}

	var req model.AIConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式错误"})
		return
	}

	existing.DisplayName = req.DisplayName
	existing.Provider = req.Provider
	existing.Endpoint = req.Endpoint
	if req.APIKey != "" {
		existing.APIKey = req.APIKey
	}
	existing.ModelName = req.ModelName
	existing.SystemPrompt = req.SystemPrompt
	existing.Temperature = req.Temperature
	existing.MaxTokens = req.MaxTokens
	existing.IsEnabled = req.IsEnabled
	existing.UpdatedAt = time.Now()

	repository.DB.Save(&existing)

	c.JSON(http.StatusOK, gin.H{
		"message": "AI 引擎配置已更新并生效！",
		"config":  existing,
	})
}

// TestAIPlayground 核心特性：技术维护组实时调试 AI（多模态/文本）
func (tc *TechController) TestAIPlayground(c *gin.Context) {
	var req TestAIRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供调试引擎类型与测试数据"})
		return
	}

	var cfg model.AIConfig
	if err := repository.DB.Where("config_key = ?", req.ConfigKey).First(&cfg).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到指定的 AI 配置项"})
		return
	}

	startTime := time.Now()
	var testResult interface{}
	var err error

	if req.ConfigKey == "vision_engine" {
		// 调试多模态 AI
		img := req.ImageURL
		if img == "" {
			img = "/uploads/sample_violation.jpg"
		}
		pType := req.PhotoType
		if pType == "" {
			pType = "violation"
		}
		rawOutput, vErr := ai.CallVisionAI(img, pType, "测试7-302室", &cfg)
		err = vErr
		testResult = gin.H{
			"type":           "multimodal_vision",
			"vision_extract": rawOutput,
			"simulated_room": "测试7-302室",
		}
	} else {
		// 调试文本归纳入库 AI
		inputText := req.InputText
		if inputText == "" {
			inputText = "【测试输入】巡查发现西区12号楼405室桌面私拉电线插线板，并使用额定1200W电磁炉煮火锅，地面散落易燃包装纸盒。"
		}
		structRes, tErr := ai.CallTextAI(inputText, "violation", &cfg)
		err = tErr
		testResult = gin.H{
			"type":            "text_structuring",
			"input_snippet":   inputText,
			"structured_json": structRes,
		}
	}

	duration := time.Since(startTime).Milliseconds()
	now := time.Now()
	cfg.LastTestedAt = &now
	if err != nil {
		cfg.LastTestResult = fmt.Sprintf("调试异常: %v", err)
	} else {
		cfg.LastTestResult = fmt.Sprintf("测试通过，响应耗时 %d ms", duration)
	}
	repository.DB.Save(&cfg)

	testStatus := "success"
	if err != nil {
		testStatus = "failed"
	}

	c.JSON(http.StatusOK, gin.H{
		"status":            testStatus,
		"engine_configured": ai.IsConfigured(&cfg),
		"duration_ms":       duration,
		"engine":            cfg.DisplayName,
		"model_name":        cfg.ModelName,
		"result":            testResult,
		"error":             fmt.Sprintf("%v", err),
	})
}

// GetRosterPresets 获取预置宿管花名册列表
func (tc *TechController) GetRosterPresets(c *gin.Context) {
	var list []model.DormRosterPreset
	repository.DB.Order("id desc").Find(&list)
	c.JSON(http.StatusOK, gin.H{
		"total": len(list),
		"items": list,
	})
}

// AddRosterPreset 技术组手动录入/更新宿管名单（供手机端三要素匹配）
func (tc *TechController) AddRosterPreset(c *gin.Context) {
	var req AddRosterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供宿管姓名、手机号与负责楼栋"})
		return
	}

	preset := model.DormRosterPreset{
		RealName:  req.RealName,
		Phone:     req.Phone,
		Building:  req.Building,
		Floor:     req.Floor,
		CreatedAt: time.Now(),
	}
	repository.DB.Create(&preset)

	c.JSON(http.StatusOK, gin.H{
		"message": "宿管预置信息录入成功，宿管可立即在手机端凭【手机号+楼栋+姓名】认证登录！",
		"item":    preset,
	})
}

// GetSystemOverview 技术维护组系统运行指标看板
func (tc *TechController) GetSystemOverview(c *gin.Context) {
	var userCount, photoCount, shiftCount, paperCount int64
	repository.DB.Model(&model.User{}).Count(&userCount)
	repository.DB.Model(&model.InspectionPhoto{}).Count(&photoCount)
	repository.DB.Model(&model.ScheduleShift{}).Count(&shiftCount)
	repository.DB.Model(&model.ExamPaper{}).Count(&paperCount)

		c.JSON(http.StatusOK, gin.H{
			"user_count":     userCount,
			"photo_count":    photoCount,
			"shift_count":    shiftCount,
			"paper_count":    paperCount,
			"server_time":    time.Now().Format("2006-01-02 15:04:05"),
			"framework":      "Go Gin + GORM + Casbin RBAC",
			"ai_status":      "Multi-modal & Text Dual-pipeline Active",
		})
	}

// GetTaskSlots 获取后台配置的所有宿管时段与资料提交规范
func (tc *TechController) GetTaskSlots(c *gin.Context) {
	var list []model.DormTaskSlotConfig
	repository.DB.Order("sort_order asc, id asc").Find(&list)
	c.JSON(http.StatusOK, gin.H{
		"total": len(list),
		"items": list,
	})
}

// CreateTaskSlot 后台新增一个宿管时段与提交要求
func (tc *TechController) CreateTaskSlot(c *gin.Context) {
	var s model.DormTaskSlotConfig
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
		return
	}
	s.CreatedAt = time.Now()
	s.UpdatedAt = time.Now()
	if err := repository.DB.Create(&s).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "时段规范配置已添加！", "slot": s})
}

// UpdateTaskSlot 后台修改宿管时段与提交要求
func (tc *TechController) UpdateTaskSlot(c *gin.Context) {
	id := c.Param("id")
	var existing model.DormTaskSlotConfig
	if err := repository.DB.First(&existing, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "配置不存在"})
		return
	}

	var req model.DormTaskSlotConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析错误: " + err.Error()})
		return
	}

	existing.SlotName = req.SlotName
	existing.StartTime = req.StartTime
	existing.EndTime = req.EndTime
	existing.PeriodType = req.PeriodType
	existing.RequiredMaterials = req.RequiredMaterials
	existing.ActionPrompt = req.ActionPrompt
	existing.TargetPhotoType = req.TargetPhotoType
	existing.UrgencyLevel = req.UrgencyLevel
	existing.IsEnabled = req.IsEnabled
	existing.SortOrder = req.SortOrder
	existing.UpdatedAt = time.Now()

	repository.DB.Save(&existing)
	c.JSON(http.StatusOK, gin.H{"message": "时段规范已更新！", "slot": existing})
}

// DeleteTaskSlot 后台删除宿管时段配置
func (tc *TechController) DeleteTaskSlot(c *gin.Context) {
	id := c.Param("id")
	repository.DB.Delete(&model.DormTaskSlotConfig{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "时段规范已删除！"})
}
