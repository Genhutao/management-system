package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
)

// ScorePolicyController 积分策略与灵活调分复核。
//
// 灵活调分是唯一能凭空造出评优积分的通道，文档又把它写成部长的常规激励手段，
// 因此除了 AdjustScore 侧的额度收口，还需要一个能改规则、能看清单、能冲正的管理面。
type ScorePolicyController struct{}

// requireScoreGovernor 策略与复核面向技术维护组；部长只能读策略。
func requireScoreGovernor(c *gin.Context) (model.User, bool) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return operator, false
	}
	if operator.Role != model.RoleTechAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "积分策略与调分复核仅限技术维护组操作"})
		return operator, false
	}
	return operator, true
}

// GetPolicy 读取现行积分策略。部长也要能看到额度上限，好在提交前先对齐预期。
func (sc *ScorePolicyController) GetPolicy(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if operator.Role != model.RoleTechAdmin && operator.Role != model.RoleMinister {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅部长与技术维护组可查看积分策略"})
		return
	}
	c.JSON(http.StatusOK, service.LoadScorePolicy())
}

// SavePolicy 技术维护组修改积分策略。改的是全校规则，需要当场重验登录口令。
func (sc *ScorePolicyController) SavePolicy(c *gin.Context) {
	operator, ok := requireScoreGovernor(c)
	if !ok {
		return
	}
	if !requireStepUp(c, operator) {
		return
	}

	var req model.ScorePolicyConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
		return
	}

	policy, err := service.SaveScorePolicy(req, operator.RealName)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	logOperationAs(c, operator, "score_policy.update", "score_policy", policy.ID,
		fmt.Sprintf("积分策略改为：出勤 +%d / 旷工 -%d / 单次调分上限 %d / 七日额度 %d / 复核线 %d",
			policy.AttendanceBonus, policy.MissedPenalty, policy.ManualMaxSingle, policy.ManualWeeklyQuota, policy.ManualReviewAt))

	c.JSON(http.StatusOK, gin.H{"message": "积分策略已更新，立即对全校生效。", "policy": policy})
}

// adjustmentDTO 一条灵活调分流水及其复核状态。
type adjustmentDTO struct {
	ID           uint   `json:"id"`
	MemberID     uint   `json:"member_id"`
	MemberName   string `json:"member_name"`
	Department   string `json:"department"`
	ScoreChange  int    `json:"score_change"`
	BalanceAfter int    `json:"balance_after"`
	Reason       string `json:"reason"`
	OperatorName string `json:"operator_name"`
	CreatedAt    time.Time `json:"created_at"`
	NeedsReview  bool   `json:"needs_review"`
	Reversed     bool   `json:"reversed"`
	ReversalID   uint   `json:"reversal_id"`
}

// ListAdjustments 技术维护组复核清单：默认列出近 30 天的全部灵活调分，
// 可按"仅看待复核"过滤。被冲正过的流水保留在清单上并标注，冲正不是删除。
func (sc *ScorePolicyController) ListAdjustments(c *gin.Context) {
	if _, ok := requireScoreGovernor(c); !ok {
		return
	}

	days := 30
	if v := c.Query("days"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 365 {
			days = parsed
		}
	}
	since := time.Now().AddDate(0, 0, -days)

	var logs []model.MemberScoreLog
	if err := repository.DB.Where("change_type = ? AND created_at >= ?", "manual_adjust", since).
		Order("created_at desc").Limit(500).Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "调分清单读取失败: " + err.Error()})
		return
	}

	reversals := map[uint]uint{}
	if len(logs) > 0 {
		ids := make([]uint, 0, len(logs))
		for _, l := range logs {
			ids = append(ids, l.ID)
		}
		var rows []model.MemberScoreLog
		if err := repository.DB.Where("change_type = ? AND ref_log_id IN ?", "manual_reversal", ids).
			Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "冲正记录读取失败: " + err.Error()})
			return
		}
		for _, r := range rows {
			reversals[r.RefLogID] = r.ID
		}
	}

	policy := service.LoadScorePolicy()
	deptFilter := c.Query("department")
	reviewOnly := c.Query("review_only") == "1"

	memberIDs := make([]uint, 0, len(logs))
	seen := make(map[uint]bool, len(logs))
	for _, l := range logs {
		if !seen[l.MemberID] {
			seen[l.MemberID] = true
			memberIDs = append(memberIDs, l.MemberID)
		}
	}
	members := make(map[uint]model.User, len(memberIDs))
	if len(memberIDs) > 0 {
		var rows []model.User
		if err := repository.DB.Where("id IN ?", memberIDs).Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "部员信息读取失败: " + err.Error()})
			return
		}
		for _, u := range rows {
			members[u.ID] = u
		}
	}

	items := make([]adjustmentDTO, 0, len(logs))
	reviewCount := 0
	for _, l := range logs {
		department := members[l.MemberID].Department
		if deptFilter != "" && !sameDepartment(deptFilter, department) {
			continue
		}
		needsReview := abs(l.ScoreChange) >= policy.ManualReviewAt
		if needsReview {
			reviewCount++
		}
		if reviewOnly && !needsReview {
			continue
		}
		items = append(items, adjustmentDTO{
			ID:           l.ID,
			MemberID:     l.MemberID,
			MemberName:   l.MemberName,
			Department:   department,
			ScoreChange:  l.ScoreChange,
			BalanceAfter: l.BalanceAfter,
			Reason:       l.Reason,
			OperatorName: l.OperatorName,
			CreatedAt:    l.CreatedAt,
			NeedsReview:  needsReview,
			Reversed:     reversals[l.ID] != 0,
			ReversalID:   reversals[l.ID],
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"total":        len(items),
		"review_count": reviewCount,
		"days":         days,
		"policy":       policy,
		"items":        items,
	})
}

// ReverseAdjustment 冲正一条灵活调分：追加一条等额反向流水并把积分退回去。
// 原流水不改动，冲正可追溯；同一笔只能冲正一次。
func (sc *ScorePolicyController) ReverseAdjustment(c *gin.Context) {
	operator, ok := requireScoreGovernor(c)
	if !ok {
		return
	}
	if !requireStepUp(c, operator) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的流水 ID"})
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	reasonText := ""
	if err := c.ShouldBindJSON(&req); err == nil {
		reasonText = strings.TrimSpace(req.Reason)
	}
	if len([]rune(reasonText)) < 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "冲正必须填写理由（不少于 4 字），说明为何认定该次调分不成立"})
		return
	}

	var origin model.MemberScoreLog
	if err := repository.DB.First(&origin, uint(id)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "调分流水不存在"})
		return
	}
	if origin.ChangeType != "manual_adjust" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有部长灵活调分可以冲正，出勤与旷工结算请走对应业务流程"})
		return
	}
	var existing int64
	repository.DB.Model(&model.MemberScoreLog{}).
		Where("change_type = ? AND ref_log_id = ?", "manual_reversal", origin.ID).Count(&existing)
	if existing > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "该笔调分已经冲正过，不能重复冲正"})
		return
	}

	rollback := -origin.ScoreChange
	now := time.Now()
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("id = ?", origin.MemberID).
			Update("total_score", gorm.Expr("total_score + ?", rollback)).Error; err != nil {
			return err
		}
		var fresh model.User
		if err := tx.First(&fresh, origin.MemberID).Error; err != nil {
			return err
		}
		return tx.Create(&model.MemberScoreLog{
			MemberID:     origin.MemberID,
			MemberName:   origin.MemberName,
			RefLogID:     origin.ID,
			ChangeType:   "manual_reversal",
			ScoreChange:  rollback,
			BalanceAfter: fresh.TotalScore,
			Reason:       fmt.Sprintf("冲正流水 #%d（%s 调 %s %+d）：%s", origin.ID, origin.OperatorName, origin.MemberName, origin.ScoreChange, reasonText),
			OperatorName: operator.RealName,
			CreatedAt:    now,
		}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "冲正失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "member.score_reverse", "member_score_log", origin.ID,
		fmt.Sprintf("冲正流水 #%d：为【%s】退回 %+d 分（%s）", origin.ID, origin.MemberName, rollback, reasonText))

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已冲正流水 #%d，【%s】积分退回 %+d 分。", origin.ID, origin.MemberName, rollback)})
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
