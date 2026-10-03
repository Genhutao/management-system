// Package plugins 是文件期·独立进程插件的加载器
// （方案：仓库根《学管会系统_独立进程插件方案.md》）。
//
// 一句话口径：插件是 plugins/ 下的一个目录，**丢进去不等于上线**。
// 扫描只让它出现在"待授权"列表里；要运行还得技术维护组在界面上输口令授权，
// 授权绑定的是那一刻的文件指纹——换了文件就是另一个插件。
//
// 与编译期档（internal/modules 那套 Go 包）共用同一个注册表和同一份清单：
// 界面分不出一个模块是编译进来的还是子进程供的，这就是"两档共存"的全部含义。
package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
)

// 资源上限。刻意写死在代码里而不是放进配置：留一个"最大插件数"的环境变量，
// 结果就是出问题当晚有人把它调大。要改就来改这里，顺便过一次代码评审。
const (
	MaxPlugins       = 8
	MaxResponseBytes = 256 << 10        // 一行响应 JSON 的上限（§3）
	RequestTimeout   = 5 * time.Second  // 单次数据请求
	ActionTimeout    = 10 * time.Second // 单次动作请求（计划 §9 决策⑧：写比读慢）
	PingTimeout      = 10 * time.Second // 授权后等它自报家门
	StderrKeepBytes  = 4 << 10          // 每个插件只留最后这么多 stderr 给面板看
)

// PluginsDir 插件根目录：PLUGINS_DIR 覆盖，默认 ./plugins。
// 和 static/、uploads/ 一样按当前工作目录解析——主程序的相对路径只有这一个口径。
func PluginsDir() string {
	if v := strings.TrimSpace(os.Getenv("PLUGINS_DIR")); v != "" {
		return v
	}
	return "./plugins"
}

// State 管理面板"状态"列的取值，与方案 §7.2 那六种一一对应；
// 后两个是签名准入（S0）加的，**下一步动作与前面几个完全不同**，所以不并进 invalid：
// 校验失败是"改改你的 JSON"，未签名是"找作者要一份签过名的"，摘要不符是"这份被动过"。
type State string

const (
	StatePending   State = "pending"    // 待授权：扫到了、没人授权，不运行
	StateRunning   State = "running"    // 已授权且子进程活着
	StateRetrying  State = "retrying"   // 崩溃/超时后正在退避重启
	StateGaveUp    State = "gave_up"    // 连续失败到上限，不再自动拉起
	StateInvalid   State = "invalid"    // manifest 校验失败：列出原因，不运行
	StateStale     State = "stale"      // 文件与授权时指纹不符：需重新授权
	StateUnsigned  State = "unsigned"   // 没有签名，或签名者不在信任锚里：不运行，也**不给授权**
	StateSigBroken State = "sig_broken" // 有签名但摘要/签名对不上：可能是篡改，不运行、不给授权
)

// Manifest plugins/<id>/manifest.json 的形状，字段规则见方案 §2。
//
// 解码一律拒绝未知字段：policy 手滑写成 policies 如果被判"没写"，
// 表现为"这插件谁都不给看"，而作者会一路怀疑到权限配置上去。
type Manifest struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Icon   string   `json:"icon"`
	Group  string   `json:"group"`
	Tab    string   `json:"tab"`
	Policy []string `json:"policy"`
	// Widgets 只写组件本身；数据端口由 id 推导，不接受手填（见 DataEndpoint）。
	Widgets            []ManifestWidget `json:"widgets"`
	Actions            []ManifestAction `json:"actions"`
	Exec               string           `json:"exec"`
	MinManifestVersion *int             `json:"min_manifest_version"`
}

// ManifestWidget 一个可渲染组件；type 必须在 modules 的白名单里，
// 而那条白名单又必须和前端渲染器一一对应——三处是一条链，断在哪都白搭。
type ManifestWidget struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Label string `json:"label"`
	// ActionKey 只有 type="action" 才写：这个按钮发起的是 actions 里的哪一条。
	// 不直接在这里写路径：一份能力只能有一处定义，写在按钮上就等于同一个动作能声明两遍。
	ActionKey string `json:"action_key"`
}

// ManifestAction 插件声明"我会发起这样一次写"。
// **声明不等于允许**：path 必须在服务端可声明白名单里（action_scope.go），
// confirm 会被高危清单覆盖，两者都在扫描期判掉，不留到运行期。
type ManifestAction struct {
	Key     string                `json:"key"`
	Label   string                `json:"label"`
	Method  string                `json:"method"`
	Path    string                `json:"path"`
	Confirm string                `json:"confirm"`
	Reason  string                `json:"reason"`
	Fields  []ManifestActionField `json:"fields"`
}

// ManifestActionField 动作要收的参数。客户端只能传这里声明过的键，
// 多一个都拒——否则等于让浏览器自己决定"这次点击往插件 stdin 里塞什么"。
type ManifestActionField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Multi    bool   `json:"multiline"`
	MaxBytes int    `json:"max_bytes"`
}

// DataEndpoint 插件数据端点的完整路径。由 id 推导，manifest 里写不了别的路径：
// 一个插件只该有这一个入口，能自己填路径就等于允许它声明"我这个组件的数据其实在
// 那个业务接口上"，那已经不是插件了，是绕过 Casbin 的第二套路由表。
func DataEndpoint(id string) string { return "/api/v1/mod/" + id }

// ActionEndpoint 插件动作端点的完整路径。同样由 id 推导、只有一条：
// 发起写动作的唯一入口就是这里，插件永远拿不到"直接打业务接口"的那条路（计划 §2）。
func ActionEndpoint(id string) string { return "/api/v1/mod/" + id + "/action" }

// ActionRoutePath 动作端点在 /api/v1/mod 组内的那一段。与 RoutePath 同一套拆法：
// 清单写全路径、路由写组内路径，两处各写一遍字面量迟早会漂。
func ActionRoutePath(id string) string { return "/" + id + "/action" }

// RoutePath 该插件在 /api/v1/mod 组内注册的那一段（同 runtimestatus.RoutePath 的拆法：
// 清单写全路径、路由写组内路径，两处各写一遍字面量迟早会漂）。
func RoutePath(id string) string { return "/" + id }

// defaultExecName 可执行文件缺省名：Windows 上必须带 .exe，Linux 无扩展名。
func defaultExecName() string {
	if runtime.GOOS == "windows" {
		return "plugin.exe"
	}
	return "plugin"
}

// ManifestFrom 解析一份 manifest.json 的字节，规则与扫描期一字不差。
// 导出它只为了一个理由：签名工具（cmd/pluginctl）和加载器**必须对同一份文件有同一个读法**。
func ManifestFrom(raw []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("manifest.json 解析失败：%v", err)
	}
	// 尾巴上还有一段 JSON 的话，说明有人往一个文件里塞了两份清单
	if dec.More() {
		return m, fmt.Errorf("manifest.json 里有多余内容（一个文件只该有一份清单）")
	}
	return m, nil
}

// ExecPathFor 用与扫描期同一套规则从插件目录里定位可执行文件。
// exec 只允许一个裸文件名：出现路径分隔符就是想把加载器引到插件目录外面去。
func ExecPathFor(dir string, m Manifest) (string, error) {
	name := strings.TrimSpace(m.Exec)
	if name == "" {
		name = defaultExecName()
	}
	if name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("exec %q 不合法：只能是本目录下的一个文件名，不能带路径", m.Exec)
	}
	return filepath.Join(dir, name), nil
}

// DigestFile 一个文件的 sha256（hex）。签名工具与加载器共用，理由同 ManifestFrom。
func DigestFile(path string) (string, error) {
	sum, err := hashFile(path)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum[:]), nil
}

// allowedPolicyRoles policy 里能出现的取值（方案 §6 的五角色白名单）。
var allowedPolicyRoles = map[string]bool{
	model.RoleDormManager:  true,
	model.RoleMember:       true,
	model.RoleMinister:     true,
	model.RoleTechAdmin:    true,
	model.RoleViewerExport: true,
}

// defaultPolicyRoles manifest 不写 policy 时的取值。只给技术维护组：
// 缺省必须是最小的那一档，"忘了写"不应等于"全校都能读"。
var defaultPolicyRoles = []string{model.RoleTechAdmin}

// Plugin 一个目录 = 一个插件：磁盘上的事实 + 当前的信任与运行状态。
type Plugin struct {
	Dir          string
	Manifest     Manifest
	ExecPath     string
	ExecHash     string
	ManifestHash string
	PolicyRoles  []string // 已补默认值、已去重，顺序按五角色的固定次序

	// ManifestRaw 是 manifest.json 的原始字节：管理面板要能展开看它。
	// 存原文而不是重新序列化后的那份——"界面显示的就是磁盘上写的"这句话只有带原文才成立。
	ManifestRaw []byte

	// Module 是由 manifest 拼出来的描述符。校验没过时它只用于展示，不会进注册表。
	Module modules.Module

	State  State
	Reason string // 校验失败/签名不符的原因，或最近一次运行错误

	// SignedBy 是签名里那位开发者的 kid。**验不过也要填**：面板上"谁声称签了这份"
	// 正是运维去找人的线索，只有完全没有 signature 文件时才是空串。
	SignedBy string

	Trust *model.PluginTrust // 没有信任记录时为 nil
}

// NeedsRetrust 指纹与授权时不一致：换了文件，之前的授权不再算数。
func (p *Plugin) NeedsRetrust() bool {
	if p.Trust == nil || !p.Trust.Active() {
		return false
	}
	return p.Trust.ExecHash != p.ExecHash || p.Trust.ManifestHash != p.ManifestHash
}

// Scan 扫描插件根目录，返回按 id 排序的插件列表（含校验失败与签名不符的那些）。
//
// 只做"读目录 + 校验 + 算指纹 + 验签"四件事：不查信任表、不起进程、不碰注册表。
// 坏 manifest 不返回错误、也不影响别的插件——一个手滑的 JSON 逗号不该放倒全校系统
// （这是对 v1"拒启"口径的正式修正，理由见方案 §5）。
func Scan(dir string, anchors *AnchorSet) []*Plugin {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 目录不存在是常态（还没装过任何插件），调用方按"没有插件"处理即可。
		return nil
	}

	// os.ReadDir 已按文件名排序，所以"谁超出上限"是稳定的，不会每次重启换一批。
	out := make([]*Plugin, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue // 根目录下的散文件不是插件：多半是随手丢的 README 或备份
		}
		if len(out) >= MaxPlugins {
			// 超出上限的仍然列出来（带原因），不然运维会以为文件没传上去
			out = append(out, &Plugin{
				Dir:    filepath.Join(dir, e.Name()),
				State:  StateInvalid,
				Reason: fmt.Sprintf("插件数已达上限 %d，这个目录未纳入加载；要上新插件先撤一个", MaxPlugins),
			})
			continue
		}
		p := scanOne(filepath.Join(dir, e.Name()), e.Name(), seen, anchors)
		out = append(out, p)
	}
	return out
}

// scanOne 读一个插件目录。返回的 Plugin 一定带明确的 State：
// 校验没过就是 StateInvalid + Reason，签名不对就是 Unsigned / SigBroken，
// 绝不出现"状态是待授权但原因是空的"这种半拉子。
func scanOne(dir, dirName string, seen map[string]bool, anchors *AnchorSet) *Plugin {
	// 一律立刻转成绝对路径。Windows 上 CreateProcess 的 lpCurrentDirectory 必须是全路径，
	// 传相对目录会直接 ERROR_PATH_NOT_FOUND —— 而这个坑只在"从部署目录起服务、
	// PLUGINS_DIR 用默认 ./plugins"这种真实布局下才出现，测试里全是绝对路径，撞不到。
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	invalid := func(format string, args ...any) *Plugin {
		return &Plugin{Dir: dir, State: StateInvalid, Reason: fmt.Sprintf(format, args...)}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return invalid("读不到 manifest.json：%v", err)
	}
	sum := sha256.Sum256(raw)

	m, err := ManifestFrom(raw)
	if err != nil {
		return invalid("%v", err)
	}
	if m.MinManifestVersion == nil {
		v := modules.ManifestVersion
		m.MinManifestVersion = &v // 不填由服务端补齐
	} else if *m.MinManifestVersion != modules.ManifestVersion {
		return invalid("manifest 声明 min_manifest_version=%d，当前契约是 %d：这插件不是照着现在的接口写的",
			*m.MinManifestVersion, modules.ManifestVersion)
	}

	execPath, err := ExecPathFor(dir, m)
	if err != nil {
		return invalid("%v", err)
	}
	st, err := os.Stat(execPath)
	if err != nil {
		return invalid("清单 id=%q 但读不到可执行文件 %s：%v", m.ID, filepath.Base(execPath), err)
	}
	if st.IsDir() {
		return invalid("exec %q 是个目录，不是可执行文件", filepath.Base(execPath))
	}
	execSum, err := hashFile(execPath)
	if err != nil {
		return invalid("算可执行文件指纹失败：%v", err)
	}

	roles, err := normalizeRoles(m.Policy)
	if err != nil {
		return invalid("%v", err)
	}

	mod := modules.Module{
		ID:                 m.ID,
		Title:              m.Title,
		Icon:               m.Icon,
		Group:              m.Group,
		Tab:                m.Tab,
		MinManifestVersion: *m.MinManifestVersion,
		Widgets:            make([]modules.Widget, 0, len(m.Widgets)),
		Actions:            make([]modules.Action, 0, len(m.Actions)),
	}
	for _, w := range m.Widgets {
		// 按钮组件的"数据端点"就是动作端点：它不读数，它写。
		// 留空会让 modules.Validate 报"必须 /api/v1/ 开头"，而那条规则本是为只读组件写的，
		// 报出来只会把人引向错误的方向。
		endpoint := DataEndpoint(m.ID)
		if w.Type == modules.WidgetAction {
			endpoint = ActionEndpoint(m.ID)
		}
		mod.Widgets = append(mod.Widgets, modules.Widget{
			Key: w.Key, Type: w.Type, Label: w.Label,
			ActionKey:    w.ActionKey,
			DataEndpoint: endpoint,
		})
	}
	// 动作声明：先由 modules 判形状（路径写法、方法与口令取值、按钮与声明是否一一对应），
	// 再判服务端范围（这条路径能不能被声明、口令是否被高危清单强制抬起）。
	// 顺序是刻意的：形状错了先说形状，因为那才是作者能自己改的东西；
	// 一上来就说"不在白名单"会让人去换路径写法，而真正的问题是按钮和声明没对上。
	for _, a := range m.Actions {
		fields := make([]modules.ActionField, 0, len(a.Fields))
		for _, f := range a.Fields {
			fields = append(fields, modules.ActionField{Key: f.Key, Label: f.Label, Multi: f.Multi, MaxBytes: f.MaxBytes})
		}
		mod.Actions = append(mod.Actions, modules.Action{
			Key: a.Key, Label: a.Label, Method: strings.ToUpper(strings.TrimSpace(a.Method)),
			Path: strings.TrimSpace(a.Path), Confirm: a.Confirm, Reason: a.Reason,
			Fields: fields, Endpoint: ActionEndpoint(m.ID),
		})
	}
	// 描述符合不合法，用 modules 那一份规则判：id 格式、tab 格式、组件类型白名单、
	// 端口前缀、动作与按钮的对应关系……全都在那边，这里不复制第二套（复制出去的规则一定会漂移）。
	if err := modules.Validate(mod); err != nil {
		return invalid("%v", err)
	}
	// 形状过了再判范围：这条路径允不允许被插件声明、口令要不要被强制抬起来。
	for i, a := range mod.Actions {
		if err := actionAllowedScope(a.Method, a.Path); err != nil {
			return invalid("%v", err)
		}
		confirm, forcedWhy := effectiveConfirm(a.Method, a.Path, a.Confirm)
		if forcedWhy != "" && a.Confirm != confirm {
			// 不许静默改写：作者写的是 none、跑起来却要口令，这句话得留在日志里，
			// 否则他下次改清单会以为改丢了。
			log.Printf("[Plugins] %s 的动作 %q 声明 confirm=%s，按服务端高危清单改成 %s：%s",
				m.ID, a.Key, a.Confirm, confirm, forcedWhy)
		}
		mod.Actions[i].Confirm = confirm
	}
	if modules.Has(mod.ID) {
		return invalid("id %q 与已注册的模块撞车（编译期模块或别的插件先占了这个名字）", mod.ID)
	}
	if seen[mod.ID] {
		return invalid("id %q 在本目录下重复出现：两个目录报同一个 id，清单里只能活一个", mod.ID)
	}
	seen[mod.ID] = true

	p := &Plugin{
		Dir:          dir,
		Manifest:     m,
		ManifestRaw:  raw,
		ExecPath:     execPath,
		ExecHash:     hex.EncodeToString(execSum[:]),
		ManifestHash: hex.EncodeToString(sum[:]),
		PolicyRoles:  roles,
		Module:       mod,
		State:        StatePending,
	}

	// 签名准入放在 manifest 判完**之后**：一份连清单都不合法的目录，先说清单的错——
	// "签名无效"会盖掉那句真正能让人动手修的话。
	//
	// 但签名不过的目录仍然带着完整的 Module：面板要能说出"它声称自己是 diskusage、
	// 要 tech_admin 读、由 kid a1b2c3d4 签的"，运维才知道该去找谁，而不是看到一个匿名目录。
	sigRaw, err := os.ReadFile(filepath.Join(dir, SignatureFileName))
	if err != nil {
		p.State = StateUnsigned
		p.Reason = fmt.Sprintf("目录里没有 %s 文件：这个插件没有开发者签名，不进可授权列表。找作者要一份签过名的（读文件本身：%v）", SignatureFileName, err)
		return p
	}
	sig, err := ParseSignature(sigRaw)
	if err != nil {
		p.State = StateSigBroken
		p.Reason = fmt.Sprintf("%s 文件读不懂：%v", SignatureFileName, err)
		return p
	}
	p.SignedBy = sig.Kid // 验不过也留着：这是"该找谁"的线索
	if err := anchors.Verify(sig, p.ManifestHash, p.ExecHash); err != nil {
		if errors.Is(err, ErrUnknownKey) {
			p.State = StateUnsigned // 没签 or 我们不认这个人
		} else {
			p.State = StateSigBroken // 文件与签名不符，或签名本身伪造/损坏
		}
		p.Reason = "签名校验未通过：" + err.Error()
		return p
	}
	return p
}

// normalizeRoles 补默认值 + 去重 + 按五角色的固定次序排一遍。
// 顺序固定下来，面板上"申请角色"那一栏才不会因为 manifest 里字段顺序不同而看起来变了。
func normalizeRoles(in []string) ([]string, error) {
	if len(in) == 0 {
		return append([]string(nil), defaultPolicyRoles...), nil
	}
	set := map[string]bool{}
	for _, r := range in {
		r = strings.TrimSpace(r)
		if !allowedPolicyRoles[r] {
			return nil, fmt.Errorf("policy 里有未知角色 %q：只认 dorm_manager / member / minister / tech_admin / viewer_export", r)
		}
		set[r] = true
	}
	out := make([]string, 0, len(set))
	for _, r := range roleOrder {
		if set[r] {
			out = append(out, r)
		}
	}
	return out, nil
}

// roleOrder 就是 seedCasbinRules 里那五个角色的书写次序，面板与管理接口都按它排。
var roleOrder = []string{
	model.RoleDormManager, model.RoleMember, model.RoleMinister,
	model.RoleTechAdmin, model.RoleViewerExport,
}

func hashFile(path string) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil { // 整份文件读进来算指纹：授权绑的就是这些字节
		return zero, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
