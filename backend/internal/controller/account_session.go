package controller

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/jwt"
)

// 会话环境状态
const (
	EnvStatusKnown          = "known"
	EnvStatusPendingConfirm = "pending_confirm"
)

// clientIPTrustworthy 判定本次取到的 ClientIP 能否作为"异地登录"的依据。
// 配置了 TRUSTED_PROXIES 时按反代白名单取值可信；未配置时 Gin 默认全信
// X-Forwarded-For，客户端自称的假 IP 不能用来判定风险，否则评分形同自证。
func clientIPTrustworthy(c *gin.Context) bool {
	if strings.TrimSpace(os.Getenv("TRUSTED_PROXIES")) != "" {
		return true
	}
	return c.GetHeader("X-Forwarded-For") == "" && c.GetHeader("X-Real-IP") == ""
}

// seenLoginIPs 取该账号近 30 天登记过的登录 IP，作为"是否陌生环境"的比对基线。
func seenLoginIPs(userID uint, since time.Time) []string {
	var ips []string
	if err := repository.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND login_ip <> '' AND login_at >= ?", userID, since).
		Distinct().
		Pluck("login_ip", &ips).Error; err != nil {
		log.Printf("[Warn] 账号 %d 历史登录 IP 查询失败: %v", userID, err)
	}
	return ips
}

// registerLoginSession 把刚签发的令牌登记进 user_sessions，供会话列表、吊销与
// 新环境判定使用，并返回本次会话的环境状态（是否待本人确认）。
// 登记失败必须让登录失败：会话没登记，后续每个请求都会被中间件判成
// session_revoked，假装成功只会把一个坏会话留给用户。
func registerLoginSession(c *gin.Context, user *model.User, token string) (string, error) {
	claims, err := jwt.ParseToken(token)
	if err != nil {
		return EnvStatusKnown, fmt.Errorf("解析刚签发的令牌失败: %w", err)
	}

	now := time.Now()
	ip := c.ClientIP()
	agent := c.Request.UserAgent()
	if len(agent) > 255 {
		agent = agent[:255]
	}

	envStatus := EnvStatusKnown
	if clientIPTrustworthy(c) {
		history := seenLoginIPs(user.ID, now.Add(-30*24*time.Hour))
		// 没有历史可比时不算异常：账号首次通过系统登录无从判断"异地"
		if len(history) > 0 && !containsFold(history, ip) {
			envStatus = EnvStatusPendingConfirm
		}
	}

	sess := model.UserSession{
		UserID:     user.ID,
		Jti:        claims.ID,
		LoginIP:    ip,
		UserAgent:  agent,
		LoginAt:    now,
		LastSeenAt: now,
		EnvStatus:  envStatus,
	}
	if err := repository.DB.Create(&sess).Error; err != nil {
		return envStatus, fmt.Errorf("会话登记失败: %w", err)
	}

	if err := repository.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Updates(map[string]any{"last_login_ip": ip, "last_login_at": now}).Error; err != nil {
		log.Printf("[Warn] 账号 %d 最近登录信息更新失败: %v", user.ID, err)
	}
	user.LastLoginIP = ip
	user.LastLoginAt = &now
	return envStatus, nil
}

// revokeCurrentSession 登出时吊销当前 jti，使被偷走的 Cookie 在服务端也失效，
// 而不是只清掉浏览器里的值。
func revokeCurrentSession(c *gin.Context) {
	jti := c.GetString("jti")
	if jti == "" {
		return
	}
	if err := repository.DB.Model(&model.UserSession{}).
		Where("jti = ? AND revoked_at IS NULL", jti).
		Update("revoked_at", time.Now()).Error; err != nil {
		log.Printf("[Warn] 会话 %s 吊销失败: %v", jti, err)
	}
}

// revokeAllSessions 吊销该账号全部未失效会话，用于改密与"退出所有设备"。
// 调用方需同时 bump TokenVersion 并重新签发当前设备的令牌。
func revokeAllSessions(userID uint) error {
	return repository.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", time.Now()).Error
}

func containsFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

// newRandomInitialPassword 生成一次性随机初始口令（24 位十六进制）。
// 用在"系统建号但本人还没设过口令"的场合：既避免人人可猜的默认口令，
// 又不需要把口令存成日后可解回的形态——调用方要么当场交付本人，要么直接丢弃。
func newRandomInitialPassword() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败意味着进程已无法取得熵，继续签发口令没有意义
		log.Fatalf("[Security] 生成随机初始口令失败: %v", err)
	}
	return hex.EncodeToString(b)
}
