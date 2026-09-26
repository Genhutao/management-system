package secretbox

import (
	"os"
	"strings"
	"testing"
)

// TestMain 先固定密钥再跑用例：密钥是懒加载且进程内只解析一次，
// 若不预设，用例会在工作目录里生成一个 crypto_secret.key 文件。
func TestMain(m *testing.M) {
	os.Setenv("CRYPTO_SECRET", "unit-test-secret")
	os.Exit(m.Run())
}

func TestSealOpenRoundTrip(t *testing.T) {
	plaintext := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	sealed, err := Seal(plaintext)
	if err != nil {
		t.Fatalf("Seal 失败: %v", err)
	}
	if sealed == plaintext {
		t.Fatal("Seal 返回了原值，等于没有加密")
	}
	if strings.Contains(sealed, plaintext) {
		t.Fatal("密文里仍能读出明文")
	}
	if !IsSealed(sealed) {
		t.Fatalf("缺少封装前缀: %s", sealed)
	}
	opened, err := Open(sealed)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if opened != plaintext {
		t.Fatalf("往返不一致: got %q want %q", opened, plaintext)
	}
}

func TestSealIsRandomized(t *testing.T) {
	plaintext := "sk-same-value-same-value-same-value"
	a, _ := Seal(plaintext)
	b, _ := Seal(plaintext)
	if a == b {
		t.Fatal("同一明文两次封装结果相同，说明初始向量被复用")
	}
	for _, sealed := range []string{a, b} {
		opened, err := Open(sealed)
		if err != nil || opened != plaintext {
			t.Fatalf("随机初始向量化后解密失败: %v", err)
		}
	}
}

func TestSealSkipsEmptyAndAlreadySealed(t *testing.T) {
	if got, err := Seal(""); err != nil || got != "" {
		t.Fatalf("空值应原样返回，got %q err %v", got, err)
	}
	sealed, _ := Seal("sk-abcdefg-12345678")
	again, err := Seal(sealed)
	if err != nil {
		t.Fatalf("重复封装报错: %v", err)
	}
	if again != sealed {
		t.Fatal("已封装的值被二次封装，密文会越写越厚")
	}
}

func TestOpenPassesThroughLegacyPlaintext(t *testing.T) {
	// 接入加密钩子之前的库里有明文行，读取必须原样通过，否则升级即导致配置全部读不出。
	legacy := "sk-legacy-plaintext-key"
	got, err := Open(legacy)
	if err != nil {
		t.Fatalf("明文直通过程报错: %v", err)
	}
	if got != legacy {
		t.Fatalf("明文被改写: %q", got)
	}
}

func TestOpenDetectsTampering(t *testing.T) {
	sealed, _ := Seal("sk-secret-value-abcdef")
	body := strings.TrimPrefix(sealed, prefix)
	corrupted := prefix + body[:len(body)-2] + ("AA"[:1]+body[len(body)-1:])
	if corrupted == sealed {
		t.Skip("未能构造出篡改样本")
	}
	if _, err := Open(corrupted); err == nil {
		t.Fatal("篡改密文仍解密成功，GCM 认证标签未生效")
	}
	if _, err := Open(prefix + "@@@not-base64@@@"); err == nil {
		t.Fatal("非法 base64 未报错")
	}
}

func TestMaskNeverRevealsWholeKey(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"空值", "", ""},
		{"短密钥", "sk-12345", "****"},
		{"长密钥", "sk-abcdefghijklmnopqrstuvwxyz", "sk-a****wxyz"},
	}
	for _, c := range cases {
		if got := Mask(c.input); got != c.want {
			t.Errorf("%s: Mask 得到 %q，期望 %q", c.name, got, c.want)
		}
	}
	sealed, _ := Seal("sk-abcdefghijklmnopqrstuvwxyz")
	if got := Mask(sealed); got != "sk-a****wxyz" {
		t.Errorf("对密文取掩码应先解密再脱敏，得到 %q", got)
	}
	if strings.Contains(Mask(sealed), "bcdefghijklmnopqrstuv") {
		t.Error("掩码泄露了密钥中段")
	}
}
