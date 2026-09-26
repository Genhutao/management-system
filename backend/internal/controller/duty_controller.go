package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/service"
)

type DutyController struct{}

// CompleteShift 部长/宿管核销一次查寝：班次置 completed，当班部员各按校级策略记一次出勤加分。
// 文档 5.2 "干事准时完成一次排班查寝，系统自动记入 +5 积分" 的落地入口。
func (dc *DutyController) CompleteShift(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if operator.Role != model.RoleMinister && operator.Role != model.RoleTechAdmin && operator.Role != model.RoleDormManager {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅部长、宿管或技术维护组可核销班次"})
		return
	}

	shiftID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的班次 ID"})
		return
	}

	var req struct {
		Note string `json:"note"`
	}
	_ = c.ShouldBindJSON(&req)

	shift, rewarded, pointsPerMember, err := service.CompleteShiftAndReward(uint(shiftID), operator, req.Note)
	if err == service.ErrShiftNotPending {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err == service.ErrMemberNotFound {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "核销失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "duty.complete", "schedule_shift", shift.ID,
		fmt.Sprintf("核销班次 %s %s（%s），为 %d 名部员各记出勤 %+d 分", shift.Date, shift.ShiftPeriod, shift.Building, rewarded, pointsPerMember))

	c.JSON(http.StatusOK, gin.H{
		"message":         fmt.Sprintf("班次已核销，%d 名部员各 %+d 分。", rewarded, pointsPerMember),
		"shift":           shift,
		"rewarded_count":  rewarded,
		"points_per_user": pointsPerMember,
	})
}

// SweepMissedShifts 旷工扫描：把指定日期之前仍为 scheduled 的班次置 missed，并按校级策略每人扣一次分。
// 幂等，可重复执行；有请假记录的班次正常销班不扣分。文档 5.2 "无故缺勤自动红牌并扣分" 的落地入口。
func (dc *DutyController) SweepMissedShifts(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if operator.Role != model.RoleMinister && operator.Role != model.RoleTechAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅部长或技术维护组可执行旷工扫描"})
		return
	}

	beforeDate := c.Query("before_date")
	if beforeDate == "" {
		beforeDate = time.Now().Format("2006-01-02")
	}
	shifts, members, penaltyPoints, err := service.SweepMissedShifts(beforeDate, operator)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "扫描失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "duty.sweep_missed", "schedule_shift", 0,
		fmt.Sprintf("旷工扫描（截止 %s）：处理 %d 个班次，涉及 %d 名部员，每人 %+d 分", beforeDate, shifts, members, -penaltyPoints))

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("扫描完成：处理 %d 个过期未核销班次，涉及 %d 名部员，每人 %+d 分（有请假记录的销班不扣分）。",
			shifts, members, -penaltyPoints),
		"settled_shifts":   shifts,
		"members_hit":      members,
		"penalty_per_user": -penaltyPoints,
		"before_date":      beforeDate,
	})
}
