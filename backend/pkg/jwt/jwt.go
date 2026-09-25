package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret = []byte("XGH_MANAGEMENT_SUPER_SECRET_KEY_2026")

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
