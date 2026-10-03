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
// 三条口径是这套设计的关键，改动前先读：
//
//  1. **清单可见性由 Casbin 推导**，描述符里没有也不该有 Roles 字段。
//     一个模块对某角色是否出现，取决于"该角色能不能读它每个组件的 data_endpoint"。
//     另存一份角色列表就是第二个真相，两处必然漂移，后果是入口出来了、点进去 403
//     （《API接口文档.md》§4.1 记着的那个 403 就是同一类问题）。
//
//  2. **组件类型是白名单，且必须与前端渲染器一一对应**。前端只实现它承诺过的类型，
//     遇到不认识的结构一律跳过并明写"需要更新界面版本"，绝不猜着渲染。
//     加新类型的正确顺序是：前端先有渲染器并部署，后端才开始注册用它。
//
//  3. **注册表从插件 P1 起支持运行期增删**：独立进程插件在界面授权时登记、撤销时移除。
//     M0 的不变量是"只在进程启动期写入，运行期只读"，现在正式放宽为"并发安全"，由
//     RWMutex 守，并有 -race 测试覆盖（这是对 M0 设计的一处修订，不是顺手改）。
//     编译期模块仍走 Register——描述符不合法就 panic，起不来比上线后静默少一个入口好；
//     运行期那条路走 TryRegister，把错误回给调用方，而不是让一次 HTTP 请求带崩进程。
package modules

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ManifestVersion 清单契约的版本。
//
// 抬版本的判断标准不是"加了字段"，而是**老界面遇到它会猜吗**：
//   - 只读展示类（stat / note / list / table）：老界面的渲染器查不到类型，
//     走的是已经验过的"需要更新界面版本"占位卡——明说、不猜，所以不抬；
//     而且 static/ 由服务端和后端包同一份出出去，不存在"后端先上线、前端还是旧的"那种错配。
//   - 会改学校数据的那一类（action）：**必须抬**。一个只会画 stat 的老界面若把它当普通按钮
//     画出来（不带二次确认、不摊开目标接口），比不画更危险，所以要让老界面明确拒渲染。
//
// 刻意不动 URL：本项目的 Casbin 策略按精确路径存在 casbin_rule 里且只增不改，
// 换前缀等于对每个已部署库做数据迁移，而"新增"本来也不需要抬版本。
//
// v2 = S2/S3 那一轮：多出 `action` 组件类型与模块级 `actions` 声明。
const ManifestVersion = 2

// 前端渲染器白名单。这里的每一项都必须在 app.js 的 EXT_RENDERERS 里真实存在，
// 否则清单就在承诺界面做不到的事。
//
// M2 加的三个（note / list / table）都是**只读展示**：它们不新增任何权限语义，
// 所以清单契约版本仍是 2——老界面遇到未知类型本来就会明写"需要更新界面版本"，
// 而这套 static/ 是服务端出发的同一份文件，不存在"后端先上线、前端还是旧的"那种错配。
// 真正需要抬版本的是 action（点一下会改学校数据），因为猜着渲染它才是事故。
const (
	WidgetStat   = "stat"   // 一个数或一句话
	WidgetNote   = "note"   // 一段说明文字，可以换行
	WidgetList   = "list"   // 若干行"事 / 数"
	WidgetTable  = "table"  // 固定表头的网格
	WidgetAction = "action" // 一次可发起的写动作（计划 §5/§6，配合模块级 Actions 声明）
)

var widgetTypes = map[string]bool{
	WidgetStat: true, WidgetNote: true, WidgetList: true, WidgetTable: true, WidgetAction: true,
}

var moduleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,39}$`)

// actionKeyPattern 动作与参数的键名规则。比模块 id 宽一点：允许下划线。
// 模块 id 会进 URL，动作键只进 DOM id 与插件 stdin 的 key 字段，而作者习惯用 snake_case
// 写事件名（submit_news）——硬逼他们改成连字符只会换来 submit-news 这种看着别扭的写法。
var actionKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,39}$`)

// 动作能用的方法。GET 明确不在里面：读走 values（组件数据），
// 允许"动作"是 GET 就等于允许插件用一次点击去拉一份不受清单约束的数据。
var actionMethods = map[string]bool{"POST": true, "PUT": true, "DELETE": true}

// 二次确认的取值。stepup = 宿主当场重验操作者登录口令（复用 requireStepUp 的限流与 bcrypt）。
// **插件自己声明 none 不代表它就能免口令**：命中服务端高危清单的路径由加载器在扫描期
// 改成 stepup（计划 §5.1），所以这里只是"作者怎么想"，最终值以存进 Module.Actions 的为准。
const (
	ActionConfirmNone   = "none"
	ActionConfirmStepUp = "stepup"
)

var actionConfirms = map[string]bool{ActionConfirmNone: true, ActionConfirmStepUp: true}

// 动作参数的上限。为什么要设：参数会原样进插件的 stdin 行、再进被代执行接口的请求体，
// 一个没上限的字段等于让外来清单决定"一次点击能往数据库里塞多大一段文本"。
const (
	MaxActionFields   = 6
	MaxActionFieldKey = 40
	MaxActionArgBytes = 4096
)

// ActionField 一次动作要收的参数。界面照它长输入框，宿主按它做白名单——
// 客户端多传一个没声明的键就整笔拒收，绝不"顺手也带给插件"。
type ActionField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Multi    bool   `json:"multiline,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"` // 0 按 MaxActionArgBytes
}

// Action 插件签名承诺会发起的一次写。Path/Method 是**授权界面与审计**要摊开给人看的内容，
// 不是内部实现细节：只读时代"这插件读什么"够用，可写时代必须写清"会改哪个接口"。
type Action struct {
	Key      string        `json:"key"`
	Label    string        `json:"label"`
	Method   string        `json:"method"`
	Path     string        `json:"path"`
	Confirm  string        `json:"confirm"` // 扫描期已按服务端高危清单归一化
	Reason   string        `json:"reason"`
	Fields   []ActionField `json:"fields,omitempty"`
	Endpoint string        `json:"action_endpoint"` // 前端实际 POST 的那个地址（由 id 推导）
}

// Widget 一个可渲染的组件。DataEndpoint 是客户端要 GET 的完整路径（含 /api/v1 前缀），
// 只放路径、不带查询串与占位符：清单可见性要靠它去问 Casbin，而 Casbin 匹配的正是 URL.Path。
type Widget struct {
	Key          string `json:"key"`
	Type         string `json:"type"`
	Label        string `json:"label"`
	DataEndpoint string `json:"data_endpoint"`
	// ActionKey 只对 type=="action" 有意义：这个按钮对应模块 Actions 里的哪一条。
	// 刻意只放键名、不放路径与方法——那些事实的唯一体面位置是 Actions，复制一份出去迟早漂移。
	ActionKey string `json:"action_key,omitempty"`
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
	// Actions 该模块声明过的写动作，**与 widgets 里 type=="action" 的那些一一对应**。
	// 编译期模块暂不声明（动作端点只为文件期插件挂，见 plugins 包）。
	Actions []Action `json:"actions,omitempty"`
}

// registry 模块注册表。启动期由编译期模块写入，运行期由插件授权/撤销增删，
// 因此所有读写都过 registryMu（见包注释第 3 条）。
var (
	registryMu sync.RWMutex
	registry   = map[string]Module{}
)

// TryRegister 登记一个模块，不合法或 id 撞车返回 error。
// 编译期模块别用它、用 Register：那种撞车是编程错误，宁可起不来。
// 它给运行期用（插件授权），同一个锁内完成校验与写入，不给"检查过了再去抢"留窗口。
func TryRegister(m Module) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := validate(m); err != nil {
		return err
	}
	if _, exists := registry[m.ID]; exists {
		// 措辞与 M0 的 panic 报文逐字一致：那条"重复注册不应覆盖已有条目"的回归断言
		// 认的就是这个串，改 Register 的内部实现不该让老断言白挂一次。
		return fmt.Errorf("模块 id 重复 '%s' —— 两个模块抢同一个入口，界面会随机显示其中一个", m.ID)
	}
	registry[m.ID] = m
	return nil
}

// Register 登记一个编译期模块。描述符不合法或 id 重复一律 panic：
// 这是编译期的编程错误，宁可起不来，也不要上线后某个入口静默消失
// （同 regexp.MustCompile、也同本项目"鉴权引擎未就绪就拒绝请求"的失败关闭口径）。
func Register(m Module) {
	if err := TryRegister(m); err != nil {
		panic("modules.Register: " + err.Error())
	}
}

// Replace 覆盖式登记**同一个 id** 的那一份（S5：改批准后要把在册的模块换成新的批准范围）。
//
// 为什么不复用 TryRegister：它撞车就报错，而改批准这件事本来就发生在"这个 id 已经在册"
// 的时候——用它的结果就是每一次改批准都失败在"模块 id 重复"上。
// 也不给编译期模块用：编译期不存在"运行期把自己换一份"的时刻，撞 id 仍然是编程错误，
// 该走 Register 那条 panic。
//
// 覆盖是原子的（同一个写锁内完成校验与写入），所以清单接口不会读到"摘掉了还没装上"
// 的空档；而能被覆盖的只有同一个 id，加载器在授权时已经把这个 id 撞车的可能排除掉了。
func Replace(m Module) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	if err := validate(m); err != nil {
		return err
	}
	registry[m.ID] = m
	return nil
}

// Unregister 移除一个模块（插件被撤销授权时调用）。返回是否真的移除过：
// 调用方据此判断"我以为下线了，其实注册表里从来没有它"这种状态错配。
func Unregister(id string) bool {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, ok := registry[id]; !ok {
		return false
	}
	delete(registry, id)
	return true
}

// Has id 是否已在注册表里。插件 loader 用它做"撞 id"判定：
// 编译期模块先占的名字，插件不能再占（清单里出现两个同名入口比直接报错更难查）。
func Has(id string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := registry[id]
	return ok
}

// Validate 只校验不登记。插件 loader 需要它：一份 manifest 在"待授权"阶段就该知道
// 合不合法（拿 TryRegister 去问，会把还没被任何人授权的插件真的注册进表里）。
// 校验规则只有这一份，插件和编译期模块共用——复制一套出去迟早和这边漂移。
func Validate(m Module) error { return validate(m) }

// Modules 返回按 id 排序的全部模块，顺序稳定：清单顺序变了会让人以为功能换了位置。
func Modules() []Module {
	registryMu.RLock()
	out := make([]Module, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	registryMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// resetRegistry 只给测试用：注册表是进程级状态，用例之间必须能清干净。
func resetRegistry() {
	registryMu.Lock()
	registry = map[string]Module{}
	registryMu.Unlock()
}

// canRead 由调用方注入（controller 侧接 middleware.Enforcer），
// 让本包不依赖 gin / Casbin，纯逻辑可测。
type canRead func(role, method, path string) bool

// ManifestForRole 该角色能看见的模块清单。
//
// 组件级规则（S2 之后）：
//   - stat 之类只读组件：**读不到数据端点就不列**（M1 起的原口径，一字未动）；
//   - action 组件：**操作者对目标接口没权就不列这个按钮**（计划 §10.1）。
//     这一条不是为了好看，是为了不出现"按钮看得见、点下去 403"——《API接口文档.md》§4.1
//     记过的那个坑；原生端没有我们的前端兜底，只会看到一个莫名其妙的 403。
//   - 任一声明的只读组件读不到 ⇒ 整个模块不列（原口径）；筛完一个组件都不剩 ⇒ 也不列，
//     留一块空面板比不给入口更像坏了。
//
// 注意这里筛的是"看不看得见"，不是"能不能干成"：真正的判定在代执行那一刻由 Casbin
// 用**操作者自己的会话**再做一遍。绕过界面直接 POST 动作端点的人，撞的是后面那道门。
func ManifestForRole(role string, allowed canRead) []Module {
	out := []Module{}
	if allowed == nil {
		return out // 判定器缺失就返回空清单：宁可不显示，也不能默认放行
	}
	for _, m := range Modules() {
		if !statsReadable(m, role, allowed) {
			continue
		}
		keep := make([]Widget, 0, len(m.Widgets))
		for _, w := range m.Widgets {
			if w.Type == WidgetAction {
				// 按钮只对"本来就能直接调那个接口"的人出现
				if a, ok := findAction(m, w.ActionKey); ok && allowed(role, a.Method, a.Path) {
					keep = append(keep, w)
				}
				continue
			}
			keep = append(keep, w)
		}
		if len(keep) == 0 {
			continue
		}
		m.Widgets = keep
		out = append(out, m)
	}
	return out
}

// statsReadable 该角色是否读得到这个模块的每一个只读组件。
func statsReadable(m Module, role string, allowed canRead) bool {
	for _, w := range m.Widgets {
		if w.Type == WidgetAction {
			continue
		}
		if !allowed(role, "GET", w.DataEndpoint) {
			return false
		}
	}
	return true
}

// findAction 按 key 取一条动作声明。找不到就不算可见——
// 一份自相矛盾的清单不该被渲染，更不该被猜出一个权限来。
func findAction(m Module, key string) (Action, bool) {
	for _, a := range m.Actions {
		if a.Key == key {
			return a, true
		}
	}
	return Action{}, false
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
		if w.Type == WidgetAction {
			if strings.TrimSpace(w.ActionKey) == "" {
				return fmt.Errorf("模块 %q 的动作组件 %q 没有 action_key：按钮不知道该发起哪条声明，等于画了个按不动的空壳", m.ID, w.Key)
			}
		} else if w.ActionKey != "" {
			return fmt.Errorf("模块 %q 的组件 %q 不是 action 类型却带了 action_key", m.ID, w.Key)
		}
	}
	if err := validateActions(m); err != nil {
		return err
	}
	return nil
}

// validateActions 校验动作声明本身，以及它和按钮之间的那层对应关系。
//
// 对应关系必须双向闭合，少一半都会出事故：
//   - 声明了却没按钮：这条能力在界面上按不出来，却仍然能被人直接 POST 那个动作端点打上——
//     "授权时看的是清单，跑起来却有一条看不见的通道"，正是这套信任门要消灭的东西；
//   - 按钮没有对应声明：前端拿不到 method/path/confirm，只能猜，而猜出来的按钮
//     可能把一条本该要口令的动作画成免口令直接提交。
//
// 路径能不能被声明（白名单、危险段）不在这里判，那是 plugins 包 action_scope.go 的事：
// 那份清单是插件准入口径，编译期模块根本不参与，放这儿只会逼本包反过来依赖 plugins。
func validateActions(m Module) error {
	byKey := map[string]Action{}
	for _, a := range m.Actions {
		if !actionKeyPattern.MatchString(a.Key) {
			return fmt.Errorf("模块 %q 的动作 key %q 不合法：只允许小写字母、数字、下划线与连字符，长度 2-40（前端要把它拼进按钮 id）", m.ID, a.Key)
		}
		if _, dup := byKey[a.Key]; dup {
			return fmt.Errorf("模块 %q 内动作 key 重复 %q：一次点击会对应到两条声明，代执行时不知道按哪条判权", m.ID, a.Key)
		}
		if strings.TrimSpace(a.Label) == "" {
			return fmt.Errorf("模块 %q 的动作 %q 没有标签：授权界面要给人看到「这按钮会干什么」，空标签等于让人盲签", m.ID, a.Key)
		}
		if strings.TrimSpace(a.Reason) == "" {
			return fmt.Errorf("模块 %q 的动作 %q 没有 reason：这句要原样进审计，空话的审计半年后没人还原得出为什么做过", m.ID, a.Key)
		}
		if !actionMethods[a.Method] {
			return fmt.Errorf("模块 %q 的动作 %q 用了方法 %q：只允许 POST/PUT/DELETE，读走组件数据不许当动作", m.ID, a.Key, a.Method)
		}
		if !actionConfirms[a.Confirm] {
			return fmt.Errorf("模块 %q 的动作 %q 的 confirm %q 不是取值之一（none/stepup）：二次确认的方式不许猜", m.ID, a.Key, a.Confirm)
		}
		if err := validateActionPath(a.Path); err != nil {
			return fmt.Errorf("模块 %q 的动作 %q: %w", m.ID, a.Key, err)
		}
		if len(a.Fields) > MaxActionFields {
			return fmt.Errorf("模块 %q 的动作 %q 声明了 %d 个参数，超过上限 %d：一次点击要填的东西越多，越说明它不是一个动作而是半张表单",
				m.ID, a.Key, len(a.Fields), MaxActionFields)
		}
		seenField := map[string]bool{}
		for _, f := range a.Fields {
			if len(f.Key) == 0 || len(f.Key) > MaxActionFieldKey {
				return fmt.Errorf("模块 %q 的动作 %q 有个参数 key 长度不合式（1-%d）: %q", m.ID, a.Key, MaxActionFieldKey, f.Key)
			}
			if !actionFieldKeyPattern.MatchString(f.Key) {
				return fmt.Errorf("模块 %q 的动作 %q 的参数 key %q 不合法：只允许小写字母、数字、下划线与连字符", m.ID, a.Key, f.Key)
			}
			if seenField[f.Key] {
				return fmt.Errorf("模块 %q 的动作 %q 参数 key 重复 %q", m.ID, a.Key, f.Key)
			}
			seenField[f.Key] = true
			if strings.TrimSpace(f.Label) == "" {
				return fmt.Errorf("模块 %q 的动作 %q 的参数 %q 没有标签：界面上会是一个不知道填什么的框", m.ID, a.Key, f.Key)
			}
			if f.MaxBytes < 0 || f.MaxBytes > MaxActionArgBytes {
				return fmt.Errorf("模块 %q 的动作 %q 的参数 %q 上限 %d 超出允许范围（0-%d，0 表示按默认）",
					m.ID, a.Key, f.Key, f.MaxBytes, MaxActionArgBytes)
			}
		}
		byKey[a.Key] = a
	}

	for _, w := range m.Widgets {
		if w.Type != WidgetAction {
			continue
		}
		if _, ok := byKey[w.ActionKey]; !ok {
			return fmt.Errorf("模块 %q 的按钮 %q 指向动作 %q，但清单里没有这条声明：按钮要发起什么没法核对", m.ID, w.Key, w.ActionKey)
		}
	}
	for _, a := range m.Actions {
		referenced := false
		for _, w := range m.Widgets {
			if w.Type == WidgetAction && w.ActionKey == a.Key {
				referenced = true
				break
			}
		}
		if !referenced {
			return fmt.Errorf("模块 %q 声明了动作 %q（%s %s）却没有任何按钮：这条能力按不出来，但仍能被人直接打到动作端点上——看不见的通道不进清单",
				m.ID, a.Key, a.Method, a.Path)
		}
	}
	return nil
}

var actionFieldKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// validateActionPath 动作路径的形状规则。能不能被声明（白名单）由 plugins 判，
// 这里只管"这个字符串有没有资格成为一个 URL"。
func validateActionPath(path string) error {
	if !strings.HasPrefix(path, "/api/v1/") {
		return fmt.Errorf("动作路径 %q 必须以 /api/v1/ 开头：代执行走的正是这套对外接口，别的路径没有鉴权语义", path)
	}
	if strings.ContainsAny(path, "?#") || strings.Contains(path, ":") {
		return fmt.Errorf("动作路径 %q 不合法：不许带查询串、锚点或路径参数（形如 :id），清单是静态签过名的，填不出具体编号", path)
	}
	if len(path) > 200 {
		return fmt.Errorf("动作路径 %q 过长", path)
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
