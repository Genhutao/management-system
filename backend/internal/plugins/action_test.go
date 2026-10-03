package plugins

// action_test.go —— 写动作准入与代执行（计划 §5 / §5.3 / §8 的 S3 那六条验收）。
//
// 这一层要钉住的三句话：
//  1. **能发哪条接口由服务端那份清单决定**，manifest 写得再漂亮，不在白名单就是校验失败；
//  2. **插件越界 = 签名承诺被违反**，请求不发出，一次写都没有；
//  3. **代执行用的是操作者自己的会话**，插件塞的 Authorization / Cookie / 口令头一律丢掉，
//     所以"插件能改的"恰好等于"点按钮那个人自己能改的"。
//
// 回环目标用 httptest 起的一个假接口，不打真服务：验的是"发出去的请求长什么样"，
// 而不是某个业务接口此刻健不健康（那归 controller 与业务测试管）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
)

// validActionManifest 一份合法的可写插件清单：一个只读组件 + 一个动作按钮。
// 路径用白名单里真实存在的那条，免得测试通过只是因为"随便写的路径刚好没被拒"。
func validActionManifest(id string) Manifest {
	return Manifest{
		ID: id, Title: "播音稿投递", Icon: "fa-solid fa-bullhorn", Group: "宣传", Tab: id,
		Policy: []string{model.RoleMember, model.RoleMinister},
		Widgets: []ManifestWidget{
			{Key: "today", Type: modules.WidgetStat, Label: "今日播音稿"},
			{Key: "submit", Type: modules.WidgetAction, Label: "提交一条播音稿", ActionKey: "submit_news"},
		},
		Actions: []ManifestAction{{
			Key: "submit_news", Label: "提交一条播音稿", Method: "POST",
			Path: "/api/v1/publicity/broadcast/news", Confirm: "none",
			Reason: "把当天写好的稿子投进播报队列",
			Fields: []ManifestActionField{
				{Key: "title", Label: "标题"},
				{Key: "content", Label: "正文", Multi: true},
			},
		}},
	}
}

// ---- 扫描期：白名单与形状 ----------------------------------------------------

// startedActionStub 起一个真的动作桩进程。stubProcess 只装配不起，
// 而 PrepareAction 走的是"必须有活进程"那条路——忘记 start 的表现是
// "插件进程当前没在运行"，看起来像是被测代码坏了。
func startedActionStub(t *testing.T, mode string) *process {
	t.Helper()
	p := stubProcess(t, mode)
	if err := p.start(); err != nil {
		t.Fatalf("起动作为 %s 的桩失败: %v", mode, err)
	}
	return p
}

func TestScanAcceptsDeclaredAction(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "newsdrop", validActionManifest("newsdrop"), "MZ fake")

	p := Scan(root, testAnchors())[0]
	if p.State != StatePending {
		t.Fatalf("合法的动作声明应进待授权，实际 %q（原因：%s）", p.State, p.Reason)
	}
	if len(p.Module.Actions) != 1 {
		t.Fatalf("描述符里该带着那条动作，实际 %d 条", len(p.Module.Actions))
	}
	a := p.Module.Actions[0]
	if a.Endpoint != "/api/v1/mod/newsdrop/action" {
		t.Errorf("动作端点应由 id 推导，实际 %q", a.Endpoint)
	}
	// 按钮不读数：把动作组件也塞进"要取哪些键"里，面板上就会多一张无数据卡
	m := &Manager{entries: map[string]*entry{"newsdrop": {plugin: p}}}
	if keys := m.widgetKeys("newsdrop"); len(keys) != 1 || keys[0] != "today" {
		t.Errorf("只该向插件取 today，实际 %v", keys)
	}
	// 声明了动作的插件才会被挂上动作端点（main.go 按这份名单挂路由）
	if got := m.ActionCapableIDs(); len(got) != 1 || got[0] != "newsdrop" {
		t.Errorf("ActionCapableIDs 该列出它，实际 %v", got)
	}
	p.State = StateInvalid
	if got := m.ActionCapableIDs(); len(got) != 0 {
		t.Errorf("校验失败的插件绝不该挂出动作端点，实际 %v", got)
	}
}

func TestScanRejectsActionsOutsideServerScope(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"通用数据编辑器", "/api/v1/tech/db/rows", "后台通用数据编辑器"},
		{"插件自己的数据端点", "/api/v1/mod/runtimestatus", "self-deadlock"},
		{"账号动作", "/api/v1/auth/change-password", "账户安全中心"},
		{"没在白名单里的正常接口", "/api/v1/deductions", "不在服务端可声明清单里"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			m := validActionManifest("scope")
			m.Actions[0].Path = tc.path
			writePlugin(t, root, "scope", m, "MZ fake")
			p := Scan(root, testAnchors())[0]
			if p.State != StateInvalid {
				t.Fatalf("这条路径本该被扫描期拒掉，实际 %q", p.State)
			}
			if !strings.Contains(p.Reason, tc.want) {
				t.Errorf("原因里该有 %q，实际 %q", tc.want, p.Reason)
			}
			// 被拒的插件不能顺带把动作端点挂出来（main.go 按 ActionCapableIDs 挂路由，
			// 而那里第一句就排除了 StateInvalid）
			if len(p.Module.Actions) > 0 && p.State == StateInvalid {
				t.Log("描述符里仍带着动作声明：靠 ActionCapableIDs 的 StateInvalid 排除，路由不会挂上")
			}
		})
	}
}

func TestScanNormalizesHighRiskConfirm(t *testing.T) {
	root := t.TempDir()
	m := validActionManifest("slotfill")
	m.Actions[0].Path = "/api/v1/tech/task-slots" // 服务端高危清单里的一条
	m.Actions[0].Confirm = "none"                 // 作者自己说不用口令
	writePlugin(t, root, "slotfill", m, "MZ fake")

	p := Scan(root, testAnchors())[0]
	if p.State != StatePending {
		t.Fatalf("高危路径不该让清单校验失败（口令由服务端加，不是把插件拒掉），实际 %q（%s）", p.State, p.Reason)
	}
	if got := p.Module.Actions[0].Confirm; got != "stepup" {
		t.Errorf("命中高危清单却没被改成 stepup，实际 %q：插件自己写 none 就能免口令，这条门等于没有", got)
	}
}

// 动作与按钮必须双向闭合：少一半都会留下"看不见的通道"或"按不动的空壳"。
func TestScanRejectsActionWidgetMismatch(t *testing.T) {
	t.Run("声明了动作却没有按钮", func(t *testing.T) {
		root := t.TempDir()
		m := validActionManifest("hidden")
		m.Widgets = m.Widgets[:1] // 只留那张只读卡
		writePlugin(t, root, "hidden", m, "MZ fake")
		p := Scan(root, testAnchors())[0]
		if p.State != StateInvalid || !strings.Contains(p.Reason, "没有任何按钮") {
			t.Fatalf("该被拒并说明看不见通道，实际 %q：%s", p.State, p.Reason)
		}
	})
	t.Run("按钮指向没有的动作", func(t *testing.T) {
		root := t.TempDir()
		m := validActionManifest("ghost")
		m.Actions = nil
		writePlugin(t, root, "ghost", m, "MZ fake")
		p := Scan(root, testAnchors())[0]
		if p.State != StateInvalid || !strings.Contains(p.Reason, "没有这条声明") {
			t.Fatalf("该被拒，实际 %q：%s", p.State, p.Reason)
		}
	})
	t.Run("GET 不算动作", func(t *testing.T) {
		root := t.TempDir()
		m := validActionManifest("getty")
		m.Actions[0].Method = "GET"
		writePlugin(t, root, "getty", m, "MZ fake")
		if p := Scan(root, testAnchors())[0]; p.State != StateInvalid {
			t.Fatalf("GET 动作应被拒：读走 values，不该混进可写清单")
		}
	})
	t.Run("路径带编号的写法", func(t *testing.T) {
		root := t.TempDir()
		m := validActionManifest("parammy")
		m.Actions[0].Path = "/api/v1/publicity/broadcast/news/:id"
		writePlugin(t, root, "parammy", m, "MZ fake")
		p := Scan(root, testAnchors())[0]
		if p.State != StateInvalid || !strings.Contains(p.Reason, "路径参数") {
			t.Fatalf("带 :id 的动作路径应被拒，实际 %q：%s", p.State, p.Reason)
		}
	})
}

// ---- 参数：客户端不能替清单做主 ----------------------------------------------

func TestValidateActionParams(t *testing.T) {
	a := modules.Action{Key: "submit_news", Fields: []modules.ActionField{
		{Key: "title", Label: "标题"},
		{Key: "content", Label: "正文"},
	}}

	if got, err := ValidateActionParams(a, map[string]any{"title": "周一升旗", "content": "稿件正文"}); err != nil {
		t.Fatalf("合式的参数不该被拒: %v", err)
	} else if got["title"] != "周一升旗" {
		t.Errorf("参数没原样收进来: %v", got)
	}

	for _, tc := range []struct {
		name string
		got  map[string]any
		want string
	}{
		{"多一个没声明的键", map[string]any{"title": "a", "content": "b", "role": "tech_admin"}, "没有在清单里声明过"},
		{"少一个声明过的键", map[string]any{"title": "a"}, "缺参数"},
		{"值不是文本", map[string]any{"title": "a", "content": 42}, "必须是文本"},
		{"空值", map[string]any{"title": "  ", "content": "b"}, "是空的"},
		{"超长", map[string]any{"title": "a", "content": strings.Repeat("字", MaxActionParamBytes)}, "超过"},
	} {
		if _, err := ValidateActionParams(a, tc.got); err == nil {
			t.Errorf("%s：本该被拒", tc.name)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：原因里该有 %q，实际 %q", tc.name, tc.want, err.Error())
		}
	}

	// 清单里写了更小的上限就按更小的
	small := modules.Action{Fields: []modules.ActionField{{Key: "title", Label: "标题", MaxBytes: 8}, {Key: "content", Label: "正文"}}}
	if _, err := ValidateActionParams(small, map[string]any{"title": "一二三四五六七八九十", "content": "b"}); err == nil {
		t.Error("单字段上限该优先于默认上限")
	}
}

// ---- 问插件要请求：越界就拒 --------------------------------------------------

func TestPrepareActionRejectsOutOfScopeReply(t *testing.T) {
	declared := modules.Action{Key: "submit_news", Label: "提交一条播音稿", Method: "POST", Path: stubActionPath}
	m := &Manager{entries: map[string]*entry{}}
	p := startedActionStub(t, "actionrogue")
	m.entries["stubplug"] = &entry{plugin: stubPlugin(), trusted: true, proc: p}
	t.Cleanup(func() { m.StopAll() })

	if _, err := m.PrepareAction("stubplug", declared, map[string]string{"title": "a"}, 1, model.RoleMember); err == nil {
		t.Fatal("插件回了另一条路径却准备成功：那等于签名管不住去向")
	} else if !strings.Contains(err.Error(), "插件越界") {
		t.Errorf("错误要指名越界，实际 %q", err.Error())
	} else if !strings.Contains(err.Error(), "一次写都没发生") {
		t.Errorf("错误要说清请求没发出，实际 %q", err.Error())
	}
}

func TestPrepareActionAcceptsHonestReply(t *testing.T) {
	declared := modules.Action{Key: "submit_news", Label: "提交一条播音稿", Method: "POST", Path: stubActionPath}
	m := &Manager{entries: map[string]*entry{}}
	m.entries["stubplug"] = &entry{plugin: stubPlugin(), trusted: true, proc: startedActionStub(t, "actionok")}
	t.Cleanup(func() { m.StopAll() })

	prepared, err := m.PrepareAction("stubplug", declared, map[string]string{"title": "周一升旗"}, 1, model.RoleMember)
	if err != nil {
		t.Fatalf("规矩的那份不该被拒: %v", err)
	}
	if prepared.Method != "POST" || prepared.Path != stubActionPath {
		t.Errorf("准备出来的请求不对: %+v", prepared)
	}
	// 参数摘要归去重窗口用（见 TestClaimActionWindow）；PrepareAction 不回它，
	// 所以这里也没有可断言的字段——曾经有的那个 Fingerprint 是个没人读的死字段。
}

// "说做完了却没回请求"是最阴的一种：界面会以为成功。
func TestPrepareActionRejectsSilenceDisguisedAsSuccess(t *testing.T) {
	declared := modules.Action{Method: "POST", Path: stubActionPath}
	m := &Manager{entries: map[string]*entry{}}
	m.entries["stubplug"] = &entry{plugin: stubPlugin(), trusted: true, proc: startedActionStub(t, "actionnobody")}
	t.Cleanup(func() { m.StopAll() })
	if _, err := m.PrepareAction("stubplug", declared, nil, 1, model.RoleMember); err == nil ||
		!strings.Contains(err.Error(), "没有") {
		t.Fatalf("没有请求却算成功，该报错，实际: %v", err)
	}
}

func TestPrepareActionRefusesUnrunningPlugin(t *testing.T) {
	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	_, err := m.PrepareAction("notthere", modules.Action{Method: "POST", Path: stubActionPath}, nil, 1, model.RoleMember)
	if err == nil || !strings.Contains(err.Error(), "没在运行") {
		t.Fatalf("没运行的插件该被明说，实际: %v", err)
	}
}

// ---- 写路径上那道显式的信任门 -------------------------------------------------

// 六种挡住的情况要回六句不同的话（与 absentReason 同一套分法）。合并成"插件不可用"的后果很具体：
// 未授权与退避重启在界面上对应的动作是相反的——一个要去点授权按钮，一个要等它自己起来。
// 更要紧的是这道门排在"要登录口令"之前：没授权的插件不该先让人输一遍口令才听到实话。
func TestActionGateSeparatesTheStates(t *testing.T) {
	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	put := func(id string, state State, reason string, trusted bool) {
		p := stubPlugin()
		p.Module.ID = id
		p.State = state
		p.Reason = reason
		m.entries[id] = &entry{plugin: p, trusted: trusted}
	}
	put("pending", StatePending, "", false)
	put("invalid", StateInvalid, "exec 文件读不到", false)
	put("unsigned", StateUnsigned, "目录里没有 signature 文件", false)
	put("sigbroken", StateSigBroken, "可执行文件摘要不符", true)
	put("stale", StateStale, "", true)
	running := stubPlugin()
	running.Module.ID = "running"
	running.State = StateRunning
	m.entries["running"] = &entry{plugin: running, trusted: true}

	cases := []struct{ id, want string }{
		{"missing", "没有这个插件目录"},
		{"pending", "未授权"},
		{"invalid", "manifest 校验失败"},
		{"unsigned", "没有可信开发者签名"},
		{"sigbroken", "签名与磁盘上的文件对不上"},
		{"stale", "需重新授权"},
	}
	for _, tc := range cases {
		got := m.ActionGate(tc.id)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s 那一句该含 %q，实际 %q", tc.id, tc.want, got)
		}
		if !strings.Contains(got, "动作") {
			t.Errorf("%s 那句话要说清被挡住的是动作这件事，实际 %q", tc.id, got)
		}
	}
	if why := m.ActionGate("running"); why != "" {
		t.Errorf("已授权且在跑的插件该放行，实际 %q", why)
	}
	// 已授权但进程正在退避重启（表里 trusted、手上没有活进程）：门放行，让进程那边回真实故障。
	// 这条不是宽松——那一类的下一步是"等它自己起来"，不是"去点授权"，门抢话反而指错方向。
	backing := stubPlugin()
	backing.Module.ID = "retrying"
	m.entries["retrying"] = &entry{plugin: backing, trusted: true}
	if why := m.ActionGate("retrying"); why != "" {
		t.Errorf("退避重启中的已授权插件该由进程那边回故障，门不该抢话，实际 %q", why)
	}
}

// ---- 代执行那一跳 ------------------------------------------------------------

func TestExecuteActionCarriesOperatorCredentialAndDropsPluginHeaders(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := map[string]string{
			"authorization":     r.Header.Get("Authorization"),
			"cookie":            r.Header.Get("Cookie"),
			"confirm":           r.Header.Get("X-Confirm-Password"),
			"acted_for":         r.Header.Get(PluginActedForHeader),
			"content_type":      r.Header.Get("Content-Type"),
			"xgh_plugin_forged": r.Header.Get("Xgh-Plugin-Forged"),
		}
		raw, _ := io.ReadAll(r.Body)
		h["body"] = string(raw)
		got.Store(h)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()

	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	m.SetSelfBaseURL(srv.URL)

	prepared := &PreparedAction{
		Plugin: "newsdrop",
		Action: modules.Action{Key: "submit_news", Method: "POST", Path: stubActionPath},
		Method: "POST", Path: stubActionPath, Body: []byte(`{"title":"周一升旗"}`),
	}
	res, err := m.ExecuteAction(prepared, ActionCredentials{Cookie: "xgh_session=abc"})
	if err != nil {
		t.Fatalf("代执行失败: %v", err)
	}
	if res.Status != http.StatusCreated {
		t.Errorf("内层状态码必须原样带回去（改成 200 就是替插件撒谎），实际 %d", res.Status)
	}
	if string(res.Body) != `{"id":7}` {
		t.Errorf("响应体没原样带回: %q", res.Body)
	}
	observed, _ := got.Load().(map[string]string)
	if observed["cookie"] != "xgh_session=abc" {
		t.Errorf("没把操作者自己的会话带上: %v", observed)
	}
	if observed["authorization"] != "" {
		t.Errorf("插件伪造的 Authorization 被带上了: %v", observed)
	}
	if observed["confirm"] != "" {
		t.Errorf("插件回的口令头被带上了——口令绝不该经插件之手: %v", observed)
	}
	if observed["xgh_plugin_forged"] != "" {
		t.Error("插件塞的自定义头没被丢掉")
	}
	if !strings.Contains(observed["acted_for"], "plugin=newsdrop") || !strings.Contains(observed["acted_for"], "action=submit_news") {
		t.Errorf("内部标记头该由宿主自己拼，实际 %q", observed["acted_for"])
	}
	if observed["body"] != `{"title":"周一升旗"}` {
		t.Errorf("body 没原样转交: %q", observed["body"])
	}
}

func TestExecuteActionNeedsCredentialAndBaseURL(t *testing.T) {
	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	prepared := &PreparedAction{Method: "POST", Path: stubActionPath, Action: modules.Action{Method: "POST", Path: stubActionPath}}
	if _, err := m.ExecuteAction(prepared, ActionCredentials{}); err == nil ||
		!strings.Contains(err.Error(), "会话凭据") {
		t.Fatalf("没带凭据就该拒绝而不是发一趟必 401 的请求: %v", err)
	}
	if _, err := m.ExecuteAction(prepared, ActionCredentials{Cookie: "x"}); err == nil ||
		!strings.Contains(err.Error(), "回环地址") {
		t.Fatalf("没配回环地址该明说: %v", err)
	}
}

// 那一跳转不过去时，这句话不许含糊成"操作失败"，也不许断言"没写进去"。
// 连不上与读完前超时的处置方式相同：**结果未知，先核对再决定重不重试**。
func TestExecuteActionFailureSaysResultIsUnknown(t *testing.T) {
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead := closed.URL
	closed.Close() // 立刻关掉，拿到一个"没人听"的地址

	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	m.SetSelfBaseURL(dead)

	_, err := m.ExecuteAction(&PreparedAction{
		Method: "POST", Path: stubActionPath,
		Action: modules.Action{Method: "POST", Path: stubActionPath},
	}, ActionCredentials{Cookie: "x"})
	if err == nil {
		t.Fatal("打一个没人听的地址该报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "结果") || !strings.Contains(msg, "核对") {
		t.Errorf("错误要提醒人去核对结果，实际 %q", msg)
	}
	if strings.Contains(msg, "操作失败") || strings.Contains(msg, "没有写入") {
		t.Errorf("错误不该断言结果（成功或失败都没资格），实际 %q", msg)
	}
}

// ---- 去重窗口 ---------------------------------------------------------------

func TestClaimActionWindow(t *testing.T) {
	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	fp := ParamsFingerprint("newsdrop", "submit_news", map[string]string{"title": "a", "content": "b"})

	if err := m.ClaimAction(fp, 11); err != nil {
		t.Fatalf("第一次认领不该被拦: %v", err)
	}
	if err := m.ClaimAction(fp, 11); err == nil || !strings.Contains(err.Error(), "你已经发起过一次") {
		t.Fatalf("窗口内同一个人重复点击该被拦，并说清是他自己，实际: %v", err)
	}
	// 换人不换窗：上一轮端到端就是撞在这里——宿管绕过界面打了一次、被内层 403 挡下，
	// 那次失败的认领把全校的窗口占住，紧接着部员点自己那份正常的稿子收到"已经发起过一次"。
	if err := m.ClaimAction(fp, 12); err != nil {
		t.Errorf("另一个人同样的参数不该被上一个人的窗口挡住: %v", err)
	}
	other := ParamsFingerprint("newsdrop", "submit_news", map[string]string{"title": "a", "content": "c"})
	if err := m.ClaimAction(other, 13); err != nil {
		t.Errorf("不同参数是不同动作，不该共用窗口: %v", err)
	}
	// 顺序无关：map 遍历顺序每次都不一样，不排序的话同一份参数会产生两个摘要，窗口形同虚设
	shuffled := ParamsFingerprint("newsdrop", "submit_news", map[string]string{"content": "b", "title": "a"})
	if shuffled != fp {
		t.Errorf("参数摘要对键序敏感了：去重会因随机遍历顺序失效")
	}
	// 换动作、换插件都算另一笔
	if ParamsFingerprint("other", "submit_news", map[string]string{"title": "a", "content": "b"}) == fp {
		t.Error("不同插件不该撞同一个指纹")
	}
}

// 窗口是进程内存里的：重启即清，一次重启不该把某个动作永久锁住。
func TestClaimsLiveOnlyInProcess(t *testing.T) {
	m := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	fp := ParamsFingerprint("p", "k", map[string]string{"a": "b"})
	if err := m.ClaimAction(fp, 11); err != nil {
		t.Fatal(err)
	}
	if n := len(m.claims); n != 1 {
		t.Fatalf("认领记录只该活在内存里，claims=%d", n)
	}
	second := NewManagerWithAnchors(t.TempDir(), nil, testAnchors())
	if err := second.ClaimAction(fp, 11); err != nil {
		t.Errorf("新进程（等于重启）不该继承上一次的窗口: %v", err)
	}
}

// ---- 策略注入：动作端点也要同批增删 ------------------------------------------

func TestActionEndpointPolicyInjectedAndRemoved(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	mod := modules.Module{
		ID: "newsdrop", Title: "播音稿投递", Tab: "newsdrop", MinManifestVersion: modules.ManifestVersion,
		Widgets: []modules.Widget{
			{Key: "today", Type: modules.WidgetStat, Label: "今日播音稿", DataEndpoint: DataEndpoint("newsdrop")},
			{Key: "submit", Type: modules.WidgetAction, Label: "提交", DataEndpoint: ActionEndpoint("newsdrop"), ActionKey: "submit_news"},
		},
		Actions: []modules.Action{{Key: "submit_news", Method: "POST", Path: stubActionPath, Endpoint: ActionEndpoint("newsdrop")}},
	}
	roles := []string{model.RoleMember}
	if err := grantPolicy(e, mod, roles); err != nil {
		t.Fatalf("注入失败: %v", err)
	}
	if ok, _ := e.Enforce("role:"+model.RoleMember, DataEndpoint("newsdrop"), "GET"); !ok {
		t.Error("只读端点没放开")
	}
	// 这一条是 S3 新加的：不注入就会出现"按钮看得见、点下去被动作端点自己的 Casbin 拦掉"
	if ok, _ := e.Enforce("role:"+model.RoleMember, ActionEndpoint("newsdrop"), "POST"); !ok {
		t.Error("声明了动作却没放开动作端点：那个按钮永远按不动")
	}
	if err := revokePolicy(e, mod, roles); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if ok, _ := e.Enforce("role:"+model.RoleMember, ActionEndpoint("newsdrop"), "POST"); ok {
		t.Error("撤销后动作端点还开着：面板说下线了，门其实还开着")
	}
}

// 只读插件不该被挂上动作端点的策略（多一条能 POST 的空门没有用处）。
func TestReadOnlyPluginGetsNoActionPolicy(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	mod := modules.Module{ID: "diskusage", Title: "磁盘", Tab: "diskusage", MinManifestVersion: modules.ManifestVersion}
	if err := grantPolicy(e, mod, []string{model.RoleMember}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.Enforce("role:"+model.RoleMember, ActionEndpoint("diskusage"), "POST"); ok {
		t.Error("没声明动作的插件拿到了动作端点权限")
	}
}

// ---- 清单里的按钮可见性（§10.1：不出现"看得见、点了 403"）--------------------

func TestManifestHidesActionButtonsTheRoleCannotUse(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	mod := modules.Module{
		ID: "newsdrop", Title: "播音稿投递", Tab: "newsdrop", MinManifestVersion: modules.ManifestVersion,
		Widgets: []modules.Widget{
			{Key: "today", Type: modules.WidgetStat, Label: "今日播音稿", DataEndpoint: DataEndpoint("newsdrop")},
			{Key: "submit", Type: modules.WidgetAction, Label: "提交", DataEndpoint: ActionEndpoint("newsdrop"), ActionKey: "submit_news"},
		},
		// 部员读不到 /minister/*，这个按钮就不该出现在他的清单里
		Actions: []modules.Action{{
			Key: "submit_news", Label: "跑一次本周评优", Method: "POST",
			Path: "/api/v1/minister/honors/evaluate", Confirm: "none", Reason: "生成本周评优名单",
		}},
	}
	if err := modules.TryRegister(mod); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { modules.Unregister("newsdrop") })

	// 先把只读端点放开：模块本身对部员可见，只有按钮该被筛掉
	if err := grantPolicy(e, modules.Module{ID: "newsdrop"}, []string{model.RoleMember}); err != nil {
		t.Fatal(err)
	}
	list := modules.ManifestForRole(model.RoleMember, roleCanRead(e))
	got := false
	for _, m := range list {
		if m.ID != "newsdrop" {
			continue
		}
		got = true
		for _, w := range m.Widgets {
			if w.Type == modules.WidgetAction {
				t.Errorf("部员对目标接口没权，清单里却给了按钮：点下去只会拿到一句 403")
			}
		}
	}
	if !got {
		t.Fatal("只读组件都读得到，模块却整个不见了")
	}
}

// 全是按钮、而这个角色一个都用不了 ⇒ 模块整体不列（留一块空面板比不给入口更像坏了）。
func TestModuleWithOnlyUnusableActionsIsNotListed(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	mod := modules.Module{
		ID: "onlyactions", Title: "只有按钮", Tab: "onlyactions", MinManifestVersion: modules.ManifestVersion,
		Widgets: []modules.Widget{
			{Key: "go", Type: modules.WidgetAction, Label: "来一笔", DataEndpoint: ActionEndpoint("onlyactions"), ActionKey: "go"},
		},
		Actions: []modules.Action{{
			Key: "go", Label: "来一笔", Method: "POST",
			Path: "/api/v1/minister/honors/evaluate", Confirm: "none", Reason: "测试用的第二条按钮",
		}},
	}
	if err := modules.TryRegister(mod); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { modules.Unregister("onlyactions") })
	if hasModule(modules.ManifestForRole(model.RoleMember, roleCanRead(e)), "onlyactions") {
		t.Error("一个按钮都用不了却列出了模块：点进去是空的")
	}
}
