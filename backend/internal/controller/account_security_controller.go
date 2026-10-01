package controller

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
)

type AccountSecurityController struct{}

// envBaselineDays 与登录时的"陌生 IP 判定"使用同一窗口，
// 保证扣分依据和确认列表说的是同一批记录。
const envBaselineDays = 30

// sessionView 对外暴露的会话行。jti 是服务端定位凭据的内部标识，不外发。
type sessionView struct {
	ID         uint       `json:"id"`
	LoginIP    string     `json:"login_ip"`
	UserAgent  string     `json:"user_agent"`
	LoginAt    time.Time  `json:"login_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	EnvStatus  string     `json:"env_status"`
	Current    bool       `json:"current"`
}

// isPrivilegedAccount 治理与视觉标尺用的"高权"口径：技术维护组、部长、以及
// 实际持有打表权的副部长。注意这只影响 baseline 画在哪条线上，不参与任何授权判定。
func isPrivilegedAccount(u model.User) bool {
	return u.Role == model.RoleTechAdmin || u.Role == model.RoleMinister || model.HasDeductionAuthority(u)
}

func toSessionView(s model.UserSession, currentJti string) sessionView {
	return sessionView{
		ID:         s.ID,
		LoginIP:    s.LoginIP,
		UserAgent:  s.UserAgent,
		LoginAt:    s.LoginAt,
		LastSeenAt: s.LastSeenAt,
		RevokedAt:  s.RevokedAt,
		EnvStatus:  s.EnvStatus,
		Current:    currentJti != "" && s.Jti == currentJti,
	}
}

// Summary 安全中心首页：评分 + 四项明细 + 账号事实 + 在线会话 + 近期登录。
// 全部数据只限当前登录账号本人，读的是会话表与 users 表里属于自己那几行。
func (a *AccountSecurityController) Summary(c *gin.Context) {
	user, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}

	since := time.Now().AddDate(0, 0, -envBaselineDays)
	seenIPs := seenLoginIPs(user.ID, since)

	var active []model.UserSession
	if err := repository.DB.Where("user_id = ? AND revoked_at IS NULL", user.ID).
		Order("last_seen_at desc").Limit(50).Find(&active).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "会话列表读取失败"})
		return
	}
	var history []model.UserSession
	if err := repository.DB.Where("user_id = ?", user.ID).
		Order("login_at desc").Limit(10).Find(&history).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "登录历史读取失败"})
		return
	}

	var pendingEnv int64
	if err := repository.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND env_status = ? AND login_at >= ?", user.ID, EnvStatusPendingConfirm, since).
		Count(&pendingEnv).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "登录环境统计失败"})
		return
	}

	trustworthy := clientIPTrustworthy(c)
	assessment := service.AssessAccountSecurity(service.SecurityInput{
		PasswordSet:       user.PasswordChangedAt != nil,
		PasswordStrength:  user.PasswordStrength,
		HasSessionHistory: len(seenIPs) > 0,
		PendingEnvLogins:  int(pendingEnv),
		IPTrustworthy:     trustworthy,
		TotpEnabled:       totpEnabled(user),
		AllLoginPathsMFA:  totpEnabled(user),
		TransportHTTPS:    c.Request.TLS != nil,
		Privileged:        isPrivilegedAccount(user),
	})

	currentJti := c.GetString("jti")
	sessions := make([]sessionView, 0, len(active))
	for _, s := range active {
		sessions = append(sessions, toSessionView(s, currentJti))
	}
	logins := make([]sessionView, 0, len(history))
	for _, s := range history {
		logins = append(logins, toSessionView(s, currentJti))
	}

	c.JSON(http.StatusOK, gin.H{
		"score":    assessment,
		"sessions": sessions,
		"logins":   logins,
		"account": gin.H{
			"id":                   user.ID,
			"username":             user.Username,
			"real_name":            user.RealName,
			"role":                 user.Role,
			"department":           user.Department,
			"position":             user.Position,
			"privileged":           isPrivilegedAccount(user),
			"password_set":         user.PasswordChangedAt != nil,
			"password_changed_at":  user.PasswordChangedAt,
			"password_strength":    user.PasswordStrength,
			"totp_enabled":         totpEnabled(user),
			"last_login_ip":        user.LastLoginIP,
			"last_login_at":        user.LastLoginAt,
			"must_change_password": user.PasswordChangedAt == nil,
		},
		"ip_trustworthy": trustworthy,
	})
}

// RevokeOtherSessions 下线除当前设备以外的全部会话。
// 保留当前会话，用户点完不用重新登录；真要连本机一起退，走「退出登录」。
// 按会话行吊销即可：每个令牌都对应一行登记，中间件逐次核对，无需再 bump 令牌版本。
func (a *AccountSecurityController) RevokeOtherSessions(c *gin.Context) {
	user, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}

	res := repository.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND revoked_at IS NULL AND jti <> ?", user.ID, c.GetString("jti")).
		Update("revoked_at", time.Now())
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "会话下线失败: " + res.Error.Error()})
		return
	}

	logOperationAs(c, user, "account.sessions_revoke_others", "user", user.ID,
		"已下线其他设备的全部登录会话")

	c.JSON(http.StatusOK, gin.H{
		"message":       "其他设备已全部退出登录",
		"revoked_count": res.RowsAffected,
	})
}

// RevokeSession 下线指定会话。按 user_id 收窄查询，避免越权吊销他人会话。
func (a *AccountSecurityController) RevokeSession(c *gin.Context) {
	user, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}
	rawID, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || rawID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话编号无效"})
		return
	}
	sessionID := uint(rawID)

	var sess model.UserSession
	if err := repository.DB.Where("id = ? AND user_id = ?", sessionID, user.ID).First(&sess).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到该会话"})
		return
	}
	if sess.Jti == c.GetString("jti") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "这是当前设备使用的会话，请改用「退出登录」"})
		return
	}
	if sess.RevokedAt != nil {
		c.JSON(http.StatusOK, gin.H{"message": "该会话已下线", "already_revoked": true})
		return
	}

	if err := repository.DB.Model(&model.UserSession{}).Where("id = ?", sess.ID).
		Update("revoked_at", time.Now()).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "会话下线失败"})
		return
	}

	logOperationAs(c, user, "account.session_revoke", "user_session", sess.ID,
		"已下线一个登录会话")

	c.JSON(http.StatusOK, gin.H{"message": "该设备已退出登录"})
}

// ConfirmEnvironment 本人确认近期陌生环境登录为本人操作，确认后该项扣分归零。
// 高危操作：只持有一个被盗会话的人不该能把"异地登录"信号抹平，因此当场重验登录口令。
func (a *AccountSecurityController) ConfirmEnvironment(c *gin.Context) {
	user, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}
	if !requireStepUpAccountHygiene(c, user) {
		return
	}

	since := time.Now().AddDate(0, 0, -envBaselineDays)
	res := repository.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND env_status = ? AND login_at >= ?", user.ID, EnvStatusPendingConfirm, since).
		Update("env_status", EnvStatusKnown)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "确认失败: " + res.Error.Error()})
		return
	}

	logOperationAs(c, user, "account.environment_confirm", "user", user.ID,
		"已确认近期陌生环境登录为本人操作，环境项扣分已消除")

	c.JSON(http.StatusOK, gin.H{
		"message":       "已确认这些登录来自本人，安全评分中的环境项已恢复",
		"confirmed":     res.RowsAffected,
		"current_score": scoreAfterConfirm(user, c, since),
	})
}

// scoreAfterConfirm 返回确认后重算的评分，省掉前端再拉一次 Summary 的往返。
func scoreAfterConfirm(user model.User, c *gin.Context, since time.Time) service.SecurityAssessment {
	var pending int64
	_ = repository.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND env_status = ? AND login_at >= ?", user.ID, EnvStatusPendingConfirm, since).
		Count(&pending).Error

	return service.AssessAccountSecurity(service.SecurityInput{
		PasswordSet:       user.PasswordChangedAt != nil,
		PasswordStrength:  user.PasswordStrength,
		HasSessionHistory: len(seenLoginIPs(user.ID, since)) > 0,
		PendingEnvLogins:  int(pending),
		IPTrustworthy:     clientIPTrustworthy(c),
		TotpEnabled:       totpEnabled(user),
		AllLoginPathsMFA:  totpEnabled(user),
		TransportHTTPS:    c.Request.TLS != nil,
		Privileged:        isPrivilegedAccount(user),
	})
}
