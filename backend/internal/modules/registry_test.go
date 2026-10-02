package modules

import (
	"strings"
	"testing"
)

func goodModule(id, endpoint string) Module {
	return Module{
		ID: id, Title: "测试模块", Icon: "fa-solid fa-flask", Group: "测试",
		Tab: id, MinManifestVersion: ManifestVersion,
		Widgets: []Widget{{Key: "core", Type: WidgetStat, Label: "计数", DataEndpoint: endpoint}},
	}
}

func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		got := recover()
		if got == nil {
			t.Fatalf("期望 panic 含 %q，实际没有 panic", want)
		}
		if !strings.Contains(got.(string), want) {
			t.Fatalf("panic 报文不符：期望包含 %q，实际 %q", want, got)
		}
	}()
	fn()
}

// Register 对不合法描述符必须当场炸：清单承诺了界面做不到的事，
// 只会表现为用户看到一个空白或错误的入口，而线上没有任何人会去查注册日志。
func TestRegisterRejectsBadDescriptors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(m *Module)
		want string
	}{
		{"id 为空", func(m *Module) { m.ID = "" }, "不合法"},
		{"id 有大写", func(m *Module) { m.ID = "DormCount" }, "不合法"},
		{"id 过长", func(m *Module) { m.ID = strings.Repeat("a", 45) }, "不合法"},
		{"缺标题", func(m *Module) { m.Title = "  " }, "没有标题"},
		{"缺 tab", func(m *Module) { m.Tab = "" }, "没有 tab"},
		// tab 会被前端拼进 DOM id 与 onclick 属性，规则必须和 id 一样严
		{"tab 含空格", func(m *Module) { m.Tab = "ok module" }, "不合法"},
		{"tab 含引号与尖括号", func(m *Module) { m.Tab = `a" onclick="alert(` }, "不合法"},
		{"tab 大写", func(m *Module) { m.Tab = "OkModule" }, "不合法"},
		{"清单版本不符", func(m *Module) { m.MinManifestVersion = ManifestVersion + 1 }, "manifest_version"},
		{"没有组件", func(m *Module) { m.Widgets = nil }, "一个组件都没有"},
		{"未知组件类型", func(m *Module) { m.Widgets[0].Type = "chart" }, "未登记类型"},
		{"组件缺 key", func(m *Module) { m.Widgets[0].Key = "" }, "缺少 key"},
		{"组件缺标签", func(m *Module) { m.Widgets[0].Label = "" }, "没有标签"},
		{"数据端口不在 v1 前缀下", func(m *Module) { m.Widgets[0].DataEndpoint = "/ext/count" }, "必须以 /api/v1/ 开头"},
		{"数据端口带查询串", func(m *Module) { m.Widgets[0].DataEndpoint = "/api/v1/dashboard/summary?x=1" }, "不得带查询串"},
		{"数据端口带路径参数", func(m *Module) { m.Widgets[0].DataEndpoint = "/api/v1/dorm/inspections/:id" }, "不得含路径参数"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetRegistry()
			m := goodModule("ok-module", "/api/v1/dashboard/summary")
			tc.mut(&m)
			mustPanic(t, tc.want, func() { Register(m) })
			if len(registry) != 0 {
				t.Errorf("被拒绝的描述符不应进表，实际表里有 %d 项", len(registry))
			}
		})
	}
}

func TestRegisterRejectsDuplicateID(t *testing.T) {
	resetRegistry()
	Register(goodModule("dup", "/api/v1/dashboard/summary"))
	mustPanic(t, "模块 id 重复", func() {
		Register(goodModule("dup", "/api/v1/messages/unread-count"))
	})
	// 原条目不能被覆盖：两个模块抢同一个入口时，后注册的悄悄顶掉前一个最难查
	if got := registry["dup"].Widgets[0].DataEndpoint; got != "/api/v1/dashboard/summary" {
		t.Errorf("重复注册不应改动已有条目，实际端点变成 %q", got)
	}
}

// 可见性由注入的判定函数决定：任一组件读不到就整个模块不列（宁缺勿滥，不给半空面板）。
func TestManifestForRoleHidesWholeModule(t *testing.T) {
	resetRegistry()
	Register(Module{
		ID: "two-widget", Title: "两个组件", Tab: "two-widget", MinManifestVersion: ManifestVersion,
		Widgets: []Widget{
			{Key: "a", Type: WidgetStat, Label: "甲", DataEndpoint: "/api/v1/dashboard/summary"},
			{Key: "b", Type: WidgetStat, Label: "乙", DataEndpoint: "/api/v1/tech/db/tables"},
		},
	})
	Register(goodModule("one-widget", "/api/v1/dashboard/summary"))

	allowed := func(role, method, path string) bool {
		if method != "GET" {
			return false
		}
		// 模拟真实 Casbin：技术组什么都能读，部员读不到 tech 段
		return role == "tech_admin" || !strings.HasPrefix(path, "/api/v1/tech")
	}

	tech := ManifestForRole("tech_admin", allowed)
	if len(tech) != 2 {
		t.Fatalf("技术维护组应看到 2 个模块，实际 %d: %+v", len(tech), tech)
	}
	member := ManifestForRole("member", allowed)
	if len(member) != 1 || member[0].ID != "one-widget" {
		t.Fatalf("部员应只看到不被 tech 组件拖累的那个模块，实际 %+v", member)
	}
	// 关键反向断言：清单里出现的模块，其每个组件都必须对该角色可读
	for _, m := range member {
		for _, w := range m.Widgets {
			if !allowed("member", "GET", w.DataEndpoint) {
				t.Errorf("清单泄漏了 %s 读不到的组件：%s -> %s", "member", m.ID, w.DataEndpoint)
			}
		}
	}

	// 判定器缺失时返回空清单，而不是全量放行
	if got := ManifestForRole("member", nil); len(got) != 0 {
		t.Errorf("allowed 为 nil 应返回空清单，实际 %d 项", len(got))
	}
	// 空表也必须是 []（非 nil），前端 .length 不会崩
	resetRegistry()
	if out := ManifestForRole("member", allowed); out == nil {
		t.Error("无模块时应返回空切片而不是 nil")
	}
}

func TestModulesSortedByID(t *testing.T) {
	resetRegistry()
	Register(goodModule("zeta", "/api/v1/dashboard/summary"))
	Register(goodModule("alpha", "/api/v1/dashboard/summary"))
	Register(goodModule("mid", "/api/v1/dashboard/summary"))
	list := Modules()
	var ids []string
	for _, m := range list {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "alpha,mid,zeta" {
		t.Errorf("清单顺序应稳定按 id 升序，实际 %v", ids)
	}
}
