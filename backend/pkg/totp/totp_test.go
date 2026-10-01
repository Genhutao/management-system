package totp

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

// RFC 6238 附录 B 的官方测试向量：HMAC-SHA1、20 字节 ASCII 种子、8 位口令。
// 这六个值只要有一个对不上，实现就是错的，没有商量余地。
func TestRFC6238AppendixBVectors(t *testing.T) {
	seed := []byte("12345678901234567890")
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	// 20 字节种子 → 32 个 Base32 字符，与 RFC 6238 附录 B 给出的同一串一致
	if want := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"; secret != want {
		t.Fatalf("Base32 编码与 RFC 6238 向量不一致: got %s want %s", secret, want)
	}

	cases := []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, c := range cases {
		got, err := Code(secret, time.Unix(c.unix, 0), 8)
		if err != nil {
			t.Fatalf("T=%d 生成口令失败: %v", c.unix, err)
		}
		if got != c.want {
			t.Errorf("T=%d 期望 %s，实际 %s", c.unix, c.want, got)
		}
	}
}

func TestNewSecretDecodes(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret 失败: %v", err)
	}
	raw, err := DecodeSecret(secret)
	if err != nil {
		t.Fatalf("生成的密钥无法解回: %v", err)
	}
	if len(raw) != SecretBytes {
		t.Fatalf("密钥长度 = %d, want %d", len(raw), SecretBytes)
	}
	other, _ := NewSecret()
	if other == secret {
		t.Fatal("两次生成的密钥相同，随机性失效")
	}
}

func TestValidateAcceptsWindowAndRejectsReplay(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret 失败: %v", err)
	}
	now := time.Unix(1700000000, 0)

	for _, offset := range []int64{-ClockWindow, 0, ClockWindow} {
		at := now.Add(time.Duration(offset) * time.Second * StepSeconds)
		code, err := Code(secret, at, Digits)
		if err != nil {
			t.Fatalf("生成口令失败: %v", err)
		}
		res, err := Validate(secret, code, now, Digits, -1)
		if err != nil || !res.OK {
			t.Fatalf("时窗内偏移 %d 步的口令应通过，实际 %+v err=%v", offset, res, err)
		}
	}

	// 防重放：同一口令用过一次后，lastStep 必须把它挡在门外
	current, _ := Code(secret, now, Digits)
	res, err := Validate(secret, current, now, Digits, -1)
	if err != nil || !res.OK {
		t.Fatalf("首次校验应通过: %+v err=%v", res, err)
	}
	if again, err := Validate(secret, current, now, Digits, res.Step); err != nil || again.OK {
		t.Fatalf("同一口令重复使用应被拒绝，实际 %+v err=%v", again, err)
	}

	// 超出时窗一步（T=now+2*step）应失败
	next, _ := Code(secret, now.Add(2*time.Second*StepSeconds), Digits)
	if res, err := Validate(secret, next, now, Digits, -1); err != nil || res.OK {
		t.Fatal("超出时窗的口令应被拒绝")
	}

	// 错口令：先取当前真值，再选一个必然不同的串，避免 10⁻⁶ 概率的偶发失败
	trueCode, _ := Code(secret, now, Digits)
	wrong := "000000"
	if trueCode == wrong {
		wrong = "000001"
	}
	if res, err := Validate(secret, wrong, now, Digits, -1); err != nil {
		t.Fatalf("校验不应因错口令而报错: %v", err)
	} else if res.OK {
		t.Fatalf("错口令 %s 不应通过（真值为 %s）", wrong, trueCode)
	}
}

func TestDecodeSecretToleratesUserInputForms(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret 失败: %v", err)
	}
	messy := strings.ToLower(strings.Join([]string{secret[:8], secret[8:16], secret[16:]}, "-"))
	raw, err := DecodeSecret(messy)
	if err != nil {
		t.Fatalf("小写+连字符的密钥应能解析: %v", err)
	}
	if len(raw) != SecretBytes {
		t.Fatalf("解析结果长度 = %d", len(raw))
	}

	for _, bad := range []string{"", "   ", "!!!!", "ABC"} {
		if _, err := DecodeSecret(bad); err == nil {
			t.Errorf("非法密钥 %q 应当报错", bad)
		}
	}
}

func TestOTPAuthURL(t *testing.T) {
	got := OTPAuthURL("学管会综合管理系统", "member_li", "GEZDGNBVGY3TQOJQ")
	for _, want := range []string{"otpauth://totp/", "secret=GEZDGNBVGY3TQOJQ", "issuer=", "digits=6", "period=30", "algorithm=SHA1"} {
		if !strings.Contains(got, want) {
			t.Errorf("otpauth 链接缺少 %s: %s", want, got)
		}
	}
}
