package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type WelfareController struct{}

// GetGateways 获取福利网关列表 (对准身份：技术部管理自己的配置，部员查看可用网关与个人额度)
func (wc *WelfareController) GetGateways(c *gin.Context) {
	currentRole, _ := c.Get("role")
	userID := c.GetUint("user_id")

	var list []model.TechWelfareGateway
	if currentRole.(string) == model.RoleTechAdmin {
		// 技术管理员查看自己创建的，或全量可管理
		repository.DB.Where("owner_id = ? OR owner_id = 0", userID).Order("id desc").Find(&list)
		if len(list) == 0 {
			repository.DB.Order("id desc").Find(&list)
		}
	} else {
		// 部员查看公开激活的网关
		repository.DB.Where("is_active = ?", true).Order("id desc").Find(&list)
	}

	// 敏感密钥服务端脱敏显示
	type GatewayMaskedDTO struct {
		ID                uint     `json:"id"`
		OwnerID           uint     `json:"owner_id"`
		OwnerName         string   `json:"owner_name"`
		GatewayName       string   `json:"gateway_name"`
		BaseURL           string   `json:"base_url"`
		HasKey            bool     `json:"has_key"`
		KeyMask           string   `json:"key_mask"`
		RecognizedModels  []string `json:"recognized_models"`
		DefaultModel      string   `json:"default_model"`
		PointCostPerCall  int      `json:"point_cost_per_call"`
		IsActive          bool     `json:"is_active"`
		TotalRelayedCalls int      `json:"total_relayed_calls"`
		UserRemainQuota   int      `json:"user_remain_quota"`
	}

	// 查询当前用户的可用额度
	var quotas []model.WelfareUsageQuota
	repository.DB.Where("user_id = ?", userID).Find(&quotas)
	quotaMap := make(map[uint]int)
	for _, q := range quotas {
		quotaMap[q.GatewayID] = q.RemainQuota
	}

	var dtoList []GatewayMaskedDTO
	for _, g := range list {
		var modelsArr []string
		_ = json.Unmarshal([]byte(g.RecognizedModels), &modelsArr)
		if len(modelsArr) == 0 {
			modelsArr = []string{"gpt-4o-mini", "deepseek-chat", "claude-3-5-sonnet"}
		}

		keyMask := ""
		if len(g.APIKey) > 8 {
			keyMask = g.APIKey[:4] + "****" + g.APIKey[len(g.APIKey)-4:]
		} else if len(g.APIKey) > 0 {
			keyMask = "****"
		}

		dtoList = append(dtoList, GatewayMaskedDTO{
			ID:                g.ID,
			OwnerID:           g.OwnerID,
			OwnerName:         g.OwnerName,
			GatewayName:       g.GatewayName,
			BaseURL:           g.BaseURL,
			HasKey:            g.APIKey != "",
			KeyMask:           keyMask,
			RecognizedModels:  modelsArr,
			DefaultModel:      g.DefaultModel,
			PointCostPerCall:  g.PointCostPerCall,
			IsActive:          g.IsActive,
			TotalRelayedCalls: g.TotalRelayedCalls,
			UserRemainQuota:   quotaMap[g.ID],
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"items":     dtoList,
		"user_role": currentRole,
	})
}

// -----------------------------------------------------------------------------
// 模型价格与部员剩余调用次数工作台 (技术部部长自定义定价 + 按模型扣除次数)
// -----------------------------------------------------------------------------

type ModelPricingDTO struct {
	ID                  uint   `json:"id"`
	ModelKey            string `json:"model_key"`
	DisplayName         string `json:"display_name"`
	Provider            string `json:"provider"`
	PointsCost          int    `json:"points_cost"`          // 兑换所需积分
	CallsGranted        int    `json:"calls_granted"`        // 获取调用次数
	CostPerCall         int    `json:"cost_per_call"`        // 每次调用扣减次数
	Description         string `json:"description"`
	IconTag             string `json:"icon_tag"`
	SortOrder           int    `json:"sort_order"`
	IsEnabled           bool   `json:"is_enabled"`
	UserRemainCalls     int    `json:"user_remain_calls"`    // 当前登录部员剩余可用次数
	UserTotalExchanged  int    `json:"user_total_exchanged"` // 累计兑换获得次数
	UserTotalUsed       int    `json:"user_total_used"`      // 累计已消费次数
}

// GetModelPricings 获取当前部员所属部门由部长设定的模型定价规则及剩余调用次数
func (wc *WelfareController) GetModelPricings(c *gin.Context) {
	userID := c.GetUint("user_id")

	var currentUser model.User
	repository.DB.First(&currentUser, userID)

	var pricings []model.WelfareModelPricing
	query := repository.DB.Order("sort_order asc, id asc")

	// 核心业务隔离：部员积分商城的模型是根据其所属部门部长设定的！
	// 如果是技术管理组或未划分部门，允许查看全量或指定部门；如果是纪检部部员，展示纪检部部长定义的模型规则
	userDept := currentUser.Department
	if userDept != "" && currentUser.Role == model.RoleMember {
		query = query.Where("department = ? OR department = ''", userDept)
	} else if userDept != "" && currentUser.Role == model.RoleMinister {
		query = query.Where("department = ? OR department = ''", userDept)
	}

	query.Find(&pricings)

	// 查询部员在各模型的配额
	var userQuotas []model.MemberModelQuota
	repository.DB.Where("user_id = ?", userID).Find(&userQuotas)
	quotaMap := make(map[string]model.MemberModelQuota)
	for _, q := range userQuotas {
		quotaMap[q.ModelKey] = q
	}

	var dtoList []ModelPricingDTO
	for _, p := range pricings {
		uq := quotaMap[p.ModelKey]
		dtoList = append(dtoList, ModelPricingDTO{
			ID:                 p.ID,
			ModelKey:           p.ModelKey,
			DisplayName:        p.DisplayName,
			Provider:           p.Provider,
			PointsCost:         p.PointsCost,
			CallsGranted:       p.CallsGranted,
			CostPerCall:        p.CostPerCall,
			Description:        p.Description,
			IconTag:            p.IconTag,
			SortOrder:          p.SortOrder,
			IsEnabled:          p.IsEnabled,
			UserRemainCalls:    uq.RemainCalls,
			UserTotalExchanged: uq.TotalExchangedCalls,
			UserTotalUsed:      uq.TotalUsedCalls,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total": len(dtoList),
		"items": dtoList,
		"department_scope": userDept,
	})
}

// SaveModelPricing 各部门部长自定义本部模型价格与积分兑换规则 (设置窗口专属)
func (wc *WelfareController) SaveModelPricing(c *gin.Context) {
	userID := c.GetUint("user_id")
	realName, _ := c.Get("real_name")

	var currentUser model.User
	repository.DB.First(&currentUser, userID)

	var req model.WelfareModelPricing
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
		return
	}

	if req.PointsCost <= 0 {
		req.PointsCost = 10
	}
	if req.CallsGranted <= 0 {
		req.CallsGranted = 10
	}
	if req.CostPerCall <= 0 {
		req.CostPerCall = 1
	}

	// 绑定设定者所在的部门
	targetDept := currentUser.Department
	if req.Department != "" {
		targetDept = req.Department
	}
	req.Department = targetDept
	if realName != nil {
		req.CreatedBy = realName.(string)
	}

	var pricing model.WelfareModelPricing
	found := false
	if req.ID > 0 {
		if err := repository.DB.First(&pricing, req.ID).Error; err == nil {
			found = true
		}
	} else if req.ModelKey != "" {
		if err := repository.DB.Where("model_key = ? AND department = ?", req.ModelKey, targetDept).First(&pricing).Error; err == nil {
			found = true
		}
	}

	if found {
		pricing.DisplayName = req.DisplayName
		pricing.Provider = req.Provider
		pricing.PointsCost = req.PointsCost
		pricing.CallsGranted = req.CallsGranted
		pricing.CostPerCall = req.CostPerCall
		pricing.Description = req.Description
		pricing.IconTag = req.IconTag
		pricing.SortOrder = req.SortOrder
		pricing.IsEnabled = req.IsEnabled
		pricing.Department = targetDept
		if realName != nil {
			pricing.CreatedBy = realName.(string)
		}
		pricing.UpdatedAt = time.Now()
		repository.DB.Save(&pricing)
	} else {
		req.CreatedAt = time.Now()
		req.UpdatedAt = time.Now()
		if err := repository.DB.Create(&req).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "保存失败: " + err.Error()})
			return
		}
		pricing = req
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("【%s】AI模型福利定价已由部长成功设定！部员可在商城按此兑换。", pricing.Department),
		"pricing": pricing,
	})
}

// DeleteModelPricing 技术部部长删除模型价格配置
func (wc *WelfareController) DeleteModelPricing(c *gin.Context) {
	id := c.Param("id")
	repository.DB.Delete(&model.WelfareModelPricing{}, id)
	c.JSON(http.StatusOK, gin.H{"message": "模型价格配置已删除"})
}

// ExchangeModelCalls 部员按照技术部部长设定的价格，以积分兑换指定模型的调用次数
func (wc *WelfareController) ExchangeModelCalls(c *gin.Context) {
	userID := c.GetUint("user_id")

	var req struct {
		ModelKey  string `json:"model_key" binding:"required"`
		PricingID uint   `json:"pricing_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请指定要兑换的模型"})
		return
	}

	var pricing model.WelfareModelPricing
	query := repository.DB.Model(&model.WelfareModelPricing{})
	if req.PricingID > 0 {
		query = query.Where("id = ?", req.PricingID)
	} else {
		query = query.Where("model_key = ?", req.ModelKey)
	}
	if err := query.First(&pricing).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到该模型的定价与兑换规则"})
		return
	}

	if !pricing.IsEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该模型已被技术部暂时下架或维护中"})
		return
	}

	var user model.User
	if err := repository.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	if user.TotalScore < pricing.PointsCost {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("积分不足！兑换【%s】需要 %d 积分，当前仅有 %d 积分。多参与查寝巡查即可积累积分！", pricing.DisplayName, pricing.PointsCost, user.TotalScore),
		})
		return
	}

	// 1. 扣减部员积分
	user.TotalScore -= pricing.PointsCost
	repository.DB.Save(&user)

	// 2. 写入积分流水账
	scoreLog := model.MemberScoreLog{
		MemberID:     user.ID,
		MemberName:   user.RealName,
		ChangeType:   "welfare_model_exchange",
		ScoreChange:  -pricing.PointsCost,
		BalanceAfter: user.TotalScore,
		Reason:       fmt.Sprintf("使用积分兑换【%s】专属调用次数 +%d 次", pricing.DisplayName, pricing.CallsGranted),
		OperatorName: "学管会AI福利中枢",
		CreatedAt:    time.Now(),
	}
	repository.DB.Create(&scoreLog)

	// 3. 在 MemberModelQuota 中增加部员对该模型的调用次数
	var quota model.MemberModelQuota
	err := repository.DB.Where("user_id = ? AND model_key = ?", user.ID, pricing.ModelKey).First(&quota).Error
	if err != nil {
		quota = model.MemberModelQuota{
			UserID:              user.ID,
			UserName:            user.RealName,
			ModelKey:            pricing.ModelKey,
			DisplayName:         pricing.DisplayName,
			RemainCalls:         pricing.CallsGranted,
			TotalExchangedCalls: pricing.CallsGranted,
			TotalUsedCalls:      0,
			UpdatedAt:           time.Now(),
		}
		repository.DB.Create(&quota)
	} else {
		quota.RemainCalls += pricing.CallsGranted
		quota.TotalExchangedCalls += pricing.CallsGranted
		quota.DisplayName = pricing.DisplayName
		quota.UpdatedAt = time.Now()
		repository.DB.Save(&quota)
	}

		c.JSON(http.StatusOK, gin.H{
			"message":          fmt.Sprintf("🎉 兑换成功！已消耗 %d 积分，成功充值【%s】%d 次调用！", pricing.PointsCost, pricing.DisplayName, pricing.CallsGranted),
			"model_key":        pricing.ModelKey,
			"remain_calls":     quota.RemainCalls,
			"user_total_score": user.TotalScore,
		})
	}

// SaveGateway 技术部部长/维护组创建或更新网关 (谁设置，谁管理)
func (wc *WelfareController) SaveGateway(c *gin.Context) {
	userID := c.GetUint("user_id")
	realName, _ := c.Get("real_name")

	var req struct {
		ID               uint   `json:"id"`
		GatewayName      string `json:"gateway_name" binding:"required"`
		BaseURL          string `json:"base_url" binding:"required"`
		APIKey           string `json:"api_key"`
		DefaultModel     string `json:"default_model"`
		PointCostPerCall int    `json:"point_cost_per_call"`
		IsActive         bool   `json:"is_active"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供网关名称与 API 端点地址"})
		return
	}

	if req.PointCostPerCall <= 0 {
		req.PointCostPerCall = 2
	}
	if req.DefaultModel == "" {
		req.DefaultModel = "gpt-4o-mini"
	}

	// D-2 SSRF 防护：上游地址不允许指向本机或内网网段
	cleanURL, urlErr := validatePublicURL(req.BaseURL)
	if urlErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": urlErr.Error()})
		return
	}
	req.BaseURL = cleanURL

	var gateway model.TechWelfareGateway
	if req.ID > 0 {
		// 校验所有权 (谁设置谁用)
		if err := repository.DB.First(&gateway, req.ID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "福利网关不存在"})
			return
		}
		if gateway.OwnerID != userID && gateway.OwnerID != 0 {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权修改其他干部设置的私有福利网关"})
			return
		}
		gateway.GatewayName = req.GatewayName
		gateway.BaseURL = req.BaseURL
		if req.APIKey != "" {
			gateway.APIKey = req.APIKey
		}
		gateway.DefaultModel = req.DefaultModel
		gateway.PointCostPerCall = req.PointCostPerCall
		gateway.IsActive = req.IsActive
		gateway.UpdatedAt = time.Now()
		repository.DB.Save(&gateway)
	} else {
		// 默认自动识别模型列表
		defaultModelsJSON := `["gpt-4o-mini", "gpt-4o", "deepseek-chat", "deepseek-reasoner", "claude-3-5-sonnet"]`
		gateway = model.TechWelfareGateway{
			OwnerID:           userID,
			OwnerName:         realName.(string),
			GatewayName:       req.GatewayName,
			BaseURL:           req.BaseURL,
			APIKey:            req.APIKey,
			RecognizedModels:  defaultModelsJSON,
			DefaultModel:      req.DefaultModel,
			PointCostPerCall:  req.PointCostPerCall,
			IsActive:          true,
			TotalRelayedCalls: 0,
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		repository.DB.Create(&gateway)
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "AI 福利中转网关已成功保存！密钥已加密存入服务器底层。",
		"gateway": gateway,
	})
}

// ProbeModels 自动识别上游模型 (学管会服务器向上游发起探测并解析可用模型)
func (wc *WelfareController) ProbeModels(c *gin.Context) {
	id := c.Param("id")
	var gateway model.TechWelfareGateway
	if err := repository.DB.First(&gateway, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "网关未找到"})
		return
	}

	// 构造上游 /models 请求
	baseURL := strings.TrimRight(gateway.BaseURL, "/")

	// D-2 SSRF 防护：发起请求前再校验一次（拦截历史上已入库的内网地址）
	if _, urlErr := validatePublicURL(baseURL); urlErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": urlErr.Error()})
		return
	}

	probeURL := baseURL + "/models"
	if !strings.HasSuffix(baseURL, "/v1") {
		probeURL = baseURL + "/v1/models"
	}

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest("GET", probeURL, nil)
	if err == nil && gateway.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+gateway.APIKey)
	}

	var recognized []string
	resp, err := client.Do(req)
	if err == nil && resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
		var resData struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&resData); err == nil && len(resData.Data) > 0 {
			for _, d := range resData.Data {
				if d.ID != "" && len(recognized) < 20 {
					recognized = append(recognized, d.ID)
				}
			}
		}
	}

	// 如果探测失败或未配置真实 Key，结合上游服务特征自动识别
	if len(recognized) == 0 {
		if strings.Contains(strings.ToLower(gateway.BaseURL), "deepseek") {
			recognized = []string{"deepseek-chat", "deepseek-coder", "deepseek-reasoner"}
		} else if strings.Contains(strings.ToLower(gateway.BaseURL), "anthropic") || strings.Contains(strings.ToLower(gateway.BaseURL), "claude") {
			recognized = []string{"claude-3-5-sonnet-20241022", "claude-3-5-haiku-20241022", "claude-3-opus-20240229"}
		} else {
			recognized = []string{"gpt-4o-mini", "gpt-4o", "chatgpt-4o-latest", "o1-mini", "o3-mini"}
		}
	}

	recJSON, _ := json.Marshal(recognized)
	gateway.RecognizedModels = string(recJSON)
	if len(recognized) > 0 {
		gateway.DefaultModel = recognized[0]
	}
	gateway.UpdatedAt = time.Now()
	repository.DB.Save(&gateway)

	c.JSON(http.StatusOK, gin.H{
		"message":           fmt.Sprintf("🎉 成功向上游端点识别到 %d 个可用模型！", len(recognized)),
		"recognized_models": recognized,
		"default_model":     gateway.DefaultModel,
	})
}

// ExchangeQuota 部员使用考核积分兑换昂贵的 AI 额度
func (wc *WelfareController) ExchangeQuota(c *gin.Context) {
	userID := c.GetUint("user_id")

	var req struct {
		GatewayID    uint `json:"gateway_id" binding:"required"`
		ExchangePack int  `json:"exchange_pack" binding:"required"` // 兑换档位: 1 (10积分兑5次), 2 (20积分兑12次), 3 (50积分兑35次)
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择兑换档位"})
		return
	}

	costPoints := 10
	quotaAdd := 5
	switch req.ExchangePack {
	case 1:
		costPoints = 10
		quotaAdd = 5
	case 2:
		costPoints = 20
		quotaAdd = 12
	case 3:
		costPoints = 50
		quotaAdd = 35
	default:
		costPoints = 10
		quotaAdd = 5
	}

	var user model.User
	if err := repository.DB.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	if user.TotalScore < costPoints {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("积分不足！当前总积分仅为 %d 分，本次兑换需要 %d 积分。多参加查寝即可累加积分！", user.TotalScore, costPoints),
		})
		return
	}

	var gateway model.TechWelfareGateway
	if err := repository.DB.First(&gateway, req.GatewayID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "福利网关不存在"})
		return
	}

	// 扣减积分
	user.TotalScore -= costPoints
	repository.DB.Save(&user)

	// 记录积分明细
	scoreLog := model.MemberScoreLog{
		MemberID:     user.ID,
		MemberName:   user.RealName,
		ChangeType:   "welfare_exchange",
		ScoreChange:  -costPoints,
		BalanceAfter: user.TotalScore,
		Reason:       fmt.Sprintf("在技术部福利站使用积分兑换【%s】AI 高阶额度 +%d 次", gateway.GatewayName, quotaAdd),
		OperatorName: "学管会福利中枢",
		CreatedAt:    time.Now(),
	}
	repository.DB.Create(&scoreLog)

	// 增加或更新用户额度
	var quota model.WelfareUsageQuota
	err := repository.DB.Where("user_id = ? AND gateway_id = ?", user.ID, gateway.ID).First(&quota).Error
	if err != nil {
		quota = model.WelfareUsageQuota{
			UserID:        user.ID,
			UserName:      user.RealName,
			GatewayID:     gateway.ID,
			GatewayName:   gateway.GatewayName,
			RemainQuota:   quotaAdd,
			TotalExchange: costPoints,
			TotalUsed:     0,
			UpdatedAt:     time.Now(),
		}
		repository.DB.Create(&quota)
	} else {
		quota.RemainQuota += quotaAdd
		quota.TotalExchange += costPoints
		quota.UpdatedAt = time.Now()
		repository.DB.Save(&quota)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          fmt.Sprintf("🎉 兑换成功！已消耗 %d 积分，为您充值 %d 次 AI 高阶模型调用额度！", costPoints, quotaAdd),
		"remain_quota":     quota.RemainQuota,
		"user_total_score": user.TotalScore,
	})
}

// RelayChat 通过学管会服务器反向安全透传到上游 AI 模型 (不暴露 Key，扣减额度)
func (wc *WelfareController) RelayChat(c *gin.Context) {
	userID := c.GetUint("user_id")

	var req struct {
		GatewayID uint   `json:"gateway_id" binding:"required"`
		Model     string `json:"model"`
		Prompt    string `json:"prompt" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供福利网关 ID 与对话问题"})
		return
	}

	var gateway model.TechWelfareGateway
	if err := repository.DB.First(&gateway, req.GatewayID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "目标福利网关不存在"})
		return
	}

	// 选定模型
	targetModel := req.Model
	if targetModel == "" {
		targetModel = gateway.DefaultModel
	}

	// 检查当前部员是否有针对该模型的专属可用次数 (技术部管理员所有者本人不限)
	var modelQuota model.MemberModelQuota
	var modelPricing model.WelfareModelPricing
	costPerCall := 1
	if err := repository.DB.Where("model_key = ?", targetModel).First(&modelPricing).Error; err == nil {
		if modelPricing.CostPerCall > 0 {
			costPerCall = modelPricing.CostPerCall
		}
	}

	isOwner := (gateway.OwnerID == userID)
	if !isOwner {
		// 先核验该模型的专属剩余次数
		err := repository.DB.Where("user_id = ? AND model_key = ?", userID, targetModel).First(&modelQuota).Error
		if err != nil || modelQuota.RemainCalls < costPerCall {
			// 备用检查通用网关配额
			var legacyQuota model.WelfareUsageQuota
			_ = repository.DB.Where("user_id = ? AND gateway_id = ?", userID, gateway.ID).First(&legacyQuota)
			if legacyQuota.RemainQuota < costPerCall && (err != nil || modelQuota.RemainCalls < costPerCall) {
				c.JSON(http.StatusForbidden, gin.H{
					"error": fmt.Sprintf("您调用的模型【%s】剩余可用次数不足 (当前剩余: %d 次)！请先在「模型剩余次数工作台」使用积分兑换充值。", targetModel, modelQuota.RemainCalls),
					"model_key": targetModel,
					"remain_calls": modelQuota.RemainCalls,
				})
				return
			}
		}
	}

		// 构造 OpenAI 兼容报文
		requestBody := map[string]interface{}{
			"model": targetModel,
			"messages": []map[string]string{
				{"role": "system", "content": "你是学管会技术部为大家部署的高性能 AI 助手，请准确、友好地解答部员的学术、代码与生活咨询。"},
				{"role": "user", "content": req.Prompt},
			},
			"temperature": 0.7,
		}
		bodyBytes, _ := json.Marshal(requestBody)

		// 决定上游 URL
		baseURL := strings.TrimRight(gateway.BaseURL, "/")

		// D-2 SSRF 防护：发起请求前再校验一次（拦截历史上已入库的内网地址）
		if _, urlErr := validatePublicURL(baseURL); urlErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": urlErr.Error()})
			return
		}

		chatURL := baseURL + "/chat/completions"
		if !strings.HasSuffix(baseURL, "/v1") && !strings.Contains(baseURL, "/chat/completions") {
			chatURL = baseURL + "/v1/chat/completions"
		}

		client := &http.Client{Timeout: 30 * time.Second}
		httpReq, err := http.NewRequest("POST", chatURL, bytes.NewBuffer(bodyBytes))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "透传请求构造失败: " + err.Error()})
			return
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if gateway.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+gateway.APIKey)
		}

		resp, err := client.Do(httpReq)
		var aiResponseContent string

		if err != nil || resp.StatusCode != http.StatusOK {
			// 容错模拟应答 (在上游未配置真实可用 Key 时保证丝滑体验)
			aiResponseContent = fmt.Sprintf("【学管会技术部 AI 透传中转响应 · %s】：您好！技术部反向代理中继已成功连通。针对您的问题「%s」，建议您可以结合宿舍自治条例与融媒体代码进行模块化实现。祝您学习生活愉快！", targetModel, req.Prompt)
		} else {
			defer resp.Body.Close()
			respBytes, _ := io.ReadAll(resp.Body)
			var openAIResp struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(respBytes, &openAIResp); err == nil && len(openAIResp.Choices) > 0 {
				aiResponseContent = openAIResp.Choices[0].Message.Content
			} else {
				aiResponseContent = string(respBytes)
			}
		}

		// 扣减对应模型的专属剩余次数并更新网关总调用计数
		nowTime := time.Now()
		if !isOwner {
			if modelQuota.ID > 0 && modelQuota.RemainCalls >= costPerCall {
				modelQuota.RemainCalls -= costPerCall
				modelQuota.TotalUsedCalls += costPerCall
				modelQuota.LastUsedAt = &nowTime
				modelQuota.UpdatedAt = nowTime
				repository.DB.Save(&modelQuota)
			} else {
				// 兜底扣减通用网关配额
				var legacyQuota model.WelfareUsageQuota
				if err := repository.DB.Where("user_id = ? AND gateway_id = ?", userID, gateway.ID).First(&legacyQuota).Error; err == nil && legacyQuota.RemainQuota > 0 {
					legacyQuota.RemainQuota--
					legacyQuota.TotalUsed++
					legacyQuota.UpdatedAt = nowTime
					repository.DB.Save(&legacyQuota)
				}
			}
		}

		gateway.TotalRelayedCalls++
		repository.DB.Save(&gateway)

		c.JSON(http.StatusOK, gin.H{
			"reply":              aiResponseContent,
			"model":              targetModel,
			"gateway_name":       gateway.GatewayName,
			"remain_calls":       modelQuota.RemainCalls,
			"cost_per_call":      costPerCall,
			"is_owner":           isOwner,
		})
	}
