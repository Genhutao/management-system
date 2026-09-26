package model

import "testing"

func TestNormalizePosition(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"部员", PositionMember, true},
		{"副部长", PositionVice, true},
		{"部长", PositionMinister, true},
		{" 副部长 ", PositionVice, true},
		{"代理副部长", "", false},
		{"vice", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := NormalizePosition(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("NormalizePosition(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestHasDeductionAuthority(t *testing.T) {
	cases := []struct {
		name string
		u    User
		want bool
	}{
		{"技术维护组", User{Role: RoleTechAdmin, Position: PositionMember}, true},
		{"技术部副部长", User{Role: RoleMember, Department: "组织部 · 技术组", Position: PositionVice}, true},
		{"技术部部长职务", User{Role: RoleMinister, Department: "技术部", Position: PositionVice}, true},
		{"纪检部副部长", User{Role: RoleMember, Department: "纪检部", Position: PositionVice}, false},
		{"技术部普通部员", User{Role: RoleMember, Department: "技术部", Position: PositionMember}, false},
		{"技术部干事脏职务", User{Role: RoleMember, Department: "技术部", Position: "代理副部长"}, false},
		{"停用账号", User{Role: RoleTechAdmin, Status: "disabled"}, false},
		{"宿管", User{Role: RoleDormManager, Department: "技术部", Position: PositionVice}, false},
	}
	for _, c := range cases {
		if got := HasDeductionAuthority(c.u); got != c.want {
			t.Errorf("%s: HasDeductionAuthority() = %v, want %v", c.name, got, c.want)
		}
	}
}
