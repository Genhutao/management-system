package service

import (
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 积分策略默认值。校级参数表还没有记录时按这些值走，与规则写死在代码里时的行为完全一致。
const (
	DefaultAttendanceBonus   = 5
	DefaultMissedPenalty     = 5
	DefaultManualMaxSingle   = 10
	DefaultManualWeeklyQuota = 20
	DefaultManualReviewAt    = 5
)

// scorePolicyRowID 全库唯一一行配置的固定主键。
const scorePolicyRowID = uint(1)

// LoadScorePolicy 读取校级积分策略。表为空（老库刚升级完）时返回默认值而不落库，
// 避免只读接口产生写副作用。
func LoadScorePolicy() model.ScorePolicyConfig {
	policy := defaultScorePolicy()
	var stored model.ScorePolicyConfig
	if err := repository.DB.Where("id = ?", scorePolicyRowID).First(&stored).Error; err != nil {
		return policy
	}
	return normalizeScorePolicy(stored)
}

func defaultScorePolicy() model.ScorePolicyConfig {
	return model.ScorePolicyConfig{
		ID:                scorePolicyRowID,
		AttendanceBonus:   DefaultAttendanceBonus,
		MissedPenalty:     DefaultMissedPenalty,
		ManualMaxSingle:   DefaultManualMaxSingle,
		ManualWeeklyQuota: DefaultManualWeeklyQuota,
		ManualReviewAt:    DefaultManualReviewAt,
	}
}

// normalizeScorePolicy 兼容历史上分值被清空（0）的配置行：分值字段为 0 说明这行没写完，
// 回落默认值，而不是把加分与额度上限悄悄归零。
func normalizeScorePolicy(p model.ScorePolicyConfig) model.ScorePolicyConfig {
	if p.AttendanceBonus <= 0 && p.MissedPenalty <= 0 && p.ManualMaxSingle <= 0 {
		p.AttendanceBonus = DefaultAttendanceBonus
		p.MissedPenalty = DefaultMissedPenalty
		p.ManualMaxSingle = DefaultManualMaxSingle
	}
	if p.ManualWeeklyQuota <= 0 {
		p.ManualWeeklyQuota = DefaultManualWeeklyQuota
	}
	if p.ManualReviewAt <= 0 {
		p.ManualReviewAt = DefaultManualReviewAt
	}
	p.ID = scorePolicyRowID
	return p
}

// SaveScorePolicy 校验并落库积分策略。分值语义为绝对值，负数与越界一律拒绝。
func SaveScorePolicy(p model.ScorePolicyConfig, operatorName string) (model.ScorePolicyConfig, error) {
	between := func(name string, v, lo, hi int) error {
		if v < lo || v > hi {
			return fmt.Errorf("%s 取值非法：应在 %d ~ %d 之间", name, lo, hi)
		}
		return nil
	}
	if err := between("出勤加分", p.AttendanceBonus, 0, 100); err != nil {
		return p, err
	}
	if err := between("旷工扣分", p.MissedPenalty, 0, 100); err != nil {
		return p, err
	}
	if err := between("单次调分上限", p.ManualMaxSingle, 1, 100); err != nil {
		return p, err
	}
	if err := between("七日累计调分额度", p.ManualWeeklyQuota, 1, 500); err != nil {
		return p, err
	}
	if p.ManualWeeklyQuota < p.ManualMaxSingle {
		return p, errors.New("七日累计额度不能小于单次调分上限，否则单次上限永远用不到")
	}
	if err := between("复核阈值", p.ManualReviewAt, 1, 100); err != nil {
		return p, err
	}

	p.ID = scorePolicyRowID
	p.UpdatedAt = time.Now()
	p.UpdatedBy = operatorName

	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.ScorePolicyConfig
		err := tx.Where("id = ?", scorePolicyRowID).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&p).Error
		}
		if err != nil {
			return err
		}
		// 整行覆盖：策略字段少且都在表单里，不做逐字段判空
		return tx.Model(&model.ScorePolicyConfig{}).Where("id = ?", scorePolicyRowID).Updates(map[string]interface{}{
			"attendance_bonus":    p.AttendanceBonus,
			"missed_penalty":      p.MissedPenalty,
			"manual_max_single":   p.ManualMaxSingle,
			"manual_weekly_quota": p.ManualWeeklyQuota,
			"manual_review_at":    p.ManualReviewAt,
			"updated_by":          p.UpdatedBy,
			"updated_at":          p.UpdatedAt,
		}).Error
	})
	if err != nil {
		return p, err
	}
	return p, nil
}

// ManualAdjustUsed 统计某部员近 7 天已通过灵活调分变动的分值绝对值之和，用于额度上限判定。
// 冲正流水不计入：复核不通过而撤掉的调分不应继续占用部员额度。
func ManualAdjustUsed(memberID uint) (int, error) {
	since := time.Now().AddDate(0, 0, -7)
	var logs []model.MemberScoreLog
	if err := repository.DB.Where("member_id = ? AND change_type = ? AND created_at >= ?",
		memberID, "manual_adjust", since).Find(&logs).Error; err != nil {
		return 0, err
	}

	reversed := make(map[uint]bool)
	if len(logs) > 0 {
		ids := make([]uint, 0, len(logs))
		for _, l := range logs {
			ids = append(ids, l.ID)
		}
		var reversals []model.MemberScoreLog
		if err := repository.DB.Where("change_type = ? AND ref_log_id IN ?", "manual_reversal", ids).
			Find(&reversals).Error; err != nil {
			return 0, err
		}
		for _, r := range reversals {
			reversed[r.RefLogID] = true
		}
	}

	used := 0
	for _, l := range logs {
		if reversed[l.ID] {
			continue
		}
		used += int(math.Abs(float64(l.ScoreChange)))
	}
	return used, nil
}
