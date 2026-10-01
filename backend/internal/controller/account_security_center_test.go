package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/secretbox"
	"xgh-system/pkg/totp"
)

// 账户安全中心自助端的三条硬约束：
//  1. 段内任何处理器只能读到当前登录账号本人的数据（他人资源一律 404）；
//  2. 高危动作（确认异地登录、解绑二次验证）必须当场重验登录口令；
//  3. TOTP 一旦绑定，两条登录通道都拦得住，且同一个验证码不能用第二次。

const secTestPassword = "Xgh#Passw0rd-2026"

func setupSecurityDB(t *testing.T) {
	t.Helper()
	t.Setenv("CRYPTO_SECRET", "unit-test-crypto-secret-for-security-center")
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.OperationLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	prev := repository.DB
	repository.DB = db
	t.Cleanup(func() {
		repository.DB = prev
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
}

func seedSecUser(t *testing.T, username, role string) model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(secTestPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("口令哈希失败: %v", err)
	}
	now := time.Now()
	u := model.User{
		Username:          username,
		PasswordHash:      string(hash),
		RealName:          "测试账号",
		Role:              role,
		Status:            "active",
		TokenVersion:      1,
		PasswordChangedAt: &now,
		PasswordStrength:  "strong",
	}
	if err := repository.DB.Create(&u).Error; err != nil {
		t.Fatalf("写入用户失败: %v", err)
	}
	// 建号后读回一次：带库端默认值的字段在内存零值里是 0，与生产登录路径同样处理
	var back model.User
	if err := repository.DB.First(&back, u.ID).Error; err != nil {
		t.Fatalf("读回用户失败: %v", err)
	}
	return back
}

func seedSecSession(t *testing.T, userID uint, jti, ip string, revoked bool, envStatus string) model.UserSession {
	t.Helper()
	now := time.Now()
	sess := model.UserSession{
		UserID:     userID,
		Jti:        jti,
		LoginIP:    ip,
		UserAgent:  "Mozilla/5.0 (Test)",
		LoginAt:    now,
		LastSeenAt: now,
		EnvStatus:  envStatus,
	}
	if revoked {
		at := now.Add(-time.Hour)
		sess.RevokedAt = &at
	}
	if err := repository.DB.Create(&sess).Error; err != nil {
		t.Fatalf("写入会话失败: %v", err)
	}
	return sess
}

// newSecurityRouter 挂上真实路由形状，并用一个最小中间件复现 AuthMiddleware
// 会写进上下文的两个键，确保 :id 这类 URI 绑定与生产一致。
func newSecurityRouter(ctrl *AccountSecurityController, uid uint, jti string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	authenticated := r.Group("")
	authenticated.Use(func(c *gin.Context) {
		c.Set("user_id", uid)
		c.Set("jti", jti)
		c.Next()
	})
	{
		authenticated.GET("/api/v1/account/security", ctrl.Summary)
		authenticated.POST("/api/v1/account/sessions/revoke", ctrl.RevokeOtherSessions)
		authenticated.DELETE("/api/v1/account/sessions/:id", ctrl.RevokeSession)
		authenticated.POST("/api/v1/account/environment/confirm", ctrl.ConfirmEnvironment)
		authenticated.POST("/api/v1/account/totp/setup", ctrl.SetupTOTP)
		authenticated.POST("/api/v1/account/totp/enable", ctrl.EnableTOTP)
		authenticated.POST("/api/v1/account/totp/disable", ctrl.DisableTOTP)
	}
	return r
}

func doSec(t *testing.T, r *gin.Engine, method, path, body, confirm string) (int, map[string]interface{}) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if confirm != "" {
		req.Header.Set("X-Confirm-Password", confirm)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	out := map[string]interface{}{}
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是合法 JSON (%s %s): %s", method, path, w.Body.String())
		}
	}
	return w.Code, out
}

func jsonString(v interface{}) string {
	s, _ := v.(string)
	return s
}

// pickWrong 选出一个必然不等于任何真码的 6 位串，
// 避免"随手写的错码恰好撞上真值"这种 10⁻⁶ 概率的偶发失败。
func pickWrong(exclude ...string) string {
	for _, cand := range []string{"000000", "000001", "000002", "123456", "654321"} {
		taken := false
		for _, e := range exclude {
			if e == cand {
				taken = true
				break
			}
		}
		if !taken {
			return cand
		}
	}
	return "987654"
}

// runGate 直接驱动登录二次验证闸门，返回是否拦截、HTTP 状态码与响应正文。
func runGate(user *model.User, input string) (handled bool, status int, body string) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	handled = loginTotpGate(c, "gate|127.0.0.1", user, input)
	return handled, rec.Code, rec.Body.String()
}

// TestTOTPBindAndLoginGate 覆盖绑定全流程与绑定后的登录拦截。
func TestTOTPBindAndLoginGate(t *testing.T) {
	setupSecurityDB(t)
	ctrl := &AccountSecurityController{}
	user := seedSecUser(t, "member_bind", model.RoleMember)
	r := newSecurityRouter(ctrl, user.ID, "jti-bind")

	// 未绑定时不应拦截登录
	var fresh model.User
	if err := repository.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if handled, _, _ := runGate(&fresh, ""); handled {
		t.Fatal("未绑定 TOTP 的账号不应被二次验证拦截")
	}

	code, body := doSec(t, r, http.MethodPost, "/api/v1/account/totp/setup", "", "")
	if code != http.StatusOK {
		t.Fatalf("setup 应成功，实际 %d %v", code, body)
	}
	secret := jsonString(body["secret"])
	if secret == "" || jsonString(body["otpauth_url"]) == "" {
		t.Fatalf("setup 未返回密钥或 otpauth 链接: %v", body)
	}

	// 待绑定阶段还不能生效
	repository.DB.First(&fresh, user.ID)
	if totpEnabled(fresh) {
		t.Fatal("仅发起绑定就生效，等于跳过验证码核验")
	}

	// 错验证码：拒绝，并且本次待绑定密钥作废
	current, err := totp.Code(secret, time.Now(), totp.Digits)
	if err != nil {
		t.Fatalf("生成测试验证码失败: %v", err)
	}
	wrong := pickWrong(current)
	if code, _ = doSec(t, r, http.MethodPost, "/api/v1/account/totp/enable", `{"code":"`+wrong+`"}`, ""); code != http.StatusUnauthorized {
		t.Fatalf("错验证码应 401，实际 %d", code)
	}
	if code, body = doSec(t, r, http.MethodPost, "/api/v1/account/totp/enable", `{"code":"111111"}`, ""); code != http.StatusConflict {
		t.Fatalf("作废后的待绑定会话应要求重新扫码，实际 %d %v", code, body)
	}

	// 重新扫码并填对口令
	_, body = doSec(t, r, http.MethodPost, "/api/v1/account/totp/setup", "", "")
	secret = jsonString(body["secret"])
	real, err := totp.Code(secret, time.Now(), totp.Digits)
	if err != nil {
		t.Fatalf("生成测试验证码失败: %v", err)
	}
	if code, body = doSec(t, r, http.MethodPost, "/api/v1/account/totp/enable", `{"code":"`+real+`"}`, ""); code != http.StatusOK {
		t.Fatalf("正确验证码应启用成功，实际 %d %v", code, body)
	}

	if err := repository.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if !totpEnabled(fresh) {
		t.Fatal("启用后 totp_secret_enc 应非空")
	}
	if !secretbox.IsSealed(fresh.TotpSecretEnc) {
		t.Fatal("TOTP 密钥必须经 secretbox 封装入库")
	}
	if strings.Contains(fresh.TotpSecretEnc, secret) {
		t.Fatal("库里出现了明文密钥")
	}

	if handled, _, body := runGate(&fresh, real); !handled {
		t.Fatal("绑定阶段花掉的验证码不应还能直接用于登录")
	} else if !strings.Contains(body, "totp_invalid") {
		t.Fatalf("应报 totp_invalid，实际 %s", body)
	}

	// enable 已把步长记进 totp_last_step，登录要用下一个步长的口令
	next, err := totp.Code(secret, time.Now().Add(time.Duration(totp.StepSeconds)*time.Second), totp.Digits)
	if err != nil {
		t.Fatalf("生成下一step验证码失败: %v", err)
	}
	bad := pickWrong(real, next)

	// 不带验证码被要求补码
	if handled, status, body := runGate(&fresh, ""); !handled {
		t.Fatal("已绑定账号提交空验证码应被拦截")
	} else if status != http.StatusUnauthorized || !strings.Contains(body, "totp_required") {
		t.Fatalf("应返回 totp_required，实际 %d %s", status, body)
	}

	// 错码被拒
	if handled, status, body := runGate(&fresh, bad); !handled {
		t.Fatal("错验证码应被拦截")
	} else if status != http.StatusUnauthorized || !strings.Contains(body, "totp_invalid") {
		t.Fatalf("应返回 totp_invalid，实际 %d %s", status, body)
	}

	// 正码放行，并把步长落库
	if handled, status, body := runGate(&fresh, next); handled {
		t.Fatalf("正确验证码应放行，实际响应 %d %s", status, body)
	}
	if err := repository.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if fresh.TotpLastStep <= 0 {
		t.Fatalf("成功核验后应落库步长用于防重放，实际 %d", fresh.TotpLastStep)
	}

	// 同一个码再用一次：必须被步长挡住
	if handled, _, body := runGate(&fresh, next); !handled {
		t.Fatal("重复使用同一验证码应被拒绝")
	} else if !strings.Contains(body, "totp_invalid") {
		t.Fatalf("重放应报 totp_invalid，实际 %s", body)
	}
}

// TestDisableTOTPRequiresStepUp 解绑是降门槛操作，口令不过就不给解。
func TestDisableTOTPRequiresStepUp(t *testing.T) {
	setupSecurityDB(t)
	ctrl := &AccountSecurityController{}
	user := seedSecUser(t, "member_disable", model.RoleMember)
	sealed, err := secretbox.Seal("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	if err != nil {
		t.Fatalf("封装测试密钥失败: %v", err)
	}
	if err := repository.DB.Model(&model.User{}).Where("id = ?", user.ID).
		Update("totp_secret_enc", sealed).Error; err != nil {
		t.Fatalf("预置密钥失败: %v", err)
	}
	r := newSecurityRouter(ctrl, user.ID, "jti-disable")

	if code, _ := doSec(t, r, http.MethodPost, "/api/v1/account/totp/disable", "", ""); code != http.StatusBadRequest {
		t.Fatalf("缺少二次确认口令应 400，实际 %d", code)
	}
	if code, _ := doSec(t, r, http.MethodPost, "/api/v1/account/totp/disable", "", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("确认口令错误应 401，实际 %d", code)
	}
	var still model.User
	repository.DB.First(&still, user.ID)
	if !totpEnabled(still) {
		t.Fatal("口令未核验通过时不得解绑")
	}

	if code, _ := doSec(t, r, http.MethodPost, "/api/v1/account/totp/disable", "", secTestPassword); code != http.StatusOK {
		t.Fatalf("正确口令应可解绑，实际 %d", code)
	}
	repository.DB.First(&still, user.ID)
	if totpEnabled(still) || still.TotpLastStep != 0 {
		t.Fatalf("解绑后应清空密钥与步长，实际 %+v", still)
	}
}

// TestSummaryOnlyOwnData 评分总览不得串号：只出现本人会话，且标出当前设备。
func TestSummaryOnlyOwnData(t *testing.T) {
	setupSecurityDB(t)
	ctrl := &AccountSecurityController{}
	mine := seedSecUser(t, "member_owner", model.RoleMember)
	other := seedSecUser(t, "member_other", model.RoleMember)
	seedSecSession(t, mine.ID, "jti-mine-current", "10.0.0.9", false, EnvStatusKnown)
	seedSecSession(t, mine.ID, "jti-mine-old", "10.0.0.8", true, EnvStatusPendingConfirm)
	seedSecSession(t, other.ID, "jti-other", "10.9.9.9", false, EnvStatusKnown)

	r := newSecurityRouter(ctrl, mine.ID, "jti-mine-current")
	code, body := doSec(t, r, http.MethodGet, "/api/v1/account/security", "", "")
	if code != http.StatusOK {
		t.Fatalf("总览应 200，实际 %d %v", code, body)
	}

	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "jti-") {
		t.Fatalf("响应里不得出现任何 jti: %s", raw)
	}
	sessions, _ := body["sessions"].([]interface{})
	if len(sessions) != 1 {
		t.Fatalf("只应列出本人未吊销会话，实际 %d 条", len(sessions))
	}
	first, _ := sessions[0].(map[string]interface{})
	if jsonString(first["login_ip"]) != "10.0.0.9" || first["current"] != true {
		t.Fatalf("当前设备应被标出且只含本人会话: %+v", first)
	}

	account, _ := body["account"].(map[string]interface{})
	if jsonString(account["username"]) != "member_owner" || account["password_set"] != true {
		t.Fatalf("账号事实字段不正确: %+v", account)
	}
	if account["totp_enabled"] != false {
		t.Fatalf("未绑定时 totp_enabled 应为 false: %+v", account)
	}
	score, _ := body["score"].(map[string]interface{})
	items, _ := score["items"].([]interface{})
	if len(items) != 4 {
		t.Fatalf("四项明细应齐全，实际 %d 项", len(items))
	}
	if score["total"] == nil || score["baseline"] == nil {
		t.Fatalf("评分缺少总分或标尺: %+v", score)
	}
	if privileged, _ := account["privileged"].(bool); privileged {
		t.Fatal("普通部员不应被判为高权账号")
	}
}

// TestSessionRevocationScoping 单条吊销按 user_id 收窄，本机会话留给登出。
func TestSessionRevocationScoping(t *testing.T) {
	setupSecurityDB(t)
	ctrl := &AccountSecurityController{}
	mine := seedSecUser(t, "member_revoke", model.RoleMember)
	other := seedSecUser(t, "minister_revoke", model.RoleMinister)
	current := seedSecSession(t, mine.ID, "jti-current", "10.0.0.1", false, EnvStatusKnown)
	otherDev := seedSecSession(t, mine.ID, "jti-other-dev", "10.0.0.2", false, EnvStatusKnown)
	foreign := seedSecSession(t, other.ID, "jti-foreign", "10.0.0.99", false, EnvStatusKnown)

	r := newSecurityRouter(ctrl, mine.ID, "jti-current")

	if code, _ := doSec(t, r, http.MethodDelete, "/api/v1/account/sessions/"+secID(foreign.ID), "", ""); code != http.StatusNotFound {
		t.Fatalf("吊销他人会话应 404，实际 %d", code)
	}
	var foreignRow model.UserSession
	repository.DB.First(&foreignRow, foreign.ID)
	if foreignRow.RevokedAt != nil {
		t.Fatal("他人会话被误吊销")
	}

	if code, _ := doSec(t, r, http.MethodDelete, "/api/v1/account/sessions/"+secID(current.ID), "", ""); code != http.StatusBadRequest {
		t.Fatalf("吊销本机会话应 400 并指向退出登录，实际 %d", code)
	}

	if code, body := doSec(t, r, http.MethodDelete, "/api/v1/account/sessions/"+secID(otherDev.ID), "", ""); code != http.StatusOK {
		t.Fatalf("下线本人其他会话应 200，实际 %d %v", code, body)
	}
	var reloaded model.UserSession
	if err := repository.DB.First(&reloaded, otherDev.ID).Error; err != nil {
		t.Fatalf("读回被下线会话失败: %v", err)
	}
	if reloaded.RevokedAt == nil {
		t.Fatal("本人其他设备会话应已吊销")
	}

	// 批量下线：保留本机
	seedSecSession(t, mine.ID, "jti-third-dev", "10.0.0.3", false, EnvStatusKnown)
	code, body := doSec(t, r, http.MethodPost, "/api/v1/account/sessions/revoke", "", "")
	if code != http.StatusOK {
		t.Fatalf("批量下线应 200，实际 %d %v", code, body)
	}
	if n, _ := body["revoked_count"].(float64); n != 1 {
		t.Fatalf("应只下线除本机外的 1 条会话，实际 %v", body["revoked_count"])
	}
	var alive []model.UserSession
	repository.DB.Where("user_id = ? AND revoked_at IS NULL", mine.ID).Find(&alive)
	if len(alive) != 1 || alive[0].Jti != "jti-current" {
		t.Fatalf("本机会话必须存活: %+v", alive)
	}
	repository.DB.First(&foreignRow, foreign.ID)
	if foreignRow.RevokedAt != nil {
		t.Fatal("批量下线不得动到他人账号的会话")
	}
}

// TestConfirmEnvironmentNeedsPassword 异地登录确认必须先重验口令，
// 否则只持有一个被盗会话的人就能把风险信号抹平。
func TestConfirmEnvironmentNeedsPassword(t *testing.T) {
	setupSecurityDB(t)
	ctrl := &AccountSecurityController{}
	mine := seedSecUser(t, "member_env", model.RoleMember)
	seedSecSession(t, mine.ID, "jti-env-current", "10.0.0.1", false, EnvStatusKnown)
	pending := seedSecSession(t, mine.ID, "jti-env-strange", "172.16.5.5", false, EnvStatusPendingConfirm)

	r := newSecurityRouter(ctrl, mine.ID, "jti-env-current")
	if code, _ := doSec(t, r, http.MethodPost, "/api/v1/account/environment/confirm", "", ""); code != http.StatusBadRequest {
		t.Fatalf("缺少二次确认口令应 400，实际 %d", code)
	}

	code, body := doSec(t, r, http.MethodPost, "/api/v1/account/environment/confirm", "", secTestPassword)
	if code != http.StatusOK {
		t.Fatalf("口令正确时应确认成功，实际 %d %v", code, body)
	}
	if n, _ := body["confirmed"].(float64); n != 1 {
		t.Fatalf("应确认 1 条待核验登录，实际 %v", body["confirmed"])
	}
	var row model.UserSession
	repository.DB.First(&row, pending.ID)
	if row.EnvStatus != EnvStatusKnown {
		t.Fatalf("确认后环境状态应转 known，实际 %s", row.EnvStatus)
	}
	score, _ := body["current_score"].(map[string]interface{})
	if score == nil || score["total"] == nil {
		t.Fatalf("确认响应应带回重算评分: %+v", body)
	}

	// 审计里不能出现口令本身，只该记"已确认"
	var logs []model.OperationLog
	repository.DB.Where("action = ?", "account.environment_confirm").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("应留痕一条确认记录，实际 %d", len(logs))
	}
	if strings.Contains(logs[0].Detail, secTestPassword) {
		t.Fatal("审计明细不得包含口令")
	}
}

func secID(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}
