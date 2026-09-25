package service

import (
	"fmt"
	"sort"
	"strings"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// CandidateScore 替补候选人综合负荷评估结构
type CandidateScore struct {
	User           model.User `json:"user"`
	DutyCount      int64      `json:"duty_count"`       // 累计/近期排班上工班次
	HasConflict    bool       `json:"has_conflict"`     // 当天是否已有班次冲突
	OnLeave        bool       `json:"on_leave"`         // 当天是否有请假
	SameDepartment bool       `json:"same_department"`  // 是否本部部员优先
	Recommendation string     `json:"recommendation"`   // 推荐理由说明
}

// FindBestSubstituteForShift 根据人员信息与历史出勤算法优选最近上工最少的人员替补
func FindBestSubstituteForShift(shiftID uint, excludeMemberID uint) (*model.User, string, error) {
	var shift model.ScheduleShift
	if err := repository.DB.First(&shift, shiftID).Error; err != nil {
		return nil, "", fmt.Errorf("未找到对应的排班班次: %w", err)
	}

	var excludeUser model.User
	repository.DB.First(&excludeUser, excludeMemberID)

	// 1. 查询所有在册可指派部员 (排除请假本人)
	var members []model.User
	if err := repository.DB.Where("role = ? AND status = 'active' AND id != ?", model.RoleMember, excludeMemberID).Find(&members).Error; err != nil {
		return nil, "", err
	}

	if len(members) == 0 {
		return nil, "", fmt.Errorf("系统内暂无其他可用空闲部员")
	}

	var candidatePool []CandidateScore

	for _, m := range members {
		// 检查当天是否有排班冲突
		var conflictCount int64
		repository.DB.Model(&model.ScheduleShift{}).
			Where("date = ? AND (member_names LIKE ? OR member_ids_json LIKE ?)", shift.Date, "%"+m.RealName+"%", fmt.Sprintf("%%%d%%", m.ID)).
			Count(&conflictCount)

		// 检查当天是否有请假
		var leaveCount int64
		repository.DB.Model(&model.LeaveRequest{}).
			Where("member_id = ? AND status IN ('pending', 'approved')", m.ID).
			Count(&leaveCount)

		if conflictCount > 0 || leaveCount > 0 {
			continue // 排除冲突人员
		}

		// 计算其在系统中的累计/近期排班出勤班次
		var dutyCount int64
		repository.DB.Model(&model.ScheduleShift{}).
			Where("member_names LIKE ?", "%"+m.RealName+"%").
			Count(&dutyCount)

		isSameDept := false
		if excludeUser.Department != "" && strings.Contains(m.Department, strings.Split(excludeUser.Department, " · ")[0]) {
			isSameDept = true
		}

		reason := fmt.Sprintf("系统算法优选：%s（所属 %s，近期上工仅 %d 班次，出勤负荷最低，同日无冲突）",
			m.RealName, m.Department, dutyCount)

		candidatePool = append(candidatePool, CandidateScore{
			User:           m,
			DutyCount:      dutyCount,
			HasConflict:    false,
			OnLeave:        false,
			SameDepartment: isSameDept,
			Recommendation: reason,
		})
	}

	if len(candidatePool) == 0 {
		return nil, "", fmt.Errorf("当前班次日期（%s）其他所有部员均有排班或处于请假中，未能匹配到无冲突人员", shift.Date)
	}

	// 排序核心准则：
	// 1. 最近上工班次最少者优先 (DutyCount 升序排列，劳逸均等)
	// 2. 上工次数相同时，同部门优先
	// 3. 积分较高者/稳定部员优先
	sort.Slice(candidatePool, func(i, j int) bool {
		if candidatePool[i].DutyCount != candidatePool[j].DutyCount {
			return candidatePool[i].DutyCount < candidatePool[j].DutyCount
		}
		if candidatePool[i].SameDepartment != candidatePool[j].SameDepartment {
			return candidatePool[i].SameDepartment
		}
		return candidatePool[i].User.TotalScore > candidatePool[j].User.TotalScore
	})

	best := candidatePool[0]
	return &best.User, best.Recommendation, nil
}
