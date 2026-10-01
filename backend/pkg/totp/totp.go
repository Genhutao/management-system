// Package totp 实现 RFC 6238（基于时间的一次性口令）。
//
// 之所以手写：系统要接入 L3 二次验证只需要 HMAC-SHA1 + 30 秒步长 + 6 位数字
// 这一条最小路径，引第三方库会为一个 40 行的算法多背一串依赖，
// 而这段实现直接用 RFC 6238 附录 B 的官方测试向量自证正确性。
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	// StepSeconds 是 RFC 6238 默认的 30 秒时窗，验证器 App 都按这个步长走。
	StepSeconds = 30
	// Digits 是给人手输的位数。RFC 测试向量用 8 位，本系统展示与录入用 6 位。
	Digits = 6
	// SecretBytes 20 字节 = 160 位，与 HMAC-SHA1 的输出宽度对齐，也是各验证器的事实标准。
	SecretBytes = 20
	// ClockWindow 允许前后各一步的时钟漂移，再大就该提示用户校时而不是继续放行。
	ClockWindow = 1
)

var errShortSecret = errors.New("TOTP 密钥长度不足")

// NewSecret 生成随机 Base32 密钥（不含填充），交由调用方展示一次给用户扫码。
// 密钥本身入库前必须经 secretbox 封装，见 controller 侧 TotpSecretEnc。
func NewSecret() (string, error) {
	raw := make([]byte, SecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成 TOTP 密钥失败: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

// DecodeSecret 解析 Base32 密钥，容忍空格、连字符与小写输入（用户手输常见形态）。
func DecodeSecret(secret string) ([]byte, error) {
	cleaned := strings.NewReplacer(" ", "", "-", "", "\n", "", "\r", "").Replace(strings.TrimSpace(secret))
	if cleaned == "" {
		return nil, errShortSecret
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(cleaned))
	if err != nil {
		return nil, fmt.Errorf("TOTP 密钥不是合法 Base32: %w", err)
	}
	if len(raw) < 10 {
		return nil, errShortSecret
	}
	return raw, nil
}

// Step 把时刻折算成 RFC 6238 计数器值。
func Step(t time.Time) int64 {
	return t.Unix() / StepSeconds
}

// Code 按密钥与时刻生成指定位数的一次性口令。
func Code(secret string, t time.Time, digits int) (string, error) {
	raw, err := DecodeSecret(secret)
	if err != nil {
		return "", err
	}
	return at(Step(t), raw, digits), nil
}

// ValidateResult 携带通过时的步长，调用方要把它写回 TotpLastStep 才能防重放。
type ValidateResult struct {
	OK     bool
	Step   int64
	Reason string
}

// Validate 校验口令，并把已用过的步长挡在门外。
// lastStep 是上次成功验证的步长（从未用过传 -1）；
// 落在时窗内但小于等于 lastStep 的口令一律拒绝——同一个码只能用一次。
func Validate(secret, input string, t time.Time, digits int, lastStep int64) (ValidateResult, error) {
	raw, err := DecodeSecret(secret)
	if err != nil {
		return ValidateResult{}, err
	}
	input = strings.TrimSpace(input)

	current := Step(t)
	for offset := -ClockWindow; offset <= ClockWindow; offset++ {
		step := current + int64(offset)
		if step <= lastStep {
			continue
		}
		if hmac.Equal([]byte(at(step, raw, digits)), []byte(input)) {
			return ValidateResult{OK: true, Step: step}, nil
		}
	}
	return ValidateResult{OK: false, Reason: "动态口令不正确或已使用过"}, nil
}

func at(step int64, raw []byte, digits int) string {
	return format(digits, hotp(raw, step))
}

func hotp(raw []byte, counter int64) uint32 {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, raw)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	bin := uint32(sum[offset]&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return bin
}

func format(digits int, bin uint32) string {
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, bin%mod)
}

// OTPAuthURL 生成验证器 App 可直接扫描的配置链接。
func OTPAuthURL(issuer, accountName, secret string) string {
	label := url.PathEscape(accountName)
	q := url.Values{}
	q.Set("secret", strings.ToUpper(strings.TrimSpace(secret)))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(StepSeconds))
	return fmt.Sprintf("otpauth://totp/%s:%s?%s", url.PathEscape(issuer), label, q.Encode())
}
