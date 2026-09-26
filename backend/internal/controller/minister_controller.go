package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
	"xgh-system/pkg/ai"
)

type MinisterController struct{}

type LeaveReviewRequest struct {
	Action         string `json:"action" binding:"required"` // approved, rejected
	Comment        string `json:"comment"`
	AutoSubstitute bool   `json:"auto_substitute"` // 可选功能：审批时是否执行智能算法自动安排替补
}

type GenerateScheduleRequest struct {
	Title       string   `json:"title" binding:"required"`
	RuleType    string   `json:"rule_type" binding:"required"`  // daily, weekly_single_double, weekday, custom
	StartDate   string   `json:"start_date" binding:"required"` // YYYY-MM-DD
	EndDate     string   `json:"end_date" binding:"required"`
	ShiftPeriod string   `json:"shift_period"` // 如 "19:00-21:00 晚查寝"
	Buildings   []string `json:"buildings"`    // 轮检楼栋列表
	MemberPool  []uint   `json:"member_pool"`  // 参与排班的部员 ID 列表
	CustomRules string   `json:"custom_rules"` // 额外自定义指令
}

type AdjustScoreRequest struct {
	MemberID    uint   `json:"member_id" binding:"required"`
	ScoreChange int    `json:"score_change" binding:"required"` // 正数加分、负数扣分
	Reason      string `json:"reason" binding:"required"`
}

// sameDepartment 判定两个部门字符串是否指向同一个部门。
// 部门名允许 "组织部 · 技术组" 这类复合写法，因此双向包含才算同一部门；
// 任一侧为空必须判否，否则 strings.Contains(x, "") 恒为真会让检查形同虚设。
func sameDepartment(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

// GetPendingLeaves 获取待审批的请假列表
func (mc *MinisterController) GetPendingLeaves(c *gin.Context) {
	status := c.DefaultQuery("status", "pending")
	var leaves []model.LeaveRequest
	query := repository.DB.Order("created_at desc")
	if status != "all" {
		query = query.Where("status = ?", status)
	}
	query.Find(&leaves)

	c.JSON(http.StatusOK, gin.H{
		"total": len(leaves),
		"items": leaves,
	})
}

// ReviewLeave 审核部员请假（批准/驳回）
func (mc *MinisterController) ReviewLeave(c *gin.Context) {
	leaveID := c.Param("id")
	userID := c.GetUint("user_id")
	realName, _ := c.Get("real_name")

	var req LeaveReviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择审批动作"})
		return
	}

	var leave model.LeaveRequest
	if err := repository.DB.First(&leave, leaveID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "请假记录不存在"})
		return
	}

	now := time.Now()
	leave.Status = req.Action
	leave.MinisterID = userID
	leave.MinisterName = realName.(string)
	leave.ReviewComment = req.Comment
	leave.ReviewedAt = &now

	// 核心特性：监测到批准请假时，若启用自动替补且未指派人选，执行算法匹配最近上工最少的部员
	if req.Action == "approved" && (req.AutoSubstitute || leave.AutoSubstitute) && leave.SubstituteName == "" && leave.ShiftID > 0 {
		if bestUser, reason, err := service.FindBestSubstituteForShift(leave.ShiftID, leave.MemberID); err == nil && bestUser != nil {
			leave.SubstituteID = bestUser.ID
			leave.SubstituteName = bestUser.RealName
			leave.SubstituteReason = reason
			leave.AutoSubstitute = true
		}
	}

	repository.DB.Save(&leave)

	// 若审批通过且确定了替班部员，自动同步更新原排班班次大盘
	if req.Action == "approved" && leave.SubstituteName != "" && leave.ShiftID > 0 {
		var shift model.ScheduleShift
		if err := repository.DB.First(&shift, leave.ShiftID).Error; err == nil {
			// 在班次名单中更新或标明替补
			if strings.Contains(shift.MemberNames, leave.MemberName) {
				shift.MemberNames = strings.Replace(shift.MemberNames, leave.MemberName, leave.SubstituteName, 1)
			} else {
				shift.MemberNames = fmt.Sprintf("%s, %s", shift.MemberNames, leave.SubstituteName)
			}
			notePrefix := "部员请假"
			if leave.AutoSubstitute {
				notePrefix = "系统算法自动替补(负荷最低)"
			}
			shift.Note = fmt.Sprintf("%s [%s: 原部员【%s】请假，由【%s】替补上岗]", shift.Note, notePrefix, leave.MemberName, leave.SubstituteName)
			repository.DB.Save(&shift)

			// 替补加分激励 (+3 分) 并记入台账流水
			if leave.SubstituteID > 0 {
				var subUser model.User
				if err := repository.DB.First(&subUser, leave.SubstituteID).Error; err == nil {
					subUser.TotalScore += 3
					repository.DB.Save(&subUser)
					scoreLog := model.MemberScoreLog{
						MemberID:     subUser.ID,
						MemberName:   subUser.RealName,
						ShiftID:      shift.ID,
						ChangeType:   "duty_substitute",
						ScoreChange:  3,
						BalanceAfter: subUser.TotalScore,
						Reason:       fmt.Sprintf("临时代班替补履职加分（接替 %s 的 %s 查寝班次）", leave.MemberName, shift.ShiftPeriod),
						OperatorName: realName.(string),
						CreatedAt:    time.Now(),
					}
					repository.DB.Create(&scoreLog)
				}
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    fmt.Sprintf("请假审批完成，已标记为【%s】！", req.Action),
		"leave":      leave,
		"substitute": leave.SubstituteName,
		"reason":     leave.SubstituteReason,
	})
}

// PreviewLeaveSubstitute 部长审批时实时预览算法推荐的最佳替补人员
func (mc *MinisterController) PreviewLeaveSubstitute(c *gin.Context) {
	leaveID := c.Param("id")
	var leave model.LeaveRequest
	if err := repository.DB.First(&leave, leaveID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "请假记录不存在"})
		return
	}

	bestUser, reason, err := service.FindBestSubstituteForShift(leave.ShiftID, leave.MemberID)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"has_candidate": false,
			"message":       err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"has_candidate": true,
		"candidate": gin.H{
			"id":          bestUser.ID,
			"real_name":   bestUser.RealName,
			"department":  bestUser.Department,
			"phone":       bestUser.Phone,
			"total_score": bestUser.TotalScore,
			"reason":      reason,
		},
	})
}

// GenerateSchedule 核心特色：多模式排班轮换生成引擎（单双周/每日轮换/周内/自定义）
func (mc *MinisterController) GenerateSchedule(c *gin.Context) {
	userID := c.GetUint("user_id")

	var req GenerateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数有误，请指定排班起止日期与轮换规则"})
		return
	}

	start, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "开始日期格式错误，应为 YYYY-MM-DD"})
		return
	}
	end, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil || end.Before(start) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "结束日期格式错误或早于开始日期"})
		return
	}

	// 1. 创建排班主计划
	plan := model.SchedulePlan{
		Title:       req.Title,
		RuleType:    req.RuleType,
		StartDate:   req.StartDate,
		EndDate:     req.EndDate,
		Description: fmt.Sprintf("由部长生成：规则类型=%s，轮转楼栋数=%d", req.RuleType, len(req.Buildings)),
		CreatedBy:   userID,
		Status:      "active",
		CreatedAt:   time.Now(),
	}
	repository.DB.Create(&plan)

	// 2. 准备轮换资源池（部员与宿管）
	var members []model.User
	if len(req.MemberPool) > 0 {
		repository.DB.Where("id IN ?", req.MemberPool).Find(&members)
	} else {
		repository.DB.Where("role = ?", model.RoleMember).Find(&members)
	}
	if len(members) == 0 {
		// 容错备用
		members = []model.User{
			{ID: 3, RealName: "李部员"},
			{ID: 4, RealName: "王部员"},
		}
	}

	if len(req.Buildings) == 0 {
		req.Buildings = []string{"东区7号楼", "西区12号楼", "南区3号楼"}
	}
	if req.ShiftPeriod == "" {
		req.ShiftPeriod = "19:00-21:00 晚查寝与安全巡查"
	}

	var generatedShifts []model.ScheduleShift
	cur := start
	dayIndex := 0

	for !cur.After(end) {
		dateStr := cur.Format("2006-01-02")
		weekday := cur.Weekday()

		// 单双周判定 (以年中的周数模2为基准)
		_, weekNum := cur.ISOWeek()
		weekType := "single"
		if weekNum%2 == 0 {
			weekType = "double"
		}

		// 根据规则类型匹配是否排班
		shouldSchedule := true
		if req.RuleType == "weekday" && (weekday == time.Saturday || weekday == time.Sunday) {
			shouldSchedule = false
		}

		if shouldSchedule {
			// 轮换指派楼栋与部员
			bldgIndex := dayIndex % len(req.Buildings)
			targetBldg := req.Buildings[bldgIndex]

			m1 := members[dayIndex%len(members)]
			m2 := members[(dayIndex+1)%len(members)]
			assignedIDs, _ := json.Marshal([]uint{m1.ID, m2.ID})
			assignedNames := fmt.Sprintf("%s, %s", m1.RealName, m2.RealName)

			// 匹配协同宿管
			var preset model.DormRosterPreset
			managerName := "值班宿管"
			var managerID uint = 0
			if err := repository.DB.Where("building LIKE ?", "%"+targetBldg+"%").First(&preset).Error; err == nil {
				managerName = preset.RealName
				managerID = preset.BoundUserID
			}

			shift := model.ScheduleShift{
				PlanID:        plan.ID,
				Date:          dateStr,
				WeekType:      weekType,
				ShiftPeriod:   req.ShiftPeriod,
				Building:      targetBldg,
				Floor:         "全楼",
				MemberIDsJSON: string(assignedIDs),
				MemberNames:   assignedNames,
				DormManagerID: managerID,
				ManagerName:   managerName,
				Status:        "scheduled",
				Note:          fmt.Sprintf("轮换批次 #%d (%s周)", dayIndex+1, weekType),
				CreatedAt:     time.Now(),
			}
			generatedShifts = append(generatedShifts, shift)
		}

		cur = cur.AddDate(0, 0, 1)
		dayIndex++
	}

	if len(generatedShifts) > 0 {
		repository.DB.Create(&generatedShifts)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":         fmt.Sprintf("排班计划【%s】创建成功，基于【%s】规则自动生成了 %d 个工作班次！", req.Title, req.RuleType, len(generatedShifts)),
		"plan":            plan,
		"generated_count": len(generatedShifts),
	})
}

// PromoteMember 部长将旗下部员升职/调整职务 (升为副部长/复位部员)。
//
// 职务不是纯展示字段：model.HasDeductionAuthority 把"技术部门 + 副部长"作为打表授权条件，
// 所以这里每一次任免都在派发或回收打表权。于是必须校验职务枚举、禁止任命自己、
// 限制部长只能动本部部员，并把权限变化回传给前端提示，让"任免即联动权限"可验证。
func (mc *MinisterController) PromoteMember(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if operator.Status == "disabled" {
		c.JSON(http.StatusForbidden, gin.H{"error": "该账号已被停用"})
		return
	}
	// 变更职务会改动能否录入全校扣分，是权限链上最敏感的动作：必须当场重验登录口令
	if !requireStepUp(c, operator) {
		return
	}

	var req struct {
		MemberID uint   `json:"member_id" binding:"required"`
		Position string `json:"position" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误，请提供部员ID与目标职务"})
		return
	}

	position, valid := model.NormalizePosition(req.Position)
	// 部长职务跟着账号角色 role=minister 走，由技术维护组在账号管理里派发；
	// 放进任免接口等于让部长凭空造出同级部长。
	if !valid || position == model.PositionMinister {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "目标职务不合法，任免仅支持「副部长」与「部员」；部长请由技术维护组变更账号角色",
			"allowed": []string{model.PositionVice, model.PositionMember},
		})
		return
	}

	var targetUser model.User
	if err := repository.DB.First(&targetUser, req.MemberID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到指定部员"})
		return
	}

	if targetUser.ID == operator.ID {
		c.JSON(http.StatusForbidden, gin.H{"error": "不能调整自己的职务，任免必须由他人或技术维护组执行"})
		return
	}
	if targetUser.Status == "disabled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该账号已被停用，请先由技术维护组恢复后再调整职务"})
		return
	}

	// 权限约束：普通部长只能调整本部门部员，且不得触及部长、宿管等其他身份。
	if operator.Role != model.RoleTechAdmin {
		if targetUser.Role != model.RoleMember {
			c.JSON(http.StatusForbidden, gin.H{"error": "部长只能调整部员职务，不能任命部长、宿管或其他角色"})
			return
		}
		if !sameDepartment(operator.Department, targetUser.Department) {
			c.JSON(http.StatusForbidden, gin.H{"error": "您只能任命或调整本部门部员的职位"})
			return
		}
	} else if targetUser.Role == model.RoleTechAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "技术维护组成员的职务由账号管理维护，不在任免接口内调整"})
		return
	}

	previousPosition := targetUser.Position
	if previousPosition == position {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("【%s】当前已是%s，无需重复调整", targetUser.RealName, position)})
		return
	}

	authorityBefore := model.HasDeductionAuthority(targetUser)
	targetUser.Position = position
	targetUser.UpdatedAt = time.Now()
	if err := repository.DB.Save(&targetUser).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "职务变更保存失败: " + err.Error()})
		return
	}
	authorityAfter := model.HasDeductionAuthority(targetUser)

	actionTitle := "任命升职"
	if position == model.PositionMember {
		actionTitle = "调回职务"
	}
	authorityNote := "打表权限不变"
	if authorityAfter && !authorityBefore {
		authorityNote = "并派发打表权限"
	} else if authorityBefore && !authorityAfter {
		authorityNote = "并回收打表权限"
	}

	logOperationAs(c, operator, "member.position_change", "user", targetUser.ID,
		fmt.Sprintf("将【%s】（%s）职务由 %s 调整为 %s，%s", targetUser.RealName, targetUser.Department, previousPosition, position, authorityNote))

	tip := ""
	switch {
	case authorityAfter && !authorityBefore:
		tip = "系统已自动派发【数据总结与宿管上午数据打表】权限，该成员重新进入工作台后即可看到专属分栏。"
	case authorityBefore && !authorityAfter:
		tip = "系统已自动回收打表权限，该成员的专属分栏与扣分接口立即失效。"
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("【%s】已由【%s】成功%s为【%s】！%s", targetUser.RealName, operator.RealName, actionTitle, position, tip),
		"user":    targetUser,
		"authority": gin.H{
			"before":  authorityBefore,
			"after":   authorityAfter,
			"granted": authorityAfter && !authorityBefore,
			"revoked": authorityBefore && !authorityAfter,
		},
		"tip": tip,
	})
}

// AdjustScore 部长调整部员积分。
//
// 文档 5.2 把"部长可灵活调分"当作激励手段，但灵活调分是唯一能凭空造出评优积分的通道，
// 因此这里同时收口四件事：理由必填、单次上限、同一部员七日累计额度、以及部长只能动本部门部员。
// 分值阈值由校级积分策略（service.LoadScorePolicy）决定，不再写死在代码里。
func (mc *MinisterController) AdjustScore(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	// 调整他人积分属高危操作：必须当场重验登录口令
	if !requireStepUp(c, operator) {
		return
	}

	var req AdjustScoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误，需同时提供 member_id、score_change 与 reason"})
		return
	}

	reason := strings.TrimSpace(req.Reason)
	if r := []rune(reason); len(r) < 4 || len(r) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "调整事由必须填写，长度 4 ~ 120 字；该理由会随流水长期留痕并接受复核"})
		return
	}

	policy := service.LoadScorePolicy()
	amount := req.ScoreChange
	if amount < 0 {
		amount = -amount
	}
	if amount > policy.ManualMaxSingle {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":             fmt.Sprintf("单次灵活调分不得超过 %d 分（校级积分策略），本次申请 %+d 分", policy.ManualMaxSingle, req.ScoreChange),
			"max_single_adjust": policy.ManualMaxSingle,
		})
		return
	}

	var user model.User
	if err := repository.DB.First(&user, req.MemberID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "部员不存在"})
		return
	}
	if user.Role != model.RoleMember {
		c.JSON(http.StatusForbidden, gin.H{"error": "灵活调分只面向在册部员账号"})
		return
	}
	if operator.Role != model.RoleTechAdmin && !sameDepartment(operator.Department, user.Department) {
		c.JSON(http.StatusForbidden, gin.H{"error": "只能调整本部门部员的积分"})
		return
	}

	used, err := service.ManualAdjustUsed(user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "额度核对失败: " + err.Error()})
		return
	}
	if used+amount > policy.ManualWeeklyQuota {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("【%s】近 7 天已凭灵活调分变动 %d 分，本次 %+d 分将超出 %d 分的周额度上限",
				user.RealName, used, req.ScoreChange, policy.ManualWeeklyQuota),
			"used_this_week": used,
			"weekly_quota":   policy.ManualWeeklyQuota,
		})
		return
	}

	// 增量更新而不是读-改-写，避免并发调分互相覆盖
	var balanceAfter int
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", user.ID).
			Update("total_score", gorm.Expr("total_score + ?", req.ScoreChange)).Error; err != nil {
			return err
		}
		var fresh model.User
		if err := tx.First(&fresh, user.ID).Error; err != nil {
			return err
		}
		balanceAfter = fresh.TotalScore
		return tx.Create(&model.MemberScoreLog{
			MemberID:     user.ID,
			MemberName:   user.RealName,
			ChangeType:   "manual_adjust",
			ScoreChange:  req.ScoreChange,
			BalanceAfter: balanceAfter,
			Reason:       reason,
			OperatorName: operator.RealName,
			CreatedAt:    time.Now(),
		}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "积分调整失败: " + err.Error()})
		return
	}

	needsReview := amount >= policy.ManualReviewAt
	message := fmt.Sprintf("已成功为部员 %s 调整积分 (%+d)，当前总积分：%d", user.RealName, req.ScoreChange, balanceAfter)
	if needsReview {
		message += fmt.Sprintf("；本次分值已达复核线（%d 分），已列入技术维护组复核清单", policy.ManualReviewAt)
	}

	reviewMark := ""
	if needsReview {
		reviewMark = "【达复核线】"
	}
	logOperationAs(c, operator, "member.score_adjust", "user", user.ID,
		fmt.Sprintf("为【%s】调整积分 %+d（现 %d 分）：理由：%s%s", user.RealName, req.ScoreChange, balanceAfter, reason, reviewMark))

	c.JSON(http.StatusOK, gin.H{
		"message":      message,
		"total_score":  balanceAfter,
		"needs_review": needsReview,
		"policy":       policy,
	})
}

// MemberPerformanceDTO 部员积分与考勤综合履职明细
type MemberPerformanceDTO struct {
	ID          uint   `json:"id"`
	RealName    string `json:"real_name"`
	Username    string `json:"username"`
	Department  string `json:"department"`
	Position    string `json:"position"` // "部员", "副部长"
	Phone       string `json:"phone"`
	Building    string `json:"building"`
	TotalScore  int    `json:"total_score"`
	DutyCount   int    `json:"duty_count"`   // 累计上工出勤班次
	LeaveCount  int    `json:"leave_count"`  // 请假次数
	MissedCount int    `json:"missed_count"` // 缺工次数
	HonorBadge  string `json:"honor_badge"`  // 荣誉称号
}

// GetAllMembers 获取部员列表及最优上工、最高积分统计
// 技术维护组支持浏览所有部门所有人；各部门部长默认浏览本部所有人
func (mc *MinisterController) GetAllMembers(c *gin.Context) {
	currentRole, _ := c.Get("role")
	currentUID := c.GetUint("user_id")

	var currentUser model.User
	repository.DB.First(&currentUser, currentUID)

	deptQuery := c.Query("department")
	roleStr := currentRole.(string)

	query := repository.DB.Model(&model.User{}).Where("role = ?", model.RoleMember)

	var departmentScope string
	if roleStr == model.RoleTechAdmin {
		// 技术维护组：支持全局视界，可按部门筛选，默认查看全部
		if deptQuery != "" && deptQuery != "全部部门" {
			query = query.Where("department LIKE ?", "%"+deptQuery+"%")
			departmentScope = deptQuery
		} else {
			departmentScope = "全校所有部门"
		}
	} else {
		// 各部门部长：默认严格隔离至“本部”，支持浏览本部所有人
		targetDept := currentUser.Department
		if targetDept == "" {
			targetDept = "纪检部"
		}
		query = query.Where("department LIKE ?", "%"+targetDept+"%")
		departmentScope = targetDept
	}

	var rawMembers []model.User
	query.Order("total_score desc").Find(&rawMembers)

	// 综合计算每名部员的上工出勤班次、请假次数与缺工
	var membersDTO []MemberPerformanceDTO
	var topScoreMember *MemberPerformanceDTO
	var bestDutyMember *MemberPerformanceDTO

	maxScore := -999
	maxDuty := -1
	totalScoreSum := 0

	for _, m := range rawMembers {
		// 1. 真实查寝班次统计
		var dutyCount int64
		repository.DB.Model(&model.ScheduleShift{}).Where("member_names LIKE ? AND status = 'completed'", "%"+m.RealName+"%").Count(&dutyCount)

		// 2. 真实请假次数统计
		var leaveCount int64
		repository.DB.Model(&model.LeaveRequest{}).Where("member_id = ?", m.ID).Count(&leaveCount)

		// 3. 真实缺工违纪统计
		var missedCount int64
		repository.DB.Model(&model.MemberScoreLog{}).Where("member_id = ? AND (change_type = 'late' OR change_type = 'penalty')", m.ID).Count(&missedCount)

		// 荣誉徽章
		badge := "在册履职"
		if m.TotalScore >= 110 {
			badge = "标兵先锋"
		} else if m.TotalScore >= 105 {
			badge = "优秀部员"
		}

		dto := MemberPerformanceDTO{
			ID:          m.ID,
			RealName:    m.RealName,
			Username:    m.Username,
			Department:  m.Department,
			Position:    m.Position,
			Phone:       m.Phone,
			Building:    m.Building,
			TotalScore:  m.TotalScore,
			DutyCount:   int(dutyCount),
			LeaveCount:  int(leaveCount),
			MissedCount: int(missedCount),
			HonorBadge:  badge,
		}

		// 评定最高积分标兵
		if m.TotalScore > maxScore {
			maxScore = m.TotalScore
			dtoCopy := dto
			topScoreMember = &dtoCopy
		}

		// 评定最优上工标兵 (出勤班次最高且缺工为0优先)
		if int(dutyCount) > maxDuty || (int(dutyCount) == maxDuty && int(missedCount) == 0) {
			maxDuty = int(dutyCount)
			dtoCopy := dto
			bestDutyMember = &dtoCopy
		}

		totalScoreSum += m.TotalScore
		membersDTO = append(membersDTO, dto)
	}

	avgScore := 0.0
	if len(membersDTO) > 0 {
		avgScore = float64(totalScoreSum) / float64(len(membersDTO))
	}

	c.JSON(http.StatusOK, gin.H{
		"department_scope":     departmentScope,
		"is_tech_admin":        roleStr == model.RoleTechAdmin,
		"total_members":        len(membersDTO),
		"department_avg_score": fmt.Sprintf("%.1f", avgScore),
		"top_score_member":     topScoreMember,
		"best_duty_member":     bestDutyMember,
		"items":                membersDTO,
	})
}

// -----------------------------------------------------------------------------
// 部长端：AI 交互式对话排表引擎 (支持多轮上下文修改、图片自动识别、严格匹配真实部员)
// -----------------------------------------------------------------------------

type ChatMessageItem struct {
	Role    string `json:"role"`    // "user", "assistant", "system"
	Content string `json:"content"` // 对话内容
}

type AIScheduleChatRequest struct {
	Messages []ChatMessageItem `json:"messages"`  // 多轮对话上下文
	ImageURL string            `json:"image_url"` // 可选上传的排班/课表图片
	PlanDate string            `json:"plan_date"` // 目标排班周或日期 (可选)
}

type SuggestedShiftItem struct {
	Date        string `json:"date"`         // YYYY-MM-DD
	ShiftPeriod string `json:"shift_period"` // 如 "19:00-21:00 晚查寝"
	Building    string `json:"building"`     // 楼栋
	MemberNames string `json:"member_names"` // 逗号隔开的真实部员姓名
	Remark      string `json:"remark"`       // 班次备注或AI说明
}

// ChatAISchedule 处理部长对话输入，多轮上下文修改排班，支持图片多模态输入，确保所有人员均为真实部员。
//
// 这一版之前的实现根本不碰模型：用"消息里有没有换/调/改/加"这些关键词拼一段"已为您智能调整"的话术，
// 把上传的图片写成一句固定的"多模态OCR识别结果"，班次则是"今天往后 5 天 × 4 栋楼"的模板循环，
// 部员为空时还会拿四个"测试员-xx"顶替——界面上看是 AI 在排班，实际全是本地字符串。
// 现在一律真实外呼：拿不到模型结论就是失败，不再退回任何模板。
func (mc *MinisterController) ChatAISchedule(c *gin.Context) {
	var req AIScheduleChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	if len(req.Messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入排表对话指令或需求"})
		return
	}

	// 1. 严格读取全校所有真实存在的活跃部员库；空库就是排不了班，不许用假名字凑
	var realMembers []model.User
	repository.DB.Where("role = ? AND status = ?", model.RoleMember, "active").Order("id asc").Find(&realMembers)
	if len(realMembers) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "系统内暂无在册活跃部员，无法排班；请先在部员名单中补录后再来。"})
		return
	}

	realMemberNames := make([]string, 0, len(realMembers))
	realMemberMap := make(map[string]model.User)
	for _, m := range realMembers {
		realMemberNames = append(realMemberNames, m.RealName)
		realMemberMap[m.RealName] = m
	}

	// 2. 文本引擎是唯一能产出排班的东西，未配置就明确告诉部长去配，而不是演一遍
	var textCfg model.AIConfig
	if err := repository.DB.Where("config_key = ?", "text_engine").First(&textCfg).Error; err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"ai_status": "disabled",
			"error":     "尚未配置文本 AI 引擎：请技术维护组在「AI 引擎配置与调度中枢」填好文本引擎的端点、密钥与模型名并启用。",
		})
		return
	}
	if !ai.IsConfigured(&textCfg) {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"ai_status": "disabled",
			"error":     "文本 AI 引擎未启用或端点/密钥不完整，暂时无法进行 AI 对话排班。",
		})
		return
	}

	now := time.Now()
	todayStr := now.Format("2006-01-02")
	windowEnd := now.AddDate(0, 0, scheduleHorizonDays)

	// 3. 可选图片：真实调用视觉引擎转写课程表/便签，转写不出来就报错，不写假 OCR 结论
	imageNote := ""
	if strings.TrimSpace(req.ImageURL) != "" {
		// 图片来源不合法是用法问题，不该记到上游账上（502 会让部长以为是模型坏了而反复重试）
		src := strings.TrimSpace(req.ImageURL)
		if !strings.HasPrefix(src, "data:image/") && !strings.HasPrefix(src, "/") &&
			!strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "随附图片未能识别：请按页面提示重新选择本地图片，或改用文字描述排班需求。",
			})
			return
		}
		var visionCfg model.AIConfig
		if err := repository.DB.Where("config_key = ?", "vision_engine").First(&visionCfg).Error; err != nil || !ai.IsConfigured(&visionCfg) {
			c.JSON(http.StatusBadRequest, gin.H{
				"ai_status": "disabled",
				"error":     "本次附带了图片，但视觉 AI 引擎未配置或未启用；请改用文字描述排班需求，或先让技术维护组配置视觉引擎。",
			})
			return
		}
		transcript, err := ai.CallVisionDescription(&visionCfg, req.ImageURL,
			"请把这张课程表/值班表/请假便签逐条转写成纯文本，保留日期、星期、节次或时段、楼栋与人名，不要总结也不要补写图片上没有的内容。")
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"ai_status": "failed",
				"error":     "图片转写失败，本次未产生排班：" + err.Error(),
			})
			return
		}
		imageNote = transcript
	}

	// 4. 组装提示词：真实部员清单、可排日期窗口、在册楼栋，全部由服务端给出
	var allowedBuildings []string
	repository.DB.Model(&model.User{}).Where("building <> ''").Distinct("building").Order("building asc").Pluck("building", &allowedBuildings)

	var sb strings.Builder
	sb.WriteString("你是学管会纪检部的排班助手。硬性约束：\n")
	sb.WriteString("1) 只能使用下列真实在册部员姓名，不得出现任何清单外的人名：")
	sb.WriteString(strings.Join(realMemberNames, "、"))
	sb.WriteString("。\n")
	sb.WriteString(fmt.Sprintf("2) 排班日期只能落在 %s 至 %s 之间（含两端），格式 YYYY-MM-DD；今天就是 %s。\n",
		todayStr, windowEnd.Format("2006-01-02"), todayStr))
	if len(allowedBuildings) > 0 {
		sb.WriteString("3) 楼栋优先从这些在册楼栋里选：" + strings.Join(allowedBuildings, "、") + "。\n")
	}
	sb.WriteString("4) 每个班次至少 1 名部员、至多 2 名，同一部员同一天不要重复排到不同楼栋。\n")
	sb.WriteString(`只输出一个 JSON 对象，不要 markdown 代码块，结构为：` +
		`{"reply":"给部长看的一两句中文说明","shifts":[{"date":"YYYY-MM-DD","shift_period":"如 19:00-21:00 晚查寝","building":"楼栋","member_names":"张三、李四","remark":"排班依据"}]}`)

	messages := []ai.ChatMessage{{Role: "system", Content: sb.String()}}
	if start := len(req.Messages) - scheduleHistoryTurns; start > 0 {
		req.Messages = req.Messages[start:]
	}
	for _, m := range req.Messages {
		role := strings.TrimSpace(m.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if len([]rune(content)) > scheduleMaxTurnRunes {
			content = string([]rune(content)[:scheduleMaxTurnRunes])
		}
		messages = append(messages, ai.ChatMessage{Role: role, Content: content})
	}
	if imageNote != "" {
		messages = append(messages, ai.ChatMessage{
			Role:    "user",
			Content: "【服务端视觉引擎对随附图片的真实转写，请据此安排以避开冲突】\n" + imageNote,
		})
	}

	rawOutput, callErr := ai.ChatCompletion(
		textCfg.Endpoint, textCfg.APIKey, textCfg.ModelName, messages, textCfg.Temperature, textCfg.MaxTokens)
	if callErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"ai_status": "failed",
			"error":     "排班模型调用失败，本次没有生成任何草案：" + callErr.Error(),
		})
		return
	}

	// 5. 解析模型报文；解析不出排班结构就是失败，不退回模板班次
	var parsed struct {
		Reply  string               `json:"reply"`
		Shifts []SuggestedShiftItem `json:"shifts"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(rawOutput)), &parsed); err != nil || len(parsed.Shifts) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{
			"ai_status":  "failed",
			"error":      "模型返回的内容无法解析为排班结构，本次没有生成任何草案。可以把要求说得更具体一些再试。",
			"raw_output": rawOutput,
		})
		return
	}

	// 6. 硬核校验：人名必须在册、日期必须在窗口内；不在册的姓名剔除，而不是替别人顶上
	var verifiedShifts []SuggestedShiftItem
	rejectedNames := 0
	droppedShifts := 0
	for _, s := range parsed.Shifts {
		date := strings.TrimSpace(s.Date)
		d, dateErr := time.ParseInLocation("2006-01-02", date, time.Local)
		if dateErr != nil || d.Before(now.AddDate(0, 0, -1)) || d.After(windowEnd) {
			droppedShifts++
			continue
		}
		if strings.TrimSpace(s.Building) == "" || strings.TrimSpace(s.ShiftPeriod) == "" {
			droppedShifts++
			continue
		}

		var validNames []string
		for _, n := range splitNames(s.MemberNames) {
			if _, ok := realMemberMap[n]; ok {
				validNames = append(validNames, n)
				continue
			}
			rejectedNames++
		}
		if len(validNames) == 0 {
			droppedShifts++
			continue
		}
		s.Date = date
		s.MemberNames = strings.Join(validNames, "、")
		verifiedShifts = append(verifiedShifts, s)
	}

	if len(verifiedShifts) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{
			"ai_status": "failed",
			"error": fmt.Sprintf(
				"模型给出的 %d 个班次经真实在册部员与日期窗口核验后全部不合格（不在册姓名 %d 个、超窗或字段缺失班次 %d 个），已拒绝任何草案入库。请把要求说得更具体一些。",
				len(parsed.Shifts), rejectedNames, droppedShifts),
		})
		return
	}

	// 7. 回执里如实说明服务端核验做了什么——这段是事实陈述，不是模型结论
	aiReply := strings.TrimSpace(parsed.Reply)
	if aiReply == "" {
		aiReply = "模型未附带说明文字，以下为排班草案。"
	}
	aiReply += fmt.Sprintf(
		"\n\n【服务端真实核验】在册部员 %d 名；模型给出 %d 个班次，剔除不在册姓名 %d 个、丢弃超窗或字段缺失班次 %d 个，最终可入库 %d 个。确认无误后点「一键应用入库」。",
		len(realMemberNames), len(parsed.Shifts), rejectedNames, droppedShifts, len(verifiedShifts))
	if imageNote != "" {
		aiReply += "\n（本次已真实调用视觉引擎转写随附图片作为排班依据。）"
	}

	c.JSON(http.StatusOK, gin.H{
		"reply":            aiReply,
		"suggested_shifts": verifiedShifts,
		"real_members":     realMemberNames,
		"server_date":      todayStr,
		"ai_status":        ai.StatusReal,
		"model":            textCfg.ModelName,
	})
}

// scheduleHorizonDays 与 scheduleHistoryTurns/scheduleMaxTurnRunes 约束 AI 排班的取值范围：
// 日期只允许往后两周，多轮上下文只带最近若干条，单条长度封顶，避免超长报文把上游费用与延迟拉爆。
const (
	scheduleHorizonDays  = 14
	scheduleHistoryTurns = 8
	scheduleMaxTurnRunes = 2000
)

// stripJSONFence 模型偶尔仍会把 JSON 包进 ```json 代码块，解析前先剥掉。
func stripJSONFence(s string) string {
	out := strings.TrimSpace(s)
	if !strings.HasPrefix(out, "```") {
		return out
	}
	if i := strings.Index(out, "\n"); i >= 0 {
		out = out[i+1:]
	}
	out = strings.TrimSpace(out)
	return strings.TrimSuffix(out, "```")
}

// ApplyAISchedule 一键将 AI 建议的排班班次正式落盘到系统排班计划中
func (mc *MinisterController) ApplyAISchedule(c *gin.Context) {
	var req struct {
		PlanTitle string               `json:"plan_title"`
		Shifts    []SuggestedShiftItem `json:"shifts" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供需要落盘的排班班次列表"})
		return
	}

	userID := c.GetUint("user_id")
	if req.PlanTitle == "" {
		req.PlanTitle = fmt.Sprintf("AI智能对话交互排班计划_%s", time.Now().Format("2006-01-02"))
	}

	startDate := req.Shifts[0].Date
	endDate := req.Shifts[len(req.Shifts)-1].Date

	plan := model.SchedulePlan{
		Title:       req.PlanTitle,
		RuleType:    "ai_interactive",
		StartDate:   startDate,
		EndDate:     endDate,
		Description: fmt.Sprintf("由部长通过 AI 对话排表引擎确认生成，共包含 %d 个标准班次", len(req.Shifts)),
		CreatedBy:   userID,
		Status:      "active",
		CreatedAt:   time.Now(),
	}
	repository.DB.Create(&plan)

	// 批量持久化班次
	count := 0
	for _, s := range req.Shifts {
		shift := model.ScheduleShift{
			PlanID:      plan.ID,
			Date:        s.Date,
			ShiftPeriod: s.ShiftPeriod,
			Building:    s.Building,
			MemberNames: s.MemberNames,
			Status:      "scheduled",
		}
		repository.DB.Create(&shift)
		count++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":      fmt.Sprintf("排班表已成功持久化并同步发布生效！共生成 %d 个班次。", count),
		"plan_id":      plan.ID,
		"shifts_count": count,
	})
}

// GetSchedules 获取全部排班班次列表
func (mc *MinisterController) GetSchedules(c *gin.Context) {
	date := c.Query("date")
	query := repository.DB.Order("date asc")
	if date != "" {
		query = query.Where("date = ?", date)
	}

	var shifts []model.ScheduleShift
	query.Limit(100).Find(&shifts)

	c.JSON(http.StatusOK, gin.H{
		"total": len(shifts),
		"items": shifts,
	})
}

// MemberDutyStatusItem 部员本周值班状态项
type MemberDutyStatusItem struct {
	ID         uint   `json:"id"`
	RealName   string `json:"real_name"`
	Department string `json:"department"`
	Status     string `json:"status"`      // "completed" (已值班-绿), "unworked" (未值班-蓝/灰), "missed" (旷工-红)
	StatusText string `json:"status_text"` // "已值班", "本周未值班", "旷工/缺勤"
	ShiftCount int    `json:"shift_count"` // 本周出勤班次数
	MissCount  int    `json:"miss_count"`  // 本周缺勤次数
	ShiftsInfo string `json:"shifts_info"` // 涉及班次摘要
}

// GetWeekDutyStatus 获取当前周部员排班履职三色状态大盘 (已值班/未值班/旷工 姓名颜色高亮)
func (mc *MinisterController) GetWeekDutyStatus(c *gin.Context) {
	now := time.Now()

	// 计算本周周一和周日 (ISO 周，周一为一周起始)
	weekday := int(now.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := now.AddDate(0, 0, -weekday+1)
	sunday := monday.AddDate(0, 0, 6)

	mondayStr := monday.Format("2006-01-02")
	sundayStr := sunday.Format("2006-01-02")

	// 1. 获取本周所有班次记录
	var weekShifts []model.ScheduleShift
	repository.DB.Where("date >= ? AND date <= ?", mondayStr, sundayStr).Order("date asc, id asc").Find(&weekShifts)

	// 2. 获取所有在册部员
	var allMembers []model.User
	repository.DB.Where("role = ? AND status = ?", model.RoleMember, "active").Order("department asc, id asc").Find(&allMembers)
	if len(allMembers) == 0 {
		repository.DB.Where("role = ?", model.RoleMember).Find(&allMembers)
	}

	// 统计每位部员在本周的班次
	type MemberWeekRecord struct {
		CompletedShifts []string
		MissedShifts    []string
		ScheduledShifts []string
	}
	memberRecordMap := make(map[string]*MemberWeekRecord)
	for _, m := range allMembers {
		memberRecordMap[m.RealName] = &MemberWeekRecord{}
	}

	for _, s := range weekShifts {
		names := strings.Split(s.MemberNames, "、")
		if len(names) == 1 {
			names = strings.Split(s.MemberNames, ",")
		}
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			rec, exists := memberRecordMap[n]
			if !exists {
				rec = &MemberWeekRecord{}
				memberRecordMap[n] = rec
			}
			desc := fmt.Sprintf("%s %s(%s)", s.Date[5:], s.Building, s.ShiftPeriod)
			if s.Status == "completed" {
				rec.CompletedShifts = append(rec.CompletedShifts, desc)
			} else if s.Status == "missed" {
				rec.MissedShifts = append(rec.MissedShifts, desc)
			} else {
				rec.ScheduledShifts = append(rec.ScheduledShifts, desc)
			}
		}
	}

	var completedList []MemberDutyStatusItem
	var unworkedList []MemberDutyStatusItem
	var missedList []MemberDutyStatusItem
	var allStatusList []MemberDutyStatusItem

	for _, m := range allMembers {
		rec := memberRecordMap[m.RealName]
		status := "unworked"
		statusText := "本周未值班"
		var shiftSummary []string

		if rec != nil && len(rec.MissedShifts) > 0 {
			// 旷工优先警示
			status = "missed"
			statusText = "本周有旷工/缺勤"
			shiftSummary = append(shiftSummary, rec.MissedShifts...)
		} else if rec != nil && len(rec.CompletedShifts) > 0 {
			// 已值班
			status = "completed"
			statusText = fmt.Sprintf("已完成值班 (%d次)", len(rec.CompletedShifts))
			shiftSummary = append(shiftSummary, rec.CompletedShifts...)
		} else if rec != nil && len(rec.ScheduledShifts) > 0 {
			status = "unworked"
			statusText = fmt.Sprintf("待值班 (%d班)", len(rec.ScheduledShifts))
			shiftSummary = append(shiftSummary, rec.ScheduledShifts...)
		}

		item := MemberDutyStatusItem{
			ID:         m.ID,
			RealName:   m.RealName,
			Department: m.Department,
			Status:     status,
			StatusText: statusText,
			ShiftCount: 0,
			MissCount:  0,
			ShiftsInfo: strings.Join(shiftSummary, "；"),
		}
		if rec != nil {
			item.ShiftCount = len(rec.CompletedShifts)
			item.MissCount = len(rec.MissedShifts)
		}

		if status == "completed" {
			completedList = append(completedList, item)
		} else if status == "missed" {
			missedList = append(missedList, item)
		} else {
			unworkedList = append(unworkedList, item)
		}
		allStatusList = append(allStatusList, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"week_range":        fmt.Sprintf("%s ~ %s", mondayStr, sundayStr),
		"total_members":     len(allMembers),
		"completed_count":   len(completedList),
		"unworked_count":    len(unworkedList),
		"missed_count":      len(missedList),
		"completed_members": completedList, // 绿色：已值班名单
		"unworked_members":  unworkedList,  // 蓝色/灰：未值班名单
		"missed_members":    missedList,    // 红色：旷工名单
		"all_members":       allStatusList,
		"week_shifts":       weekShifts,
	})
}
