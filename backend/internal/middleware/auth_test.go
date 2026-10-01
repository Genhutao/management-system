package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/jwt"
)

// TestMain 在首个令牌操作之前把密钥固定在环境变量里，
// 避免测试进程把随机密钥落盘成 jwt_secret.key / crypto_secret.key。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Setenv("JWT_SECRET", "unit-test-jwt-secret")
	os.Setenv("CRYPTO_SECRET", "unit-test-crypto-secret")
	if _, err := repository.InitDB("file:auth_mw_test?mode=memory&cache=shared"); err != nil {
		fmt.Printf("测试库初始化失败: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func newTestUser(t *testing.T, name string) *model.User {
	t.Helper()
	u := model.User{
		Username:     name,
		RealName:     "测试部员",
		Role:         model.RoleMember,
		Department:   "纪检部",
		Status:       "active",
		TokenVersion: 1,
	}
	if err := repository.DB.Create(&u).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}
	return &u
}

func issueToken(t *testing.T, u *model.User) string {
	t.Helper()
	token, err := jwt.GenerateTokenWithVersion(u.ID, u.Username, u.RealName, u.Role, u.Building, u.Floor, u.TokenVersion)
	if err != nil {
		t.Fatalf("签发令牌失败: %v", err)
	}
	return token
}

func registerSession(t *testing.T, u *model.User, token string, revoked bool) *model.UserSession {
	t.Helper()
	claims, err := jwt.ParseToken(token)
	if err != nil {
		t.Fatalf("解析令牌失败: %v", err)
	}
	if claims.ID == "" {
		t.Fatal("令牌没有 jti，会话校验无从进行")
	}
	now := time.Now()
	sess := model.UserSession{
		UserID:     u.ID,
		Jti:        claims.ID,
		LoginIP:    "10.0.0.1",
		UserAgent:  "unit-test",
		LoginAt:    now,
		LastSeenAt: now,
		EnvStatus:  "known",
	}
	if revoked {
		sess.RevokedAt = &now
	}
	if err := repository.DB.Create(&sess).Error; err != nil {
		t.Fatalf("登记会话失败: %v", err)
	}
	return &sess
}

func authTestRouter() *gin.Engine {
	r := gin.New()
	r.Use(AuthMiddleware())
	r.GET("/api/v1/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "user_id": c.GetUint("user_id")})
	})
	return r
}

// doAuthGet 分别走 Cookie 与 Bearer 两条凭据通道
func doAuthGet(t *testing.T, r *gin.Engine, token string, useHeader bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	if useHeader {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if token != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func responseCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Code
}

func TestAuthMiddlewareAcceptsCookieAndBearer(t *testing.T) {
	r := authTestRouter()
	for _, useHeader := range []bool{false, true} {
		u := newTestUser(t, fmt.Sprintf("mw_ok_%v", useHeader))
		token := issueToken(t, u)
		registerSession(t, u, token, false)

		rec := doAuthGet(t, r, token, useHeader)
		if rec.Code != http.StatusOK {
			t.Fatalf("凭据通道 useHeader=%v 期望 200，实际 %d: %s", useHeader, rec.Code, rec.Body.String())
		}
	}
}

func TestAuthMiddlewareErrorCodes(t *testing.T) {
	r := authTestRouter()

	t.Run("无凭据", func(t *testing.T) {
		rec := doAuthGet(t, r, "", false)
		if rec.Code != http.StatusUnauthorized || responseCode(t, rec) != CodeSessionMissing {
			t.Fatalf("期望 401 %s，实际 %d %s", CodeSessionMissing, rec.Code, rec.Body.String())
		}
	})

	t.Run("令牌无效", func(t *testing.T) {
		rec := doAuthGet(t, r, "not-a-valid-jwt", false)
		if rec.Code != http.StatusUnauthorized || responseCode(t, rec) != CodeSessionExpired {
			t.Fatalf("期望 401 %s，实际 %d %s", CodeSessionExpired, rec.Code, rec.Body.String())
		}
	})

	t.Run("会话未登记", func(t *testing.T) {
		u := newTestUser(t, "mw_unregistered")
		rec := doAuthGet(t, r, issueToken(t, u), false)
		if rec.Code != http.StatusUnauthorized || responseCode(t, rec) != CodeSessionRevoked {
			t.Fatalf("期望 401 %s，实际 %d %s", CodeSessionRevoked, rec.Code, rec.Body.String())
		}
	})

	t.Run("会话已吊销", func(t *testing.T) {
		u := newTestUser(t, "mw_revoked")
		token := issueToken(t, u)
		registerSession(t, u, token, true)
		rec := doAuthGet(t, r, token, false)
		if rec.Code != http.StatusUnauthorized || responseCode(t, rec) != CodeSessionRevoked {
			t.Fatalf("期望 401 %s，实际 %d %s", CodeSessionRevoked, rec.Code, rec.Body.String())
		}
	})

	t.Run("令牌版本落后", func(t *testing.T) {
		u := newTestUser(t, "mw_stale_tv")
		token := issueToken(t, u)
		registerSession(t, u, token, false)
		if err := repository.DB.Model(u).Update("token_version", u.TokenVersion+1).Error; err != nil {
			t.Fatalf("bump token_version 失败: %v", err)
		}
		rec := doAuthGet(t, r, token, false)
		if rec.Code != http.StatusUnauthorized || responseCode(t, rec) != CodeSessionRevoked {
			t.Fatalf("期望 401 %s，实际 %d %s", CodeSessionRevoked, rec.Code, rec.Body.String())
		}
	})

	t.Run("账号已停用", func(t *testing.T) {
		u := newTestUser(t, "mw_disabled")
		token := issueToken(t, u)
		registerSession(t, u, token, false)
		if err := repository.DB.Model(u).Update("status", "disabled").Error; err != nil {
			t.Fatalf("停用账号失败: %v", err)
		}
		rec := doAuthGet(t, r, token, false)
		if rec.Code != http.StatusUnauthorized || responseCode(t, rec) != CodeAccountDisabled {
			t.Fatalf("期望 401 %s，实际 %d %s", CodeAccountDisabled, rec.Code, rec.Body.String())
		}
	})
}

func TestSessionCookieSecureFlag(t *testing.T) {
	build := func() string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		SetSessionCookie(c, "token-value")
		rec := c.Writer
		return rec.Header().Get("Set-Cookie")
	}

	t.Setenv("COOKIE_SECURE", "")
	if got := build(); got == "" {
		t.Fatal("未下发会话 Cookie")
	} else if strings.Contains(got, "Secure") {
		t.Fatalf("明文 HTTP 且未开启 COOKIE_SECURE 时不应带 Secure: %s", got)
	}

	t.Setenv("COOKIE_SECURE", "1")
	if got := build(); !strings.Contains(got, "Secure") {
		t.Fatalf("COOKIE_SECURE=1 时应带 Secure: %s", got)
	}
	if got := build(); !strings.Contains(got, "HttpOnly") {
		t.Fatalf("会话 Cookie 必须是 HttpOnly: %s", got)
	}
}
