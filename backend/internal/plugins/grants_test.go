package plugins

// grants_test.go —— S5：一次授权"批准了哪几条写动作"是数据库事实，不是界面上的勾选状态。
//
// 这个文件要钉住的四句话：
//  1. 只能批准清单里申请过的那几条（勾一条没申请的 ⇒ 当场拒，而且一点痕迹都不留）；
//  2. 没批准的那条**在清单里长不出按钮**（进注册表的是批准子集，声明与按钮一起摘），
//     但它在管理面板的申请单上必须照样列得出来——不然超管看不见还剩几条没处理；
//  3. 批准绑文件指纹：换了文件的插件重启之后不会因为"上次批过"就把写通道自己续回来；
//  4. 撤销整插件要逐条标撤、行留着，才答得出"这个插件当时被批准过什么、谁撤的"。
//
// enforcer 是真的（同 policy_test.go 的理由）：按钮可见性是问 Casbin 问出来的，
// 用自己写的桩判"这条策略在不在"，测的只是我对桩的想象。

import (
	"strings"
	"testing"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
	"xgh-system/internal/repository"
)

const s5Mode = "actionok"

func s5ID() string { return stubIDPrefix + s5Mode }

// twoActionManifest 一份申请了两条写动作的清单：一条免口令、一条正好命中服务端高危清单，
// 另带一个只读组件。两条路径都在白名单里，测试过不是因为"随便写的路径刚好没被拒"。
func twoActionManifest(id string) Manifest {
	m := validActionManifest(id)
	m.Actions = append(m.Actions, ManifestAction{
		Key: "add_slot", Label: "加一条值班时段", Method: "POST",
		Path: "/api/v1/tech/task-slots", Confirm: "none",
		Reason: "把当晚的查寝时段补进值班表",
	})
	m.Widgets = append(m.Widgets, ManifestWidget{
		Key: "btn_slot", Type: modules.WidgetAction, Label: "加一条值班时段", ActionKey: "add_slot",
	})
	return m
}

// buttonOnlyManifest 只有按钮的那种插件（一个只读组件都没有）。
// 它是"一条都没批"这条路的极端情形：批准子集一个入口都不剩，清单里到底该不该有它，
// 只有这种插件能测出来。
func buttonOnlyManifest(id string) Manifest {
	m := twoActionManifest(id)
	keep := make([]ManifestWidget, 0, len(m.Widgets))
	for _, w := range m.Widgets {
		if w.Type != modules.WidgetStat {
			keep = append(keep, w)
		}
	}
	m.Widgets = keep
	return m
}

// s5Boot 铺好一个"目录里躺着一份会写的插件、还没授权"的现场，返回加载器、真 enforcer
// 和那个临时目录（第二个加载器要拿同一个目录模拟重启）。
func s5Boot(t *testing.T, manifest Manifest) (*Manager, policyEnforcer, string) {
	t.Helper()
	root := t.TempDir()
	stubPluginDirWith(t, root, s5Mode, manifest)
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)
	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(s5ID()) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	return m, enf, root
}

func s5Op() Operator { return Operator{ID: 1, Name: "技术维护组甲"} }

func registeredModule(t *testing.T, id string) (modules.Module, bool) {
	t.Helper()
	for _, m := range modules.Modules() {
		if m.ID == id {
			return m, true
		}
	}
	return modules.Module{}, false
}

func actionKeysOf(m modules.Module) string {
	keys := make([]string, 0, len(m.Actions))
	for _, a := range m.Actions {
		keys = append(keys, a.Key)
	}
	return strings.Join(keys, ",")
}

func buttonKeysOf(m modules.Module) string {
	out := make([]string, 0, len(m.Widgets))
	for _, w := range m.Widgets {
		if w.Type == modules.WidgetAction {
			out = append(out, w.ActionKey)
		}
	}
	return strings.Join(out, ",")
}

func canEnforce(t *testing.T, e policyEnforcer, role, obj, act string) bool {
	t.Helper()
	ok, err := e.Enforce("role:"+role, obj, act)
	if err != nil {
		t.Fatalf("判定器出错: %v", err)
	}
	return ok
}

func activeGrantCount(t *testing.T, id string) int {
	t.Helper()
	var n int64
	if err := repository.DB.Model(&model.PluginActionGrant{}).
		Where("plugin_id = ? AND revoked_at IS NULL", id).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func totalGrantRows(t *testing.T, id string) int {
	t.Helper()
	var n int64
	if err := repository.DB.Model(&model.PluginActionGrant{}).
		Where("plugin_id = ?", id).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func viewOf(t *testing.T, m *Manager) View {
	t.Helper()
	for _, v := range m.Views() {
		if v.ID == s5ID() {
			return v
		}
	}
	t.Fatalf("面板里没有 %s 这一行", s5ID())
	return View{}
}

func actionViewOf(t *testing.T, v View, key string) ActionView {
	t.Helper()
	for _, a := range v.Actions {
		if a.Key == key {
			return a
		}
	}
	t.Fatalf("申请单里没有 %s 这条：他申请过的东西一条都不能藏", key)
	return ActionView{}
}

// ---- 授权当场：勾了什么就批了什么 --------------------------------------------

// 只勾一条 ⇒ 库里一条、注册表一条、另一条既拦得住又在申请单上看得见。
func TestAuthorizeApprovesOnlyCheckedActions(t *testing.T) {
	m, enf, _ := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), []string{"submit_news"}); err != nil {
		t.Fatalf("只勾一条也该授权成功: %v", err)
	}

	if got := grantRowsOf(t, id); strings.Join(got, ",") != "submit_news" {
		t.Errorf("批准表里该只有勾中的那条，实际 %v", got)
	}
	mod, ok := registeredModule(t, id)
	if !ok {
		t.Fatal("授权后清单里没有它")
	}
	if actionKeysOf(mod) != "submit_news" {
		t.Errorf("注册表里的动作应是批准那一条，实际 %q", actionKeysOf(mod))
	}
	if buttonKeysOf(mod) != "submit_news" {
		t.Errorf("没批准的那条把按钮留下了（点下去只会得到一句 409）: %q", buttonKeysOf(mod))
	}
	if why := m.GrantGate(id, "submit_news"); why != "" {
		t.Errorf("已批准的这条被拦: %s", why)
	}
	if why := m.GrantGate(id, "add_slot"); why == "" || !strings.Contains(why, "没被批准") {
		t.Errorf("未批准的这条该被点名拦住，实际 %q", why)
	}

	// 策略：读数照常放开；动作端点因为批了至少一条而放开（逐条判定在 GrantGate，不在 Casbin）
	if !canEnforce(t, enf, model.RoleMember, DataEndpoint(id), "GET") {
		t.Error("授权后部员读不到数据端点")
	}
	if !canEnforce(t, enf, model.RoleMember, ActionEndpoint(id), "POST") {
		t.Error("批过一条动作之后，动作端点的策略没注入：按钮会看得见却打不通")
	}

	v := viewOf(t, m)
	if len(v.Actions) != 2 {
		t.Fatalf("申请单应整份列出他申请的 2 条，实际 %d 条", len(v.Actions))
	}
	sub := actionViewOf(t, v, "submit_news")
	slot := actionViewOf(t, v, "add_slot")
	if !sub.Granted || sub.GrantedBy != "技术维护组甲" || sub.GrantedAt == nil {
		t.Errorf("已批准的那条状态不全: %+v", sub)
	}
	if slot.Granted || slot.GrantedBy != "" {
		t.Errorf("没勾的那条被记成已批准: %+v", slot)
	}
	// 服务端那句说明必须逐条带上：批准时只看到作者自述的 reason，等于只是信了那个人
	if sub.ScopeNote == "" || slot.ScopeNote == "" {
		t.Errorf("两条都缺服务端说明: %q / %q", sub.ScopeNote, slot.ScopeNote)
	}
}

// 勾一条他没申请过的 ⇒ 当场点名拒绝，并且什么都不留。
// 静默丢掉那一条更糟：人以为批了 3 条、实际落了 2 条，是最难查的那种"点了没生效"。
func TestAuthorizeRejectsKeyThatWasNeverAppliedFor(t *testing.T) {
	m, _, _ := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	err := m.Authorize(id, s5Op(), []string{"submit_news", "delete_everything"})
	if err == nil || !strings.Contains(err.Error(), "delete_everything") {
		t.Fatalf("没申请过的动作该点名拒掉，实际 %v", err)
	}
	if modules.Has(id) {
		t.Error("授权失败了却进了注册表")
	}
	if n := trustRowCount(t); n != 0 {
		t.Errorf("信任表被写脏: %d 行", n)
	}
	if totalGrantRows(t, id) != 0 {
		t.Error("批准表被写脏")
	}
}

// 一条都不批是合法决定：读数照常，界面里一个按钮都不长。
func TestAuthorizeWithZeroGrantsIsReadOnly(t *testing.T) {
	m, enf, _ := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), nil); err != nil {
		t.Fatalf("只批读数也该授权成功: %v", err)
	}
	if !canEnforce(t, enf, model.RoleMember, DataEndpoint(id), "GET") {
		t.Error("只批读数却连读都没放开")
	}
	if canEnforce(t, enf, model.RoleMember, ActionEndpoint(id), "POST") {
		t.Error("一条都没批却放开了动作端点：Casbin 这道门先开着，GrantGate 就成了唯一那道")
	}
	mod, ok := registeredModule(t, id)
	if !ok {
		t.Fatal("有只读组件的插件即便不批动作也该在清单里")
	}
	if len(mod.Actions) != 0 || buttonKeysOf(mod) != "" {
		t.Errorf("批准子集里混进了动作: %q / 按钮 %q", actionKeysOf(mod), buttonKeysOf(mod))
	}
	// 路由照旧挂着（gin 启动期定死）：直接打它得到的是"没被批准"，不是 404
	found := false
	for _, pid := range m.ActionCapableIDs() {
		found = found || pid == id
	}
	if !found {
		t.Error("申请过动作的插件不该在重启前就没挂动作端点")
	}
}

// 只有按钮的插件 + 一条都没批 ⇒ 清单里不该有它（没有能摆的入口），
// 但它确实是已授权、在运行的：把这种状态说成"授权失败"是骗人。
func TestButtonOnlyPluginWithZeroGrantsStaysAuthorized(t *testing.T) {
	m, enf, _ := s5Boot(t, buttonOnlyManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), nil); err != nil {
		t.Fatalf("纯按钮插件只批读数（等于什么都不批）也该成功: %v", err)
	}
	if modules.Has(id) {
		t.Error("一个入口都不剩却登记进了清单：侧栏会多出一个点开空白的页")
	}
	if v := viewOf(t, m); v.State != StateRunning {
		t.Errorf("它应该是在运行的已授权插件，实际 %q", v.State)
	}
	if !canEnforce(t, enf, model.RoleMember, DataEndpoint(id), "GET") {
		t.Error("没登记进清单就不该放读数？—— 授权与有没有界面入口是两件事，这里读权限该照常")
	}
	if why := m.GrantGate(id, "submit_news"); why == "" {
		t.Error("没登记进清单却可以发起动作")
	}
	// 撤整个插件时不能因为"清单里没有它"就报状态错配
	if err := m.Revoke(id, Operator{ID: 2, Name: "乙"}); err != nil {
		t.Fatalf("撤销一个没登记进清单的已授权插件失败: %v", err)
	}
}

// ---- 改批准：一个独立的决定，另一条留痕 --------------------------------------

// 批 A 改成批 B：库里的旧行标撤、新行生效，注册表跟着换，策略不用动（两条都还在同一条端点后头）。
func TestUpdateGrantsSwapsWhichButtonExists(t *testing.T) {
	m, enf, _ := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), []string{"submit_news"}); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if err := m.UpdateGrants(id, Operator{ID: 2, Name: "技术维护组乙"}, []string{"add_slot"}); err != nil {
		t.Fatalf("改批准失败: %v", err)
	}

	if got := grantRowsOf(t, id); strings.Join(got, ",") != "add_slot" {
		t.Errorf("当前有效的批准应换成 add_slot，实际 %v", got)
	}
	if totalGrantRows(t, id) != 2 {
		t.Errorf("旧那一条该标撤而不是删掉（历史要留）：总行数 %d", totalGrantRows(t, id))
	}
	var old model.PluginActionGrant
	if err := repository.DB.Where("plugin_id = ? AND action_key = ?", id, "submit_news").
		First(&old).Error; err != nil {
		t.Fatal(err)
	}
	if old.RevokedAt == nil || old.RevokedByName != "技术维护组乙" {
		t.Errorf("旧批准没留下是被谁作废的: %+v", old)
	}
	mod, ok := registeredModule(t, id)
	if !ok {
		t.Fatal("改批准之后清单里没有它了")
	}
	if actionKeysOf(mod) != "add_slot" || buttonKeysOf(mod) != "add_slot" {
		t.Errorf("清单没跟着换: 动作 %q 按钮 %q", actionKeysOf(mod), buttonKeysOf(mod))
	}
	if why := m.GrantGate(id, "submit_news"); why == "" {
		t.Error("改批准后旧的那条还能发起")
	}
	if why := m.GrantGate(id, "add_slot"); why != "" {
		t.Errorf("新批的这条被拦: %s", why)
	}
	if !canEnforce(t, enf, model.RoleMember, ActionEndpoint(id), "POST") {
		t.Error("批 A 换批 B 不该动动作端点那条策略")
	}

	// 再改成"一条都不批"：按钮消失，端点策略关掉，读数照旧
	if err := m.UpdateGrants(id, s5Op(), []string{}); err != nil {
		t.Fatalf("改成一条都不批失败: %v", err)
	}
	if canEnforce(t, enf, model.RoleMember, ActionEndpoint(id), "POST") {
		t.Error("一条都不批之后动作端点还开着")
	}
	if !canEnforce(t, enf, model.RoleMember, DataEndpoint(id), "GET") {
		t.Error("关写通道顺手把读数也关了：撤的是批准，不是授权")
	}
	mod, _ = registeredModule(t, id)
	if buttonKeysOf(mod) != "" {
		t.Errorf("按钮没跟着撤干净: %q", buttonKeysOf(mod))
	}
}

// 从"一条都不批"改回"批一条"，注册表要能把之前没登记的那份补回去。
// 这条路只有纯按钮型插件会走，也是 Replace 必须"不在册就登记"的原因。
func TestUpdateGrantsRegistersButtonOnlyPluginAgain(t *testing.T) {
	m, _, _ := s5Boot(t, buttonOnlyManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), []string{"submit_news"}); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if !modules.Has(id) {
		t.Fatal("批了一条按钮就该在清单里")
	}
	if err := m.UpdateGrants(id, s5Op(), nil); err != nil {
		t.Fatalf("改成一条都不批失败: %v", err)
	}
	if modules.Has(id) {
		t.Error("摘掉入口这一步没做：清单里剩一个长按钮的页")
	}
	if err := m.UpdateGrants(id, s5Op(), []string{"submit_news"}); err != nil {
		t.Fatalf("再批回来失败: %v", err)
	}
	mod, ok := registeredModule(t, id)
	if !ok {
		t.Fatal("再批一条之后没回到清单里")
	}
	if buttonKeysOf(mod) != "submit_news" {
		t.Errorf("回来的按钮不对: %q", buttonKeysOf(mod))
	}
}

// 没授权的插件谈不上批准它的动作；勾一条没申请的同样点名拒。
func TestUpdateGrantsRejectsBadRequests(t *testing.T) {
	m, _, _ := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.UpdateGrants(id, s5Op(), []string{"submit_news"}); err == nil ||
		!strings.Contains(err.Error(), "还没授权") {
		t.Fatalf("未授权就改批准该被拒，实际 %v", err)
	}
	if totalGrantRows(t, id) != 0 {
		t.Error("被拒的请求还是写了批准表")
	}
	if err := m.Authorize(id, s5Op(), nil); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if err := m.UpdateGrants(id, s5Op(), []string{"submit_new"}); err == nil ||
		!strings.Contains(err.Error(), "submit_new") {
		t.Fatalf("改批准时勾了没申请的 key 也要点名拒，实际 %v", err)
	}
	if activeGrantCount(t, id) != 0 {
		t.Error("被拒的改批准动了库里的有效批准")
	}
	mod, _ := registeredModule(t, id)
	if buttonKeysOf(mod) != "" {
		t.Errorf("被拒的改批准还改了清单: %q", buttonKeysOf(mod))
	}
}

// 重复点授权不该顺手把批准改掉：那是两个决定、两条留痕。
func TestDuplicateAuthorizeDoesNotTouchGrants(t *testing.T) {
	m, _, _ := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), []string{"submit_news"}); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	err := m.Authorize(id, Operator{ID: 3, Name: "丙"}, []string{"add_slot"})
	if err == nil || !strings.Contains(err.Error(), "改批准") {
		t.Fatalf("重复授权该被拒并指路到「改批准」，实际 %v", err)
	}
	if got := grantRowsOf(t, id); strings.Join(got, ",") != "submit_news" {
		t.Errorf("那一次失败的重复点击改掉了批准: %v", got)
	}
}

// ---- 撤销与重启 --------------------------------------------------------------

// 撤销整插件 ⇒ 批准逐条标撤；重新授权时必须重新勾一遍，上一条都不会自己回来。
func TestRevokeMarksEveryGrantAndReauthorizeStartsClean(t *testing.T) {
	m, _, _ := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), []string{"submit_news", "add_slot"}); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if err := m.Revoke(id, Operator{ID: 2, Name: "技术维护组乙"}); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if activeGrantCount(t, id) != 0 {
		t.Errorf("撤销后还有 %d 条有效批准：整插件下线却没关掉写通道", activeGrantCount(t, id))
	}
	if totalGrantRows(t, id) != 2 {
		t.Error("批准行被删了：那半年后就答不出当时批过什么")
	}
	if v := viewOf(t, m); v.State != StatePending {
		t.Errorf("撤销后应回到待授权，实际 %q", v.State)
	}

	if err := m.Authorize(id, s5Op(), nil); err != nil {
		t.Fatalf("重新授权失败: %v", err)
	}
	if activeGrantCount(t, id) != 0 {
		t.Error("重新授权把上次的批准自动续回来了")
	}
	if why := m.GrantGate(id, "submit_news"); why == "" {
		t.Error("续回来的那条现在还能发起")
	}
}

// 重启对账：批准绑的是**当时那份文件**。把某条批准的 exec_hash 改成别的文件的，
// 下一次 Boot 它就不该生效——但读数照常，因为"这个插件可以跑"是另一条事实。
func TestBootIgnoresGrantsBoundToAnotherFile(t *testing.T) {
	m, _, root := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), []string{"submit_news", "add_slot"}); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if err := repository.DB.Model(&model.PluginActionGrant{}).
		Where("plugin_id = ? AND action_key = ?", id, "add_slot").
		Update("exec_hash", strings.Repeat("0", 64)).Error; err != nil {
		t.Fatal(err)
	}
	m.StopAll()
	modules.Unregister(id)

	_, enf2 := setupPolicyEnforcer(t)
	second := NewManagerWithAnchors(root, enf2, testAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("第二次 Boot 失败: %v", err)
	}
	got := grantRowsOf(t, id)
	if strings.Join(got, ",") != "submit_news,add_slot" {
		t.Logf("库里两条都还标着有效（改 exec_hash 只是模拟换文件，不标撤）: %v", got)
	}
	if why := second.GrantGate(id, "add_slot"); why == "" {
		t.Error("绑着另一份文件的批准在这次重启后仍然可用")
	}
	if why := second.GrantGate(id, "submit_news"); why != "" {
		t.Errorf("指纹相符的那一条被误伤: %s", why)
	}
	mod, ok := registeredModule(t, id)
	if !ok {
		t.Fatal("批准范围缩水不该让整个插件掉出清单")
	}
	if actionKeysOf(mod) != "submit_news" {
		t.Errorf("注册表里的动作不是那份文件被批准过的那条: %q", actionKeysOf(mod))
	}
	if !canEnforce(t, enf2, model.RoleMember, DataEndpoint(id), "GET") {
		t.Error("重启后读数策略没恢复")
	}
	if !canEnforce(t, enf2, model.RoleMember, ActionEndpoint(id), "POST") {
		t.Error("还剩一条有效批准，动作端点策略却没了")
	}
}

// 批准记录读不出来时（这次没有那张表），加载器要照常把读数放开、动作一律按未批准。
// 这条走的是 Boot 里那个 gerr 分支：少一张表不该让全校插件下线，但绝不能默认放行。
func TestBootSurvivesUnreadableGrantsTable(t *testing.T) {
	m, _, root := s5Boot(t, twoActionManifest(s5ID()))
	id := s5ID()
	if err := m.Authorize(id, s5Op(), []string{"submit_news"}); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	m.StopAll()
	modules.Unregister(id)
	if err := repository.DB.Migrator().DropTable(&model.PluginActionGrant{}); err != nil {
		t.Fatal(err)
	}

	_, enf2 := setupPolicyEnforcer(t)
	second := NewManagerWithAnchors(root, enf2, testAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("批准表读不出来时 Boot 失败了：读数那一半本来与该照常: %v", err)
	}
	if !modules.Has(id) {
		t.Error("批准表坏了却把整个插件下线了")
	}
	if why := second.GrantGate(id, "submit_news"); why == "" {
		t.Error("读不到批准记录时默认放行写动作")
	}
}
