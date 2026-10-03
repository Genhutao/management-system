package plugins

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"xgh-system/internal/modules"
)

// 桩进程走 re-exec：`go test` 编出来的这个测试二进制被加载器再拉一次时，
// 按参数变成各种坏行为的"插件"。两种认法：
//   - 单元测试直接给 args（快，不需要真目录）
//   - Boot/Authorize 那套要跑真目录真清单的用例，用 XGH_PLUGIN_ID 认模式：
//     加载器只许给子进程这一个环境变量，那就用它，绝不为了测试再塞第二个变量。
const stubArgv = "-xgh-stub"

const stubIDPrefix = "stub-"

// stubActionPath 桩在 actionok/actionbadjson 模式下"想发"的那个接口。
// 它是白名单里的真实路径：越界用例（actionrogue）要证的不是"这条路能不能走"，
// 而是"插件回了第二条路，宿主认不认"，所以规矩那份必须落在一个本来就合法的目标上。
const stubActionPath = "/api/v1/publicity/broadcast/news"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == stubArgv {
		mode := "normal"
		if len(os.Args) > 2 {
			mode = os.Args[2]
		}
		runStub(mode)
		os.Exit(0)
	}
	if id := os.Getenv("XGH_PLUGIN_ID"); strings.HasPrefix(id, stubIDPrefix) {
		runStub(strings.TrimPrefix(id, stubIDPrefix))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runStub 读一行答一行，直到 stdin 关闭 —— 这就是插件作者须知里那句"stdin 关了就走"，
// 也是主程序被硬杀时子进程不变成孤儿唯一的指望（本项目没有优雅关闭）。
func runStub(mode string) {
	if mode == "die" {
		fmt.Fprintln(os.Stderr, "stub: 这活我不干")
		os.Exit(3)
	}
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		var req wireRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		switch mode {
		case "stderr":
			fmt.Fprintf(os.Stderr, "stub stderr %d\n", req.ID)
		case "flood":
			for i := 0; i < 400; i++ {
				fmt.Fprintf(os.Stderr, "flood %08d %s\n", i, strings.Repeat("y", 64))
			}
		}

		var raw []byte
		switch mode {
		case "badjson":
			raw = []byte(`这不是 JSON`)
		case "huge":
			raw = hugeLine(req.ID)
		case "slow":
			// 只在 data 上装死：ping 先过，才能把"请求中途超时"这条路径单独验出来
			if req.Op == opData {
				time.Sleep(30 * time.Second)
			}
			raw = mustMarshal(wireResponse{ID: req.ID, OK: true, Protocol: ProtocolVersion})
		case "wrongid":
			raw = mustMarshal(wireResponse{ID: req.ID + 1, OK: true, Protocol: ProtocolVersion})
		case "protocol2":
			raw = mustMarshal(wireResponse{ID: req.ID, OK: true, Protocol: ProtocolVersion + 1})
		case "selferr":
			raw = mustMarshal(wireResponse{ID: req.ID, OK: false, Error: "读不到传感器\n第二行不该跑到卡上"})
		// 动作桩的三个面孔：规矩的那份、越界的那份、以及"说做完了却没给请求"那份。
		// 路径写死在这里而不是从参数里读，是为了让"清单说什么"和"插件回什么"这两件事
		// 在测试里明确分开 —— 否则越界那条用例就成了自己证明自己。
		case "actionok":
			body, _ := json.Marshal(map[string]string{"title": "桩提交的标题", "echo": req.Action})
			raw = mustMarshal(wireResponse{ID: req.ID, OK: true, Protocol: ProtocolVersion, Request: &wireActionRequest{
				Method: "POST",
				Path:   stubActionPath,
				Body:   body,
				// 这两个头**必须被宿主丢掉**：插件能塞 Authorization/Cookie 就等于它拿到了别人的会话，
				// 而动作设计的全部意义正是"它只能说要发哪儿，发出去用的是点按钮那人自己的凭据"。
				Headers: map[string]string{"Authorization": "Bearer 伪造的", "X-Confirm-Password": "不该由插件知道"},
			}})
		case "actionrogue":
			raw = mustMarshal(wireResponse{ID: req.ID, OK: true, Protocol: ProtocolVersion, Request: &wireActionRequest{
				Method: "DELETE",
				Path:   "/api/v1/tech/db/rows",
				Body:   []byte(`{}`),
			}})
		case "actionnobody":
			raw = mustMarshal(wireResponse{ID: req.ID, OK: true, Protocol: ProtocolVersion})
		default:
			raw = mustMarshal(normalReply(req))
		}
		fmt.Fprintln(os.Stdout, string(raw))
	}
}

func normalReply(req wireRequest) wireResponse {
	resp := wireResponse{ID: req.ID, OK: true, Protocol: ProtocolVersion}
	if req.Op == opData {
		resp.Values = map[string]modules.Value{}
		for _, k := range req.Keys {
			resp.Values[k] = modules.Value{
				Text:  "48% 已用",
				Hint:  fmt.Sprintf("key=%s user=%d role=%s", k, req.UserID, req.Role),
				Level: "ok",
			}
		}
	}
	return resp
}

func hugeLine(id int) []byte {
	pad := strings.Repeat("x", MaxResponseBytes+4096)
	return []byte(fmt.Sprintf(`{"id":%d,"ok":true,"protocol":1,"error":"%s"}`, id, pad))
}

func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func stubPlugin() *Plugin {
	return &Plugin{
		Module: modules.Module{
			ID: "stubplug", Title: "桩", Tab: "stubplug", MinManifestVersion: modules.ManifestVersion,
			Widgets: []modules.Widget{
				{Key: "data_disk", Type: modules.WidgetStat, Label: "数据盘", DataEndpoint: DataEndpoint("stubplug")},
			},
		},
		ExecPath: os.Args[0],
	}
}

func stubProcess(t *testing.T, mode string) *process {
	t.Helper()
	p := newProcess(stubPlugin())
	p.dir = t.TempDir()
	p.args = []string{stubArgv, mode}
	t.Cleanup(p.stop)
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

func TestPingHandshake(t *testing.T) {
	p := stubProcess(t, "normal")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	if err := p.Ping(3 * time.Second); err != nil {
		t.Fatalf("ping 应通过: %v", err)
	}
	if p.State() != StateRunning {
		t.Errorf("ping 通过后状态应是运行中，实际 %q", p.State())
	}
}

// protocol 对不上就是"它说的不是这门话"，不能当成授权成功。
func TestPingRejectsForeignProtocol(t *testing.T) {
	p := stubProcess(t, "protocol2")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	err := p.Ping(3 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "协议版本") {
		t.Fatalf("自报版本不符应被拒，实际: %v", err)
	}
}

func TestDataRoundTripCarriesUserAndRole(t *testing.T) {
	p := stubProcess(t, "normal")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	values, selfErr, err := p.Data([]string{"data_disk"}, 7, "tech_admin")
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if selfErr != "" {
		t.Fatalf("正常桩不该自报错误: %q", selfErr)
	}
	v, ok := values["data_disk"]
	if !ok {
		t.Fatalf("缺 data_disk，实际 %v", values)
	}
	// user_id / role 会进插件可见的范围（§14 第 4 条），这里确认它确实传到了
	if !strings.Contains(v.Hint, "user=7") || !strings.Contains(v.Hint, "role=tech_admin") {
		t.Errorf("请求里没带上账号与角色: %q", v.Hint)
	}
}

// 插件自报 ok:false 是"它算不出这个数"，不是传输坏了：错误文本走 values 的 warn 卡，
// 而不是让代理去编一个 0。
func TestPluginSelfReportedErrorIsNotTransportError(t *testing.T) {
	p := stubProcess(t, "selferr")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	values, selfErr, err := p.Data([]string{"data_disk"}, 1, "member")
	if err != nil {
		t.Fatalf("插件自报错误不该算传输失败: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("自报失败时不该带回任何值: %v", values)
	}
	if selfErr == "" {
		t.Fatal("自报的错误文本要交给界面，不能吞掉")
	}
	if strings.Contains(selfErr, "\n") {
		t.Errorf("原因必须压成一行，否则警示卡会被撑爆: %q", selfErr)
	}
}

func TestRequestTimeoutKillsTheProcess(t *testing.T) {
	p := stubProcess(t, "slow")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	if err := p.Ping(3 * time.Second); err != nil {
		t.Fatalf("ping 应通过（slow 只在 data 上装死）: %v", err)
	}
	_, err := p.call(wireRequest{Op: opData, Keys: []string{"data_disk"}, UserID: 1, Role: "member"}, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "没有回答") {
		t.Fatalf("超时应报错并说明是它没回答，实际: %v", err)
	}
	// 超时之后这个连接已经作废：进程被终止，状态从运行中掉下来
	waitFor(t, "超时后进程被判死", func() bool { return p.State() != StateRunning })
}

func TestBadJSONAndWrongIDEchoAreErrors(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"badjson", "不是 JSON"},
		{"wrongid", "id 回显错误"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			p := stubProcess(t, tc.mode)
			if err := p.start(); err != nil {
				t.Fatalf("起桩失败: %v", err)
			}
			err := p.Ping(3 * time.Second)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误包含 %q，实际: %v", tc.want, err)
			}
		})
	}
}

// 一行无限长的输出是坏插件最容易写出来的东西：读它之前先设上限。
func TestOversizedLineRejected(t *testing.T) {
	p := stubProcess(t, "huge")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	err := p.Ping(3 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("超长行应被拒，实际: %v", err)
	}
}

func TestImmediateExitIsNotLive(t *testing.T) {
	p := stubProcess(t, "die")
	if err := p.start(); err != nil {
		t.Fatalf("start 只看能不能拉起来，进程随后自杀不算它失败: %v", err)
	}
	waitFor(t, "进程退出后 live 掉下来", func() bool { return p.State() != StateRunning })
	if !strings.Contains(p.LastError(), "退出") {
		t.Errorf("最近错误要写清是进程退出了，实际 %q", p.LastError())
	}
	if p.State() != StateRetrying {
		t.Errorf("刚退出一次应是退避重启中，实际 %q", p.State())
	}
}

// 退避与放弃是纯计数逻辑，单独测：拿真进程跑满 10 次要等好几分钟，测的是时长不是逻辑。
func TestBackoffCappedAndGivesUp(t *testing.T) {
	base := time.Now()
	p := newProcess(stubPlugin())
	p.nowFn = func() time.Time { return base }

	for i := 1; i < giveUpAfter-1; i++ {
		p.noteFailureLocked()
	}
	delay := p.nextTry.Sub(base)
	if delay > backoffCeiling {
		t.Fatalf("退避不该超过上限: %v", delay)
	}
	if delay < 2*backoffUnit {
		t.Errorf("退避该随失败次数变长，实际 %v", delay)
	}
	for i := p.fails; i < giveUpAfter; i++ {
		p.noteFailureLocked()
	}
	if !p.gaveUp {
		t.Fatal("连续失败到上限就该放弃自动重启")
	}
	if p.State() != StateGaveUp {
		t.Errorf("放弃之后界面不能还说它运行中，实际 %q", p.State())
	}
	if !strings.Contains(p.LastError(), "重新授权") {
		t.Errorf("放弃的原因要告诉人下一步该做什么，实际 %q", p.LastError())
	}
}

// 一次正常往返要把攒下来的退避清零，否则一个跑了几天的插件会因为很久以前的一次崩溃
// 被记着"再过几次就放弃"。
func TestSuccessResetsBackoff(t *testing.T) {
	p := newProcess(stubPlugin())
	p.noteFailureLocked()
	p.noteFailureLocked()
	if p.fails != 2 {
		t.Fatalf("先攒两次失败，实际 %d", p.fails)
	}
	p.noteSuccess()
	if p.fails != 0 || !p.nextTry.IsZero() {
		t.Errorf("成功之后退避计数该清零: fails=%d nextTry=%v", p.fails, p.nextTry)
	}
}

func TestStderrCollectedForPanel(t *testing.T) {
	p := stubProcess(t, "stderr")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	if err := p.Ping(3 * time.Second); err != nil {
		t.Fatalf("ping 失败: %v", err)
	}
	waitFor(t, "stderr 被收上来", func() bool { return strings.Contains(p.stderr.String(), "stub stderr") })
}

// stderr 只能留尾巴：一个刷屏的坏插件不该靠日志把主程序内存吃掉。
func TestStderrTailIsCapped(t *testing.T) {
	p := stubProcess(t, "flood")
	if err := p.start(); err != nil {
		t.Fatalf("起桩失败: %v", err)
	}
	if err := p.Ping(3 * time.Second); err != nil {
		t.Fatalf("ping 失败: %v", err)
	}
	waitFor(t, "stderr 攒够超出上限的量", func() bool { return len(p.stderr.String()) >= StderrKeepBytes })
	if got := len(p.stderr.String()); got > StderrKeepBytes {
		t.Errorf("尾巴没截住: %d > %d", got, StderrKeepBytes)
	}
}

func TestReadCappedLine(t *testing.T) {
	t.Run("正常一行", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader(`{"id":1}` + "\n后头不该被读走"))
		line, err := readCappedLine(r)
		if err != nil {
			t.Fatalf("读行失败: %v", err)
		}
		if strings.TrimSpace(string(line)) != `{"id":1}` {
			t.Errorf("读到的不是那一行: %q", line)
		}
	})
	t.Run("超限拒收", func(t *testing.T) {
		r := bufio.NewReader(strings.NewReader(strings.Repeat("z", MaxResponseBytes+10) + "\n"))
		if _, err := readCappedLine(r); err == nil || !strings.Contains(err.Error(), "上限") {
			t.Fatalf("超过上限应报错，实际: %v", err)
		}
	})
}

func TestTruncateReasonCollapsesLines(t *testing.T) {
	got := truncateReason("  第一行\n第二行   ")
	if got != "第一行 第二行" {
		t.Errorf("该行合并、该裁的裁，实际 %q", got)
	}
	long := strings.Repeat("字", 300)
	out := truncateReason(long)
	if len([]rune(out)) > 202 {
		t.Errorf("截断没生效: %d 个 rune", len([]rune(out)))
	}
	if !strings.HasSuffix(out, "…") {
		t.Error("被截断要留个记号，不然人以为是完整原因")
	}
}

// 子进程环境：只有 XGH_PLUGIN_ID（Windows 另加系统根目录一条），
// 数据库路径与两把密钥绝不影响进去。
func TestChildEnvCarriesNoSecrets(t *testing.T) {
	env := childEnv("diskusage")
	joined := strings.Join(env, ";")
	if !strings.Contains(joined, "XGH_PLUGIN_ID=diskusage") {
		t.Fatalf("至少要带上插件自己的 id，实际 %v", env)
	}
	for _, forbidden := range []string{"DB_PATH", "JWT", "SECRET", "PATH="} {
		if strings.Contains(strings.ToUpper(joined), forbidden) {
			t.Errorf("子进程环境里出现了 %q：%v", forbidden, env)
		}
	}
}
