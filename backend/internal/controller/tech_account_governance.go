package controller

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
)

// governanceDefaultLimit 一次最多呈现的账号数：清单是给技术维护组看板的，
// 再大就该走报表而不是治理页；同时给评分计算定了个可预期的上界。
const governanceDefaultLimit = 500

// 技术维护组的账号治理面：只负责"看见风险"和"替本人重置凭据"两件事。
// 与自助面的分工是硬性的：治理端点不读任何人的口令，也不写任何人的评分——
// 评分是本人账号事实的函数，改事实即改分，不存在"调分"这条路。
type AccountGovernanceController struct{}

// userServerOwnedFields 账户安全中心里由服务端自持的列。
// 通用数据编辑器写入这批字段会直接推翻既有安全口径，因此一律剔除：
// password_changed_at 写了就等于替别人完成"已自行设密"，
// totp_secret_enc / totp_last_step 是二次验证凭据与防重放步长，
// token_version 是会话吊销水位，last_login_* 是异地登录判定基线。
var userServerOwnedFields = []string{
	"password_changed_at", "password_strength",
	"totp_secret_enc", "totp_last_step",
	"token_version", "last_login_ip", "last_login_at",
}

func sortedKeys(payload map[string]interface{}) []string {
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fieldNames 把一次写入涉及的字段名拼成一句可读明细。
// 只列字段名不列取值：取值里可能是口令、手机号等个人数据。
func fieldNames(payload map[string]interface{}) string {
	return strings.Join(sortedKeys(payload), "、")
}

// auditTechDBEdit 给通用编辑器的账号类写操作补一条留痕。
// 明细只记字段名，不记字段值：值里可能出现口令、手机号等个人数据。
func auditTechDBEdit(c *gin.Context, targetType string, targetID uint, action, detail string) {
	if operator, ok := operatorFromContext(c); ok {
		logOperationAs(c, operator, action, targetType, targetID, detail)
	}
}

// governanceRow 单个账号的治理视图。
type governanceRow struct {
	ID               uint       `json:"id"`
	Username         string     `json:"username"`
	RealName         string     `json:"real_name"`
	Role             string     `json:"role"`
	Department       string     `json:"department"`
	Position         string     `json:"position"`
	Status           string     `json:"status"`
	Privileged       bool       `json:"privileged"`
	PasswordSet      bool       `json:"password_set"`
	PasswordStrength string     `json:"password_strength"`
	TotpEnabled      bool       `json:"totp_enabled"`
	Score            int        `json:"score"`
	Level            string     `json:"level"`
	Baseline         int        `json:"baseline"`
	MeetsLine        bool       `json:"meets_baseline"`
	PendingEnvLogins int        `json:"pending_env_logins"`
	LastLoginIP      string     `json:"last_login_ip"`
	LastLoginAt      *time.Time `json:"last_login_at"`
}

// governanceSummary 治理看板的汇总口径。
type governanceSummary struct {
	TotalAccounts      int `json:"total_accounts"`
	InitialPassword    int `json:"initial_password_accounts"`
	WeakPassword       int `json:"weak_password_accounts"`
	UnratedPassword    int `json:"unrated_password_accounts"`
	NoSecondFactor     int `json:"no_second_factor_accounts"`
	PrivilegedLowRisk  int `json:"privileged_below_baseline"`
	PendingEnvAccounts int `json:"pending_env_accounts"`
}

// List 治理清单：按"权限高低 → 分数高低"排序，风险账号自然沉到前面。
// 排序只是阅读顺序，不参与任何授权判定。
func (g *AccountGovernanceController) List(c *gin.Context) {
	var users []model.User
	limit := governanceDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > governanceDefaultLimit {
		limit = governanceDefaultLimit
	}
	if err := repository.DB.Limit(limit).Order("id asc").Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "账号清单读取失败"})
		return
	}

	since := time.Now().AddDate(0, 0, -envBaselineDays)
	pendingByUser := pendingEnvCounts(users, since)
	historyUsers := usersWithSessionHistory(users, since)

	trustworthy := clientIPTrustworthy(c)
	https := c.Request.TLS != nil

	rows := make([]governanceRow, 0, len(users))
	var sum governanceSummary
	sum.TotalAccounts = len(users)

	for _, u := range users {
		pending := pendingByUser[u.ID]
		in := service.SecurityInput{
			PasswordSet:       u.PasswordChangedAt != nil,
			PasswordStrength:  u.PasswordStrength,
			HasSessionHistory: historyUsers[u.ID],
			PendingEnvLogins:  pending,
			IPTrustworthy:     trustworthy,
			TotpEnabled:       totpEnabled(u),
			AllLoginPathsMFA:  totpEnabled(u),
			TransportHTTPS:    https,
			Privileged:        isPrivilegedAccount(u),
		}
		assess := service.AssessAccountSecurity(in)
		privileged := in.Privileged

		if u.PasswordChangedAt == nil {
			sum.InitialPassword++
		}
		switch u.PasswordStrength {
		case service.PasswordStrengthWeak:
			sum.WeakPassword++
		case "":
			if u.PasswordChangedAt != nil {
				sum.UnratedPassword++
			}
		}
		if !in.TotpEnabled {
			sum.NoSecondFactor++
		}
		if pending > 0 {
			sum.PendingEnvAccounts++
		}
		if privileged && !assess.MeetsLine {
			sum.PrivilegedLowRisk++
		}

		rows = append(rows, governanceRow{
			ID: u.ID, Username: u.Username, RealName: u.RealName,
			Role: u.Role, Department: u.Department, Position: u.Position, Status: u.Status,
			Privileged:  privileged,
			PasswordSet: in.PasswordSet, PasswordStrength: u.PasswordStrength,
			TotpEnabled: in.TotpEnabled,
			Score:       assess.Total, Level: assess.Level,
			Baseline: assess.Baseline, MeetsLine: assess.MeetsLine,
			PendingEnvLogins: pending,
			LastLoginIP:      u.LastLoginIP, LastLoginAt: u.LastLoginAt,
		})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Privileged != rows[j].Privileged {
			return rows[i].Privileged
		}
		if rows[i].Score != rows[j].Score {
			return rows[i].Score < rows[j].Score
		}
		return rows[i].ID < rows[j].ID
	})

	c.JSON(http.StatusOK, gin.H{
		"summary":         sum,
		"accounts":        rows,
		"ip_trustworthy":  trustworthy,
		"transport_https": https,
		"baseline_note":   "分数与标尺只用于展示和排序，不参与任何授权判定；收权仍然只有停用账号与回收打表权两条。",
		"not_returned":    "接口不返回任何口令，明文口令在库里本就不可恢复（bcrypt 单向）。",
		"limit":           limit,
	})
}

// ResetUserPassword 替本人重置登录口令：用于设备遗失、账号交接等场景。
// 新口令随机生成且只在这一次响应里出现；同时把账号打回"待本人设密"状态，
// 并吊销其全部会话、抬高令牌版本，使旧凭据立即失效。
func (g *AccountGovernanceController) ResetUserPassword(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "操作者账号不存在"})
		return
	}
	if !requireStepUp(c, operator) {
		return
	}

	targetID, err := parsePathID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "账号编号无效"})
		return
	}
	var target model.User
	if err := repository.DB.First(&target, targetID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "目标账号不存在"})
		return
	}

	newPassword := newRandomInitialPassword()
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "口令加密失败: " + err.Error()})
		return
	}

	// 抬高版本水位后必须用新版本重新登记会话，否则本人下一次登录又会拿到旧版本令牌
	newVersion := target.TokenVersion + 1
	if err := repository.DB.Model(&model.User{}).Where("id = ?", target.ID).
		Updates(map[string]any{
			"password_hash":       string(hash),
			"password_changed_at": nil,
			"password_strength":   service.EvaluatePassword(newPassword),
			"token_version":       newVersion,
			"updated_at":          time.Now(),
		}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "重置失败: " + err.Error()})
		return
	}
	if err := revokeAllSessions(target.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "会话吊销失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "tech.user_reset_password", "user", target.ID,
		"已重置该账号的登录口令为一次性初始口令，并吊销其全部登录会话（口令仅在本次响应中交付，不留痕）")

	c.JSON(http.StatusOK, gin.H{
		"message":          "口令已重置。请把新口令当面交给本人，并提醒其登录后立即自行修改。",
		"initial_password": newPassword,
		"notice":           "初始口令仅显示这一次；该账号已回到\"待本人设密\"状态，高危操作与强制改密提示会随之生效。",
	})
}

// UnbindUserTOTP 解绑某账号的动态口令：用于验证器遗失。
// 解绑等于降低一道门槛，因此同样要求当场重验操作者口令并留痕。
func (g *AccountGovernanceController) UnbindUserTOTP(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "操作者账号不存在"})
		return
	}
	if !requireStepUp(c, operator) {
		return
	}

	targetID, err := parsePathID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "账号编号无效"})
		return
	}
	var target model.User
	if err := repository.DB.First(&target, targetID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "目标账号不存在"})
		return
	}
	if !totpEnabled(target) {
		c.JSON(http.StatusNotFound, gin.H{"error": "该账号未绑定动态口令"})
		return
	}

	if err := repository.DB.Model(&model.User{}).Where("id = ?", target.ID).
		Updates(map[string]any{"totp_secret_enc": "", "totp_last_step": 0}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解绑失败: " + err.Error()})
		return
	}
	totpPendingMu.Lock()
	delete(totpPending, target.ID)
	totpPendingMu.Unlock()

	logOperationAs(c, operator, "tech.user_totp_unbind", "user", target.ID,
		"已为该账号解绑动态口令二次验证（验证器遗失场景）")

	c.JSON(http.StatusOK, gin.H{"message": "已解绑该账号的动态口令，请提醒本人重新绑定"})
}

// pendingEnvCounts 一次聚合取出各账号待确认的异地登录数，避免逐账号查询。
func pendingEnvCounts(users []model.User, since time.Time) map[uint]int {
	ids := make([]uint, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	out := make(map[uint]int, len(ids))
	if len(ids) == 0 {
		return out
	}
	type row struct {
		UserID uint
		N      int
	}
	var rows []row
	if err := repository.DB.Model(&model.UserSession{}).
		Select("user_id, count(*) as n").
		Where("user_id IN ? AND env_status = ? AND login_at >= ?", ids, EnvStatusPendingConfirm, since).
		Group("user_id").Scan(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.UserID] = r.N
	}
	return out
}

// usersWithSessionHistory 标记哪些账号有可比对的登录基线。
func usersWithSessionHistory(users []model.User, since time.Time) map[uint]bool {
	ids := make([]uint, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	out := make(map[uint]bool, len(ids))
	if len(ids) == 0 {
		return out
	}
	var withHistory []uint
	if err := repository.DB.Model(&model.UserSession{}).
		Where("user_id IN ? AND login_ip <> '' AND login_at >= ?", ids, since).
		Distinct().Pluck("user_id", &withHistory).Error; err != nil {
		return out
	}
	for _, id := range withHistory {
		out[id] = true
	}
	return out
}

// parsePathID 取路径里的 :id，非正整数一律视为无效。
func parsePathID(c *gin.Context) (uint, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("账号编号无效")
	}
	return uint(n), nil
}
