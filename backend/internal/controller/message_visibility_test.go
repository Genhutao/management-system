package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"xgh-system/internal/model"
)

func u(id uint, role, dept, building, status string) model.User {
	return model.User{
		ID: id, Role: role, Department: dept, Building: building,
		Status: status, RealName: "账号" + string(rune('A'+int(id))),
		Phone: "13800000000",
	}
}

// 站内信矩阵是"谁能发给谁"的唯一口径，选人列表和发送接口共用。
// 这里断言的不是实现细节，而是边界：谁不该通、一条都不能通。
func TestCanMessageBetweenMatrix(t *testing.T) {
	memberA := u(1, model.RoleMember, "纪检部", "", "active")
	memberB := u(2, model.RoleMember, "纪检部", "", "active")
	memberOther := u(3, model.RoleMember, "组织部", "", "active")
	ministerSame := u(4, model.RoleMinister, "纪检部", "", "active")
	ministerOther := u(5, model.RoleMinister, "组织部", "", "active")
	ministerCompound := u(6, model.RoleMinister, "技术组", "", "active")
	dormSameBuilding := u(7, model.RoleDormManager, "", "西区7号楼", "active")
	dormOtherBuilding := u(8, model.RoleDormManager, "", "12号楼", "active")
	dormTwoBuilding := u(9, model.RoleDormManager, "", "2号楼", "active")
	viewer := u(10, model.RoleViewerExport, "", "", "active")
	tech := u(11, model.RoleTechAdmin, "技术组", "", "active")
	memberCompound := u(12, model.RoleMember, "组织部 · 技术组", "", "active")
	disabledMinister := u(13, model.RoleMinister, "纪检部", "", "disabled")
	unknownRole := u(14, "hall_monitor", "", "", "active")

	cases := []struct {
		name string
		from model.User
		to   model.User
		want bool
	}{
		{"部员发本部门员", memberA, memberB, true},
		{"部员发跨部门员", memberA, memberOther, false},
		{"部员发本部门部长", memberA, ministerSame, true},
		{"部员发跨部门部长", memberA, ministerOther, false},
		{"部员发宿管", memberA, dormSameBuilding, false},
		{"部员发查看岗", memberA, viewer, false},
		{"部员发技术组", memberA, tech, false},
		{"复合部门按互相包含判定（复用现有口径）", memberCompound, ministerCompound, true},
		{"部长放开到全校-跨部门员", ministerSame, memberOther, true},
		{"部长放开到全校-宿管", ministerSame, dormSameBuilding, true},
		{"部长放开到全校-查看岗", ministerSame, viewer, true},
		{"部长放开到全校-技术组", ministerSame, tech, true},
		{"宿管发部长（不限部门）", dormSameBuilding, ministerOther, true},
		{"宿管发本楼栋宿管", dormSameBuilding, u(15, model.RoleDormManager, "", "7号楼", "active"), true},
		{"宿管发别楼栋宿管", dormSameBuilding, dormOtherBuilding, false},
		{"12号楼 发不到 2号楼（宽松互含会误判的那对）", dormOtherBuilding, dormTwoBuilding, false},
		{"宿管发部员", dormSameBuilding, memberA, false},
		{"技术组发全校", tech, memberOther, true},
		{"技术组发查看岗", tech, viewer, true},
		{"查看岗只收不发", viewer, ministerSame, false},
		{"查看岗发同部门部长", viewer, ministerSame, false},
		{"不能发给自己", memberA, memberA, false},
		{"停用账号收不到（部长虽有全校权限）", ministerSame, disabledMinister, false},
		{"未知角色一律不通", memberA, unknownRole, false},
		{"未知角色也发不出去", unknownRole, memberA, false},
	}

	for _, c := range cases {
		if got := canMessageBetween(c.from, c.to); got != c.want {
			t.Errorf("%s: 期望 canMessageBetween=%v，实际 %v", c.name, c.want, got)
		}
	}
}

// 楼栋判定不能用"包含"：12号楼 会命中 2号楼；也不能沿用打表那个空值放行的宽松口径，
// 否则两个都没填楼栋的宿管会被判成同一栋而互发。这条测试钉住两个坑。
func TestDormBuildingsMatchIsStrict(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"7号楼", "西区7号楼", true},
		{"西12号楼", "12号楼", true},
		{"12号楼", "2号楼", false},
		{"1号楼", "12号楼", false},
		{"A栋", "A栋", true},
		{"A栋", "B栋", false},
		{"", "7号楼", false},
		{"7号楼", "   ", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := dormBuildingsMatch(c.a, c.b); got != c.want {
			t.Errorf("dormBuildingsMatch(%q, %q) 期望 %v，实际 %v", c.a, c.b, c.want, got)
		}
	}
}

// 选人列表必须只含矩阵允许的人，且不能把联系方式带出去。
func TestMessageContactsOfFiltersAndOmitsPhone(t *testing.T) {
	member := u(1, model.RoleMember, "纪检部", "", "active")
	all := []model.User{
		member, // 自己，不该出现
		u(2, model.RoleMember, "纪检部", "", "active"),      // 同部门员，该出现
		u(3, model.RoleMinister, "纪检部", "", "active"),    // 本部门部长，该出现
		u(4, model.RoleMember, "组织部", "", "active"),      // 跨部门，不该出现
		u(5, model.RoleDormManager, "", "7号楼", "active"), // 宿管，不该出现
		u(6, model.RoleMinister, "纪检部", "", "disabled"),  // 停用，不该出现
	}

	got := messageContactsOf(member, all)
	if len(got) != 2 {
		t.Fatalf("部员应只看到 2 个可发对象，实际 %d: %+v", len(got), got)
	}

	// phone 在 model.User 上是带 json tag 的，所以列表绝不能直接返回 User
	if blob, err := json.Marshal(got); err != nil {
		t.Fatalf("序列化选人列表失败: %v", err)
	} else if strings.Contains(string(blob), "13800000000") {
		t.Errorf("选人列表里出现了手机号，等于让部员可导出全校联系方式")
	}
}
