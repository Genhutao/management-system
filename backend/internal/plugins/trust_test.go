package plugins

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
	"xgh-system/internal/repository"
)

// 这一层验的是信任门本身：丢文件 ≠ 上线、授权要三步齐全才算成、撤销要立刻说话，
// 以及重启以后"该自动回来的自动回来、该拦住的拦住"。
// 桩是真进程：id 写成 stub-<模式>，加载器递给子进程的唯一环境变量 XGH_PLUGIN_ID
// 正好就是模式名（见 TestMain），所以不需要为测试再放宽环境变量的边界。

func stubPluginDir(t *testing.T, root, mode string, policy []string) string {
	t.Helper()
	m := validManifest(stubIDPrefix + mode)
	m.Policy = policy
	return stubPluginDirWith(t, root, mode, m)
}

// stubPluginDirWith 同一个桩目录，但清单由调用方给：S5 的用例要的是"申请了两三条写动作"
// 那份，而 id 必须仍是 stub-<mode>——桩进程靠它认自己该演哪一出。
func stubPluginDirWith(t *testing.T, root, mode string, m Manifest) string {
	t.Helper()
	id := stubIDPrefix + mode
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建插件目录失败: %v", err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, defaultExecName())
	if err := os.Link(src, target); err != nil {
		// 硬链接不成（跨卷、文件系统不支持）就退回整份复制；再不成明确跳过。
		// 拿"差不多是个进程"的东西凑出来的绿灯，比没有绿灯更坏。
		if err := copyFileTo(src, target); err != nil {
			t.Skipf("这台机器上造不出可执行的桩: link 与 copy 都失败")
		}
	}
	signDir(t, dir) // 签名准入之后，"合法的桩"必然带签名（按摘要签，不把几十 MB 的测试二进制读进内存）
	return dir
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func useTrustDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.PluginTrust{}, &model.PluginActionGrant{}); err != nil {
		t.Fatalf("建信任表/批准表失败: %v", err)
	}
	prev := repository.DB
	repository.DB = db
	t.Cleanup(func() { repository.DB = prev })
}

// grantRowsOf 库里该插件**当前有效**的批准（按清单顺序无关，只取 key）。
func grantRowsOf(t *testing.T, id string) []string {
	t.Helper()
	var rows []model.PluginActionGrant
	if err := repository.DB.Where("plugin_id = ? AND revoked_at IS NULL", id).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ActionKey)
	}
	return out
}

func trustRowCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := repository.DB.Model(&model.PluginTrust{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func cleanupRegistry(id string) {
	modules.Unregister(id)
}

func TestBootLeavesUntrustedPluginAlone(t *testing.T) {
	root := t.TempDir()
	id := stubIDPrefix + "normal"
	stubPluginDir(t, root, "normal", nil)
	useTrustDB(t)

	m := NewManagerWithAnchors(root, nil, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	views := m.Views()
	if len(views) != 1 {
		t.Fatalf("应列出 1 个插件，实际 %d", len(views))
	}
	if views[0].State != StatePending {
		t.Errorf("丢进来的插件默认应是待授权，实际 %q", views[0].State)
	}
	if modules.Has(id) {
		t.Error("扫描阶段就登记进清单了 —— 五角色里现在有人能看见一个没人授权过的入口")
	}
	if n := trustRowCount(t); n != 0 {
		t.Errorf("信任表被扫描写脏: %d 行", n)
	}
}

// 验收①②③的主干：未授权时清单里没有它 → 授权后注册表与 Casbin 同时翻面 →
// 撤销后立刻消失。三步都在同一份真 enforcer 上跑。
func TestAuthorizeAndRevokeFullCycle(t *testing.T) {
	root := t.TempDir()
	mode := "normal"
	id := stubIDPrefix + mode
	stubPluginDir(t, root, mode, []string{model.RoleMember})
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	obj := DataEndpoint(id)
	if ok, _ := enf.Enforce("role:"+model.RoleMember, obj, "GET"); ok {
		t.Fatal("授权前部员就能读插件端点")
	}

	op := Operator{ID: 1, Name: "技术维护组甲"}
	if err := m.Authorize(id, op, nil); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if !modules.Has(id) {
		t.Error("授权后没进注册表，清单不会出现这个模块")
	}
	if ok, _ := enf.Enforce("role:"+model.RoleMember, obj, "GET"); !ok {
		t.Error("授权后部员仍读不到该端点")
	}
	if v := m.Views()[0]; v.State != StateRunning {
		t.Errorf("授权后状态应是运行中，实际 %q", v.State)
	}
	var row model.PluginTrust
	if err := repository.DB.First(&row, "id = ?", id).Error; err != nil {
		t.Fatalf("信任记录没落库: %v", err)
	}
	if row.TrustedByName != "技术维护组甲" || row.RevokedAt != nil {
		t.Errorf("信任记录内容不对: %+v", row)
	}
	p := m.entries[id].plugin
	if row.ExecHash != p.ExecHash || row.ManifestHash != p.ManifestHash {
		t.Error("记下来的指纹不是授权那一刻的指纹")
	}

	if err := m.Revoke(id, Operator{ID: 2, Name: "技术维护组乙"}); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if modules.Has(id) {
		t.Error("撤销后还在注册表里：清单会继续列出它")
	}
	if ok, _ := enf.Enforce("role:"+model.RoleMember, obj, "GET"); ok {
		t.Error("撤销后部员仍可读该端点：面板说下线了，门其实还开着")
	}
	if v := m.Views()[0]; v.State != StatePending {
		t.Errorf("撤销后应回到待授权，实际 %q", v.State)
	}
	if err := repository.DB.First(&row, "id = ?", id).Error; err != nil {
		t.Fatalf("撤销记录被删掉了（历史要留着才答得出谁撤的）: %v", err)
	}
	if row.RevokedAt == nil || row.RevokedByName != "技术维护组乙" {
		t.Errorf("撤销没留下执行人: %+v", row)
	}
}

// 授权失败必须一点痕迹都不留：注册表、Casbin、信任表三处都不能动过。
func TestFailedAuthorizeLeavesNoResidue(t *testing.T) {
	root := t.TempDir()
	mode := "protocol2"
	id := stubIDPrefix + mode
	stubPluginDir(t, root, mode, []string{model.RoleMember})
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err == nil {
		t.Fatal("插件自报协议版本不对，授权不该成功")
	}
	if modules.Has(id) {
		t.Error("授权失败却进了注册表")
	}
	if ok, _ := enf.Enforce("role:"+model.RoleMember, DataEndpoint(id), "GET"); ok {
		t.Error("授权失败却留下了策略")
	}
	if n := trustRowCount(t); n != 0 {
		t.Errorf("授权失败却在信任表里留了记录: %d 行", n)
	}
	if v := m.Views()[0]; v.State != StatePending {
		t.Errorf("失败之后状态该退回待授权，实际 %q", v.State)
	}
}

// 验收④：重启以后已授权且指纹一致的自动回来，不需要人再点一次。
func TestBootAutoRecoversTrustedPlugin(t *testing.T) {
	root := t.TempDir()
	mode := "normal"
	id := stubIDPrefix + mode
	stubPluginDir(t, root, mode, nil)
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	first := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { first.StopAll(); cleanupRegistry(id) })
	if err := first.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := first.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	first.StopAll()
	modules.Unregister(id)

	// 换一个 Manager 等于重启一次主程序
	second := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("第二次 Boot 失败: %v", err)
	}
	if !modules.Has(id) {
		t.Error("已授权的插件重启后没自动登记进清单")
	}
	if v := second.Views()[0]; v.State != StateRunning {
		t.Errorf("重启后应直接是运行中，实际 %q（原因：%s）", v.State, v.Reason)
	}
	// 自动恢复不该把授权人改写成别人，也不该多出第二行
	var row model.PluginTrust
	if err := repository.DB.First(&row, "id = ?", id).Error; err != nil {
		t.Fatalf("信任记录丢了: %v", err)
	}
	if row.TrustedByName != "甲" {
		t.Errorf("自动恢复不该改授权人: %+v", row)
	}
	if n := trustRowCount(t); n != 1 {
		t.Errorf("重启一次多一条信任记录的话，历史就没人读得懂: %d 行", n)
	}
}

// 验收⑤：换过文件就不拉起，并且要说清是"需重新授权"而不是"待授权"。
// 这里改的是 manifest（加一个角色）——那正是信任门最怕的一种偷换：
// 目录名没变、可执行文件没变，权限却悄悄大了。
func TestBootBlocksOnFingerprintChange(t *testing.T) {
	root := t.TempDir()
	mode := "normal"
	id := stubIDPrefix + mode
	wide := []string{model.RoleDormManager, model.RoleMember, model.RoleMinister, model.RoleViewerExport}
	dir := stubPluginDir(t, root, mode, wide)
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	// 授权时把四个非技术角色一起放开，才看得出"重启后指纹不符有没有照样注入"
	if err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	for _, role := range wide {
		if ok, _ := enf.Enforce("role:"+role, DataEndpoint(id), "GET"); !ok {
			t.Fatalf("夹具没铺好：授权后 %s 反而读不到", role)
		}
	}
	m.StopAll()
	modules.Unregister(id)

	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tampered Manifest
	if err := json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	// 只改标题：目录名没变、可执行文件没变，但"授权绑的是内容"要落到每一次内容变化上
	tampered.Title = "换了个标题"
	write, _ := json.Marshal(tampered)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), write, 0o644); err != nil {
		t.Fatal(err)
	}
	// 改完**重签**：这才是"作者交付了另一份签过名的东西"，也就是 stale 的真实场景。
	// 改了不重签是另一种更坏的情况（连作者都没签过这份），由 TestTamperedFileOutranksStale 管。
	signDir(t, dir)

	// 换一个 enforcer 才等于换了一次进程：内存策略本来就不该跨重启活下来，
	// 复用同一个会把"上一世注入的"和"这一次没注入的"搅成一锅。
	_, enf2 := setupPolicyEnforcer(t)
	second := NewManagerWithAnchors(root, enf2, testAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("第二次 Boot 失败: %v", err)
	}
	v := second.Views()[0]
	if v.State != StateStale {
		t.Errorf("文件变过应标需重新授权，实际 %q", v.State)
	}
	if modules.Has(id) {
		t.Error("指纹不符还被登记进清单了 —— 授权绑定的是文件内容这句话没做到")
	}
	// 四个角色的读权限一条都不许有：指纹不符就不注入，宁可少给不可多给
	for _, role := range wide {
		if ok, _ := enf2.Enforce("role:"+role, DataEndpoint(id), "GET"); ok {
			t.Errorf("指纹不符却仍给 %s 注入了策略", role)
		}
	}
	// 重新授权一次要能恢复正常：被拦下不等于永久封死
	if err := second.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err != nil {
		t.Fatalf("改回文件后重新授权应成功: %v", err)
	}
	if second.Views()[0].State != StateRunning {
		t.Errorf("重新授权后应运行中，实际 %q", second.Views()[0].State)
	}
}

// stale 状态下点撤销必须干干净净地成功：这种插件的清单里本来就没有它，
// "注册表里摘不到"是常态而不是错配 —— 报一个错出来会让人以为撤销没生效。
func TestRevokeOfStalePluginIsClean(t *testing.T) {
	root := t.TempDir()
	mode := "normal"
	id := stubIDPrefix + mode
	dir := stubPluginDir(t, root, mode, nil)
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	m.StopAll()
	modules.Unregister(id)
	// 只改一个字段就够：内容变了，指纹就对不上了
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"`+id+`","title":"改了","icon":"fa","tab":"`+id+`","widgets":[{"key":"k","type":"stat","label":"L"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	signDir(t, dir) // 作者签了另一份 ⇒ stale；改了不重签是 sig_broken，另一个用例管
	_, enf2 := setupPolicyEnforcer(t)
	second := NewManagerWithAnchors(root, enf2, testAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("第二次 Boot 失败: %v", err)
	}
	if v := second.Views()[0]; v.State != StateStale {
		t.Fatalf("夹具应是需重新授权状态，实际 %q", v.State)
	}
	if err := second.Revoke(id, Operator{ID: 2, Name: "乙"}); err != nil {
		t.Fatalf("撤销 stale 插件不该报错: %v", err)
	}
	if v := second.Views()[0]; v.State != StatePending {
		t.Errorf("撤销后应回到待授权，实际 %q", v.State)
	}
}

// 撤销过的插件重启后不许自己回来：撤销是人的决定，和授权一样要跨重启说话。
func TestBootKeepsRevokedDown(t *testing.T) {
	root := t.TempDir()
	mode := "normal"
	id := stubIDPrefix + mode
	stubPluginDir(t, root, mode, nil)
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if err := m.Revoke(id, Operator{ID: 1, Name: "甲"}); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	m.StopAll()

	second := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("第二次 Boot 失败: %v", err)
	}
	if modules.Has(id) {
		t.Error("已撤销的插件重启后自己回来了")
	}
	if v := second.Views()[0]; v.State != StatePending {
		t.Errorf("应回到待授权，实际 %q", v.State)
	}
}

// pluginstatus 那四个数的口径要与现实一致：验收⑦。
func TestStatsMatchReality(t *testing.T) {
	root := t.TempDir()
	mode := "normal"
	id := stubIDPrefix + mode
	stubPluginDir(t, root, "normal", nil)
	// 一个坏 manifest（校验失败）
	bad := filepath.Join(root, "bad-one")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "manifest.json"), []byte(`{"id":"bad-one",`), 0o644); err != nil {
		t.Fatal(err)
	}
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	s := m.Stats()
	if s.Pending != 1 || s.Attention != 1 || s.Loaded != 0 || s.Running != 0 {
		t.Fatalf("没授权之前该是 待授权1/校验失败1/其余0，实际 %+v", s)
	}
	if err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	s = m.Stats()
	if s.Loaded != 1 || s.Running != 1 || s.Pending != 0 {
		t.Fatalf("授权之后该是 已加载1/运行中1，实际 %+v", s)
	}
	if err := m.Revoke(id, Operator{ID: 1, Name: "甲"}); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	s = m.Stats()
	if s.Loaded != 0 || s.Running != 0 || s.Pending != 1 {
		t.Fatalf("撤销之后该退回 待授权1，实际 %+v", s)
	}
}
