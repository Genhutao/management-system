package controller

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/secretbox"
	"xgh-system/pkg/totp"
)

// totpIssuer 验证器里显示的发行方，与系统名保持一致便于用户辨认。
const totpIssuer = "学管会综合管理系统"

// totpEnabled 判定账号是否已绑定动态口令。密钥非空即为已绑定并已在登录通道生效，
// 因此 setup 只把待绑定密钥放在内存里，确认通过后才落库。
func totpEnabled(u model.User) bool {
	return strings.TrimSpace(u.TotpSecretEnc) != ""
}

// 待绑定的 TOTP 密钥：绑定是"扫码→填码"一次性连续动作，15 分钟内不完成即作废。
// 刻意不建 pending 列：加字段要动模型与 AutoMigrate，而进程重启后重新扫码即可，
// 代价远小于收益。
const totpPendingTTL = 15 * time.Minute

type pendingTOTPSecret struct {
	secret    string
	expiresAt time.Time
}

var (
	totpPendingMu sync.Mutex
	totpPending   = map[uint]pendingTOTPSecret{}
)

func savePendingTOTP(userID uint, secret string) {
	totpPendingMu.Lock()
	defer totpPendingMu.Unlock()
	totpPending[userID] = pendingTOTPSecret{secret: secret, expiresAt: time.Now().Add(totpPendingTTL)}
}

// takePendingTOTP 取出并作废待绑定密钥，保证一次 setup 只能 enable 一次。
func takePendingTOTP(userID uint) string {
	totpPendingMu.Lock()
	defer totpPendingMu.Unlock()
	p, ok := totpPending[userID]
	if !ok {
		return ""
	}
	delete(totpPending, userID)
	if time.Now().After(p.expiresAt) {
		return ""
	}
	return p.secret
}

// loginTotpGate 在口令/三要素核验通过之后校验动态口令。
// 返回 true 表示响应已写好，调用方必须立即 return。
// 两条登录通道都要走这里：只要有一条通道能绕过，二次验证就形同虚设。
func loginTotpGate(c *gin.Context, key string, user *model.User, inputCode string) bool {
	if !totpEnabled(*user) {
		return false
	}

	// 首次提交不带口令属正常流程（前端据此弹出验证码输入框），不计入失败次数，
	// 否则每次正常登录都会消耗一次锁定额度。
	if strings.TrimSpace(inputCode) == "" {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":  "totp_required",
			"error": "该账号已开启动态口令二次验证，请输入验证器上的 6 位口令",
		})
		return true
	}

	plain, err := secretbox.Open(user.TotpSecretEnc)
	if err != nil {
		// 密钥解不开说明存储或 CRYPTO_SECRET 已损坏，失败关闭而不是放行
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":  "totp_unavailable",
			"error": "二次验证暂不可用，请联系学管会技术维护组核查",
		})
		return true
	}

	res, err := totp.Validate(plain, inputCode, time.Now(), totp.Digits, user.TotpLastStep)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":  "totp_unavailable",
			"error": "二次验证暂不可用，请联系学管会技术维护组核查",
		})
		return true
	}
	if !res.OK {
		// 错口令同样计入锁定，否则 6 位数字可被在线穷举
		loginRecordFailure(key)
		logFailedLogin(c, user.Username, "动态口令校验未通过")
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":  "totp_invalid",
			"error": "动态口令不正确或已过期，请使用验证器上当前显示的口令",
		})
		return true
	}

	if err := repository.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Update("totp_last_step", res.Step).Error; err != nil {
		// 步长没落库只影响防重放强度，不影响本次核验结果，但必须让运维看见
		log.Printf("[Warn] 账号 %d 的 TOTP 步长落库失败: %v", user.ID, err)
	}
	user.TotpLastStep = res.Step
	return false
}

// SetupTOTP 生成待绑定密钥并给出 otpauth 链接，供验证器扫码。
// 密钥只出现在这一次响应里，且不写审计、不写日志。
func (a *AccountSecurityController) SetupTOTP(c *gin.Context) {
	user, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}
	if totpEnabled(user) {
		c.JSON(http.StatusConflict, gin.H{"error": "该账号已绑定动态口令，如需更换请先解绑"})
		return
	}

	secret, err := totp.NewSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成密钥失败: " + err.Error()})
		return
	}
	savePendingTOTP(user.ID, secret)

	logOperationAs(c, user, "account.totp_setup", "user", user.ID, "发起动态口令绑定，密钥已下发待验证")

	c.JSON(http.StatusOK, gin.H{
		"secret":      secret,
		"otpauth_url": totp.OTPAuthURL(totpIssuer, user.Username, secret),
		"expires_in":  int(totpPendingTTL.Seconds()),
		"digits":      totp.Digits,
		"period":      totp.StepSeconds,
		"message":     "请在 15 分钟内用验证器扫码，并填写当前 6 位口令完成绑定",
	})
}

// EnableTOTP 用一次正确的口令把待绑定密钥正式启用。
// 没有这一步就没有"绑定即生效"，也就没法阻止用户扫了一张废码。
func (a *AccountSecurityController) EnableTOTP(c *gin.Context) {
	user, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}
	if totpEnabled(user) {
		c.JSON(http.StatusConflict, gin.H{"error": "该账号已绑定动态口令"})
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请填写验证器上当前显示的 6 位口令"})
		return
	}

	secret := takePendingTOTP(user.ID)
	if secret == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "绑定会话已过期或不存在，请重新扫码后再填写口令"})
		return
	}

	res, err := totp.Validate(secret, req.Code, time.Now(), totp.Digits, 0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "口令格式不正确"})
		return
	}
	if !res.OK {
		// 失败即作废本次待绑定密钥：错码者不该拿同一个密钥反复试探
		logFailedLogin(c, user.Username, "绑定动态口令时验证码错误")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "验证码校验未通过，请重新扫码发起绑定"})
		return
	}

	sealed, err := secretbox.Seal(secret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密钥封装失败: " + err.Error()})
		return
	}
	if err := repository.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Updates(map[string]any{"totp_secret_enc": sealed, "totp_last_step": res.Step}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "绑定保存失败: " + err.Error()})
		return
	}

	logOperationAs(c, user, "account.totp_enable", "user", user.ID, "已开启动态口令二次验证")

	c.JSON(http.StatusOK, gin.H{"message": "动态口令二次验证已开启，下次登录起生效"})
}

// DisableTOTP 解绑动态口令。解绑是把账号的门槛降回一层，必须当场重验登录口令。
func (a *AccountSecurityController) DisableTOTP(c *gin.Context) {
	user, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不存在"})
		return
	}
	if !totpEnabled(user) {
		c.JSON(http.StatusNotFound, gin.H{"error": "该账号尚未绑定动态口令"})
		return
	}
	if !requireStepUpAccountHygiene(c, user) {
		return
	}

	if err := repository.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Updates(map[string]any{"totp_secret_enc": "", "totp_last_step": 0}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "解绑失败: " + err.Error()})
		return
	}
	totpPendingMu.Lock()
	delete(totpPending, user.ID)
	totpPendingMu.Unlock()

	logOperationAs(c, user, "account.totp_disable", "user", user.ID, "已解绑动态口令二次验证")

	c.JSON(http.StatusOK, gin.H{"message": "动态口令二次验证已关闭"})
}
