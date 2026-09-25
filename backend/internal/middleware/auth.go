package middleware

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/pkg/jwt"
)

// SessionCookieName 会话 Cookie 名。登录后由服务端签发 HttpOnly Cookie，
// 前端不再持有可被 JS 读取的长期 token，XSS 窃取凭据的通道随之消失。
const SessionCookieName = "xgh_session"

// sessionTTL 与 JWT 有效期保持一致（见 pkg/jwt）。
const sessionTTL = 7 * 24 * time.Hour

// allowedOrigins 允许跨域的来源白名单，取自 ALLOWED_ORIGINS（逗号分隔）。
// 缺省为空 = 仅同源可用，不再发放通配符跨域许可。
var allowedOrigins = func() map[string]bool {
	set := make(map[string]bool)
	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		o = strings.TrimSpace(o)
		if o != "" && o != "*" {
			set[o] = true
		}
	}
	return set
}()

// CORSMiddleware 默认仅同源；只有显式列入 ALLOWED_ORIGINS 的来源才获得跨域许可，
// 且不再使用 "Access-Control-Allow-Origin: *"（该值与携带凭据的请求互斥）。
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && allowedOrigins[origin] {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Set("Vary", "Origin")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization, accept, Cache-Control, X-Requested-With")
			c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE")
		}

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// SetSessionCookie 把会话令牌写入 HttpOnly Cookie。
// Secure 依据当前请求是否走 TLS 自动决定，无需额外配置。
func SetSessionCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie 登出时清除会话 Cookie。
func ClearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// AuthMiddleware 校验会话：优先 Authorization: Bearer（移动端 APK 用），
// 否则回落到 HttpOnly 会话 Cookie（Web 端用）。
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := bearerToken(c.GetHeader("Authorization"))
		if tokenStr == "" {
			if ck, err := c.Cookie(SessionCookieName); err == nil {
				tokenStr = ck
			}
		}
		if tokenStr == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "请先登录系统"})
			c.Abort()
			return
		}

		claims, err := jwt.ParseToken(tokenStr)
		if err != nil {
			ClearSessionCookie(c)
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

func bearerToken(header string) string {
	parts := strings.SplitN(strings.TrimSpace(header), " ", 2)
	if len(parts) == 2 && parts[0] == "Bearer" {
		return strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(header)
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
			"error":        "权限不足：当前角色无权访问此模块",
			"current_role": currentRole,
		})
		c.Abort()
	}
}
