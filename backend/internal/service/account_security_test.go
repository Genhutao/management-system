package service

import (
	"strings"
	"testing"
)

func itemByID(t *testing.T, a SecurityAssessment, id string) SecurityItem {
	t.Helper()
	for _, it := range a.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("结果中缺少评分项 %s", id)
	return SecurityItem{}
}

// 理想账号：自设强口令 + 环境全确认 + L3 全通道 TOTP，走 HTTPS
func TestAssessAccountSecurityFullScore(t *testing.T) {
	a := AssessAccountSecurity(SecurityInput{
		PasswordSet:       true,
		PasswordStrength:  PasswordStrengthStrong,
		HasSessionHistory: true,
		PendingEnvLogins:  0,
		IPTrustworthy:     true,
		TotpEnabled:       true,
		AllLoginPathsMFA:  true,
		TransportHTTPS:    true,
	})
	if a.Total != 100 || a.Level != "优秀" {
		t.Fatalf("期望 100/优秀，实际 %d/%s", a.Total, a.Level)
	}
	if len(a.Notices) != 0 {
		t.Fatalf("HTTPS + 可信 IP 下不应有系统公示，实际 %v", a.Notices)
	}
	if len(a.Actionable) != 0 {
		t.Fatalf("满分不应有待办项，实际 %v", a.Actionable)
	}
}

// 存量账号：口令未改过 → 两项都是 0，且强度项标注"无法回测"而不是谎称弱
func TestAssessAccountSecurityLegacyAccount(t *testing.T) {
	a := AssessAccountSecurity(SecurityInput{
		PasswordSet:      false,
		PasswordStrength: "",
		IPTrustworthy:    true,
		TransportHTTPS:   true,
	})
	if got := itemByID(t, a, SecurityItemStrength); got.Status != "unknown" || !strings.Contains(got.Detail, "无法回测") {
		t.Fatalf("存量口令应标未评定，实际 %+v", got)
	}
	if got := itemByID(t, a, SecurityItemPasswordSet); got.Score != 0 || got.Status != "fail" {
		t.Fatalf("初始口令应判不合格，实际 %+v", got)
	}
	// 口令 0 + 强度 0 + 环境 25（无历史可比）+ 二次验证 L2 18 = 43
	if a.Total != 43 {
		t.Fatalf("存量账号总分期望 43，实际 %d（%+v）", a.Total, a.Items)
	}
}

// 未配置 TRUSTED_PROXIES 时环境项退出判定，总分按其余三项折算，
// 不能让部署缺陷扣在个人头上，也不能因此假绿。
func TestAssessAccountSecurityExcludesUntrustedIP(t *testing.T) {
	a := AssessAccountSecurity(SecurityInput{
		PasswordSet:       true,
		PasswordStrength:  PasswordStrengthStrong,
		HasSessionHistory: true,
		PendingEnvLogins:  3,
		IPTrustworthy:     false,
		TransportHTTPS:    false,
	})
	env := itemByID(t, a, SecurityItemEnvironment)
	if env.Scored {
		t.Fatalf("IP 不可信时环境项应退出判定，实际 %+v", env)
	}
	factor := itemByID(t, a, SecurityItemSecondFactor)
	if factor.Score != 10 {
		t.Fatalf("IP 不可信时二次验证只能到 L1，实际 %+v", factor)
	}
	// 口令 25 + 强度 25 + 二次验证 10 = 60 / 75 → 80
	if a.Total != 80 {
		t.Fatalf("折算后总分期望 80，实际 %d", a.Total)
	}
	if len(a.Notices) != 2 || !strings.Contains(a.Notices[0], "HTTPS") {
		t.Fatalf("应公示 HTTPS 与 IP 两项系统风险，实际 %v", a.Notices)
	}
}

func TestAssessAccountSecurityPendingEnvironment(t *testing.T) {
	cases := []struct {
		pending   int
		wantScore int
		wantStat  string
	}{
		{0, 25, "ok"},
		{1, 20, "warn"},
		{5, 0, "fail"},
		{9, 0, "fail"},
	}
	for _, c := range cases {
		a := AssessAccountSecurity(SecurityInput{
			PasswordSet:       true,
			PasswordStrength:  PasswordStrengthStrong,
			HasSessionHistory: true,
			PendingEnvLogins:  c.pending,
			IPTrustworthy:     true,
			TransportHTTPS:    true,
		})
		env := itemByID(t, a, SecurityItemEnvironment)
		if env.Score != c.wantScore || env.Status != c.wantStat {
			t.Errorf("pending=%d 期望 %d/%s，实际 %d/%s", c.pending, c.wantScore, c.wantStat, env.Score, env.Status)
		}
	}
}

// L3 完整性规则：绑定 TOTP 但仍有免口令通道（宿管三要素）时，只按 L2 给分。
func TestAssessAccountSecuritySecondFactorLevels(t *testing.T) {
	totpNoEnforce := AssessAccountSecurity(SecurityInput{
		PasswordSet: true, PasswordStrength: PasswordStrengthStrong,
		HasSessionHistory: true, IPTrustworthy: true, TransportHTTPS: true,
		TotpEnabled: true, AllLoginPathsMFA: false,
	})
	if got := itemByID(t, totpNoEnforce, SecurityItemSecondFactor); got.Score != 18 {
		t.Fatalf("TOTP 未覆盖全部通道应按 L2=18，实际 %+v", got)
	}

	totpEnforced := AssessAccountSecurity(SecurityInput{
		PasswordSet: true, PasswordStrength: PasswordStrengthStrong,
		HasSessionHistory: true, IPTrustworthy: true, TransportHTTPS: true,
		TotpEnabled: true, AllLoginPathsMFA: true,
	})
	if got := itemByID(t, totpEnforced, SecurityItemSecondFactor); got.Score != itemMax {
		t.Fatalf("全通道强制 TOTP 应得 L3=25，实际 %+v", got)
	}

	minimal := AssessAccountSecurity(SecurityInput{TransportHTTPS: true})
	if got := itemByID(t, minimal, SecurityItemSecondFactor); got.Score != 10 {
		t.Fatalf("无历史可信 IP 时应有 L1=10（危险操作口令核验），实际 %+v", got)
	}
}

func TestAssessAccountSecurityBaseline(t *testing.T) {
	a := AssessAccountSecurity(SecurityInput{Privileged: true, TransportHTTPS: true, IPTrustworthy: true})
	if a.Baseline != BaselinePrivileged {
		t.Fatalf("高权账号基准线应为 %d，实际 %d", BaselinePrivileged, a.Baseline)
	}
	b := AssessAccountSecurity(SecurityInput{Privileged: false, TransportHTTPS: true, IPTrustworthy: true})
	if b.Baseline != BaselineRegular {
		t.Fatalf("普通部员基准线应为 %d，实际 %d", BaselineRegular, b.Baseline)
	}
	// 基准线只标注不拦截：低于线不产生任何收权动作，Actionable 只来自未达标项
	if b.MeetsLine {
		t.Fatal("默认空账号不应达标")
	}
}
