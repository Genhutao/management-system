package middleware

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/jwt"
)

// 401 错误码契约：前端据此区分"没登录 / 过期 / 被吊销 / 被停用"四种跳转，
// error 字段保留中文提示供 toast 直接展示。
const (
	CodeSessionMissing  = "session_missing"
	CodeSessionExpired  = "session_expired"
	CodeSessionRevoked  = "session_revoked"
	CodeAccountDisabled = "account_disabled"
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

// secureCookie HTTPS 尚未全线普及，默认跟随请求是否 TLS；
// 部署在反向代理之后（浏览器到反代走 HTTPS）时用 COOKIE_SECURE=1 强制开启，
// 否则 Secure 恒为 false，会话 Cookie 会随明文 HTTP 请求外泄。
func secureCookie(c *gin.Context) bool {
	return c.Request.TLS != nil || os.Getenv("COOKIE_SECURE") == "1"
}

// SetSessionCookie 把会话令牌写入 HttpOnly Cookie。
func SetSessionCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   secureCookie(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie 登出时清除会话 Cookie。属性需与签发时一致，否则浏览器不会覆盖旧值。
func ClearSessionCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureCookie(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func abortUnauthorized(c *gin.Context, code, message string) {
	c.JSON(http.StatusUnauthorized, gin.H{"error": message, "code": code})
	c.Abort()
}

// touchSession 刷新会话活跃时间。该字段只影响安全中心的展示，不参与鉴权，
// 因此按分钟节流写库，避免每个 API 请求都落一次盘。
func touchSession(sess *model.UserSession) {
	if time.Since(sess.LastSeenAt) < time.Minute {
		return
	}
	if err := repository.DB.Model(&model.UserSession{}).
		Where("id = ?", sess.ID).
		Update("last_seen_at", time.Now()).Error; err != nil {
		log.Printf("[Warn] 会话 #%d 活跃时间刷新失败: %v", sess.ID, err)
	}
}

// AuthMiddleware 校验会话：优先 Authorization: Bearer（移动端 APK 用），
// 否则回落到 HttpOnly 会话 Cookie（Web 端用）。
// 令牌有效只是第一层：还需账号未停用、令牌版本与用户当前版本一致、jti 已登记且未被吊销。
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenStr := bearerToken(c.GetHeader("Authorization"))
		if tokenStr == "" {
			if ck, err := c.Cookie(SessionCookieName); err == nil {
				tokenStr = ck
			}
		}
		if tokenStr == "" {
			abortUnauthorized(c, CodeSessionMissing, "请先登录系统")
			return
		}

		claims, err := jwt.ParseToken(tokenStr)
		if err != nil {
			ClearSessionCookie(c)
			abortUnauthorized(c, CodeSessionExpired, "登录已失效或无效令牌，请重新登录")
			return
		}
		// 会话校验上线前签发的旧令牌没有 jti，无从比对，一律要求重新登录
		if claims.ID == "" {
			ClearSessionCookie(c)
			abortUnauthorized(c, CodeSessionRevoked, "登录方式已升级，请重新登录")
			return
		}

		var account struct {
			ID           uint
			TokenVersion uint
			Status       string
		}
		if err := repository.DB.Model(&model.User{}).
			Select("id", "token_version", "status").
			Where("id = ?", claims.UserID).
			First(&account).Error; err != nil {
			ClearSessionCookie(c)
			abortUnauthorized(c, CodeSessionRevoked, "账号已不存在，请重新登录")
			return
		}
		if account.Status == "disabled" {
			ClearSessionCookie(c)
			abortUnauthorized(c, CodeAccountDisabled, "该账号已被停用，请联系学管会技术维护组")
			return
		}
		// 改密或"退出所有设备"会 bump TokenVersion，此后旧版本的令牌一律作废
		if claims.TokenVersion != account.TokenVersion {
			ClearSessionCookie(c)
			abortUnauthorized(c, CodeSessionRevoked, "口令或登录状态已变更，当前设备需重新登录")
			return
		}

		var sess model.UserSession
		if err := repository.DB.Where("jti = ?", claims.ID).First(&sess).Error; err != nil {
			ClearSessionCookie(c)
			abortUnauthorized(c, CodeSessionRevoked, "该登录会话未登记或已失效，请重新登录")
			return
		}
		if sess.RevokedAt != nil {
			ClearSessionCookie(c)
			abortUnauthorized(c, CodeSessionRevoked, "该设备已退出登录，请重新登录")
			return
		}
		touchSession(&sess)

		// 存入 Gin 上下文
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("real_name", claims.RealName)
		c.Set("role", claims.Role)
		c.Set("building", claims.Building)
		c.Set("floor", claims.Floor)
		c.Set("jti", claims.ID)
		c.Set("session_id", sess.ID)

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
