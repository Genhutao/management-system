package plugins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/modules"
)

// proxy 这一层唯一要钉死的事：**取不到就明说取不到**。
// 任何一种失败都必须回 200 + warn 值，既不能编一个 0，也不能用 500 把面板打死。

func ginContextFor(role string, userID uint) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, DataEndpoint("plug"), nil)
	if role != "" {
		c.Set("role", role)
	}
	c.Set("user_id", userID)
	return c, w
}

func decodePayload(t *testing.T, w *httptest.ResponseRecorder) modules.DataPayload {
	t.Helper()
	var p modules.DataPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("响应不是 DataPayload 形状: %v / body=%s", err, w.Body.String())
	}
	return p
}

func writeScannedPlugin(t *testing.T, mode string) (*Plugin, string) {
	t.Helper()
	root := t.TempDir()
	id := "plug"
	m := validManifest(id)
	m.ID = id
	m.Tab = id
	dir := writePlugin(t, root, id, m, "占位：未授权的插件不会被起起来")
	got := Scan(root, testAnchors())
	if len(got) != 1 || got[0].State != StatePending {
		t.Fatalf("夹具没造对: %+v", got)
	}
	return got[0], dir
}

func TestProxyUntrustedPluginNeverRuns(t *testing.T) {
	p, _ := writeScannedPlugin(t, "normal")
	m := NewManagerWithAnchors(filepath.Dir(p.Dir), nil, testAnchors())
	m.entries[p.Module.ID] = &entry{plugin: p}

	c, w := ginContextFor("tech_admin", 3)
	m.HandleData(p.Module.ID)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("未授权的直接请求该回 200+warn，实际 %d: %s", w.Code, w.Body.String())
	}
	payload := decodePayload(t, w)
	v, ok := payload.Values["data_disk"]
	if !ok {
		t.Fatalf("该把请求的键都填上 warn 值，实际 %v", payload.Values)
	}
	if v.Text != "读取失败" || v.Level != "warn" {
		t.Errorf("失败值形状不对: %+v", v)
	}
	if !strings.Contains(v.Hint, "未授权") {
		t.Errorf("原因要说什么叫没授权，实际 %q", v.Hint)
	}
	// 关键反向断言：没授权 = 没进程 = 注册表里也没有它
	if modules.Has(p.Module.ID) {
		t.Error("读了一下数据端点，插件就进注册表了 —— 信任门被绕开")
	}
	if p.State != StatePending {
		t.Errorf("状态被读操作改掉了: %q", p.State)
	}
}

func TestProxyRunningPluginReturnsRealValues(t *testing.T) {
	p, _ := writeScannedPlugin(t, "normal")
	proc := stubProcess(t, "normal")
	if err := proc.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	if err := proc.Ping(3 * time.Second); err != nil {
		t.Fatalf("ping 失败: %v", err)
	}
	m := NewManagerWithAnchors(filepath.Dir(p.Dir), nil, testAnchors())
	m.entries[p.Module.ID] = &entry{plugin: p, trusted: true, proc: proc}

	c, w := ginContextFor("tech_admin", 9)
	m.HandleData(p.Module.ID)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("应回 200，实际 %d", w.Code)
	}
	payload := decodePayload(t, w)
	v := payload.Values["data_disk"]
	if v.Text != "48% 已用" {
		t.Errorf("没拿到桩回的真值: %+v", v)
	}
	if !strings.Contains(v.Hint, "user=9") {
		t.Errorf("当前账号没传给插件: %q", v.Hint)
	}
}

// 插件自报"我算不出来"时，界面上必须是一眼能看懂的 warn 卡，
// 而不是一个空面板或一个 500。
func TestProxySelfReportedFailureBecomesWarnCard(t *testing.T) {
	p, _ := writeScannedPlugin(t, "normal")
	proc := stubProcess(t, "selferr")
	if err := proc.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	m := NewManagerWithAnchors(filepath.Dir(p.Dir), nil, testAnchors())
	m.entries[p.Module.ID] = &entry{plugin: p, trusted: true, proc: proc}

	c, w := ginContextFor("tech_admin", 1)
	m.HandleData(p.Module.ID)(c)

	payload := decodePayload(t, w)
	v := payload.Values["data_disk"]
	if v.Text != "读取失败" || v.Level != "warn" {
		t.Fatalf("自报失败要出警示值，实际 %+v", v)
	}
	if !strings.Contains(v.Hint, "读不到传感器") {
		t.Errorf("插件自己写的原因要带给界面，实际 %q", v.Hint)
	}
	if strings.Contains(v.Hint, "\n") {
		t.Errorf("原因被压成一行之前不该带换行: %q", v.Hint)
	}
}

// 进程已放弃自动重启：那句话必须说清"要重新授权"，否则运维只会反复点刷新。
func TestProxyGaveUpExplainsNextStep(t *testing.T) {
	p, _ := writeScannedPlugin(t, "normal")
	m := NewManagerWithAnchors(filepath.Dir(p.Dir), nil, testAnchors())
	proc := newProcess(p)
	proc.gaveUp = true
	m.entries[p.Module.ID] = &entry{plugin: p, trusted: true, proc: proc}

	c, w := ginContextFor("tech_admin", 1)
	m.HandleData(p.Module.ID)(c)

	v := decodePayload(t, w).Values["data_disk"]
	if !strings.Contains(v.Hint, "重新授权") {
		t.Errorf("放弃状态下该行该说下一步做什么，实际 %q", v.Hint)
	}
}

func TestProxyUnknownPluginIDHasNoWidgets(t *testing.T) {
	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	c, w := ginContextFor("tech_admin", 1)
	m.HandleData("not-a-plugin")(c)
	if w.Code != http.StatusNotFound {
		t.Errorf("不是插件的 id 不该假装有一块面板: %d", w.Code)
	}
}

// 校验失败的目录同样不能因为一次请求就被跑起来。
func TestProxyInvalidPluginSaysWhy(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"broken",`), 0o644); err != nil {
		t.Fatal(err)
	}
	scanned := Scan(root, testAnchors())
	if len(scanned) != 1 || scanned[0].State != StateInvalid {
		t.Fatalf("夹具应是校验失败: %+v", scanned)
	}
	m := NewManagerWithAnchors(root, nil, testAnchors())
	m.entries[lookupKey(scanned[0])] = &entry{plugin: scanned[0]}
	// 校验失败的插件路由根本不会注册（main.go 只给合规模的 id 挂路由），
	// 这里直接调 absentReason 问那句话本身。
	if reason := m.absentReason(lookupKey(scanned[0])); !strings.Contains(reason, "校验失败") {
		t.Errorf("实际: %q", reason)
	}
}
