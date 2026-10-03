package plugins

import (
	"crypto/ed25519"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
	"xgh-system/internal/repository"
)

// trust.go —— 信任门：把"界面上输过一次口令"变成一条能跨重启说话的数据库记录。
//
// 四条不变量，改这个文件之前先读：
//  1. 起进程只有两处——启动期恢复已授权且指纹一致的，以及 Authorize 成功的那一刻。
//     数据请求永远不能懒启动一个未授权的插件。
//  2. 授权绑定**授权那一刻**的文件指纹。换了文件就是另一个插件：启动期比对不上就
//     不拉起、面板上标"需重新授权"（失败关闭）。
//  3. 注册表、Casbin 内存策略、信任表三者同批增删。少做任何一半，就会出现
//     "清单里有入口但点进去 403"或"清单没入口但端点读得到"这两种骗人的界面。
//  4. 状态是算出来的，不是存下来的（见 entry.state）：进程会自己死，
//     把 StateRunning 写进字段里就等于允许界面在一个进程已经退出之后还说它运行中。

// Operator 一次授权/撤销的执行人。controller 层从会话里取，那边已经过完口令二次确认。
type Operator struct {
	ID   uint
	Name string
}

type entry struct {
	plugin  *Plugin
	trusted bool
	proc    *process
	busy    bool // 一次授权/撤销正在进行：ping 最长等 10s，别让第二个人挤进来
	// granted 这次授权批准了哪几条写动作（S5）。空 = 只批读数，一条动作都没批。
	//
	// 刻意不"批全部当作默认值"：授权按钮一次点击能批的东西必须由人在申请单上逐条勾出来，
	// 给个"全选"默认等于把 S5 又变回"点了个按钮"。
	granted []grantRow
}

// grantKeys 已批准的动作 key 列表。
func (e *entry) grantKeys() []string {
	out := make([]string, 0, len(e.granted))
	for _, g := range e.granted {
		out = append(out, g.Key)
	}
	return out
}

// grantedModule 登记进注册表、下发给清单与 Casbin 的那份模块：**只含被批准的动作**。
//
// 为什么在这里筛而不是在清单接口里筛：注册表是唯一那份"对外承诺"，
// 未批准的动作为了一句"没批准"就存在于清单里，界面就会长出按下去只会失败的按钮
// （ManifestForRole 已经会把找不到声明的按钮丢掉，所以注册子集之后按钮自然消失，
// 不需要再多一处第二真相）。
//
// 磁盘上那份完整声明留在 e.plugin.Module.Actions 里——面板要看的是"他申请了什么"，
// 而执行时要核对的"签名承诺的是哪条接口"也必须是那份完整的（没批准的动作回的是
// "这条没被批准"，不是"清单里没这条"）。
func (e *entry) grantedModule() modules.Module {
	return grantedModuleFor(e.plugin, e.grantKeys())
}

// blocksLoad 磁盘上的事实已经判定"这个目录不能进可授权列表"的三种情况。
// 共同点：都不碰信任表、不起进程、不给授权按钮，只在面板上把原因说清。
func blocksLoad(s State) bool {
	return s == StateInvalid || s == StateUnsigned || s == StateSigBroken
}

// state 当前该对界面说的那句话。
// invalid / stale / unsigned / sig_broken 存在 plugin.State 上（那是磁盘与信任表的
// 事实，不随进程生死变），其余情况一律现问进程。
func (e *entry) state() State {
	switch e.plugin.State {
	case StateInvalid, StateStale, StateUnsigned, StateSigBroken:
		return e.plugin.State
	}
	if !e.trusted {
		return StatePending
	}
	if e.proc == nil {
		return StateRetrying
	}
	return e.proc.State()
}

// Manager 加载器的全部运行期状态。进程内单例，main.go 建一次。
type Manager struct {
	mu      sync.RWMutex
	dir     string
	enf     policyEnforcer
	anchors *AnchorSet
	entries map[string]*entry

	// selfBaseURL 代执行那一跳回环地址的出处（计划 §5.3）。
	// 端口是 main.go 那侧才知道的事实（PORT 环境变量在它读完之后才用到这里来），
	// 所以由它显式塞进来，而不是在这里再读一遍 env：两处读同一个变量迟早会读出两个值。
	selfURL string
	// claims 动作去重窗口（键含操作者，见 executor.go 的 ClaimAction），值 = 最近一次认领的时刻。
	claims map[string]time.Time
}

// SetSelfBaseURL 告诉加载器"你自己监听在哪个地址上"。
// 没配过的话代执行会明确报"没有配本机回环地址"而不是静默打到某个默认端口上——
// 打错了端口的那一句 connection refused 看起来会像"插件的接口坏了"。
func (m *Manager) SetSelfBaseURL(url string) {
	m.mu.Lock()
	m.selfURL = strings.TrimRight(url, "/")
	m.mu.Unlock()
}

// SelfBaseURL 当前配好的回环地址。
func (m *Manager) SelfBaseURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.selfURL
}

// NewManager 用默认的信任锚来源：编译内置 + 运行目录的 plugin_keys/*.pub。
func NewManager(dir string, enf policyEnforcer) *Manager {
	return NewManagerWithAnchors(dir, enf, LoadAnchors(dir))
}

// NewManagerWithAnchors 把锚集合交出去，测试与夹具才有办法自带一把测试密钥。
// 刻意不提供"运行期加公钥"的接口：那等于让能授权插件的人自己造可信开发者（计划 §3.2）。
func NewManagerWithAnchors(dir string, enf policyEnforcer, anchors *AnchorSet) *Manager {
	if anchors == nil {
		anchors = &AnchorSet{keys: map[string]ed25519.PublicKey{}, source: map[string]string{}}
	}
	return &Manager{dir: dir, enf: enf, anchors: anchors, entries: map[string]*entry{}}
}

// Dir 插件根目录。管理接口要把它回给界面：授权失败时"去哪个目录看文件"是第一件事。
func (m *Manager) Dir() string { return m.dir }

// TrustedKids 当前被信任的开发者列表。管理接口把它回给界面是必要的：
// 面板上说"签名者不认"却不说我们认谁，运维就只能猜。
func (m *Manager) TrustedKids() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.anchors.Kids()
}

// RoutableIDs 该挂数据端点的插件 id，**只含 manifest 校验通过的**。
// 校验失败的目录连 id 都可能是坏字符串，挂进路由表等于让外来内容决定 URL 形状。
//
// 撤销之后路由仍然留着（gin 不支持运行期加路由，一个新插件要能被读到，
// 它的路由必须在启动期就挂上 —— 这就是方案 §13"新增目录要重启"的由来）：
// 读一个已撤销的插件得到的是"未授权"那句实话加 warn 卡，而不是 404 让人以为功能坏了。
func (m *Manager) RoutableIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.entries))
	for id, e := range m.entries {
		if e.plugin.State == StateInvalid || e.plugin.Module.ID == "" {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// View 单个插件的当前视图（授权/撤销之后回给界面刷新那一行用）。
func (m *Manager) View(id string) (View, bool) {
	for _, v := range m.Views() {
		if v.ID == id {
			return v, true
		}
	}
	return View{}, false
}

// lookupKey 校验失败的目录没有可用的 Module.ID，按目录名挂进列表（面板要能列它）。
func lookupKey(p *Plugin) string {
	if p.Module.ID != "" {
		return p.Module.ID
	}
	return "#" + filepath.Base(p.Dir)
}

// Boot 启动期扫描，并按信任表自动恢复（方案 §8 的启动顺序）。
//
// 签名准入在这里是**第二次**生效（第一次是 Scan）：一位开发者的公钥从信任锚里被撤掉
// 之后，用它签的插件在这次重启起就不再加载——这正好是使用方拍板的读法 B：
// **不即时 kill**（运行期没有密钥监听，正在跑的照旧跑完这一程），
// 要立刻停就用"撤销授权"，那条路是即时的。
//
// 与界面授权还有一处刻意不同：**这里不等 ping**。授权时人要立刻知道按钮点了算不算成，
// 所以 ping 一次；启动期最多八个插件、每个最多等 10 秒，拿全校系统的启动时间去等
// 一个外来二进制自报家门不值当。它要是不回话，第一次有人打开面板就会看见 warn 卡，
// 进程也已经在退避重启的路上了。
func (m *Manager) Boot() error {
	found := Scan(m.dir, m.anchors)
	trusts, err := loadAllTrust()
	if err != nil {
		return err
	}
	// 批准记录读不出来时**不停整个加载器**：读数照常，动作一律视为未批准。
	// 少读一张表就把全校插件下线是过度反应，而"未批准"是失败关闭那一边，安全。
	grants, gerr := loadActiveGrants()
	if gerr != nil {
		log.Printf("[Plugins] 写动作批准表读不出来（本次所有插件的动作都按未批准处理）: %v", gerr)
		grants = map[string][]grantRow{}
	}

	next := make(map[string]*entry, len(found))
	adopted := make([]*process, 0, len(found))
	for _, p := range found {
		if p.Module.ID != "" {
			p.Trust = trusts[p.Module.ID]
		}
		e := &entry{plugin: p}
		switch {
		case blocksLoad(p.State):
			// 清单坏 / 没签名 / 签名不符：只列原因，不碰信任表、不起进程
		case !p.Trust.Active():
			p.State = StatePending
		case p.NeedsRetrust():
			// 授权史是真的，但文件已经不是当初那份：既不拉起，也要和"从没授权过"区分开
			p.State = StateStale
			e.trusted = true
		default:
			e.trusted = true
			// 批准也按指纹对账：只认"批的就是现在这份文件"的那些行（S5）。
			// 对不上不静默丢掉，说一声——面板上"一条都没批"和"曾经批过两条但现在这份没批"
			// 是两件不同的事，后一句的下一步是重新授权重勾，前一句的下一步是去看申请单。
			for _, g := range grants[p.Module.ID] {
				if g.ExecHash == p.ExecHash {
					e.granted = append(e.granted, g)
					continue
				}
				log.Printf("[Plugins] %s 的动作 %q（%s %s）的批准记录绑的是另一份文件，本次不生效", p.Module.ID, g.Key, g.Method, g.Path)
			}
			if proc := m.adopt(e); proc != nil {
				adopted = append(adopted, proc)
			}
		}
		next[lookupKey(p)] = e
	}

	m.mu.Lock()
	m.entries = next
	m.mu.Unlock()

	for _, p := range adopted {
		go p.supervise()
	}
	return nil
}

// adopt 把"已授权且指纹一致"落到运行期：进注册表 + 注入策略 + 起进程。
// 失败时把已做的部分退干净，并把原因写进 plugin.Reason 让面板说得出话。
// 返回的进程由调用方在解锁后交给 supervise。
//
// 注册表那一步是**可以跳过的**：批准子集一个组件都不剩时（纯按钮型插件一条都没批），
// 清单里没有它是真话，而策略照旧注入、进程照旧起——"能不能读"与"界面上有没有入口"
// 是两件事，后者只由批准范围决定。
func (m *Manager) adopt(e *entry) *process {
	id := e.plugin.Module.ID
	registered := e.grantedModule()
	if displayable(registered) {
		if err := modules.TryRegister(registered); err != nil {
			e.plugin.Reason = "已授权但登记进清单失败：" + err.Error()
			e.plugin.State = StateInvalid
			e.trusted = false
			log.Printf("[Plugins] %s", e.plugin.Reason)
			return nil
		}
	}
	if err := grantPolicy(m.enf, registered, e.plugin.PolicyRoles); err != nil {
		if displayable(registered) {
			modules.Unregister(id)
		}
		e.plugin.Reason = "已授权但放开读权限失败：" + err.Error()
		e.plugin.State = StateInvalid
		e.trusted = false
		log.Printf("[Plugins] %s", e.plugin.Reason)
		return nil
	}
	p := newProcess(e.plugin)
	if err := p.start(); err != nil {
		// 起不来不摘注册：授权是人的决定，进程活着与否是机器的现状。
		// 界面要显示的是"已授权、正在退避重启"，而不是装作没这回事。
		p.mu.Lock()
		p.lastErr = err.Error()
		p.noteFailureLocked()
		p.mu.Unlock()
	}
	e.proc = p
	return p
}

// refreshAtAuthorizeTime 授权那一刻按**磁盘上当下的字节**重判一次（计划 §3.3 第 2 条）。
//
// 为什么不能只信缓存：面板上那行"待授权"是上一次扫描（通常就是主程序启动）时的快照，
// 从"人看见它"到"人点下授权"之间，目录里的文件可以被人换过。真按缓存授权的话，
// 授权绑定的是旧指纹、起来的却是新那份——签名和指纹两道门同时被绕过，而这正是这两道门
// 存在的全部理由。
//
// 换了文件时**拒绝并要求再看一次**，而不是默默改成绑新内容：人批准的是他在页面上读到的
// 那份（标题、可读角色、签名者 kid），悄悄换绑等于让一次点击批准一份他没看过的东西。
// 拒绝之前先把缓存刷成磁盘现状，界面重读后那一行显示的就是真话，第二下点的是新那份。
//
// 信任记录是数据库里的事实，不随文件变，所以重扫后要贴回 fresh 上。
func (m *Manager) refreshAtAuthorizeTime(e *entry) error {
	// 单独扫一个目录时 seen 必须给一张新表，否则会把自己判成"两个目录重复 id"
	fresh := scanOne(e.plugin.Dir, filepath.Base(e.plugin.Dir), map[string]bool{}, m.anchors)
	fresh.Trust = e.plugin.Trust
	if fresh.State == StatePending && fresh.NeedsRetrust() {
		fresh.State = StateStale // 与 Boot 同一套判法：有授权史但文件不是当初那份
	}

	idChanged := fresh.Module.ID != "" && fresh.Module.ID != e.plugin.Module.ID
	changed := fresh.ExecHash != e.plugin.ExecHash || fresh.ManifestHash != e.plugin.ManifestHash

	m.mu.Lock()
	e.plugin = fresh
	m.mu.Unlock()

	switch fresh.State {
	case StateInvalid:
		return fmt.Errorf("这个插件现在过不了校验，不能授权：%s", fresh.Reason)
	case StateUnsigned:
		return fmt.Errorf("插件 %s 没有可信开发者签名，不能授权：%s", fresh.Module.ID, fresh.Reason)
	case StateSigBroken:
		return fmt.Errorf("插件 %s 的签名与磁盘上的文件对不上，不能授权（先确认这份就是作者那份）：%s", fresh.Module.ID, fresh.Reason)
	}
	if idChanged {
		return fmt.Errorf("这个目录里的清单在两次读取之间换了 id，本次授权已中止：请刷新列表确认它到底是谁")
	}
	if changed {
		return fmt.Errorf("插件 %s 的文件在上次扫描之后被换过，本次授权没有生效：列表已刷成现在这份，请核对标题、可读角色与开发者后再点一次", fresh.Module.ID)
	}
	return nil
}

// Authorize 界面上的一次授权：起进程 + 等它自报家门 + 记下这次批准了哪几条写动作。
// 失败不留任何痕迹（不写信任表、不注册、不注入策略、不写批准）——半套授权比没授权更糟。
//
// grant 是申请单上被勾中的那几个动作 key（S5）。它只会往两个方向被约束：
//   - 出现清单里没申请过的 key ⇒ 当场拒（"批一张没写着的申请单"比不批更糟）；
//   - 空 ⇒ 合法，意思是"这个插件我只让它读数，一个写动作都不批"。
//
// 刻意没有"默认全批"这条路：一次点击批掉几条接口，必须看得出来是几条。
func (m *Manager) Authorize(id string, op Operator, grant []string) error {
	e, err := m.beginOp(id)
	if err != nil {
		return err
	}
	defer m.endOp(id)

	// 重复授权先挡掉，**顺序在重扫之前**：已在授权状态的插件它的 id 就登记在注册表里，
	// 而 scanOne 会把"撞上已注册 id"判成校验失败——先重扫会让这句正常的"不用重复点"
	// 变成一句吓人的"你的 manifest 不合法"。
	// 要改批准范围走 UpdateGrants（那是另一个决定，也有另一条留痕），不是再点一次授权。
	if e.trusted && e.plugin.State != StateStale {
		return fmt.Errorf("插件 %s 已在授权状态，无需重复授权；要改哪几条动作被批准，用它下方的「改批准」", id)
	}

	// 签名准入的第三道门（前两道在 Scan 与 Boot），顺带把磁盘上的文件重读一遍。
	// 界面上把按钮藏掉只是省人一次点击，**真正的门在这里**：绕过界面直接 POST 授权接口，
	// 也必须被拒——只挡前端的门等于没门。
	if err := m.refreshAtAuthorizeTime(e); err != nil {
		return err
	}
	// 勾选先对着**重扫之后那份签名清单**验一遍。放在起进程之前：
	// 批准范围写错不该让一个外来二进制先跑起来再告诉人失败。
	keys, err := normalizeGrantKeys(e.plugin, grant)
	if err != nil {
		return err
	}

	registered := grantedModuleFor(e.plugin, keys)
	proc := newProcess(e.plugin)
	if err := proc.start(); err != nil {
		return fmt.Errorf("插件起不来: %w", err)
	}
	if err := proc.Ping(PingTimeout); err != nil {
		proc.stop()
		return fmt.Errorf("插件起来了但不回话: %w", err)
	}
	// 一条都没批且这插件只有按钮 ⇒ 清单里不该有它（没有能摆的入口），但授权本身成立。
	// 这一步跳过注册而不是塞一份空组件进去：注册表明写"一个组件都没有"是骗人。
	if displayable(registered) {
		if err := modules.TryRegister(registered); err != nil {
			proc.stop()
			return err
		}
	}
	if err := grantPolicy(m.enf, registered, e.plugin.PolicyRoles); err != nil {
		if displayable(registered) {
			modules.Unregister(id)
		}
		proc.stop()
		return fmt.Errorf("放开读权限失败: %w", err)
	}
	trustRow, rows, err := saveTrustAndGrants(e.plugin, keys, op)
	if err != nil {
		revokePolicy(m.enf, registered, e.plugin.PolicyRoles)
		if displayable(registered) {
			modules.Unregister(id)
		}
		proc.stop()
		return fmt.Errorf("批准记录写不进库，授权不生效: %w", err)
	}

	m.mu.Lock()
	e.trusted = true
	e.proc = proc
	e.granted = rows
	// 内存里那份信任记录也要跟着换成刚写进库的：面板上"授权时 X → 现在 Y"读的是它。
	// 只换库不换内存的话，重新授权之后那一行会一直停在"文件换过"的样子——
	// 而状态柱此刻明明写着"运行中"，两句话自相矛盾。（上一轮端到端就是这么撞见的。）
	e.plugin.Trust = trustRow
	// 磁盘上还是那份文件：授权记的就是眼前这份指纹，之前若被标过 stale 到此为止
	e.plugin.State = StatePending
	m.mu.Unlock()
	go proc.supervise()
	return nil
}

// UpdateGrants 改一个**已授权**插件被批准的写动作范围（S5）。
//
// 为什么不复用 Authorize：那是两个决定。"让这个插件上线"和"让它能改这几条接口"
// 分开留痕，才回答得出"谁在什么时候把哪条写通道打开的"；而且撤销整插件
// 不该顺手把人只批过一次的读数功能一起说成没批准过。
//
// 只批 0 条是合法的（等于关掉全部写通道，读数照常）。
func (m *Manager) UpdateGrants(id string, op Operator, grant []string) error {
	e, err := m.beginOp(id)
	if err != nil {
		return err
	}
	defer m.endOp(id)

	if !e.trusted {
		return fmt.Errorf("插件 %s 还没授权，谈不上批准它的写动作：先授权，再勾要批的那几条", id)
	}
	if blocksLoad(e.plugin.State) {
		return fmt.Errorf("插件 %s 当前状态不允许改批准：%s", id, e.plugin.Reason)
	}
	if e.plugin.State == StateStale {
		// 文件已经换过：批准绑的是旧那份的指纹，这时候改批准等于给一份没看过的东西续批
		return fmt.Errorf("插件 %s 的文件在授权之后被换过，请先重新授权（重新勾一遍要批的动作）", id)
	}

	m.mu.RLock()
	before := e.granted
	m.mu.RUnlock()
	beforeKeys := grantKeysOf(before)

	keys, err := normalizeGrantKeys(e.plugin, grant)
	if err != nil {
		return err
	}
	rows, err := replaceGrants(e.plugin, keys, op)
	if err != nil {
		return err
	}

	newMod := grantedModuleFor(e.plugin, keys)
	if err := applyRegistered(id, newMod); err != nil {
		// 注册表还是改之前那份：库里的批准退回同一份，两边继续自洽。
		// 回滚失败不再往上叠错误——那已经是"库里说的和清单里的不是同一份"的现场，
		// 报错信息里必须把这件事说在前面，让人去重启对齐，而不是继续点。
		_, _ = replaceGrants(e.plugin, beforeKeys, op)
		_ = applyRegistered(id, grantedModuleFor(e.plugin, beforeKeys))
		return fmt.Errorf("清单没能换成新的批准范围，本次改批准未生效: %w", err)
	}
	// 只有"有没有批过动作"这件事本身变了，才需要动策略；批 A 改成批 B 不用碰策略：
	// 那条 POST 策略是按插件的动作端点整体放的，逐条判定在 GrantGate，不在 Casbin。
	if (len(beforeKeys) == 0) != (len(rows) == 0) {
		if err := setActionPolicy(m.enf, id, e.plugin.PolicyRoles, len(rows) > 0); err != nil {
			_, _ = replaceGrants(e.plugin, beforeKeys, op)
			_ = applyRegistered(id, grantedModuleFor(e.plugin, beforeKeys))
			return fmt.Errorf("动作策略切换失败，已退回改之前的批准范围: %w", err)
		}
	}
	m.mu.Lock()
	e.granted = rows
	m.mu.Unlock()
	return nil
}

// applyRegistered 把批准子集落成注册表里的现状：有入口就覆盖式登记，一个入口都不剩就摘掉。
//
// 用 Replace 而不是 TryRegister：改批准发生在"这个 id 已经在册"的时候，
// 而 TryRegister 撞自己的旧条目必报错（那条规则是给编译期防撞 id 用的）。
// 摘掉那一侧也必须做：纯按钮型插件被改成"一条都不批"之后，清单里留着它就是长按钮。
func applyRegistered(id string, mod modules.Module) error {
	if !displayable(mod) {
		modules.Unregister(id)
		return nil
	}
	return modules.Replace(mod)
}

// normalizeGrantKeys 把勾选的 key 与**签名清单里申请过的那些**对一遍。
// 返回的次序跟清单走，不跟请求走：批准记录与注册子集要每次都稳定同一份顺序，
// 否则同一批勾选会产生两种不同的注册结果，面板与审计都对不上。
func normalizeGrantKeys(p *Plugin, grant []string) ([]string, error) {
	want := map[string]bool{}
	for _, k := range grant {
		want[strings.TrimSpace(k)] = true
	}
	out := make([]string, 0, len(p.Module.Actions))
	for _, a := range p.Module.Actions {
		if want[a.Key] {
			out = append(out, a.Key)
		}
	}
	// 没被清单认领的那几个 key 必须点名报错，不能被静默丢掉：
	// 人以为自己批了 3 条、实际只落了 2 条，这是最容易被忽略的一种"点了没生效"。
	known := map[string]bool{}
	for _, k := range out {
		known[k] = true
	}
	for _, k := range grant {
		k = strings.TrimSpace(k)
		if k == "" || known[k] {
			continue
		}
		return nil, fmt.Errorf("插件 %s 的签名清单里没有申请过动作 %q，无法批准：批的只能是它自己写出来的那几条", p.Module.ID, k)
	}
	return out, nil
}

func grantKeysOf(rows []grantRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Key)
	}
	return out
}

// grantedModuleFor 用这份批准集合拼出该登记进注册表的模块。
//
// 未批准的动作要**连它的按钮一起摘**，只摘一半过不了注册表校验：validate 要求按钮与
// 声明双向闭合（registry.go 的 validateActions），只删声明会留下一个指向不存在动作的按钮，
// 那句报错读起来像"你的 manifest 写错了"，真实原因却是"这条还没批"——人会被引去改清单。
//
// 全摘完之后可能一个组件都不剩（纯按钮型插件、一条都没批）。那不是错误也不是骗人：
// 界面对这个插件确实没有任何入口可摆。注册表要求至少一个组件（"有入口、点进去空的"才骗人），
// 所以这种情况由调用方**不登记**，而不是登记一份空的进去。
func grantedModuleFor(p *Plugin, keys []string) modules.Module {
	ok := make(map[string]bool, len(keys))
	for _, k := range keys {
		ok[k] = true
	}
	acts := make([]modules.Action, 0, len(keys))
	for _, a := range p.Module.Actions {
		if ok[a.Key] {
			acts = append(acts, a)
		}
	}
	wids := make([]modules.Widget, 0, len(p.Module.Widgets))
	for _, w := range p.Module.Widgets {
		if w.Type == modules.WidgetAction && !ok[w.ActionKey] {
			continue
		}
		wids = append(wids, w)
	}
	m := p.Module
	m.Actions = acts
	m.Widgets = wids
	return m
}

// displayable 这份批准子集还有没有在界面上摆得出来的入口。
// 假的一侧是"纯按钮型插件一条都没批"，此时清单里不该有它，但它照样是已授权、能起进程的。
func displayable(m modules.Module) bool { return len(m.Widgets) > 0 }

// Revoke 界面上的一次撤销。顺序与授权相反，且每步失败都继续往下走完：
// 最坏情况是库里那行没标成撤销（下次重启会重新拉起），
// 但绝不能出现"点了撤销、这一刻它还在给人读数据"。
func (m *Manager) Revoke(id string, op Operator) error {
	e, err := m.beginOp(id)
	if err != nil {
		return err
	}
	defer m.endOp(id)

	if !e.trusted {
		return fmt.Errorf("插件 %s 本来就没在授权状态", id)
	}

	var firstErr error
	if e.proc != nil {
		e.proc.stop()
	}
	// 撤策略按**注册时那份**（只含被批准的动作）来撤，不拿完整声明去撤：
	// 没批准过的动作从来没注入过它的策略，撤它虽无害，但那句话读起来就像"这里本该有一条"。
	m.mu.RLock()
	registered := e.grantedModule()
	m.mu.RUnlock()
	// 注册表里有没有它是分情况的：stale（文件换过）与"已放弃自动重启"的插件，
	// 清单里本来就不该有它，摘不到不是错配；批准范围一条都没剩也一样（本来就没入口）。
	// 只有"进程在跑、而且它该有入口却不在清单里"才是真错配。
	if modules.Has(id) {
		modules.Unregister(id)
	} else if e.proc != nil && displayable(registered) {
		firstErr = fmt.Errorf("已授权且进程在跑，但清单里没有 %s：状态错配，需要重启对齐", id)
	}
	if err := revokePolicy(m.enf, registered, e.plugin.PolicyRoles); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := markRevoked(id, op); err != nil && firstErr == nil {
		firstErr = err
	}
	// 批准也跟着撤：撤销说的是"这个插件整体下线"，下一次授权必须重新勾一遍，
	// 不能出现"撤销过、再授权时上次批的写动作自己回来了"。
	if err := revokeGrants(id, op); err != nil && firstErr == nil {
		firstErr = err
	}

	m.mu.Lock()
	e.trusted = false
	e.proc = nil
	e.plugin.Trust = nil
	e.granted = nil
	e.plugin.State = StatePending
	m.mu.Unlock()
	return firstErr
}

// StopAll 统一 kill 全部子进程。主程序没有优雅关闭（r.Run 阻塞、无信号处理），
// 所以这条只在明确要退出的路径上有机会被调到；Windows 上父死子不死，
// 真正的兜底是插件自己"stdin 关闭即退出"（方案 §14 第 5 条）。
func (m *Manager) StopAll() {
	m.mu.RLock()
	procs := make([]*process, 0, len(m.entries))
	for _, e := range m.entries {
		if e.proc != nil {
			procs = append(procs, e.proc)
		}
	}
	m.mu.RUnlock()
	for _, p := range procs {
		p.stop()
	}
}

// ---- 供清单、管理接口与 pluginstatus 读的口子 --------------------------------

func (m *Manager) processFor(id string) *process {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok || !e.trusted {
		return nil
	}
	return e.proc
}

// widgetKeys 该插件声明的**只读**组件 key。问哪几个键只由描述符决定，不吃客户端输入。
// 动作按钮不在这里：它不是"取一块数据"的组件，把它一起问下去，插件要么回一个
// 莫名其妙的值、要么让面板显示一张"无数据"的卡——两张卡并排，人就分不清哪个坏了。
func (m *Manager) widgetKeys(id string) []string {
	m.mu.RLock()
	e, ok := m.entries[id]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	out := make([]string, 0, len(e.plugin.Module.Widgets))
	for _, w := range e.plugin.Module.Widgets {
		if w.Type == modules.WidgetAction {
			continue
		}
		out = append(out, w.Key)
	}
	return out
}

// widgetTypesOf 该插件声明的**只读**组件：key → 清单里写的类型（M2 起有 stat/note/list/table）。
// 数据出口按它判"插件回的这一格该长成什么形状"（values.go）。
// 动作按钮同样不在这份表里：它不取数据，把它一起问下去只会换来一张莫名其妙的卡。
func (m *Manager) widgetTypesOf(id string) map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return nil
	}
	out := make(map[string]string, len(e.plugin.Module.Widgets))
	for _, w := range e.plugin.Module.Widgets {
		if w.Type == modules.WidgetAction {
			continue
		}
		out[w.Key] = w.Type
	}
	return out
}

// DeclaredAction 取某插件签名清单里的一条动作声明。
// 动作端点只认这个来源：**客户端传来的 method/path 一概不采信**，
// 要执行什么由签名过的那份 manifest 说，插件只能往里填 body。
func (m *Manager) DeclaredAction(id, key string) (modules.Action, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return modules.Action{}, false
	}
	for _, a := range e.plugin.Module.Actions {
		if a.Key == key {
			return a, true
		}
	}
	return modules.Action{}, false
}

// ActionGate 写路径上那道**显式**的信任门。
//
// 为什么读路径不需要它、写路径却要有：读的时候插件进程不在，proxy 会把 absentReason
// 摊成一张 warn 卡，什么都不会发生；而动作端点上有人在等一个"写进去没有"。
// 少了这道门，一个从未授权的插件的按钮也能把人问到登录口令那一步——
// 判它不行的是"没授权"这个事实，那句实话该在要口令**之前**说，
// 而不是藏在"插件进程当前没在运行"后面让人以为授权过了、只是它坏了。
//
// 返回空串表示放行。退避重启中的已授权插件也放行：那一类由进程自己回真实故障，
// 不是门该拦的事。
func (m *Manager) ActionGate(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return "plugins/ 下没有这个插件目录，动作不执行"
	}
	// 三种"磁盘上的事实"各说各的（与 absentReason 同一套分法）：合并成一句
	// "校验未通过"会让人去查进程，而这三条各自要做的下一步完全不同。
	switch e.plugin.State {
	case StateInvalid:
		return "manifest 校验失败，动作不执行：" + e.plugin.Reason
	case StateUnsigned:
		return "没有可信开发者签名，加载器不收，动作不执行：" + e.plugin.Reason
	case StateSigBroken:
		return "签名与磁盘上的文件对不上，动作不执行：" + e.plugin.Reason
	case StateStale:
		return "插件 " + id + " 的文件在授权之后被换过，需重新授权后动作才会执行"
	}
	if !e.trusted {
		return "插件 " + id + " 未授权，只在管理面板里列着，动作不执行"
	}
	return ""
}

// GrantGate 逐条动作的批准门（S5）。返回空串表示这条被批准过、可以往下走。
//
// 它与 ActionGate 一前一后、都在要口令之前：插件没授权是一回事（整行都不该动），
// 授权了但这条没批是另一回事（"读数我批了、写没批"是超管可以合法表达的状态）。
// 两句话必须分开说，否则人会因为一句"未授权"去点授权按钮，而实际要做的是勾选。
func (m *Manager) GrantGate(id, key string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return "plugins/ 下没有这个插件目录，动作不执行"
	}
	for _, g := range e.granted {
		if g.Key == key {
			return ""
		}
	}
	return fmt.Sprintf("插件 %s 已授权，但这条动作 %q 没被批准：在「插件管理」那一行勾上它（或改批准范围），动作才会执行", id, key)
}

// GrantedActions 这个插件当前被批准的动作 key（面板与清单接口都用它筛按钮）。
func (m *Manager) GrantedActions(id string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return nil
	}
	return e.grantKeys()
}

// ActionCapableIDs 声明了动作、因而需要挂动作端点的插件 id。
// 与 RoutableIDs 同一套启动期口径（gin 不能运行期加路由）：撤销之后路由仍然留着，
// 打它得到的是"未授权"那句实话，不是 404。
// 只读插件不挂这条路由：多一条能 POST 的空门没有任何用处，还会让人以为插件能写。
func (m *Manager) ActionCapableIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.entries))
	for id, e := range m.entries {
		if e.plugin.State == StateInvalid || e.plugin.Module.ID == "" || len(e.plugin.Module.Actions) == 0 {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ActionView 面板上摊开的"一条写动作申请"（S5）。
//
// 内嵌 modules.Action 是为了让对外字段与 S2/S3 那份契约一致（key/label/method/path/
// confirm/reason/fields/action_endpoint 一字不改），外面只多三样：
//   - ScopeNote：**服务端**对这条接口的说明。它和作者自述的 reason 必须分两栏摆——
//     批准时人看到的如果只有申请人自己写的"我为什么要改"，那这次批准就只是信一个人。
//   - Granted / GrantedBy / GrantedAt：这条批了没有、谁批的、什么时候。
type ActionView struct {
	modules.Action
	ScopeNote string     `json:"scope_note,omitempty"`
	Granted   bool       `json:"granted"`
	GrantedBy string     `json:"granted_by,omitempty"`
	GrantedAt *time.Time `json:"granted_at,omitempty"`
}

// View 管理接口与 pluginstatus 共用的视图行。
type View struct {
	ID          string   `json:"id"`
	Dir         string   `json:"dir"`
	Title       string   `json:"title"`
	State       State    `json:"state"`
	Reason      string   `json:"reason,omitempty"`
	PolicyRoles []string `json:"policy_roles"`
	Widgets     []string `json:"widgets"`
	// Actions 授权界面要摊开的动作清单（计划 §6 最后一条 / §10.2）。
	// 只读时代那一行"申请可读角色"够用；插件能写之后，人在点授权前必须看到
	// "它会改哪个接口、要不要口令、为什么改"，否则授权退化成"点了个按钮"。
	// ⚠️ 这里是**完整申请单**（含没批准的），批准状态在每条的 Granted 上。
	Actions      []ActionView `json:"actions,omitempty"`
	ExecHash     string       `json:"exec_hash"`
	ManifestHash string       `json:"manifest_hash"`

	// SignedBy 是这份插件的签名者 kid。授权界面必须显示它：
	// "有签名"不是一个决定依据，"这是谁签的"才是（计划 §3.4）。
	SignedBy string `json:"signed_by,omitempty"`

	// TrustedExecHash / TrustedManifestHash 是**授权那一刻**记下的指纹。
	// 界面要把"当初授权的是哪份文件"和"现在磁盘上是哪份"并排给人看：
	// 只回当前指纹的话，stale 那一行会显示一个和信任表对不上的数，没人看得出为什么。
	TrustedExecHash     string     `json:"trusted_exec_hash,omitempty"`
	TrustedManifestHash string     `json:"trusted_manifest_hash,omitempty"`
	TrustedAt           *time.Time `json:"trusted_at,omitempty"`
	TrustedBy           string     `json:"trusted_by,omitempty"`
	RevokedAt           *time.Time `json:"revoked_at,omitempty"`
	RevokedBy           string     `json:"revoked_by,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	StderrTail          string     `json:"stderr_tail,omitempty"`
	ManifestRaw         string     `json:"manifest_raw,omitempty"`
}

// Views 按 id 排序的全部插件（含校验失败与未授权的）。
func (m *Manager) Views() []View {
	m.mu.RLock()
	out := make([]View, 0, len(m.entries))
	for key, e := range m.entries {
		p := e.plugin
		v := View{
			ID: p.Module.ID, Dir: filepath.Base(p.Dir), Title: p.Manifest.Title,
			State: e.state(), Reason: p.Reason,
			PolicyRoles: append([]string(nil), p.PolicyRoles...),
			ExecHash:    p.ExecHash, ManifestHash: p.ManifestHash,
			SignedBy:    p.SignedBy,
			ManifestRaw: string(p.ManifestRaw),
		}
		if v.ID == "" {
			v.ID = key // 校验失败的行也得有个能显示的名字
		}
		for _, w := range p.Module.Widgets {
			v.Widgets = append(v.Widgets, w.Key)
		}
		if len(p.Module.Actions) > 0 {
			// 申请单整份摆出来，一条都不藏；批准状态逐条标。
			// 只把"已批准的"显示给超管看是不行的——那他根本看不到还剩几条没处理。
			byKey := map[string]grantRow{}
			for _, g := range e.granted {
				byKey[g.Key] = g
			}
			for _, a := range p.Module.Actions {
				av := ActionView{Action: a, ScopeNote: DeclarableNote(a.Method, a.Path)}
				if g, ok := byKey[a.Key]; ok {
					av.Granted, av.GrantedBy, av.GrantedAt = true, g.By, &g.At
				}
				v.Actions = append(v.Actions, av)
			}
		}
		if p.Trust != nil {
			v.TrustedAt = &p.Trust.TrustedAt
			v.TrustedBy = p.Trust.TrustedByName
			v.RevokedAt = p.Trust.RevokedAt
			v.RevokedBy = p.Trust.RevokedByName
			v.TrustedExecHash = p.Trust.ExecHash
			v.TrustedManifestHash = p.Trust.ManifestHash
		}
		if e.proc != nil {
			v.LastError = e.proc.LastError()
			v.StderrTail = e.proc.stderr.String()
			// 授权过、进程也活着，清单里却没有它：这是状态错配，必须说出来而不是遮掉
			if e.proc.State() == StateRunning && !modules.Has(p.Module.ID) {
				v.Reason = "已授权且进程在跑，但清单里没有它（状态错配，需重启对齐）"
			}
		}
		out = append(out, v)
	}
	m.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Stats pluginstatus 那四个数（方案 §7.1），全部现算。
type Stats struct {
	Loaded    int `json:"loaded"`    // 已授权且已登记进清单（不管进程此刻活不活）
	Running   int `json:"running"`   // 进程活着
	Pending   int `json:"pending"`   // 待授权（已签名、等人点授权）
	Attention int `json:"attention"` // 校验失败 / 未签名 / 签名不符 / 需重新授权 / 已放弃
}

func (m *Manager) Stats() Stats {
	var s Stats
	for _, v := range m.Views() {
		switch v.State {
		case StateRunning:
			s.Running++
			s.Loaded++
		case StateRetrying:
			s.Loaded++ // 授权过也登记了，只是进程正在退避
		case StatePending:
			s.Pending++
		case StateInvalid, StateStale, StateGaveUp, StateUnsigned, StateSigBroken:
			s.Attention++
		}
	}
	return s
}

// ---- 信任表读写 ------------------------------------------------------------

func trustDB() (*gorm.DB, error) {
	if repository.DB == nil {
		return nil, fmt.Errorf("数据库不可用，信任门无法工作")
	}
	return repository.DB, nil
}

func loadAllTrust() (map[string]*model.PluginTrust, error) {
	out := map[string]*model.PluginTrust{}
	db, err := trustDB()
	if err != nil {
		return out, err
	}
	var rows []model.PluginTrust
	if err := db.Find(&rows).Error; err != nil {
		return out, err
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

func markRevoked(id string, op Operator) error {
	db, err := trustDB()
	if err != nil {
		return err
	}
	res := db.Model(&model.PluginTrust{}).Where("id = ?", id).Updates(map[string]any{
		"revoked_at": time.Now(), "revoked_by": op.ID, "revoked_by_name": op.Name,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("信任表里没有 id=%q 这一行", id)
	}
	return nil
}

// ---- 授权/撤销的串行小工具 --------------------------------------------------

// beginOp 取出 entry 并占住它。慢活（起进程、ping）在锁外跑，但同一插件同时只允许一个。
func (m *Manager) beginOp(id string) (*entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, fmt.Errorf("plugins/ 下没有 id=%q 的插件", id)
	}
	if e.plugin.State == StateInvalid {
		return nil, fmt.Errorf("这个插件 manifest 校验没通过，先修好它：%s", e.plugin.Reason)
	}
	if e.busy {
		return nil, fmt.Errorf("这个插件上一次操作还没结束，稍后再试")
	}
	e.busy = true
	return e, nil
}

func (m *Manager) endOp(id string) {
	m.mu.Lock()
	if e, ok := m.entries[id]; ok {
		e.busy = false
	}
	m.mu.Unlock()
}

// start 加锁起一次进程，已在跑就直接成功。
func (p *process) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.live {
		return nil
	}
	return p.startLocked()
}
