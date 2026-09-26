package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 值班核销与旷工扣分分值来自校级积分策略（service.LoadScorePolicy），不再写死在代码里。

var (
	// ErrShiftNotPending 班次不处于待执行状态（已完成/已核销/已旷工）
	ErrShiftNotPending = errors.New("该班次已完成核销或已结算，不能重复操作")
	// ErrMemberNotFound 班次上没有可加分的在册部员
	ErrMemberNotFound = errors.New("班次上没有在册部员，无法结算积分")
)

// shiftMemberIDs 解析班次的部员 ID 列表（member_ids_json 优先，缺省回落姓名匹配）。
func shiftMemberIDs(tx *gorm.DB, shift model.ScheduleShift) []model.User {
	var ids []uint
	trimmed := strings.TrimSpace(shift.MemberIDsJSON)
	if trimmed != "" && trimmed != "null" {
		if err := jsonUnmarshalIDs(trimmed, &ids); err != nil {
			ids = nil
		}
	}

	users := make([]model.User, 0, len(ids))
	if len(ids) > 0 {
		tx.Where("id IN ? AND role = ? AND status = ?", ids, model.RoleMember, "active").Find(&users)
	}
	if len(users) == 0 && strings.TrimSpace(shift.MemberNames) != "" {
		for _, name := range splitDutyNames(shift.MemberNames) {
			var u model.User
			if err := tx.Where("real_name = ? AND role = ? AND status = ?", name, model.RoleMember, "active").
				Order("id asc").First(&u).Error; err == nil {
				users = append(users, u)
			}
		}
		// 姓名匹配结果去重
		seen := make(map[uint]bool)
		uniq := users[:0]
		for _, u := range users {
			if !seen[u.ID] {
				seen[u.ID] = true
				uniq = append(uniq, u)
			}
		}
		users = uniq
	}
	return users
}

// CompleteShiftAndReward 值班核销：原子地把班次置为 completed，并给当班部员各记一次出勤加分。
// 分值取自校级积分策略；条件更新保证并发下只有一次写入成功，重复调用返回 ErrShiftNotPending（幂等）。
// 第三个返回值是本次实际生效的每人分值，供留痕与前端提示如实回显。
func CompleteShiftAndReward(shiftID uint, operator model.User, note string) (*model.ScheduleShift, int, int, error) {
	bonus := LoadScorePolicy().AttendanceBonus
	var rewarded int
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		var shift model.ScheduleShift
		if err := tx.First(&shift, shiftID).Error; err != nil {
			return fmt.Errorf("班次不存在")
		}
		if shift.Status != "scheduled" && shift.Status != "in_progress" {
			return ErrShiftNotPending
		}

		// 条件更新：仅当仍处于未结算状态时生效，天然防重复核销
		res := tx.Model(&model.ScheduleShift{}).
			Where("id = ? AND status IN ?", shiftID, []string{"scheduled", "in_progress"}).
			Updates(map[string]interface{}{
				"status": "completed",
				"note":   appendNote(shift.Note, settlementNote(operator.RealName, note)),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrShiftNotPending
		}

		members := shiftMemberIDs(tx, shift)
		if len(members) == 0 {
			return ErrMemberNotFound
		}

		now := time.Now()
		reason := fmt.Sprintf("准时完成 %s %s 查寝班次", shift.Date, shift.ShiftPeriod)
		if bonus > 0 {
			for _, m := range members {
				if err := applyScoreChange(tx, m, bonus, "attendance_ok", reason, operator.RealName, shift.ID, now); err != nil {
					return err
				}
				rewarded++
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, bonus, err
	}

	var shift model.ScheduleShift
	_ = repository.DB.First(&shift, shiftID).Error
	return &shift, rewarded, bonus, nil
}

// SweepMissedShifts 旷工红牌：把指定日期（默认今天之前）仍为 scheduled 的班次置为 missed，
// 并按校级策略给当班部员各扣一次分。批准过的请假班次不判旷工。幂等：只有 scheduled 会被处理。
// 返回处理的班次数、涉及部员数与本次生效的每人扣分值。
func SweepMissedShifts(beforeDate string, operator model.User) (int, int, int, error) {
	if beforeDate == "" {
		beforeDate = time.Now().Format("2006-01-02")
	}
	penalty := LoadScorePolicy().MissedPenalty

	var shifts []model.ScheduleShift
	if err := repository.DB.Where("date < ? AND status = ?", beforeDate, "scheduled").
		Order("date asc").Find(&shifts).Error; err != nil {
		return 0, 0, penalty, err
	}

	settledShifts, settledMembers := 0, 0
	now := time.Now()
	for _, shift := range shifts {
		touched := 0
		claimed := false
		err := repository.DB.Transaction(func(tx *gorm.DB) error {
			// 已批准/待审的请假班次不判旷工
			var leaveCount int64
			tx.Model(&model.LeaveRequest{}).
				Where("shift_id = ? AND status IN ?", shift.ID, []string{"pending", "approved"}).
				Count(&leaveCount)

			newStatus := "missed"
			changeType := "penalty"
			reason := fmt.Sprintf("无故缺勤 %s %s 查寝班次", shift.Date, shift.ShiftPeriod)
			points := -penalty
			if leaveCount > 0 {
				newStatus = "completed" // 有请假记录：班次正常销班，不扣分
				changeType = "leave"
				points = 0
				reason = fmt.Sprintf("%s %s 班次因请假销班", shift.Date, shift.ShiftPeriod)
			}

			res := tx.Model(&model.ScheduleShift{}).
				Where("id = ? AND status = ?", shift.ID, "scheduled").
				Updates(map[string]interface{}{
					"status": newStatus,
					"note":   appendNote(shift.Note, settlementNote(operator.RealName, "系统旷工扫描")),
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return nil // 并发下已被处理
			}
			claimed = true

			members := shiftMemberIDs(tx, shift)
			touched = len(members)
			for _, m := range members {
				if points == 0 && changeType == "penalty" {
					continue // 校级策略关闭了旷工扣分，只标状态不写零分流水
				}
				if err := applyScoreChange(tx, m, points, changeType, reason, operator.RealName, shift.ID, now); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return settledShifts, settledMembers, penalty, err
		}
		if claimed {
			settledShifts++
			settledMembers += touched
		}
	}
	return settledShifts, settledMembers, penalty, nil
}

// applyScoreChange 记一条积分流水并同步 users.total_score。
func applyScoreChange(tx *gorm.DB, member model.User, change int, changeType, reason, operatorName string, shiftID uint, now time.Time) error {
	// 条件更新积分，避免读-改-写竞态
	if err := tx.Model(&model.User{}).Where("id = ?", member.ID).
		Update("total_score", gorm.Expr("total_score + ?", change)).Error; err != nil {
		return err
	}
	var fresh model.User
	if err := tx.First(&fresh, member.ID).Error; err != nil {
		return err
	}
	return tx.Create(&model.MemberScoreLog{
		MemberID:     member.ID,
		MemberName:   member.RealName,
		ShiftID:      shiftID,
		ChangeType:   changeType,
		ScoreChange:  change,
		BalanceAfter: fresh.TotalScore,
		Reason:       reason,
		OperatorName: operatorName,
		CreatedAt:    now,
	}).Error
}

func jsonUnmarshalIDs(raw string, out *[]uint) error {
	return json.Unmarshal([]byte(raw), out)
}

// splitDutyNames 拆分班次上的部员姓名（兼容顿号、逗号与空白混排）。
func splitDutyNames(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case '、', '，', ',', ';', '；', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func settlementNote(operator, detail string) string {
	if detail != "" {
		return fmt.Sprintf("【结算】%s（操作人：%s）", detail, operator)
	}
	return fmt.Sprintf("【结算】操作人：%s", operator)
}

func appendNote(existing, addition string) string {
	if strings.TrimSpace(existing) == "" {
		return addition
	}
	return existing + "；" + addition
}
