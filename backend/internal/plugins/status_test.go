package plugins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
)

// 描述符必须真的在注册表里：init() 没跑（包没被 import）时，界面就少一个入口，
// 而这种事在测试里不钉一下，只有上线后 tech_admin 问"加载器页面呢"才会发现。
func TestPluginStatusDescriptorRegistered(t *testing.T) {
	if !modules.Has("pluginstatus") {
		t.Fatal("pluginstatus 没登记进注册表")
	}
	var found modules.Module
	for _, m := range modules.Modules() {
		if m.ID == "pluginstatus" {
			found = m
		}
	}
	if len(found.Widgets) != 4 {
		t.Fatalf("四个数一张卡，实际 %d 张：%+v", len(found.Widgets), found.Widgets)
	}
	if err := modules.Validate(found); err != nil {
		t.Errorf("描述符没过统一校验: %v", err)
	}
	for _, w := range found.Widgets {
		if w.DataEndpoint != StatusDataEndpoint {
			t.Errorf("组件 %s 的数据端口不是本模块那一个: %s", w.Key, w.DataEndpoint)
		}
	}
}

func statusPayload(t *testing.T, m *Manager) modules.DataPayload {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, StatusDataEndpoint, nil)
	m.HandleStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("应回 200，实际 %d", w.Code)
	}
	var p modules.DataPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("响应不是 DataPayload: %v", err)
	}
	return p
}

func statusTestPlugin(id string, st State) *Plugin {
	p := &Plugin{
		Dir: id,
		Module: modules.Module{
			ID: id, Title: id, Tab: id, MinManifestVersion: modules.ManifestVersion,
			Widgets: []modules.Widget{{Key: "data_disk", Type: modules.WidgetStat, Label: "数据盘", DataEndpoint: DataEndpoint(id)}},
		},
		State:  st,
		Reason: "夹具",
	}
	p.Manifest.Title = id
	return p
}

// 卡片上的数字要与现实一致（验收⑦）。这里刻意把"已授权但没进程"和"待授权"混在一起，
// 因为最容易骗人的正是这两张卡。
func TestPluginStatusCardsMatchReality(t *testing.T) {
	// 活着的这一个用真桩进程：id 以 stub- 开头，子进程从 XGH_PLUGIN_ID 认出自己该演什么
	// （见 TestMain），不需要为测试多给一个环境变量。
	livePlugin := statusTestPlugin("stub-live", StatePending)
	livePlugin.ExecPath = os.Args[0]
	live := newProcess(livePlugin)
	live.dir = t.TempDir()
	if err := live.start(); err != nil {
		t.Fatalf("起桩进程失败: %v", err)
	}
	t.Cleanup(live.stop)
	if live.State() != StateRunning {
		t.Fatalf("夹具进程应是活的，实际 %q", live.State())
	}

	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	m.entries = map[string]*entry{
		"stub-live":    {plugin: livePlugin, trusted: true, proc: live},
		"plug-idle":    {plugin: statusTestPlugin("plug-idle", StatePending), trusted: true},
		"plug-pending": {plugin: statusTestPlugin("plug-pending", StatePending)},
		"plug-bad":     {plugin: statusTestPlugin("plug-bad", StateInvalid)},
	}

	p := statusPayload(t, m)
	if got := p.Values[widgetLoaded].Text; got != "2 个" {
		t.Errorf("已授权已登记应是 2（一个进程活着、一个在退避），实际 %q", got)
	}
	if got := p.Values[widgetRunning].Text; got != "1 个" {
		t.Errorf("运行中应只数活着的，实际 %q", got)
	}
	if got := p.Values[widgetPending].Text; got != "1 个" {
		t.Errorf("待授权应只数没授权的，实际 %q", got)
	}
	if got := p.Values[widgetAttention].Text; got != "1 个" {
		t.Errorf("要处理的应含校验失败那一个，实际 %q", got)
	}
	// 已授权却有一个进程没在跑：运行中那张卡必须亮 warn，并把下一步说出来
	if p.Values[widgetRunning].Level != "warn" || !strings.Contains(p.Values[widgetRunning].Hint, "管理卡") {
		t.Errorf("运行中卡片没提示该去看一眼: %+v", p.Values[widgetRunning])
	}
	if p.Values[widgetAttention].Level != "warn" {
		t.Errorf("要处理的卡片该是琥珀色: %+v", p.Values[widgetAttention])
	}
	// 待授权那张要把等授权的 id 列出来：tech_admin 看到"1 个"之后第一个问题就是"哪个"
	if !strings.Contains(p.Values[widgetPending].Hint, "plug-pending") {
		t.Errorf("待授权卡没说是哪个: %+v", p.Values[widgetPending])
	}
}

// 一切正常时不该有任何一张卡亮着 warn 骗人去看。
func TestPluginStatusAllQuietIsNotWarn(t *testing.T) {
	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	p := statusPayload(t, m)
	for key, v := range p.Values {
		if v.Level == "warn" {
			t.Errorf("空系统上 %s 不该亮 warn: %+v", key, v)
		}
		if v.Text != "0 个" {
			t.Errorf("%s 该老实说 0 个，实际 %q", key, v.Text)
		}
	}
}

// 可见性仍然是推出来的：只有 tech_admin 的清单里会出现这个模块。
// 这条是 §11"清单里没有就是没有"在加载器自己身上的一次回头验。
func TestPluginStatusVisibleOnlyToTechAdmin(t *testing.T) {
	_, enf := setupPolicyEnforcer(t)
	canRead := roleCanRead(enf)

	visible := map[string]bool{}
	for _, role := range roleOrder {
		for _, m := range modules.ManifestForRole(role, canRead) {
			if m.ID == "pluginstatus" {
				visible[role] = true
			}
		}
	}
	if !visible[model.RoleTechAdmin] {
		t.Error("技术维护组看不见加载器模块")
	}
	for _, role := range []string{model.RoleDormManager, model.RoleMember, model.RoleMinister, model.RoleViewerExport} {
		if visible[role] {
			t.Errorf("%s 的清单里出现了加载器模块（它读不到 /api/v1/mod/*，不该出现）", role)
		}
	}
}
