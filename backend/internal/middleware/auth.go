package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"xgh-system/pkg/jwt"
)

// CORSMiddleware 解决跨域与移动端 APK 请求
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// AuthMiddleware 校验 JWT 令牌
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录系统"})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		var tokenStr string
		if len(parts) == 2 && parts[0] == "Bearer" {
			tokenStr = parts[1]
		} else {
			tokenStr = authHeader
		}

		claims, err := jwt.ParseToken(tokenStr)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "登录已失效或无效令牌，请重新登录"})
			c.Abort()
			return
		}

		// 存入 Gin 上下文
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("real_name", claims.RealName)
		c.Set("role", claims.Role)
		c.Set("building", claims.Building)
		c.Set("floor", claims.Floor)

		c.Next()
	}
}

// RequireRoles 角色权限隔离门禁中间件
func RequireRoles(allowedRoles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userRole, exists := c.Get("role")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未获取到用户角色信息"})
			c.Abort()
			return
		}

		currentRole := userRole.(string)
		for _, role := range allowedRoles {
			if currentRole == role {
				c.Next()
				return
			}
		}

		c.JSON(http.StatusForbidden, gin.H{
			"error": "权限不足：当前角色无权访问此模块",
			"current_role": currentRole,
		})
		c.Abort()
	}
}
