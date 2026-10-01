package service

import (
	"fmt"
	"math"
)

// 个人安全评分：四项各 25 分，满分 100。
// 分数只用于展示与治理排序，不参与任何授权判定 —— 收权手段仍然只有
// status=="disabled" 与 HasDeductionAuthority 两条，语义不变。
const (
	SecurityItemPasswordSet  = "password_set"
	SecurityItemStrength     = "password_strength"
	SecurityItemEnvironment  = "login_environment"
	SecurityItemSecondFactor = "second_factor"

	itemMax = 25

	// 视觉标尺：权限越高，线画得越高。低于线只提示，不拦截。
	BaselinePrivileged = 85
	BaselineRegular    = 60
)

// SecurityInput 评分输入。由调用方从库里取，本文件不碰数据库，
// 便于把全部判分口径写成可单测的纯函数。
type SecurityInput struct {
	PasswordSet       bool   // 是否已自行设置过口令
	PasswordStrength  string // weak / medium / strong；空串 = 存量未评定
	HasSessionHistory bool   // 有无可比对的登录历史
	PendingEnvLogins  int    // 未确认的陌生环境登录次数
	IPTrustworthy     bool   // 客户端 IP 能否作为异地判定依据
	TotpEnabled       bool   // 已绑定 TOTP
	AllLoginPathsMFA  bool   // 是否所有登录通道（含宿管三要素）都强制 TOTP
	TransportHTTPS    bool   // 当前请求是否走 HTTPS
	Privileged        bool   // 打表/高权账号
}

// SecurityItem 单项得分与说明。Scored=false 表示本项未纳入判定，
// 总分按其余项折算，不给部署缺陷背锅，也不假绿。
type SecurityItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Score  int    `json:"score"`
	Max    int    `json:"max"`
	Status string `json:"status"` // ok / warn / fail / unknown
	Detail string `json:"detail"`
	Scored bool   `json:"scored"`
}

// SecurityAssessment 评分结果，直接作为自助端点的响应体。
type SecurityAssessment struct {
	Total      int            `json:"total"`
	Level      string         `json:"level"`
	Baseline   int            `json:"baseline"`
	MeetsLine  bool           `json:"meets_baseline"`
	Items      []SecurityItem `json:"items"`
	Notices    []string       `json:"notices"`
	Actionable []string       `json:"actionable"` // 未达标项的跳转指引
}

// AssessAccountSecurity 计算个人安全评分。
func AssessAccountSecurity(in SecurityInput) SecurityAssessment {
	items := []SecurityItem{
		itemPasswordSet(in),
		itemStrength(in),
		itemEnvironment(in),
		itemSecondFactor(in),
	}

	summedMax := 0
	summed := 0
	for _, it := range items {
		if !it.Scored {
			continue
		}
		summedMax += it.Max
		summed += it.Score
	}
	total := 0
	if summedMax > 0 {
		total = int(math.Round(float64(summed) / float64(summedMax) * 100))
	}

	baseline := BaselineRegular
	if in.Privileged {
		baseline = BaselinePrivileged
	}

	a := SecurityAssessment{
		Total:     total,
		Level:     securityLevel(total),
		Baseline:  baseline,
		MeetsLine: total >= baseline,
		Items:     items,
		Notices:   systemNotices(in),
	}
	for _, it := range items {
		if it.Scored && it.Status != "ok" {
			a.Actionable = append(a.Actionable, it.ID)
		}
	}
	return a
}

func itemPasswordSet(in SecurityInput) SecurityItem {
	if in.PasswordSet {
		return SecurityItem{ID: SecurityItemPasswordSet, Name: "已自行设置口令", Score: itemMax, Max: itemMax,
			Status: "ok", Detail: "已脱离初始口令", Scored: true}
	}
	return SecurityItem{ID: SecurityItemPasswordSet, Name: "已自行设置口令", Score: 0, Max: itemMax,
		Status: "fail", Detail: "仍在使用初始口令，请立即修改", Scored: true}
}

func itemStrength(in SecurityInput) SecurityItem {
	switch in.PasswordStrength {
	case PasswordStrengthStrong:
		return SecurityItem{ID: SecurityItemStrength, Name: "口令强度", Score: itemMax, Max: itemMax,
			Status: "ok", Detail: "长度与字符类别均达标", Scored: true}
	case PasswordStrengthMedium:
		return SecurityItem{ID: SecurityItemStrength, Name: "口令强度", Score: 15, Max: itemMax,
			Status: "warn", Detail: "中等强度，建议再加长或混用字符类别", Scored: true}
	case PasswordStrengthWeak:
		return SecurityItem{ID: SecurityItemStrength, Name: "口令强度", Score: 0, Max: itemMax,
			Status: "fail", Detail: "弱口令，容易撞库命中", Scored: true}
	default:
		// 口令经 bcrypt 入库后无法回测，存量账号只能标"未评定"，
		// 计 0 分但不谎称"弱"，改密一次即自动评定。
		return SecurityItem{ID: SecurityItemStrength, Name: "口令强度", Score: 0, Max: itemMax,
			Status: "unknown", Detail: "存量口令无法回测，修改一次口令即可评定", Scored: true}
	}
}

func itemEnvironment(in SecurityInput) SecurityItem {
	if !in.IPTrustworthy {
		// 未配置 TrustedProxies 时客户端可自称任意 IP，据此扣分等于让人背部署的锅，
		// 因此整项退出判定，总分按其余三项折算。
		return SecurityItem{ID: SecurityItemEnvironment, Name: "登录环境可信", Score: 0, Max: itemMax,
			Status: "unknown", Detail: "客户端 IP 可伪造（未配置 TRUSTED_PROXIES），本项暂不纳入判定", Scored: false}
	}
	if !in.HasSessionHistory {
		return SecurityItem{ID: SecurityItemEnvironment, Name: "登录环境可信", Score: itemMax, Max: itemMax,
			Status: "ok", Detail: "暂无可比对的历史登录", Scored: true}
	}
	if in.PendingEnvLogins <= 0 {
		return SecurityItem{ID: SecurityItemEnvironment, Name: "登录环境可信", Score: itemMax, Max: itemMax,
			Status: "ok", Detail: "近期登录环境均已确认为本人", Scored: true}
	}
	score := itemMax - 5*in.PendingEnvLogins
	if score < 0 {
		score = 0
	}
	status := "warn"
	if score == 0 {
		status = "fail"
	}
	return SecurityItem{ID: SecurityItemEnvironment, Name: "登录环境可信",
		Score: score, Max: itemMax, Status: status,
		Detail: fmt.Sprintf("%d 次陌生环境登录待确认，确认后即恢复", in.PendingEnvLogins),
		Scored: true}
}

// itemSecondFactor 二次验证强度：
// L0 无=0 / L1 危险操作口令核验=10 / L2 登录环境核验=18 / L3 TOTP=25。
// L1 由服务端对所有账号强制（requireStepUp），因此人人至少有 10 分；
// L3 只有在全部登录通道都强制 TOTP 时才给，否则三要素通道就是绕开口令的那扇门。
func itemSecondFactor(in SecurityInput) SecurityItem {
	if in.TotpEnabled && in.AllLoginPathsMFA {
		return SecurityItem{ID: SecurityItemSecondFactor, Name: "登录二次验证", Score: itemMax, Max: itemMax,
			Status: "ok", Detail: "L3：所有登录通道均强制动态口令", Scored: true}
	}
	if in.TotpEnabled {
		return SecurityItem{ID: SecurityItemSecondFactor, Name: "登录二次验证", Score: 18, Max: itemMax,
			Status: "warn", Detail: "已绑定动态口令，但仍有登录通道未强制（宿管三要素），按 L2 计", Scored: true}
	}
	if in.IPTrustworthy {
		return SecurityItem{ID: SecurityItemSecondFactor, Name: "登录二次验证", Score: 18, Max: itemMax,
			Status: "warn", Detail: "L2：危险操作口令核验 + 陌生环境需本人确认；绑定动态口令可升到 L3", Scored: true}
	}
	return SecurityItem{ID: SecurityItemSecondFactor, Name: "登录二次验证", Score: 10, Max: itemMax,
		Status: "warn", Detail: "L1：仅危险操作口令核验，建议绑定动态口令", Scored: true}
}

// systemNotices 部署层风险单独公示，不折进个人分数：个人改不了的事不该由个人扣分。
func systemNotices(in SecurityInput) []string {
	var notices []string
	if !in.TransportHTTPS {
		notices = append(notices, "当前访问未启用 HTTPS，凭据在链路上可被窃听")
	}
	if !in.IPTrustworthy {
		notices = append(notices, "服务端未配置 TRUSTED_PROXIES，客户端 IP 可伪造，异地登录判定暂不可用")
	}
	return notices
}

func securityLevel(total int) string {
	switch {
	case total >= 85:
		return "优秀"
	case total >= 70:
		return "良好"
	case total >= 50:
		return "中等"
	default:
		return "需改进"
	}
}
