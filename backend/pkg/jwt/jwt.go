package jwt

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwtSecret 按以下优先级解析：环境变量 JWT_SECRET > jwt_secret.key 文件 > 随机生成并落盘。
// 密钥一旦入源码，任何拿到仓库的人都能伪造任意角色的 token。
// 惰性加载：测试可先 t.Setenv 再触发首次调用，避免测试过程落盘密钥文件。
var (
	secretOnce sync.Once
	jwtSecret  []byte
)

func secret() []byte {
	secretOnce.Do(loadSecret)
	return jwtSecret
}

func loadSecret() {
	if s := strings.TrimSpace(os.Getenv("JWT_SECRET")); s != "" {
		jwtSecret = []byte(s)
		return
	}
	if data, err := os.ReadFile("jwt_secret.key"); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			jwtSecret = []byte(s)
			return
		}
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		log.Fatalf("[Security] 生成随机签名密钥失败: %v", err)
	}
	generated := fmt.Sprintf("%x", key)
	if err := os.WriteFile("jwt_secret.key", []byte(generated), 0600); err != nil {
		log.Printf("[Security][Warn] 无法写入 jwt_secret.key，本次会话密钥重启后失效: %v", err)
	} else {
		log.Println("[Security] 未设置 JWT_SECRET，已生成随机签名密钥并写入 jwt_secret.key（0600）；生产环境建议显式设置 JWT_SECRET 环境变量")
	}
	jwtSecret = []byte(generated)
}

// newJti 生成会话唯一标识，供后续会话吊销（UserSession）按 jti 定位令牌
func newJti() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("[Security] 生成 jti 失败: %v", err)
	}
	return hex.EncodeToString(b)
}

// CustomClaims JWT 载荷。TokenVersion(tv) 用于改密/吊销后令牌立即失效：
// 校验方比对 claims 中的 tv 与用户当前 TokenVersion，不一致即拒绝。
type CustomClaims struct {
	UserID       uint   `json:"user_id"`
	Username     string `json:"username"`
	RealName     string `json:"real_name"`
	Role         string `json:"role"`
	Building     string `json:"building"`
	Floor        string `json:"floor"`
	TokenVersion uint   `json:"tv"`
	jwt.RegisteredClaims
}

// GenerateToken 生成带身份角色的令牌 (默认有效期 7 天，方便移动端与宿管常驻)。
// tv 固定为 1（当前版本基线，与存量用户的 TokenVersion 默认值一致）；
// 改密/退出所有设备后重签发时请改用 GenerateTokenWithVersion。
func GenerateToken(userID uint, username, realName, role, building, floor string) (string, error) {
	return GenerateTokenWithVersion(userID, username, realName, role, building, floor, 1)
}

// GenerateTokenWithVersion 在 GenerateToken 基础上携带指定令牌版本
func GenerateTokenWithVersion(userID uint, username, realName, role, building, floor string, tokenVersion uint) (string, error) {
	claims := CustomClaims{
		UserID:       userID,
		Username:     username,
		RealName:     realName,
		Role:         role,
		Building:     building,
		Floor:        floor,
		TokenVersion: tokenVersion,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        newJti(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "xgh_management_system",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret())
}

// ParseToken 解析令牌。算法白名单固定为 HS256，
// 防止攻击者改头部的 alg 字段做算法混淆（如 none / RS256 公钥混淆）。
func ParseToken(tokenStr string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return secret(), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*CustomClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("invalid token")
}
