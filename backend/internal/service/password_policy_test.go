package service

import "testing"

func TestCheckPasswordPolicyRejects(t *testing.T) {
	cases := []struct {
		name     string
		password string
		username string
		phone    string
		want     string
	}{
		{"空口令", "", "tech_admin", "", "口令不能为空"},
		{"长度不足", "12345", "tech_admin", "", "口令至少需要 6 个字符"},
		{"初始口令命中常见表", "123456", "tech_admin", "", "口令过于常见，请更换"},
		{"常见口令大小写不敏感", "ABC123", "tech_admin", "", "口令过于常见，请更换"},
		{"同字符重复", "aaaaaa", "tech_admin", "", "口令不能由同一字符重复构成"},
		{"升序连续", "abcdef", "tech_admin", "", "口令为连续字符序列，过于简单"},
		{"降序连续", "654321", "tech_admin", "", "口令为连续字符序列，过于简单"},
		{"包含账号名", "member_li2026", "member_li", "", "口令不能包含账号名"},
		{"包含手机号", "pwd13800000003x", "member_li", "13800000003", "口令不能包含手机号"},
		{"手机号带分隔符仍拦截", "pwd13800000003x", "member_li", "138-0000-0003", "口令不能包含手机号"},
	}
	for _, c := range cases {
		got := CheckPasswordPolicy(c.password, c.username, c.phone)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: CheckPasswordPolicy(%q) = %v, want [%s]", c.name, c.password, got, c.want)
		}
	}
}

func TestCheckPasswordPolicyAccepts(t *testing.T) {
	cases := []struct {
		name     string
		password string
		username string
		phone    string
	}{
		{"过线但强度偏低", "12345a", "member_li", "13800000003"},
		{"合规混合口令", "Xgh2026!li", "member_li", "13800000003"},
		{"中文长口令", "学管会系统安全管理", "tech_admin", ""},
		{"短账号名不做包含判定", "ab2026cd", "ab", ""},
	}
	for _, c := range cases {
		if got := CheckPasswordPolicy(c.password, c.username, c.phone); got != nil {
			t.Errorf("%s: 期望允许，实际拒绝 %v", c.name, got)
		}
	}
}

func TestEvaluatePassword(t *testing.T) {
	cases := []struct {
		password string
		want     string
	}{
		{"", PasswordStrengthWeak},
		{"12345a", PasswordStrengthWeak},       // 6 字符，两类但不足 8
		{"aaaaaaaaaa", PasswordStrengthWeak},   // 10 字符，仅一类
		{"abcdefgh", PasswordStrengthWeak},     // 8 字符，仅一类
		{"abcdefgH", PasswordStrengthMedium},   // 8 字符，两类
		{"Abcdefgh1", PasswordStrengthMedium},  // 9 字符，三类但不足 10
		{"学管会系统安全管理", PasswordStrengthWeak},    // 长度够，仅一类
		{"Abcdefgh1!", PasswordStrengthStrong}, // 10 字符，四类
		{"Xgh2026!li", PasswordStrengthStrong}, // 10 字符，三类
	}
	for _, c := range cases {
		if got := EvaluatePassword(c.password); got != c.want {
			t.Errorf("EvaluatePassword(%q) = %s, want %s", c.password, got, c.want)
		}
	}
}
