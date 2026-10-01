package service

import (
	"strings"
	"unicode"
)

// 口令强度等级，与 User.PasswordStrength 落库取值一致。
const (
	PasswordStrengthWeak   = "weak"
	PasswordStrengthMedium = "medium"
	PasswordStrengthStrong = "strong"
)

const (
	minPasswordLen    = 6 // 拒收线：低于此长度不许设置
	mediumPasswordLen = 8
	strongPasswordLen = 10
)

// 常见口令表只收录校园场景的高频命中项，不做穷举：
// 目标是拦住"123456 换个后缀"这类改法，而不是宣称覆盖全部弱口令。
var commonPasswords = map[string]struct{}{
	"123456": {}, "1234567": {}, "12345678": {}, "123456789": {}, "1234567890": {},
	"000000": {}, "111111": {}, "666666": {}, "888888": {}, "999999": {},
	"password": {}, "password1": {}, "passwd": {}, "admin": {}, "admin123": {},
	"qwerty": {}, "qazwsx": {}, "abc123": {}, "123abc": {}, "a123456": {},
	"1q2w3e4r": {}, "1qaz2wsx": {}, "iloveyou": {}, "5201314": {}, "112233": {},
	"test123": {}, "xgh123456": {}, "xgh123": {},
}

// CheckPasswordPolicy 返回拒绝设置该口令的原因（命中即返回首条）；返回 nil 表示允许设置。
// 拒收的门槛刻意低于"strong"：能过线的口令仍可能评为 weak，
// 评分引擎要如实反映强度，而不是把"允许"当成"安全"。
func CheckPasswordPolicy(password, username, phone string) []string {
	reject := func(reason string) []string { return []string{reason} }
	runes := []rune(password)

	if len(runes) == 0 {
		return reject("口令不能为空")
	}
	if len(runes) < minPasswordLen {
		return reject("口令至少需要 6 个字符")
	}
	if _, hit := commonPasswords[strings.ToLower(password)]; hit {
		return reject("口令过于常见，请更换")
	}
	if uniqueRunes(runes) == 1 {
		return reject("口令不能由同一字符重复构成")
	}
	if isSequential(runes) {
		return reject("口令为连续字符序列，过于简单")
	}
	if name := strings.ToLower(strings.TrimSpace(username)); len(name) >= 4 && strings.Contains(strings.ToLower(password), name) {
		return reject("口令不能包含账号名")
	}
	if digits := digitOnly(phone); len(digits) >= 6 && strings.Contains(password, digits) {
		return reject("口令不能包含手机号")
	}
	return nil
}

// EvaluatePassword 评定口令强度。口令一经 bcrypt 入库便不可回测，
// 因此本函数只在改密时调用，存量账号的强度保持"未评定"（空串）。
func EvaluatePassword(password string) string {
	runes := []rune(password)
	classes := charClasses(runes)
	if len(runes) >= strongPasswordLen && classes >= 3 {
		return PasswordStrengthStrong
	}
	if len(runes) >= mediumPasswordLen && classes >= 2 {
		return PasswordStrengthMedium
	}
	return PasswordStrengthWeak
}

func uniqueRunes(runes []rune) int {
	seen := make(map[rune]struct{}, len(runes))
	for _, r := range runes {
		seen[r] = struct{}{}
	}
	return len(seen)
}

// isSequential 判定整串是否为等步长 +1 / -1 的连续序列（123456、abcdef、654321）。
func isSequential(runes []rune) bool {
	if len(runes) < minPasswordLen {
		return false
	}
	up, down := true, true
	for i := 1; i < len(runes); i++ {
		delta := runes[i] - runes[i-1]
		if delta != 1 {
			up = false
		}
		if delta != -1 {
			down = false
		}
	}
	return up || down
}

// charClasses 统计字符类别数；非字母数字（含中文）合并计一类。
func charClasses(runes []rune) int {
	var lower, upper, digit, other bool
	for _, r := range runes {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, set := range []bool{lower, upper, digit, other} {
		if set {
			n++
		}
	}
	return n
}

func digitOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
