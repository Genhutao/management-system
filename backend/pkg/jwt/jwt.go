package jwt

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwtSecret 按以下优先级解析：环境变量 JWT_SECRET > jwt_secret.key 文件 > 随机生成并落盘。
// 密钥一旦入源码，任何拿到仓库的人都能伪造任意角色的 token。
var jwtSecret = loadSecret()

func loadSecret() []byte {
	if s := strings.TrimSpace(os.Getenv("JWT_SECRET")); s != "" {
		return []byte(s)
	}
	if data, err := os.ReadFile("jwt_secret.key"); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return []byte(s)
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
	return []byte(generated)
}

// CustomClaims JWT 载荷
type CustomClaims struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	RealName string `json:"real_name"`
	Role     string `json:"role"`
	Building string `json:"building"`
	Floor    string `json:"floor"`
	jwt.RegisteredClaims
}

// GenerateToken 生成带身份角色的令牌 (默认有效期 7 天，方便移动端与宿管常驻)
func GenerateToken(userID uint, username, realName, role, building, floor string) (string, error) {
	claims := CustomClaims{
		UserID:   userID,
		Username: username,
		RealName: realName,
		Role:     role,
		Building: building,
		Floor:    floor,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "xgh_management_system",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// ParseToken 解析令牌
func ParseToken(tokenStr string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*CustomClaims); ok && token.Valid {
		return claims, nil
	}
	return nil, errors.New("invalid token")
}
