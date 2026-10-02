// Package modules 是"后端加模块、前端不改动"的注册表。
//
// 加一个模块要做的两件事（Go 没有"扫目录自动发现包"，init() 只在包被 import 时跑，
// 所以这一行接线省不掉）：
//
//	backend/internal/modules/<模块名>/module.go    // 描述符 + 处理器，init() 里 Register(...)
//	backend/cmd/server/main.go                     // 加一行路由注册（这行 import 同时让 init() 生效）
//	—— 若模块**不新增路由**（只把已有接口拼成清单），才需要 internal/modules/modules.go
//	   那个汇总文件里的一行 blank import；带处理器的模块不需要，路由那行已经把它拉进来了。
//
// 两条口径是这套设计的关键，改动前先读：
//
//  1. **清单可见性由 Casbin 推导**，描述符里没有也不该有 Roles 字段。
//     一个模块对某角色是否出现，取决于"该角色能不能读它每个组件的 data_endpoint"。
//     另存一份角色列表就是第二个真相，两处必然漂移，后果是入口出来了、点进去 403
//     （《API接口文档.md》§4.1 记着的那个 403 就是同一类问题）。
//
//  2. **组件类型是白名单，且必须与前端渲染器一一对应**。前端只实现它承诺过的类型，
//     遇到不认识的结构一律跳过并明写"需要更新界面版本"，绝不猜着渲染。
//     加新类型的正确顺序是：前端先有渲染器并部署，后端才开始注册用它。
package modules

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ManifestVersion 清单契约的版本。加字段（含新组件类型）就 +1。
// 刻意不动 URL：本项目的 Casbin 策略按精确路径存在 casbin_rule 里且只增不改，
// 换前缀等于对每个已部署库做数据迁移，而"新增"本来也不需要抬版本。
const ManifestVersion = 1

// 前端渲染器白名单（目前只有 stat；list / note 计划在 M2 与渲染器一起加）。
// 这里的每一项都必须在 app.js 的 EXT_RENDERERS 里真实存在，否则清单就在承诺界面做不到的事。
const (
	WidgetStat = "stat"
)

var widgetTypes = map[string]bool{WidgetStat: true}

var moduleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)

// Widget 一个可渲染的组件。DataEndpoint 是客户端要 GET 的完整路径（含 /api/v1 前缀），
// 只放路径、不带查询串与占位符：清单可见性要靠它去问 Casbin，而 Casbin 匹配的正是 URL.Path。
type Widget struct {
	Key          string `json:"key"`
	Type         string `json:"type"`
	Label        string `json:"label"`
	DataEndpoint string `json:"data_endpoint"`
}

// Module 一个模块 = 侧栏一个入口 + 面板里的若干组件。
type Module struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Icon               string   `json:"icon"`
	Group              string   `json:"group"`
	Tab                string   `json:"tab"`
	MinManifestVersion int      `json:"min_manifest_version"`
	Widgets            []Widget `json:"widgets"`
}

// registry 模块注册表。只在进程启动期写入，运行期只读。
var registry = map[string]Module{}

// Register 登记一个模块。描述符不合法或 id 重复一律 panic：
// 这是编译期的编程错误，宁可起不来，也不要上线后某个入口静默消失
// （同 regexp.MustCompile、也同本项目"鉴权引擎未就绪就拒绝请求"的失败关闭口径）。
func Register(m Module) {
	if err := validate(m); err != nil {
		panic("modules.Register: " + err.Error())
	}
	if _, exists := registry[m.ID]; exists {
		panic("modules.Register: 模块 id 重复 '" + m.ID + "' —— 两个模块抢同一个入口，界面会随机显示其中一个")
	}
	registry[m.ID] = m
}

// Modules 返回按 id 排序的全部模块，顺序稳定：清单顺序变了会让人以为功能换了位置。
func Modules() []Module {
	out := make([]Module, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// resetRegistry 只给测试用：注册表是进程级状态，用例之间必须能清干净。
func resetRegistry() { registry = map[string]Module{} }

// canRead 由调用方注入（controller 侧接 middleware.Enforcer），
// 让本包不依赖 gin / Casbin，纯逻辑可测。
type canRead func(role, method, path string) bool

// ManifestForRole 该角色能看见的模块清单。
// 规则：模块内**任一**组件读不到，整个模块都不列出来。
// 只筛掉个别组件会留下一块半空面板，看起来像坏了，比不给入口更糟。
func ManifestForRole(role string, allowed canRead) []Module {
	out := []Module{}
	if allowed == nil {
		return out // 判定器缺失就返回空清单：宁可不显示，也不能默认放行
	}
	for _, m := range Modules() {
		visible := len(m.Widgets) > 0
		for _, w := range m.Widgets {
			if !allowed(role, "GET", w.DataEndpoint) {
				visible = false
				break
			}
		}
		if visible {
			out = append(out, m)
		}
	}
	return out
}

func validate(m Module) error {
	if !moduleIDPattern.MatchString(m.ID) {
		return fmt.Errorf("模块 id %q 不合法：只允许小写字母、数字与连字符，长度 2-40", m.ID)
	}
	if strings.TrimSpace(m.Title) == "" {
		return fmt.Errorf("模块 %q 没有标题，侧栏会是一个认不出来的图标", m.ID)
	}
	if strings.TrimSpace(m.Tab) == "" {
		return fmt.Errorf("模块 %q 没有 tab，清单给不出可打开的面板名", m.ID)
	}
	// tab 与 id 同一套规则：前端要把它拼进 DOM id 与 onclick 属性，
	// 放一个带引号、尖括号或空格的名字进去，等于让后端清单能往页面里塞脚本。
	if !moduleIDPattern.MatchString(m.Tab) {
		return fmt.Errorf("模块 %q 的 tab %q 不合法：只允许小写字母、数字与连字符，长度 2-40（前端按它生成 panel-<tab> 这个 id）", m.ID, m.Tab)
	}
	if m.MinManifestVersion != ManifestVersion {
		return fmt.Errorf("模块 %q 声明 manifest_version=%d，当前契约是 %d", m.ID, m.MinManifestVersion, ManifestVersion)
	}
	if len(m.Widgets) == 0 {
		return fmt.Errorf("模块 %q 一个组件都没有：清单里有入口、点进去什么都没有，属于骗人", m.ID)
	}
	seen := map[string]bool{}
	for _, w := range m.Widgets {
		if strings.TrimSpace(w.Key) == "" {
			return fmt.Errorf("模块 %q 有组件缺少 key", m.ID)
		}
		if seen[w.Key] {
			return fmt.Errorf("模块 %q 内组件 key 重复 %q", m.ID, w.Key)
		}
		seen[w.Key] = true
		if !widgetTypes[w.Type] {
			return fmt.Errorf("模块 %q 的组件 %q 用了未登记类型 %q：前端没有对应渲染器，清单不能承诺界面做不到的事", m.ID, w.Key, w.Type)
		}
		if strings.TrimSpace(w.Label) == "" {
			return fmt.Errorf("模块 %q 的组件 %q 没有标签", m.ID, w.Key)
		}
		if err := validateEndpoint(w.DataEndpoint); err != nil {
			return fmt.Errorf("模块 %q 的组件 %q: %w", m.ID, w.Key, err)
		}
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	if !strings.HasPrefix(endpoint, "/api/v1/") {
		return fmt.Errorf("data_endpoint %q 必须以 /api/v1/ 开头（清单可见性要靠它问 Casbin）", endpoint)
	}
	if strings.ContainsAny(endpoint, "?#") {
		return fmt.Errorf("data_endpoint %q 不得带查询串或锚点：Casbin 匹配的是 URL.Path，带串会让判定与实际请求不一致", endpoint)
	}
	if strings.Contains(endpoint, ":") {
		return fmt.Errorf("data_endpoint %q 不得含路径参数（形如 :id）：清单是静态下发的，填不了具体编号", endpoint)
	}
	return nil
}
