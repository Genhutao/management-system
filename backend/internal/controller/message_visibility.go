package controller

import (
	"strings"

	"xgh-system/internal/model"
)

// 站内信收发矩阵（口径由使用方 2026-10-02 定）：
//
//	部员   → 本部门部员、本部门部长
//	部长   → 全校（放开）
//	宿管   → 全部部长、本楼栋宿管
//	技术组 → 全校
//	查看岗 → 只收不发
//
// canMessageBetween 是这条矩阵的唯一服务端口径：选人列表与发送接口必须共用它。
// 分成两处写一定会漂移，后果就是"入口出来了、点进去被拦"——
// 同 /deductions 那条 403 的成因，见《API接口文档.md》§4.1。
func canMessageBetween(from, to model.User) bool {
	// 发给自己没有对象，选人列表里也不该出现自己
	if from.ID == to.ID {
		return false
	}
	if !recipientReachable(to) {
		return false
	}

	switch from.Role {
	case model.RoleTechAdmin, model.RoleMinister:
		return true
	case model.RoleMember:
		// 部员的上限是本部门的部长；跨部门以及对宿管/查看岗/技术组一律不通
		if to.Role != model.RoleMember && to.Role != model.RoleMinister {
			return false
		}
		return sameDepartment(from.Department, to.Department)
	case model.RoleDormManager:
		if to.Role == model.RoleMinister {
			return true
		}
		if to.Role == model.RoleDormManager {
			return dormBuildingsMatch(from.Building, to.Building)
		}
		return false
	case model.RoleViewerExport:
		return false
	}
	// 未知角色一律发不出去：宁可功能不可用，也不能让越权发信通过
	return false
}

// recipientReachable 收件人必须是启用中的账号。停用账号进不来（AuthMiddleware
// 已按 status 拦下），给它发信只会攒一条永远没人清的未读。
// 空值按 active 处理，与 GORM 的 default:'active' 一致。
func recipientReachable(u model.User) bool {
	status := strings.TrimSpace(u.Status)
	return status == "" || status == "active"
}

// messageContact 选人列表的一项。刻意不返回 model.User：
// User 上的 phone 是带 json tag 的，任何一个部员都能把全校联系方式导出。
type messageContact struct {
	ID         uint   `json:"id"`
	RealName   string `json:"real_name"`
	Role       string `json:"role"`
	Department string `json:"department"`
	Position   string `json:"position"`
	Building   string `json:"building"`
}

// messageContactsOf 按矩阵筛出 from 能发给的账号，顺序稳定便于前端直接渲染。
func messageContactsOf(from model.User, all []model.User) []messageContact {
	contacts := []messageContact{}
	for _, u := range all {
		if !canMessageBetween(from, u) {
			continue
		}
		contacts = append(contacts, messageContact{
			ID:         u.ID,
			RealName:   u.RealName,
			Role:       u.Role,
			Department: u.Department,
			Position:   u.Position,
			Building:   u.Building,
		})
	}
	return contacts
}

// dormBuildingsMatch 判定两个宿管账号是否负责同一栋楼。
//
// 刻意不复用 deduction_controller.go 里那个同名的宽松口径：那边是给打表核对名册用的，
// 按"互相包含"匹配、且任一侧为空就放行——"写法差异不该变成新的硬拒"在录入场景是对的，
// 搬到发信上就变成两件事：① 空楼栋会被判成同一栋，等于两个没填楼栋的宿管全校互发；
// ② Contains 会把 2号楼 命中进 12号楼。发信范围必须收紧，所以这里两边为空一律判否，
// 并按提取出来的楼号数字比对。
func dormBuildingsMatch(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	na, okA := buildingNumber(a)
	nb, okB := buildingNumber(b)
	return okA && okB && na == nb
}

// buildingNumber 取楼栋名里第一段数字作为楼号；不含数字则不可比（返回 false）。
func buildingNumber(s string) (int, bool) {
	n, started := 0, false
	for _, r := range s {
		if r < '0' || r > '9' {
			if started {
				break
			}
			continue
		}
		started = true
		n = n*10 + int(r-'0')
	}
	return n, started
}
