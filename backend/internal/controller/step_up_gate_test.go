package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 强制改密的服务端落点：仍在使用初始口令的账号不得执行高危业务写操作。
// 这类账号输入出厂口令也能通过 bcrypt 比对，所以闸门必须走在口令核验之前，
// 而不是指望"二次确认"本身。

func stepUpContext(confirm string) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/deductions", nil)
	if confirm != "" {
		c.Request.Header.Set("X-Confirm-Password", confirm)
	}
	return c, rec
}

func seedStepUpUser(t *testing.T, username string, changed bool) model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(secTestPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("口令哈希失败: %v", err)
	}
	u := model.User{
		Username:     username,
		PasswordHash: string(hash),
		RealName:     "闸门测试账号",
		Role:         model.RoleMinister,
		Status:       "active",
		TokenVersion: 1,
	}
	if changed {
		now := time.Now()
		u.PasswordChangedAt = &now
	}
	if err := repository.DB.Create(&u).Error; err != nil {
		t.Fatalf("写入账号失败: %v", err)
	}
	return u
}

func TestRequireStepUpBlocksInitialPasswordAccounts(t *testing.T) {
	setupSecurityDB(t)

	legacy := seedStepUpUser(t, "minister_legacy", false)
	c, rec := stepUpContext(secTestPassword) // 初始口令本身是对的
	if requireStepUp(c, legacy) {
		t.Fatal("仍在使用初始口令的账号应被挡在高危写操作之外")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("应返回 403，实际 %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "password_change_required") {
		t.Fatalf("响应应带 password_change_required 便于前端引导改密，实际 %s", rec.Body.String())
	}
	// 未通过核验时不得留下"操作已执行"的错觉，也不该把口令回显在任何响应里
	if strings.Contains(rec.Body.String(), secTestPassword) {
		t.Fatal("响应正文不得包含口令")
	}

	// 账号自身的防护性操作不跟着一起锁死，否则存量账号无法自救
	hygieneCtx, _ := stepUpContext(secTestPassword)
	if !requireStepUpAccountHygiene(hygieneCtx, legacy) {
		t.Fatal("确认异地登录/解绑二次验证应允许初始口令账号使用")
	}
	wrongCtx, wrongRec := stepUpContext("not-the-password")
	if requireStepUpAccountHygiene(wrongCtx, legacy) {
		t.Fatal("核验口令错误时防护性操作同样应被拒绝")
	}
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("口令错误应 401，实际 %d", wrongRec.Code)
	}
}

func TestRequireStepUpPassesAfterPasswordChanged(t *testing.T) {
	setupSecurityDB(t)
	changed := seedStepUpUser(t, "minister_changed", true)

	missingCtx, missingRec := stepUpContext("")
	if requireStepUp(missingCtx, changed) {
		t.Fatal("缺少二次确认口令应被拒绝")
	}
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("缺少确认口令应 400，实际 %d", missingRec.Code)
	}

	badCtx, badRec := stepUpContext("wrong-password")
	if requireStepUp(badCtx, changed) {
		t.Fatal("确认口令错误应被拒绝")
	}
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("口令错误应 401，实际 %d", badRec.Code)
	}

	okCtx, okRec := stepUpContext(secTestPassword)
	if !requireStepUp(okCtx, changed) {
		t.Fatalf("已改过口令且核验通过应放行，实际 %d %s", okRec.Code, okRec.Body.String())
	}
}
