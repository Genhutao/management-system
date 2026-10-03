package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestStripPluginInternalHeader 这个头只能由宿主自己塞。
//
// 反例很清楚：如果外部请求能带着 `X-Plugin-Acted-For: plugin=whatever` 进来，
// "这条写请求是某个插件代出来的"就变成调用方可以随便声称的事，
// 而排查与审计正是靠这句话分辨"人直接打的"和"系统替他代打的"。
func TestStripPluginInternalHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var seen string
	r := gin.New()
	r.Use(StripPluginInternalHeader())
	r.GET("/probe", func(c *gin.Context) {
		seen = c.GetHeader(PluginActedForHeader)
		c.Status(http.StatusNoContent)
	})

	cases := []struct {
		name     string
		remote   string
		wantSeen string
	}{
		{"外部来源必须被剥掉", "203.0.113.9:55555", ""},
		{"本机回环保留（代执行那一跳就是它）", "127.0.0.1:55555", "plugin=demo action=demo_key"},
		{"IPv6 回环同样保留", "[::1]:55555", "plugin=demo action=demo_key"},
		{"外部 IPv6 同样剥掉", "[2001:db8::1]:55555", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			seen = ""
			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			req.RemoteAddr = tc.remote
			req.Header.Set(PluginActedForHeader, "plugin=demo action=demo_key")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if seen != tc.wantSeen {
				t.Errorf("来源 %s：期望头是 %q，实际 %q", tc.remote, tc.wantSeen, seen)
			}
		})
	}
}

// isLoopback 那层单独钉一下：RemoteAddr 在没有真实连接时可能是空串或畸形串，
// 这时**不能**当回环处理。
func TestIsLoopbackRejectsWeirdAddresses(t *testing.T) {
	want := map[string]bool{
		"":                 false, // 没有真实连接时不该被当成本机
		"not-an-ip:1":      false,
		"127.0.0.1":        true,
		"127.0.0.1:55555":  true,
		"[::1]":            true,
		"[::1]:80":         true,
		"::ffff:127.0.0.1": true, // 映射形态（Windows 上偶见）
		"203.0.113.9:80":   false,
		"10.0.0.5:80":      false, // 同网段另一台机器不是"本机"
		"172.16.0.9:80":    false,
	}
	for addr, expect := range want {
		if got := isLoopback(addr); got != expect {
			t.Errorf("isLoopback(%q) = %v，期望 %v", addr, got, expect)
		}
	}
}
