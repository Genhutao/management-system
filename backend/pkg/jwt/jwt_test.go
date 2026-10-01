package jwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

const testSecret = "unit-test-secret-do-not-use-in-prod"

// 伪造一个 alg 字段为指定算法、签名仍是 HMAC 的令牌，
// 用于验证算法白名单（攻击者改 alg 做 none / RS256 公钥混淆）。
func forgeWithAlg(t *testing.T, alg string) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	if err != nil {
		t.Fatalf("构造 header 失败: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"user_id": 1, "username": "attacker", "role": "tech_admin",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("构造 payload 失败: %v", err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	signingInput := b64(header) + "." + b64(payload)
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + b64(mac.Sum(nil))
}

func TestParseTokenRejectsAlgNone(t *testing.T) {
	t.Setenv("JWT_SECRET", testSecret)
	if _, err := ParseToken(forgeWithAlg(t, "none")); err == nil {
		t.Fatal("alg=none 的伪造令牌被接受，算法白名单失效")
	}
}

func TestParseTokenRejectsAlgConfusion(t *testing.T) {
	t.Setenv("JWT_SECRET", testSecret)
	if _, err := ParseToken(forgeWithAlg(t, "RS256")); err == nil {
		t.Fatal("alg=RS256 的混淆令牌被接受，算法白名单失效")
	}
}

func TestParseTokenRejectsTamperedSignature(t *testing.T) {
	t.Setenv("JWT_SECRET", testSecret)
	tokenStr, err := GenerateToken(3, "bob", "鲍勃", "member", "1号楼", "2层")
	if err != nil {
		t.Fatalf("GenerateToken 失败: %v", err)
	}
	tampered := tokenStr[:len(tokenStr)-2] + "xx"
	if _, err := ParseToken(tampered); err == nil {
		t.Fatal("被篡改签名的令牌被接受")
	}
}

func TestGenerateTokenCarriesJtiAndVersion(t *testing.T) {
	t.Setenv("JWT_SECRET", testSecret)
	tokenStr, err := GenerateToken(3, "bob", "鲍勃", "member", "1号楼", "2层")
	if err != nil {
		t.Fatalf("GenerateToken 失败: %v", err)
	}
	claims, err := ParseToken(tokenStr)
	if err != nil {
		t.Fatalf("ParseToken 失败: %v", err)
	}
	if claims.ID == "" {
		t.Fatal("jti 为空，后续会话吊销无法按 jti 定位令牌")
	}
	if claims.TokenVersion != 1 {
		t.Fatalf("tv = %d, want 1", claims.TokenVersion)
	}
	if claims.UserID != 3 || claims.Role != "member" {
		t.Fatalf("身份 claims 不符: userID=%d role=%q", claims.UserID, claims.Role)
	}
}

func TestGenerateTokenWithVersionRoundTrip(t *testing.T) {
	t.Setenv("JWT_SECRET", testSecret)
	tokenStr, err := GenerateTokenWithVersion(5, "carol", "卡罗", "minister", "", "", 7)
	if err != nil {
		t.Fatalf("GenerateTokenWithVersion 失败: %v", err)
	}
	claims, err := ParseToken(tokenStr)
	if err != nil {
		t.Fatalf("ParseToken 失败: %v", err)
	}
	if claims.TokenVersion != 7 {
		t.Fatalf("tv = %d, want 7", claims.TokenVersion)
	}
}
