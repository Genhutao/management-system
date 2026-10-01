package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
	"xgh-system/pkg/secretbox"
	"xgh-system/pkg/totp"
)

// 技术维护组治理面的三条底线：
//  1. 清单只呈现风险，不返回任何凭据（口令哈希、封装后的 TOTP 密钥都不许出现）；
//  2. 替别人改凭据必须当场重验操作者口令，且新口令只在一次响应里出现，留痕里不许有它；
//  3. 通用数据编辑器不能把安全字段写进去——写了就等于绕过强制改密、二次验证与会话吊销。

func govSeedUser(t *testing.T, username, role string, passwordChanged, withTOTP bool) model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(secTestPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("口令哈希失败: %v", err)
	}
	u := model.User{
		Username:     username,
		PasswordHash: string(hash),
		RealName:     "治理测试账号",
		Role:         role,
		Status:       "active",
		TokenVersion: 1,
	}
	if passwordChanged {
		now := time.Now()
		u.PasswordChangedAt = &now
		u.PasswordStrength = service.PasswordStrengthStrong
	}
	if withTOTP {
		secret, err := totpSecretForTest()
		if err != nil {
			t.Fatalf("生成测试密钥失败: %v", err)
		}
		u.TotpSecretEnc = secret
		u.TotpLastStep = 12345
	}
	if err := repository.DB.Create(&u).Error; err != nil {
		t.Fatalf("写入账号失败: %v", err)
	}
	var back model.User
	if err := repository.DB.First(&back, u.ID).Error; err != nil {
		t.Fatalf("读回账号失败: %v", err)
	}
	return back
}

func totpSecretForTest() (string, error) {
	raw, err := totp.NewSecret()
	if err != nil {
		return "", err
	}
	return secretbox.Seal(raw)
}

func govRouter(ctrl *AccountGovernanceController, operatorID uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	tech := r.Group("")
	tech.Use(func(c *gin.Context) {
		c.Set("user_id", operatorID)
		c.Next()
	})
	{
		tech.GET("/api/v1/tech/account-governance", ctrl.List)
		tech.POST("/api/v1/tech/users/:id/reset-password", ctrl.ResetUserPassword)
		tech.POST("/api/v1/tech/users/:id/totp-unbind", ctrl.UnbindUserTOTP)
	}
	return r
}

// govRaw 走完整 HTTP 形状，返回状态码与原始正文：
// 断言"某串没出现在响应里"必须看原文，解析成 map 会丢掉键名。
func govRaw(t *testing.T, r *gin.Engine, method, path, body, confirm string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if confirm != "" {
		req.Header.Set("X-Confirm-Password", confirm)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func govJSON(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	out := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %s", raw)
	}
	return out
}

func govRows(body map[string]interface{}) []map[string]interface{} {
	raw, _ := body["accounts"].([]interface{})
	rows := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			rows = append(rows, m)
		}
	}
	return rows
}

func govRowByID(rows []map[string]interface{}, id uint) map[string]interface{} {
	for _, r := range rows {
		if uint(r["id"].(float64)) == id {
			return r
		}
	}
	return nil
}

func TestGovernanceListRanksPrivilegedFirstAndReturnsNoCredentials(t *testing.T) {
	setupSecurityDB(t)
	operator := govSeedUser(t, "tech_admin_list", model.RoleTechAdmin, true, false)
	legacy := govSeedUser(t, "minister_legacy_list", model.RoleMinister, false, false)
	clean := govSeedUser(t, "member_clean_list", model.RoleMember, true, true)

	r := govRouter(&AccountGovernanceController{}, operator.ID)
	code, raw := govRaw(t, r, http.MethodGet, "/api/v1/tech/account-governance", "", "")
	if code != http.StatusOK {
		t.Fatalf("治理清单应 200，实际 %d %s", code, raw)
	}
	body := govJSON(t, raw)
	rows := govRows(body)
	if len(rows) != 3 {
		t.Fatalf("应列出 3 个账号，实际 %d：%s", len(rows), raw)
	}

	// 权限高的风险账号排在最前，其次是分数低的
	if uint(rows[0]["id"].(float64)) != legacy.ID {
		t.Fatalf("首个账号应为高权限且未脱离初始口令的账号，实际 %v", rows[0]["id"])
	}
	legacyRow := govRowByID(rows, legacy.ID)
	cleanRow := govRowByID(rows, clean.ID)
	if legacyRow == nil || cleanRow == nil {
		t.Fatalf("清单缺少账号：\n%s", raw)
	}
	if legacyRow["privileged"] != true || cleanRow["privileged"] != false {
		t.Fatalf("特权判定错误：legacy=%v clean=%v", legacyRow["privileged"], cleanRow["privileged"])
	}
	if legacyRow["password_set"] != false || cleanRow["password_set"] != true {
		t.Fatalf("初始口令状态错误：legacy=%v clean=%v", legacyRow["password_set"], cleanRow["password_set"])
	}
	if legacyRow["meets_baseline"] != false {
		t.Fatalf("未脱离初始口令的高权账号不应达标")
	}
	if cleanRow["totp_enabled"] != true {
		t.Fatalf("已绑定动态口令的账号应在清单中标为 totp_enabled，实际 %v", cleanRow["totp_enabled"])
	}
	if cleanRow["score"].(float64) <= legacyRow["score"].(float64) {
		t.Fatalf("分数排序失真：clean=%v legacy=%v", cleanRow["score"], legacyRow["score"])
	}
	if cleanRow["meets_baseline"] != true {
		t.Fatalf("已设密 + 强口令 + 绑定动态口令的账号应达标，实际 %v", cleanRow)
	}

	// 汇总口径
	sum, _ := body["summary"].(map[string]interface{})
	if sum == nil {
		t.Fatalf("响应缺少 summary：%s", raw)
	}
	if uint(sum["total_accounts"].(float64)) != 3 {
		t.Fatalf("total_accounts 应为 3，实际 %v", sum["total_accounts"])
	}
	if sum["initial_password_accounts"].(float64) < 1 {
		t.Fatalf("应统计到初始口令账号，实际 %v", sum["initial_password_accounts"])
	}
	if sum["no_second_factor_accounts"].(float64) < 2 {
		t.Fatalf("未绑定动态口令的账号应有 2 个，实际 %v", sum["no_second_factor_accounts"])
	}

	// 凭据红线：口令哈希与封装后的密钥都不许出现在响应里
	for _, forbidden := range []string{"password_hash", "totp_secret_enc", "enc:v1:", "$2a$", secTestPassword} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("治理清单不得出现 %q", forbidden)
		}
	}
	if jsonString(body["baseline_note"]) == "" {
		t.Fatal("清单应说明分数与权限不挂钩")
	}
}

func TestAdminPasswordResetDeliversOnceAndLeavesNoSecretInAudit(t *testing.T) {
	setupSecurityDB(t)
	operator := govSeedUser(t, "tech_admin_reset", model.RoleTechAdmin, true, false)
	target := govSeedUser(t, "member_reset", model.RoleMember, false, true)
	before := target.PasswordHash
	seedSecSession(t, target.ID, "jti-reset-a", "10.0.0.1", false, EnvStatusKnown)
	seedSecSession(t, target.ID, "jti-reset-b", "10.0.0.2", false, EnvStatusPendingConfirm)

	r := govRouter(&AccountGovernanceController{}, operator.ID)
	path := "/api/v1/tech/users/" + secID(target.ID) + "/reset-password"

	// 缺少确认口令：拒绝且不改任何东西
	if code, raw := govRaw(t, r, http.MethodPost, path, "", ""); code != http.StatusBadRequest {
		t.Fatalf("缺少二次确认口令应 400，实际 %d %s", code, raw)
	}
	if code, raw := govRaw(t, r, http.MethodPost, path, "", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("确认口令错误应 401，实际 %d %s", code, raw)
	}
	var intact model.User
	repository.DB.First(&intact, target.ID)
	if intact.PasswordHash != before {
		t.Fatal("未通过核验时不得改写口令")
	}

	// 目标账号不存在
	if code, raw := govRaw(t, r, http.MethodPost, "/api/v1/tech/users/999999/reset-password", "", secTestPassword); code != http.StatusNotFound {
		t.Fatalf("目标账号不存在应 404，实际 %d %s", code, raw)
	}

	// 核验通过：新口令只在这一次响应里交付
	code, raw := govRaw(t, r, http.MethodPost, path, "", secTestPassword)
	if code != http.StatusOK {
		t.Fatalf("重置应成功，实际 %d %s", code, raw)
	}
	newPassword := jsonString(govJSON(t, raw)["initial_password"])
	if newPassword == "" || newPassword == secTestPassword {
		t.Fatalf("应交付一个新的随机口令，实际 %q", newPassword)
	}

	var after model.User
	if err := repository.DB.First(&after, target.ID).Error; err != nil {
		t.Fatalf("读回目标账号失败: %v", err)
	}
	if after.PasswordHash == before {
		t.Fatal("口令未被改写")
	}
	if bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte(newPassword)) != nil {
		t.Fatal("交付的明文口令应与库中哈希对得上")
	}
	if after.PasswordChangedAt != nil {
		t.Fatal("重置后应回到\"待本人设密\"状态，否则强制改密提示会失效")
	}
	if after.PasswordStrength != service.EvaluatePassword(newPassword) {
		t.Fatalf("强度应按新口令评定，实际 %q 期望 %q", after.PasswordStrength, service.EvaluatePassword(newPassword))
	}
	if after.TokenVersion != target.TokenVersion+1 {
		t.Fatalf("应抬高令牌版本水位，实际 %d", after.TokenVersion)
	}
	// 重置口令不等于解绑二次验证：验证器还在本人手里
	if !totpEnabled(after) {
		t.Fatal("重置口令不应顺带解绑动态口令")
	}

	var live int64
	repository.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND revoked_at IS NULL", target.ID).Count(&live)
	if live != 0 {
		t.Fatalf("该账号全部会话应被吊销，仍有 %d 条活跃", live)
	}

	var logs []model.OperationLog
	repository.DB.Where("action = ?", "tech.user_reset_password").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("应留痕一条重置记录，实际 %d 条", len(logs))
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, newPassword) || strings.Contains(l.Detail, secTestPassword) {
			t.Fatal("留痕不得包含任何口令明文")
		}
		if !strings.Contains(l.Detail, "会话") {
			t.Fatalf("留痕应说明连会话一起吊销，实际 %q", l.Detail)
		}
	}
	// 库里的封装密钥也不许出现在响应或留痕里
	if strings.Contains(raw, after.TotpSecretEnc) || strings.Contains(raw, "enc:v1:") {
		t.Fatal("响应不得带出封装后的 TOTP 密钥")
	}
}

func TestAdminTotpUnbindRequiresStepUpAndClearsReplayState(t *testing.T) {
	setupSecurityDB(t)
	operator := govSeedUser(t, "tech_admin_unbind", model.RoleTechAdmin, true, false)
	target := govSeedUser(t, "member_unbind", model.RoleMember, true, true)
	plain, err := secretbox.Open(target.TotpSecretEnc)
	if err != nil {
		t.Fatalf("测试密钥应可解开: %v", err)
	}
	sealedBefore := target.TotpSecretEnc

	r := govRouter(&AccountGovernanceController{}, operator.ID)
	path := "/api/v1/tech/users/" + secID(target.ID) + "/totp-unbind"

	if code, raw := govRaw(t, r, http.MethodPost, path, "", ""); code != http.StatusBadRequest {
		t.Fatalf("缺少确认口令应 400，实际 %d %s", code, raw)
	}
	if code, raw := govRaw(t, r, http.MethodPost, path, "", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("确认口令错误应 401，实际 %d %s", code, raw)
	}
	var intact model.User
	repository.DB.First(&intact, target.ID)
	if !totpEnabled(intact) {
		t.Fatal("未通过核验时不得解绑")
	}

	// 未绑定的账号：给明确 404，而不是"成功"地什么都不做
	plainMember := govSeedUser(t, "member_no_totp", model.RoleMember, true, false)
	if code, raw := govRaw(t, r, http.MethodPost,
		"/api/v1/tech/users/"+secID(plainMember.ID)+"/totp-unbind", "", secTestPassword); code != http.StatusNotFound {
		t.Fatalf("未绑定动态口令应 404，实际 %d %s", code, raw)
	}

	code, raw := govRaw(t, r, http.MethodPost, path, "", secTestPassword)
	if code != http.StatusOK {
		t.Fatalf("解绑应成功，实际 %d %s", code, raw)
	}
	var after model.User
	repository.DB.First(&after, target.ID)
	if after.TotpSecretEnc != "" || after.TotpLastStep != 0 {
		t.Fatalf("解绑应同时清掉密钥与防重放步长，实际 %q / %d", after.TotpSecretEnc, after.TotpLastStep)
	}

	var logs []model.OperationLog
	repository.DB.Where("action = ?", "tech.user_totp_unbind").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("应留痕一条解绑记录，实际 %d 条", len(logs))
	}
	if strings.Contains(logs[0].Detail, plain) || strings.Contains(logs[0].Detail, sealedBefore) {
		t.Fatal("留痕不得包含 TOTP 密钥")
	}
	if strings.Contains(raw, plain) {
		t.Fatal("响应不得回显 TOTP 密钥")
	}

	// 治理清单随之反映"未绑定"
	listCode, listRaw := govRaw(t, r, http.MethodGet, "/api/v1/tech/account-governance", "", "")
	if listCode != http.StatusOK {
		t.Fatalf("清单应 200，实际 %d %s", listCode, listRaw)
	}
	row := govRowByID(govRows(govJSON(t, listRaw)), target.ID)
	if row == nil || row["totp_enabled"] != false {
		t.Fatalf("清单应显示该账号已未绑定，实际 %v", row)
	}
}

func TestDBEditorCannotForgeServerOwnedUserFields(t *testing.T) {
	setupSecurityDB(t)
	operator := govSeedUser(t, "tech_admin_editor", model.RoleTechAdmin, true, false)
	target := govSeedUser(t, "member_forge", model.RoleMember, false, false)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	grp.Use(func(c *gin.Context) {
		c.Set("user_id", operator.ID)
		c.Next()
	})
	tdb := &TechDBController{}
	grp.PUT("/api/v1/tech/db/tables/:table/:id", tdb.UpdateRecord)

	payload := `{"real_name":"改了名字","role":"tech_admin","position":"副部长",` +
		`"password_changed_at":"2020-01-01T00:00:00Z","password_strength":"strong",` +
		`"token_version":9,"totp_secret_enc":"enc:v1:whatever","totp_last_step":99,` +
		`"last_login_ip":"8.8.8.8","last_login_at":"2020-01-01T00:00:00Z","phone":"13600000000"}`
	path := "/api/v1/tech/db/tables/users/" + secID(target.ID)
	code, raw := govRaw(t, r, http.MethodPut, path, payload, "")
	if code != http.StatusOK {
		t.Fatalf("编辑账号资料应 200，实际 %d %s", code, raw)
	}

	var after model.User
	if err := repository.DB.First(&after, target.ID).Error; err != nil {
		t.Fatalf("读回账号失败: %v", err)
	}
	if after.RealName != "改了名字" {
		t.Fatalf("普通资料应照常更新，实际 %q", after.RealName)
	}
	if after.Phone != "13600000000" {
		t.Fatalf("手机号应照常更新，实际 %q", after.Phone)
	}
	// 权限属性
	if after.Role != target.Role || after.Position != target.Position {
		t.Fatalf("角色与职务不得从通用编辑器改写：%s/%s", after.Role, after.Position)
	}
	// 安全自持字段
	if after.PasswordChangedAt != nil {
		t.Fatalf("password_changed_at 不得被写入，实际 %v", *after.PasswordChangedAt)
	}
	if after.PasswordStrength != "" {
		t.Fatalf("password_strength 不得被写入，实际 %q", after.PasswordStrength)
	}
	if after.TokenVersion != target.TokenVersion {
		t.Fatalf("token_version 不得被改写，实际 %d", after.TokenVersion)
	}
	if after.TotpSecretEnc != "" || after.TotpLastStep != 0 {
		t.Fatalf("二次验证凭据不得从通用编辑器写入，实际 %q / %d", after.TotpSecretEnc, after.TotpLastStep)
	}
	if after.LastLoginIP != "" || after.LastLoginAt != nil {
		t.Fatalf("异地登录判定基线不得被伪造，实际 %q", after.LastLoginIP)
	}

	// 留痕只记字段名，不记取值
	var logs []model.OperationLog
	repository.DB.Where("action = ?", "tech.db_update_users").Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("应留痕一条编辑记录，实际 %d 条", len(logs))
	}
	detail := logs[0].Detail
	if !strings.Contains(detail, "real_name") {
		t.Fatalf("留痕应列出实际写入的字段名，实际 %q", detail)
	}
	for _, owned := range userServerOwnedFields {
		if strings.Contains(detail, owned) {
			t.Fatalf("被剔除的字段不该出现在留痕里：%q 含 %q", detail, owned)
		}
	}
	for _, secretValue := range []string{"2020-01-01", "8.8.8.8", "副部长", "enc:v1:whatever", "13600000000"} {
		if strings.Contains(detail, secretValue) {
			t.Fatalf("留痕不得记录字段取值（%q 命中）", secretValue)
		}
	}
}
