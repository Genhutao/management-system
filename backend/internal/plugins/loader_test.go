package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
)

// ---- 测试用的开发者密钥与信任锚 ----------------------------------------------
// 计划 §3.2 拍了"私钥由使用方本人保管"，所以这里**不出现任何真实密钥、也不落盘**：
// 每次跑测试现生成一把内存里的测试密钥，进程一退就没了。
// 用 sync.Once 而不是 TestMain：本包的 TestMain 已被 supervisor_test 的"自我 re-exec
// 当子进程桩"占了，一个包只能有一个。
var (
	testKeyOnce sync.Once
	testKeyPriv ed25519.PrivateKey
)

func testKey() ed25519.PrivateKey {
	testKeyOnce.Do(func() {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic("生成测试密钥失败: " + err.Error())
		}
		testKeyPriv = priv
	})
	return testKeyPriv
}

func testAnchors() *AnchorSet {
	pub := testKey().Public().(ed25519.PublicKey)
	return NewAnchorSet([]string{base64.StdEncoding.EncodeToString(pub)}, "测试密钥")
}

// noAnchors 一把公钥都没有的锚集合：验签一律拒（失败关闭）。
func noAnchors() *AnchorSet { return NewAnchorSet(nil, "空") }

// signDir 用测试密钥给一个目录签名。实现放在 sign_test.go 的 signWith，
// 两边共用一份：测试里"怎么签"出现两套写法，迟早一套与加载器的读法不一致。
func signDir(t *testing.T, dir string) {
	t.Helper()
	signWith(t, dir, testKey())
}

// writePlugin 造一个插件目录：manifest 传结构体（走真实序列化，字段名和标签一起验），
// exec 传内容；exec 为空表示"清单写了但文件不在"。
//
// 默认**顺手签好**：签名准入之后"合法的插件"必然带签名，每个用例再各自签一遍只会
// 把真正要测的东西埋掉。要测未签名的分支，用 writeUnsignedPlugin 或删掉 signature 文件。
func writePlugin(t *testing.T, root, dirName string, m Manifest, exec string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建插件目录失败: %v", err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("序列化 manifest 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatalf("写 manifest 失败: %v", err)
	}
	name := m.Exec
	if name == "" {
		name = defaultExecName()
	}
	if exec != "" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(exec), 0o755); err != nil {
			t.Fatalf("写 exec 失败: %v", err)
		}
		signDir(t, dir) // 有可执行文件才签得成；exec 为空的用例本来就要被"文件不在"挡住
	}
	return dir
}

func validManifest(id string) Manifest {
	return Manifest{
		ID: id, Title: "磁盘用量", Icon: "fa-solid fa-hard-drive", Group: "运维", Tab: id,
		Widgets: []ManifestWidget{{Key: "data_disk", Type: modules.WidgetStat, Label: "数据盘"}},
	}
}

func TestScanAcceptsGoodManifest(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "diskusage", validManifest("diskusage"), "MZ fake binary")

	got := Scan(root, testAnchors())
	if len(got) != 1 {
		t.Fatalf("应扫出 1 个插件，实际 %d", len(got))
	}
	p := got[0]
	if p.State != StatePending {
		t.Fatalf("扫到但没授权，状态应是待授权，实际 %q（原因：%s）", p.State, p.Reason)
	}
	if p.Reason != "" {
		t.Errorf("校验通过不该带原因: %q", p.Reason)
	}
	// 指纹是 sha256 的十六进制：长度不对就说明算错了对象（比如把目录名拿去 hash 了）
	if len(p.ExecHash) != 64 || len(p.ManifestHash) != 64 {
		t.Errorf("指纹长度不符: exec=%d manifest=%d", len(p.ExecHash), len(p.ManifestHash))
	}
	// 缺省必须落在最小那一档
	if strings.Join(p.PolicyRoles, ",") != model.RoleTechAdmin {
		t.Errorf("不写 policy 应只给技术维护组，实际 %v", p.PolicyRoles)
	}
	if p.Module.ID != "diskusage" || p.Module.Tab != "diskusage" {
		t.Errorf("描述符没照 manifest 拼出来: %+v", p.Module)
	}
	// 数据端口由 id 推导，且清单里的可见性判定靠的就是这个路径
	if ep := p.Module.Widgets[0].DataEndpoint; ep != "/api/v1/mod/diskusage" {
		t.Errorf("data_endpoint 应是 /api/v1/mod/diskusage，实际 %q", ep)
	}
	// 待授权的东西绝不能进注册表：进了就等于"丢文件即上线"
	if modules.Has("diskusage") {
		t.Error("扫描阶段就把插件注册了 —— 信任门形同虚设")
	}
}

func TestScanRejectsBadManifestsWithoutKillingTheService(t *testing.T) {
	root := t.TempDir()

	cases := []struct {
		name  string
		write func(t *testing.T) // 往 root 里造一个坏插件
		want  string             // Reason 里必须出现的话
	}{
		{
			name: "字段名写错（policy 多写了个 s）",
			write: func(t *testing.T) {
				dir := filepath.Join(root, "typo")
				_ = os.MkdirAll(dir, 0o755)
				_ = os.WriteFile(filepath.Join(dir, "manifest.json"),
					[]byte(`{"id":"typo","title":"T","icon":"fa","tab":"typo","policies":["member"],"widgets":[{"key":"k","type":"stat","label":"L"}]}`), 0o644)
				_ = os.WriteFile(filepath.Join(dir, defaultExecName()), []byte("x"), 0o755)
			},
			want: "unknown field", // 写错字段名必须当场炸出来，不能悄悄按"没写"处理成只给技术组
		},
		{
			name: "JSON 少个括号",
			write: func(t *testing.T) {
				dir := filepath.Join(root, "broken")
				_ = os.MkdirAll(dir, 0o755)
				_ = os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"id":"broken",`), 0o644)
			},
			want: "解析失败",
		},
		{
			name: "一个文件里塞两份清单",
			write: func(t *testing.T) {
				dir := filepath.Join(root, "twice")
				_ = os.MkdirAll(dir, 0o755)
				m := validManifest("twice")
				raw, _ := json.Marshal(m)
				_ = os.WriteFile(filepath.Join(dir, "manifest.json"), append(append(raw, '\n'), raw...), 0o644)
				_ = os.WriteFile(filepath.Join(dir, defaultExecName()), []byte("x"), 0o755)
			},
			want: "多余内容",
		},
		{
			name: "契约版本对不上",
			write: func(t *testing.T) {
				m := validManifest("future")
				v := modules.ManifestVersion + 1
				m.MinManifestVersion = &v
				writePlugin(t, root, "future", m, "x")
			},
			want: "min_manifest_version",
		},
		{
			name: "id 带大写",
			write: func(t *testing.T) {
				m := validManifest("DiskCount")
				m.Tab = "DiskCount"
				writePlugin(t, root, "upper", m, "x")
			},
			want: "不合法",
		},
		{
			name: "tab 里塞引号（前端要拿它拼 DOM id）",
			write: func(t *testing.T) {
				m := validManifest("xss")
				m.Tab = `a" onclick="alert(`
				writePlugin(t, root, "xss", m, "x")
			},
			want: "不合法",
		},
		{
			name: "组件用了没实现的类型",
			write: func(t *testing.T) {
				m := validManifest("chartplug")
				m.Widgets[0].Type = "chart"
				writePlugin(t, root, "chartplug", m, "x")
			},
			want: "未登记类型",
		},
		{
			name: "一个组件都没有",
			write: func(t *testing.T) {
				m := validManifest("empty")
				m.Widgets = nil
				writePlugin(t, root, "empty", m, "x")
			},
			want: "一个组件都没有",
		},
		{
			name: "policy 里有未知角色",
			write: func(t *testing.T) {
				m := validManifest("rolex")
				m.Policy = []string{"superadmin"}
				writePlugin(t, root, "rolex", m, "x")
			},
			want: "未知角色",
		},
		{
			name: "清单写了 exec 但文件不在",
			write: func(t *testing.T) {
				writePlugin(t, root, "noexec", validManifest("noexec"), "")
			},
			want: "读不到可执行文件",
		},
		{
			name: "exec 想跳出插件目录",
			write: func(t *testing.T) {
				// 不走 writePlugin：它按清单里的 exec 名去写文件，而这个用例的 exec 名
				// 本来就指向目录外面，正好是加载器要拒的那个动作
				dir := filepath.Join(root, "escape")
				_ = os.MkdirAll(dir, 0o755)
				m := validManifest("escape")
				m.Exec = filepath.Join("..", "elsewhere", "evil.exe")
				raw, _ := json.Marshal(m)
				_ = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644)
			},
			want: "不能带路径",
		},
	}

	// 一个用例一根目录：Scan 的插件数上限会把多出来的目录一律判成"超上限"，
	// 挤在一起跑，排在后面的用例真正要验的那个失败点就被盖住了。
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root = t.TempDir()
			tc.write(t)
			got := Scan(root, testAnchors())
			if len(got) != 1 {
				t.Fatalf("本用例只造了一个目录，应扫出 1 条，实际 %d 条：%s", len(got), reasonsOf(got))
			}
			p := got[0]
			if p.State != StateInvalid {
				t.Fatalf("应判为校验失败，实际 %q（原因：%s）", p.State, p.Reason)
			}
			if !strings.Contains(p.Reason, tc.want) {
				t.Errorf("原因里该有 %q，实际 %q", tc.want, p.Reason)
			}
			// 被拒的东西绝不能顺手进注册表：那等于"校验失败也上线了"
			if p.Module.ID != "" && modules.Has(p.Module.ID) {
				t.Errorf("%s 已被拒却仍占着注册表 id %q", filepath.Base(p.Dir), p.Module.ID)
			}
		})
	}
}

func reasonsOf(list []*Plugin) string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, filepath.Base(p.Dir)+"="+p.Reason)
	}
	return strings.Join(out, " | ")
}

// 坏 manifest 不许连坐：一个目录写坏了，同批里别的插件照常扫出来。
// 这就是方案 §5 对 v1 的那处口径修正——一个手滑的 JSON 逗号不该放倒全校系统，
// 因为信任门本来就把所有丢入物挡在运行之外，面板又给了展示位。
func TestBadManifestDoesNotAffectNeighbours(t *testing.T) {
	root := t.TempDir()

	bad := filepath.Join(root, "aaa-broken")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "manifest.json"), []byte(`{"id":"aaa-broken",`), 0o644); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, root, "bbb-good", validManifest("bbb-good"), "real bytes")
	writePlugin(t, root, "ccc-nobin", validManifest("ccc-nobin"), "")

	got := Scan(root, testAnchors())
	if len(got) != 3 {
		t.Fatalf("三条都该列出来（含坏的那条），实际 %d：%s", len(got), reasonsOf(got))
	}
	byName := map[string]*Plugin{}
	for _, p := range got {
		byName[filepath.Base(p.Dir)] = p
	}
	if got := byName["bbb-good"]; got == nil || got.State != StatePending {
		t.Errorf("好那个不该被坏邻居拖累，实际 %+v", got)
	}
	if got := byName["ccc-nobin"]; got == nil || got.State != StateInvalid {
		t.Errorf("缺可执行文件的那条应判校验失败，实际 %+v", got)
	}
	if got := byName["aaa-broken"]; got == nil || got.State != StateInvalid || got.Reason == "" {
		t.Errorf("坏 JSON 要列出来且带原因，实际 %+v", got)
	}
}

// 目录名与 id 无关、可以重复出现两次同一 id：后者必须被拒，且不能污染前者。
func TestScanRejectsDuplicateIDAcrossDirs(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "aaa", validManifest("dupe"), "one")
	writePlugin(t, root, "bbb", validManifest("dupe"), "two")

	got := Scan(root, testAnchors())
	if len(got) != 2 {
		t.Fatalf("两个目录都该列出来，实际 %d", len(got))
	}
	if got[0].State != StatePending {
		t.Errorf("先出现的那个应正常待授权，实际 %q: %s", got[0].State, got[0].Reason)
	}
	if got[1].State != StateInvalid || !strings.Contains(got[1].Reason, "重复") {
		t.Errorf("后出现的那个必须被拒并说明原因，实际 %q: %s", got[1].State, got[1].Reason)
	}
}

// 撞编译期模块的名字：那种 id 已经有人在用，插件不能再占。
func TestScanRejectsIDCollidingWithRegisteredModule(t *testing.T) {
	root := t.TempDir()
	if err := modules.TryRegister(modules.Module{
		ID: "residentmod", Title: "编译期模块", Tab: "residentmod",
		MinManifestVersion: modules.ManifestVersion,
		Widgets: []modules.Widget{{
			Key: "k", Type: modules.WidgetStat, Label: "L",
			DataEndpoint: "/api/v1/dashboard/summary",
		}},
	}); err != nil {
		t.Fatalf("登记编译期模块失败: %v", err)
	}
	t.Cleanup(func() { modules.Unregister("residentmod") })

	m := validManifest("residentmod")
	writePlugin(t, root, "shadow", m, "x")

	got := Scan(root, testAnchors())
	if len(got) != 1 || got[0].State != StateInvalid {
		t.Fatalf("撞了已注册 id 应判校验失败，实际 %+v", got)
	}
	if !strings.Contains(got[0].Reason, "撞车") {
		t.Errorf("原因要指名道姓说撞了谁，实际: %s", got[0].Reason)
	}
}

func TestScanEnforcesPluginCountCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxPlugins+2; i++ {
		id := "plug" + string(rune('a'+i))
		writePlugin(t, root, id, validManifest(id), "x")
	}
	got := Scan(root, testAnchors())
	if len(got) != MaxPlugins+2 {
		t.Fatalf("超上限的也要列出来，实际 %d", len(got))
	}
	var over int
	for _, p := range got {
		if p.State == StateInvalid && strings.Contains(p.Reason, "上限") {
			over++
		}
	}
	if over != 2 {
		t.Errorf("应有 2 个因超上限被拒（且带原因），实际 %d", over)
	}
}

func TestScanSkipsLooseFilesAndMissingDir(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "good", validManifest("good"), "x")
	// 运维常丢在旁边的东西：README、备份目录压缩包解开的散文件
	if err := os.WriteFile(filepath.Join(root, "README.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Scan(root, testAnchors())
	if len(got) != 1 || filepath.Base(got[0].Dir) != "good" {
		t.Fatalf("散文件不该被当成插件，实际 %+v", got)
	}
	if len(Scan(filepath.Join(root, "not-exists"), testAnchors())) != 0 {
		t.Error("目录不存在应按\"没装过插件\"处理")
	}
}

// 指纹的唯一用途就是发现"文件换了"：内容变了必须变，没变必须不变。
func TestFingerprintDetectsContentChange(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "diskusage", validManifest("diskusage"), "v1 bytes")
	before := Scan(root, testAnchors())[0]

	if err := os.WriteFile(before.ExecPath, []byte("v2 different"), 0o755); err != nil {
		t.Fatal(err)
	}
	after := Scan(root, testAnchors())[0]

	if before.ExecHash == after.ExecHash {
		t.Error("换了可执行文件内容，指纹却没变 —— 那信任门等于没有")
	}
	if before.ManifestHash != after.ManifestHash {
		t.Error("只改 exec 不该让 manifest 指纹跟着变")
	}
	if again := Scan(root, testAnchors())[0]; again.ExecHash != after.ExecHash {
		t.Error("同一份内容两次扫描指纹不稳定")
	}
}

func TestNeedsRetrust(t *testing.T) {
	now := time.Now()
	active := func(exec, manifest string) *model.PluginTrust {
		return &model.PluginTrust{ID: "p", ExecHash: exec, ManifestHash: manifest, TrustedAt: now}
	}

	p := &Plugin{ExecHash: "aaa", ManifestHash: "bbb", Trust: active("aaa", "bbb")}
	if p.NeedsRetrust() {
		t.Error("指纹一致不该要求重新授权（重启后要能自动拉起）")
	}

	p.Trust = active("zzz", "bbb")
	if !p.NeedsRetrust() {
		t.Error("可执行文件被换过必须要求重新授权")
	}

	p2 := &Plugin{ExecHash: "aaa", ManifestHash: "bbb", Trust: active("aaa", "zzz")}
	if !p2.NeedsRetrust() {
		t.Error("manifest 被换过（比如偷偷加了一个角色）同样要重新授权")
	}

	revoked := active("aaa", "bbb")
	at := now
	revoked.RevokedAt = &at
	p3 := &Plugin{ExecHash: "aaa", ManifestHash: "bbb", Trust: revoked}
	if p3.NeedsRetrust() {
		t.Error("已撤销的插件谈不上\"需要重新授权\"，它就没被授权")
	}
	if (&Plugin{ExecHash: "a"}).NeedsRetrust() {
		t.Error("从没授权过的插件不该被报成需重新授权")
	}
}
