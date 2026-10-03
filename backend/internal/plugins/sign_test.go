package plugins

// sign_test.go —— 签名准入（S0）的行为。
//
// 这一层要钉住的是三句话：
//  1. **没签名的插件进不了可授权列表**，而且绕过界面直接调 Authorize 也被拒；
//  2. 改过文件但没重签 = 比"需重新授权"更严重的一件事（连作者都没签过这份），
//     状态必须是 sig_broken 而不是 stale；
//  3. 一把可信公钥都没有时**谁都不放行**（失败关闭），而不是"那就都算通过"。
//
// 所有密钥都是测试里现生成的，不落盘、不是任何真实发布密钥（§3.2 拍了私钥由使用方保管）。

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
	"xgh-system/internal/repository"
)

// signWith 用指定私钥给一个目录签名。signDir（loader_test.go）也走这一份实现，
// 免得"测试怎么签"出现两套而其中一套与加载器的读法不一致。
func signWith(t *testing.T, dir string, priv ed25519.PrivateKey) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("读 manifest 失败: %v", err)
	}
	m, err := ManifestFrom(raw)
	if err != nil {
		t.Fatalf("manifest 读不懂: %v", err)
	}
	execPath, err := ExecPathFor(dir, m)
	if err != nil {
		t.Fatalf("定位可执行文件失败: %v", err)
	}
	execDigest, err := DigestFile(execPath)
	if err != nil {
		t.Fatalf("算可执行文件摘要失败: %v", err)
	}
	body, err := SignDigests(priv, DigestOfBytes(raw), execDigest).Marshal()
	if err != nil {
		t.Fatalf("序列化签名失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SignatureFileName), body, 0o644); err != nil {
		t.Fatalf("写签名失败: %v", err)
	}
}

func freshKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	return priv
}

func anchorsFor(t *testing.T, privs ...ed25519.PrivateKey) *AnchorSet {
	t.Helper()
	lines := make([]string, 0, len(privs))
	for _, p := range privs {
		lines = append(lines, base64.StdEncoding.EncodeToString(p.Public().(ed25519.PublicKey)))
	}
	return NewAnchorSet(lines, "测试")
}

// ---- 纯签名逻辑 --------------------------------------------------------------

func TestSignatureRoundTripAndKid(t *testing.T) {
	priv := freshKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	manifestDigest := DigestOfBytes([]byte(`{"id":"a"}`))
	execDigest := DigestOfBytes([]byte("binary bytes"))

	sig := SignDigests(priv, manifestDigest, execDigest)
	if sig.Kid != KeyID(pub) {
		t.Errorf("kid 该由公钥算出：期望 %s，实际 %s", KeyID(pub), sig.Kid)
	}
	set := anchorsFor(t, priv)
	if err := set.Verify(sig, manifestDigest, execDigest); err != nil {
		t.Fatalf("自己签的自己验不过: %v", err)
	}
	// 换一份内容就必须验不过，否则"绑内容"这句话是空的
	if err := set.Verify(sig, DigestOfBytes([]byte(`{"id":"b"}`)), execDigest); !errors.Is(err, ErrManifestChanged) {
		t.Errorf("manifest 变了该报 ErrManifestChanged，实际 %v", err)
	}
	if err := set.Verify(sig, manifestDigest, DigestOfBytes([]byte("other bytes"))); !errors.Is(err, ErrExecChanged) {
		t.Errorf("exec 变了该报 ErrExecChanged，实际 %v", err)
	}
	// 另一个人签的同名 kid 也不该混得进来
	if err := anchorsFor(t, freshKey(t)).Verify(sig, manifestDigest, execDigest); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("不认识的签名者该报 ErrUnknownKey，实际 %v", err)
	}
}

func TestEmptyAnchorsFailClosed(t *testing.T) {
	priv := freshKey(t)
	d1, d2 := DigestOfBytes([]byte("m")), DigestOfBytes([]byte("e"))
	sig := SignDigests(priv, d1, d2)
	err := noAnchors().Verify(sig, d1, d2)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("一把公钥都没有时必须拒，实际 %v", err)
	}
	// 而且要说清是"这台服务没配钥匙"，不是"你的签名有问题"——否则作者会去查自己
	if !strings.Contains(err.Error(), "没有配置任何可信开发者公钥") {
		t.Errorf("原因该指向服务端配置，实际: %v", err)
	}
}

func TestParseSignatureIsStrict(t *testing.T) {
	// 少一个字段就当"没签 exec"放行，等于把这条防线最关键的那半份悄悄拆掉
	for name, body := range map[string]string{
		"少 sig":     `{"kid":"aa","manifest":"bb","exec":"cc"}`,
		"少 exec 摘要": `{"kid":"aa","manifest":"bb","sig":"cc"}`,
		"多一个字段":     `{"kid":"aa","manifest":"bb","exec":"cc","sig":"dd","extra":1}`,
		"不是 JSON":   `nope`,
	} {
		if _, err := ParseSignature([]byte(body)); !errors.Is(err, ErrSignatureUnreadable) {
			t.Errorf("%s：该判读不懂，实际 %v", name, err)
		}
	}
}

func TestBadSignatureDetectedWhenDigestsEditedButSigKept(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "diskusage", validManifest("diskusage"), "original bytes")

	raw, err := os.ReadFile(filepath.Join(dir, SignatureFileName))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Signature
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	// 攻击者把两份摘要换成"他现在这两份文件"的摘要，但签名字段动不了
	newExec := DigestOfBytes([]byte("trojan horse"))
	onDisk.Exec = newExec
	forged, _ := json.Marshal(onDisk)
	if err := os.WriteFile(filepath.Join(dir, SignatureFileName), forged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testAnchors().Verify(onDisk, onDisk.Manifest, newExec); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("改摘要不改签名该被验签挡下，实际 %v", err)
	}
	// 加载器同一份判定要把它列成"签名不符"，而不是因为两份摘要都对得上就放行
	if p := Scan(root, testAnchors())[0]; p.State != StateSigBroken {
		t.Errorf("扫描该判 sig_broken，实际 %q（原因：%s）", p.State, p.Reason)
	}
}

// ---- 扫描期 -----------------------------------------------------------------

func TestKeysDirSitsBesidePluginsDir(t *testing.T) {
	// 这条规则决定"服务器上把公钥放哪"，写错就是加载器读不到、面板上一句"没配公钥"。
	t.Setenv("PLUGIN_KEYS_DIR", "")
	if got := KeysDirFor("./plugins"); got != filepath.Join(".", "plugin_keys") {
		t.Errorf(`默认相对布局该解析成 ./plugin_keys，实际 %q`, got)
	}
	if got := KeysDirFor("/srv/xgh/deploy/plugins"); got != filepath.Join("/srv/xgh/deploy", "plugin_keys") {
		t.Errorf("绝对布局该落在部署目录下的 plugin_keys，实际 %q", got)
	}
	want := filepath.Join(t.TempDir(), "keys")
	t.Setenv("PLUGIN_KEYS_DIR", want)
	if got := KeysDirFor("./plugins"); got != want {
		t.Errorf("PLUGIN_KEYS_DIR 该优先于默认的兄弟目录，实际 %q", got)
	}
}

func TestLoadAnchorsReadsPubFilesOnly(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	keysDir := filepath.Join(root, "plugin_keys")
	for _, d := range []string{pluginsDir, keysDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	priv := freshKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	if err := os.WriteFile(filepath.Join(keysDir, "dev.pub"),
		[]byte("# 测试开发者\n"+base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 手滑把私钥（或任何非 .pub 文件）放进来，不能被当公钥读
	if err := os.WriteFile(filepath.Join(keysDir, "dev.key"), []byte("secret-material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	set := LoadAnchors(pluginsDir)
	kid := KeyID(pub)
	found := false
	for _, k := range set.Kids() {
		if k == kid {
			found = true
		}
	}
	if !found {
		t.Fatalf("公钥目录里的 .pub 没被读进来，当前可信：%v", set.Kids())
	}
	// 读进来的这把要真能验签，否则"装好了"只是文件名对了
	d1, d2 := DigestOfBytes([]byte("m")), DigestOfBytes([]byte("e"))
	if err := set.Verify(SignDigests(priv, d1, d2), d1, d2); err != nil {
		t.Errorf("从目录读出的公钥验不过自己那把钥匙的签名: %v", err)
	}
}

func TestScanMarksUnsignedPlugin(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "diskusage", validManifest("diskusage"), "bytes")
	if err := os.Remove(filepath.Join(dir, SignatureFileName)); err != nil {
		t.Fatal(err)
	}
	p := Scan(root, testAnchors())[0]
	if p.State != StateUnsigned {
		t.Fatalf("没签名该判 unsigned，实际 %q（原因：%s）", p.State, p.Reason)
	}
	if !strings.Contains(p.Reason, "没有开发者签名") {
		t.Errorf("原因要说清缺什么，实际: %s", p.Reason)
	}
	if p.SignedBy != "" {
		t.Errorf("没有签名文件就不该有 kid，实际 %q", p.SignedBy)
	}
	// 但描述符要完整：面板得说得出"这个未签名的东西自称是什么"，运维才知道找谁
	if p.Module.ID != "diskusage" || len(p.Module.Widgets) == 0 {
		t.Errorf("未签名不该丢掉清单信息，实际 %+v", p.Module)
	}
	if modules.Has("diskusage") {
		t.Error("未签名的插件在扫描阶段就进了注册表 —— 准入等于没有")
	}
}

func TestScanMarksUnknownSignerAsUnsigned(t *testing.T) {
	root := t.TempDir()
	outsider := freshKey(t)
	dir := writePlugin(t, root, "diskusage", validManifest("diskusage"), "bytes")
	signWith(t, dir, outsider) // 签得规规矩矩，只是我们不认这个人

	p := Scan(root, testAnchors())[0]
	if p.State != StateUnsigned {
		t.Fatalf("签名者不在锚里该判 unsigned，实际 %q（原因：%s）", p.State, p.Reason)
	}
	want := KeyID(outsider.Public().(ed25519.PublicKey))
	if p.SignedBy != want {
		t.Errorf("kid 仍要显示出来（这是\"该找谁\"的线索）：期望 %s，实际 %s", want, p.SignedBy)
	}
	if !strings.Contains(p.Reason, want) {
		t.Errorf("原因里要带上是哪个 kid，实际: %s", p.Reason)
	}
}

func TestScanMarksTamperedFilesAsSigBroken(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(t *testing.T, dir string)
		want   string
	}{
		{
			name: "换可执行文件",
			tamper: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, defaultExecName()), []byte("different payload"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "可执行文件与签名时不一致",
		},
		{
			name: "改标题（清单仍是合法 JSON）",
			tamper: func(t *testing.T, dir string) {
				raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				var m Manifest
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatal(err)
				}
				m.Title = "偷偷改个名"
				write, _ := json.Marshal(m)
				if err := os.WriteFile(filepath.Join(dir, "manifest.json"), write, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "manifest.json 与签名时不一致",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := writePlugin(t, root, "diskusage", validManifest("diskusage"), "signed payload")
			tc.tamper(t, dir)
			p := Scan(root, testAnchors())[0]
			if p.State != StateSigBroken {
				t.Fatalf("该判 sig_broken，实际 %q（原因：%s）", p.State, p.Reason)
			}
			if !strings.Contains(p.Reason, tc.want) {
				t.Errorf("原因该含 %q，实际 %q", tc.want, p.Reason)
			}
		})
	}
}

// 篡改比"换了版本"更严重：已授权的插件如果磁盘上的东西连作者都没签过，
// 状态必须是 sig_broken，不能被 stale 盖掉——两者的下一步动作完全不同。
func TestTamperedFileOutranksStale(t *testing.T) {
	root := t.TempDir()
	dir := stubPluginDir(t, root, "normal", []string{model.RoleMember})
	id := filepath.Base(dir)
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

	// 改清单但**不重签**：这正是"被人偷换已授权文件"那个已知限制要暴露出来的场景。
	// （改可执行文件那条路在这里走不通——桩是测试二进制的硬链接，本机不允许覆写；
	//  摘要不符本身已由 TestScanMarksTamperedFilesAsSigBroken 两个子用例覆盖。）
	rawManifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var swapped Manifest
	if err := json.Unmarshal(rawManifest, &swapped); err != nil {
		t.Fatal(err)
	}
	swapped.Title = "被人换过的那份"
	write, _ := json.Marshal(swapped)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), write, 0o644); err != nil {
		t.Fatal(err)
	}
	_, enf2 := setupPolicyEnforcer(t)
	second := NewManagerWithAnchors(root, enf2, testAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("第二次 Boot 失败: %v", err)
	}
	v := second.Views()[0]
	if v.State != StateSigBroken {
		t.Fatalf("被换过的东西该报签名不符而不是\"需重新授权\"，实际 %q（原因：%s）", v.State, v.Reason)
	}
	if modules.Has(id) {
		t.Error("签名不符却被登记进清单了")
	}
	// 断言用 member：tech_admin 有 seed 里那条 /api/v1/mod/* 通配，
	// 它"读得到"证明不了任何事（可见性是靠注册表推导的，不是靠这条策略）。
	if ok, _ := enf2.Enforce("role:"+model.RoleMember, DataEndpoint(id), "GET"); ok {
		t.Error("签名不符却仍给 member 注入了读权限")
	}
}

// ---- 授权与启动期两处重验 ------------------------------------------------------

// 只挡前端的门等于没门：绕过界面直接调 Authorize 也必须被拒。
func TestAuthorizeRefusesUnsignedAndSigBroken(t *testing.T) {
	root := t.TempDir()
	unsigned := writePlugin(t, root, "nosig", validManifest("nosig"), "bytes")
	if err := os.Remove(filepath.Join(unsigned, SignatureFileName)); err != nil {
		t.Fatal(err)
	}
	broken := writePlugin(t, root, "tampered", validManifest("tampered"), "bytes")
	if err := os.WriteFile(filepath.Join(broken, defaultExecName()), []byte("changed after signing"), 0o755); err != nil {
		t.Fatal(err)
	}
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll() })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	for id, want := range map[string]string{"nosig": "没有可信开发者签名", "tampered": "签名与磁盘上的文件对不上"} {
		err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil)
		if err == nil {
			t.Fatalf("%s：未过签名准入却授权成功了", id)
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s：错误里该有 %q，实际 %q", id, want, err.Error())
		}
		if modules.Has(id) {
			t.Errorf("%s：授权被拒却还是进了注册表", id)
		}
	}
}

// 公钥被撤之后：这次运行不受打扰（没有运行期密钥监听，也不即时 kill），
// 但**下一次重启不再加载**——这正是使用方拍板的读法 B。
func TestBootStopsLoadingAfterKeyRevoked(t *testing.T) {
	root := t.TempDir()
	dir := stubPluginDir(t, root, "normal", []string{model.RoleMember})
	id := filepath.Base(dir)
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

	_, enf2 := setupPolicyEnforcer(t)
	// 运维把那个开发者的 .pub 从 plugin_keys/ 删了 —— 重启之后
	second := NewManagerWithAnchors(root, enf2, noAnchors())
	t.Cleanup(func() { second.StopAll() })
	if err := second.Boot(); err != nil {
		t.Fatalf("撤钥后 Boot 失败: %v", err)
	}
	v := second.Views()[0]
	if v.State != StateUnsigned {
		t.Fatalf("撤钥后该停在 unsigned，实际 %q（原因：%s）", v.State, v.Reason)
	}
	if modules.Has(id) {
		t.Error("公钥已撤，插件却被登记进了清单")
	}
	if ok, _ := enf2.Enforce("role:"+model.RoleMember, DataEndpoint(id), "GET"); ok {
		t.Error("公钥已撤，却仍给 member 注入了读权限")
	}
	// 信任表那行**不该被偷偷改写**：撤销是人的动作，撤钥只是不再加载
	if row, err := loadAllTrust(); err != nil {
		t.Fatal(err)
	} else if !row[id].Active() {
		t.Error("撤钥不该自动把信任记录标成已撤销（那是另一个动作，要留痕）")
	}
}

// ---- 面板与统计 --------------------------------------------------------------

func TestViewsCarryKidAndStatsCountSignatureStates(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "good", validManifest("good"), "bytes")
	unsigned := writePlugin(t, root, "nosig", validManifest("nosig"), "bytes")
	if err := os.Remove(filepath.Join(unsigned, SignatureFileName)); err != nil {
		t.Fatal(err)
	}
	broken := writePlugin(t, root, "broken", validManifest("broken"), "bytes")
	if err := os.WriteFile(filepath.Join(broken, defaultExecName()), []byte("swapped"), 0o755); err != nil {
		t.Fatal(err)
	}
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)
	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry("good") })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	byID := map[string]View{}
	for _, v := range m.Views() {
		byID[v.ID] = v
	}
	want := KeyID(testKey().Public().(ed25519.PublicKey))
	if got := byID["good"].SignedBy; got != want {
		t.Errorf("签名通过的行要显示 kid %s，实际 %q", want, got)
	}
	if got := byID["nosig"].SignedBy; got != "" {
		t.Errorf("没有签名文件就不该有 kid，实际 %q", got)
	}
	if byID["nosig"].State != StateUnsigned || byID["broken"].State != StateSigBroken {
		t.Fatalf("状态不对：nosig=%q broken=%q", byID["nosig"].State, byID["broken"].State)
	}
	s := m.Stats()
	// 三种都要能被"要处理的"这一个数兜住：授权的人第一眼看的数字不许漏掉签名问题
	if s.Pending != 1 || s.Attention != 2 {
		t.Errorf("该是 待授权1 / 要处理2（未签名 + 签名不符），实际 %+v", s)
	}
}

// ---- 授权时刻重读磁盘（计划 §3.3 第 2 条）--------------------------------------
//
// 面板上那行"待授权"是上一次扫描（通常就是主程序启动）时的快照。从"人看见它"到
// "人点下授权"之间目录里的文件可以被换掉，这一刻的判定必须按磁盘上当下的字节重做，
// 否则授权绑定的是旧指纹、起来的却是新那份——签名与指纹两道门同时失效。

func TestAuthorizeRefusesManifestSwappedAfterBoot(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "swap", validManifest("swap"), "first bytes")
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry("swap") })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if v := m.Views()[0]; v.State != StatePending {
		t.Fatalf("夹具应先落在待授权，实际 %q（%s）", v.State, v.Reason)
	}
	before := m.Views()[0].ManifestHash

	// 作者交付了另一份，并且**重新签过名**：签名门是过的，问题只剩"人批准的不是眼前这份"
	m2 := validManifest("swap")
	m2.Title = "换过的一份"
	m2.Policy = []string{model.RoleDormManager} // 连可读角色都变了，更不能悄悄换绑
	raw, err := json.Marshal(m2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	signDir(t, dir)

	err = m.Authorize("swap", Operator{ID: 1, Name: "甲"}, nil)
	if err == nil {
		t.Fatal("文件在扫描后被换过却授权成功了：那一刻起来的东西没人批准过")
	}
	if !strings.Contains(err.Error(), "被换过") {
		t.Errorf("错误要说清是'扫描之后被换过'，实际 %q", err.Error())
	}
	if modules.Has("swap") {
		t.Error("授权已被拒却进了注册表")
	}
	if ok, _ := enf.Enforce("role:"+model.RoleDormManager, DataEndpoint("swap"), "GET"); ok {
		t.Error("授权已被拒却给宿管注入了读权限")
	}
	if n := trustRowCount(t); n != 0 {
		t.Errorf("被拒的授权在信任表里留了 %d 行", n)
	}
	// 拒绝之前要把缓存刷成磁盘现状：界面重读后那一行显示的就是真话，而不是停在旧指纹
	after := m.Views()[0]
	if after.ManifestHash == before {
		t.Error("列表没刷新，运维第二次看到的还是旧指纹")
	}
	if after.Title != "换过的一份" {
		t.Errorf("列表应显示现在这份的标题，实际 %q", after.Title)
	}
	if after.SignedBy == "" {
		t.Error("重扫把 kid 弄丢了：换过的这份也是同一位开发者签的")
	}
}

// 面板还写着"待授权"、磁盘上却已经是被人动过的一份——授权这一刻也要抓得住。
func TestAuthorizeCatchesTamperThatHappenedAfterBoot(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "late", validManifest("late"), "author bytes")
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll() })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}
	if v := m.Views()[0]; v.State != StatePending {
		t.Fatalf("Boot 时这份是合法且签过的，实际 %q（%s）", v.State, v.Reason)
	}
	if err := os.WriteFile(filepath.Join(dir, defaultExecName()), []byte("someone else bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := m.Authorize("late", Operator{ID: 1, Name: "甲"}, nil)
	if err == nil {
		t.Fatal("启动后被人换过的可执行文件照样授权成功了")
	}
	if !strings.Contains(err.Error(), "签名") {
		t.Errorf("应说清是签名对不上，实际 %q", err.Error())
	}
	if v := m.Views()[0]; v.State != StateSigBroken {
		t.Errorf("列表应立刻改成签名不符，实际 %q", v.State)
	}
	if modules.Has("late") {
		t.Error("已被拒的插件进了注册表")
	}
}

// 拒的不是死路：运维核对过新那份再点一次，就该按新指纹授权成功。
func TestReAuthorizeAfterRefreshSucceeds(t *testing.T) {
	root := t.TempDir()
	mode := "refresh"
	id := stubIDPrefix + mode
	dir := stubPluginDir(t, root, mode, []string{model.RoleMember})
	useTrustDB(t)
	_, enf := setupPolicyEnforcer(t)

	m := NewManagerWithAnchors(root, enf, testAnchors())
	t.Cleanup(func() { m.StopAll(); cleanupRegistry(id) })
	if err := m.Boot(); err != nil {
		t.Fatalf("Boot 失败: %v", err)
	}

	m2 := validManifest(id)
	m2.Title = "第二版"
	raw, err := json.Marshal(m2)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	signDir(t, dir)

	if err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err == nil {
		t.Fatal("第一次点下去时磁盘已不是列表那份，本该被拒")
	}
	if err := m.Authorize(id, Operator{ID: 1, Name: "甲"}, nil); err != nil {
		t.Fatalf("核对后再点一次仍没授权成功: %v", err)
	}
	if !modules.Has(id) {
		t.Error("第二次授权成功了却没进注册表")
	}
	var row model.PluginTrust
	if err := repository.DB.First(&row, "id = ?", id).Error; err != nil {
		t.Fatalf("信任记录没落库: %v", err)
	}
	if row.ManifestHash != m.Views()[0].ManifestHash {
		t.Error("记下的指纹不是第二次点击时磁盘上那份")
	}
	// 授权成功之后，面板上那两行指纹必须是同一个人的：
	// 库里换了新的、内存里还挂着旧的，界面就会一边说"运行中"一边说"文件换过"。
	view := m.Views()[0]
	if view.TrustedExecHash != view.ExecHash || view.TrustedManifestHash != view.ManifestHash {
		t.Errorf("重新授权后面板不该再摆出『授权时 → 现在』两行不同的指纹，实际 授权时=%s 现在=%s",
			view.TrustedExecHash, view.ExecHash)
	}
}
