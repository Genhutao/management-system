package plugins

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"xgh-system/internal/modules"
)

// supervisor.go —— 子进程的生老病死（方案 §3 / §8）。
//
// 一条铁律先写在最前面：**这个文件里没有"顺手把插件拉起来"的入口**。
// 起进程只发生在两处——启动期恢复已授权且指纹一致的、以及界面上输口令授权成功的那一刻。
// 数据请求打到一个没授权的 id 上，只能拿到"未运行"，绝不会在这里被懒启动：
// 否则"能读到端点"就又一次等于"插件已上线"，信任门白造。

// ProtocolVersion 协议版本；ping 时插件自报，对不上就不算它说清楚了话。
const ProtocolVersion = 1

const (
	opPing   = "ping"
	opData   = "data"
	opAction = "action" // S3：插件只**产出**一次要发的请求，执行归宿主（计划 §2）
)

// 退避参数（§8）：1s 起步、每次翻倍、60s 封顶，连续 10 次失败就放弃拉起。
// 放弃不是永久：面板上重新授权一次即可再拉——那是人的决定，不是计时器的决定。
const (
	backoffUnit    = time.Second
	backoffCeiling = 60 * time.Second
	giveUpAfter    = 10
)

type wireRequest struct {
	ID     int      `json:"id"`
	Op     string   `json:"op"`
	Keys   []string `json:"keys,omitempty"`
	UserID uint     `json:"user_id,omitempty"`
	Role   string   `json:"role,omitempty"`

	// 下面两个只在 op=="action" 时有值。参数是 map[string]string 而不是 any：
	// 一次点击要传的东西由清单里那几行 fields 决定，全都是给人填的文本；
	// 允许嵌套 JSON 等于让浏览器自己决定往插件 stdin 里塞什么形状的东西。
	Action string            `json:"key,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// wireActionRequest 插件对一次动作的答复：**它想发的那一个请求**，不是它已经做了什么。
// Body 用 RawMessage 原样带着——宿主不解析它的内容（解析就等于替插件翻译它的业务语义），
// 只把它转交给被声明的那个接口；但 method/path 必须逐字对上签名清单（计划 §5.2）。
type wireActionRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
	// Headers 协议里留着这个位置，但宿主**一律忽略**：
	// 让插件能塞 Authorization / Cookie / X-Confirm-Password 就等于把操作者的会话交给它。
	Headers map[string]string `json:"headers,omitempty"`
}

type wireResponse struct {
	ID       int                      `json:"id"`
	OK       bool                     `json:"ok"`
	Protocol int                      `json:"protocol,omitempty"`
	Values   map[string]modules.Value `json:"values,omitempty"`
	Error    string                   `json:"error,omitempty"`
	Request  *wireActionRequest       `json:"request,omitempty"`
}

// process 一个已授权插件的运行态。所有字段都在 mu 下面，外部只能通过方法碰。
type process struct {
	id       string
	execPath string
	dir      string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  *tailBuffer
	live    bool
	seq     int
	stopped bool
	gaveUp  bool
	fails   int
	nextTry time.Time
	lastErr string
	tick    time.Duration // 监督循环的检查间隔；测试会调短它
	nowFn   func() time.Time

	// args 覆盖命令行参数。**只有测试会用**（把同一个测试二进制 re-exec 成各种坏行为的桩），
	// 生产路径留空、按 execPath 原样起。放在这里是因为它是唯一一处 exec.Cmd 构造点。
	args []string
}

func newProcess(p *Plugin) *process {
	return &process{
		id: p.Module.ID, execPath: p.ExecPath, dir: p.Dir,
		stderr: newTailBuffer(StderrKeepBytes),
		tick:   200 * time.Millisecond,
		nowFn:  time.Now,
	}
}

// childEnv 交给子进程的环境。
//
// 方案 §3 写的是"只有 XGH_PLUGIN_ID"，这里额外带上 SystemRoot 一条：Windows 上
// Go 运行时起来要用它，清空环境会让子进程在打印任何一行之前就死掉，表现是
// "插件一切正常就是连不上"，比多给一个系统变量难查得多。数据库路径、两把密钥、
// 任何业务配置都不在这里，也进不去。
func childEnv(id string) []string {
	env := []string{"XGH_PLUGIN_ID=" + id}
	if runtime.GOOS == "windows" {
		if v := os.Getenv("SystemRoot"); v != "" {
			env = append(env, "SystemRoot="+v)
		}
		if v := os.Getenv("WINDIR"); v != "" {
			env = append(env, "WINDIR="+v)
		}
	}
	return env
}

// startLocked 起进程。调用方必须已持有 mu。
func (p *process) startLocked() error {
	cmd := exec.Command(p.execPath)
	if len(p.args) > 0 {
		cmd.Args = append([]string{p.execPath}, p.args...)
	}
	cmd.Dir = p.dir // 插件在自己的目录里跑：它想读随目录分发的数据文件时不必猜绝对路径
	cmd.Env = childEnv(p.id)
	cmd.Stderr = p.stderr // 日志走 stderr，主程序只留尾巴给面板看

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("接 stdin 管道失败: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("接 stdout 管道失败: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 %s 失败: %w", p.execPath, err)
	}

	p.cmd, p.stdin, p.stdout, p.live = cmd, stdin, bufio.NewReader(stdoutPipe), true
	watcher := cmd
	go p.watch(watcher)
	return nil
}

// watch 等进程退出并把状态落到"需要重拉"。
// 比对 cmd 指针：老进程晚到的 Wait 不许把已经换上去的新进程判死。
func (p *process) watch(cmd *exec.Cmd) {
	err := cmd.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != cmd {
		return
	}
	p.cmd, p.stdin, p.stdout, p.live = nil, nil, nil, false
	if p.stopped {
		return
	}
	if err != nil {
		p.lastErr = "插件进程退出: " + err.Error()
	} else {
		p.lastErr = "插件进程退出"
	}
	p.noteFailureLocked()
}

// noteFailureLocked 记一次失败并把下次允许重拉的时间推后（指数退避，封顶 60s）。
func (p *process) noteFailureLocked() {
	p.fails++
	if p.fails >= giveUpAfter {
		p.gaveUp = true
		p.lastErr = fmt.Sprintf("连续 %d 次拉起失败，已放弃自动重启（重新授权可再拉起）：%s", p.fails, p.lastErr)
		return
	}
	delay := backoffUnit << uint(p.fails-1)
	if delay > backoffCeiling || delay <= 0 {
		delay = backoffCeiling
	}
	p.nextTry = p.nowFn().Add(delay)
}

// noteSuccess 一通正常往返之后，之前攒的退避不算数了：它确实活着。
func (p *process) noteSuccess() {
	p.mu.Lock()
	p.fails, p.nextTry = 0, time.Time{}
	p.mu.Unlock()
}

// supervise 保持进程活着的循环。由授权流程在 ping 通过之后启动，stop 后自行退出。
func (p *process) supervise() {
	t := time.NewTicker(p.tick)
	defer t.Stop()
	for range t.C {
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return
		}
		needTry := !p.live && !p.gaveUp && p.nowFn().After(p.nextTry)
		var err error
		if needTry {
			err = p.startLocked()
			if err == nil {
				p.fails = 0 // 拉起来了；真活没活，接下来的请求会说话
			}
		}
		p.mu.Unlock()
		if err != nil {
			p.mu.Lock()
			p.noteFailureLocked()
			p.mu.Unlock()
		}
	}
}

// stop 结束进程并让监督循环退出。停机与撤销都走这里。
func (p *process) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.cmd, p.stdin, p.stdout, p.live = nil, nil, nil, false
}

// State 面板与 pluginstatus 都读它。只报进程层面的事实：
// "有没有授权"由 Manager 那一层判（见 trust.go 的 effectiveState），
// 一个被撤销的插件的进程停在这里，不该由进程自己说它是什么状态。
// gave_up 的优先级高于 running：已经放弃自动重启的插件，界面不能还说它"运行中"。
func (p *process) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.live:
		return StateRunning
	case p.gaveUp:
		return StateGaveUp
	default:
		return StateRetrying
	}
}

func (p *process) LastError() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastErr
}

// Ping 授权后必做的一步：等它自报家门，protocol 对不上就当它没起来。
func (p *process) Ping(timeout time.Duration) error {
	resp, err := p.call(wireRequest{Op: opPing}, timeout)
	if err != nil {
		return err
	}
	if resp.Protocol != ProtocolVersion {
		return fmt.Errorf("插件自报协议版本 %d，本加载器只认 %d", resp.Protocol, ProtocolVersion)
	}
	return nil
}

// Data 取组件值。返回的 error 是"传输层"的错（起不来、超时、坏 JSON），
// 插件自己说"这个数我算不出来"（ok:false）不算 error，走 values 里的警示值。
func (p *process) Data(keys []string, userID uint, role string) (map[string]modules.Value, string, error) {
	resp, err := p.call(wireRequest{Op: opData, Keys: keys, UserID: userID, Role: role}, RequestTimeout)
	if err != nil {
		return nil, "", err
	}
	if !resp.OK {
		return nil, truncateReason(resp.Error), nil
	}
	p.noteSuccess()
	return resp.Values, "", nil
}

// Action 向插件要"这次点击该发的那个请求"。**这里不执行任何东西**：
// 执行由宿主代做（executor.go），因为这个进程拿不到操作者的会话，也不该拿到。
// 超时按决策⑧放宽到 10s（写动作通常比读慢），串行与 256KB 上限照旧不变。
func (p *process) Action(key string, params map[string]string, userID uint, role string) (*wireActionRequest, string, error) {
	resp, err := p.call(wireRequest{Op: opAction, Action: key, Params: params, UserID: userID, Role: role}, ActionTimeout)
	if err != nil {
		return nil, "", err
	}
	if !resp.OK {
		return nil, truncateReason(resp.Error), nil
	}
	p.noteSuccess()
	if resp.Request == nil {
		return nil, "插件说动作做完了，却没有回「要发的那个请求」：这种情况不能当成什么都没发生", nil
	}
	return resp.Request, "", nil
}

// call 一次完整的往返：写一行、读一行、核对 id 回显。
//
// 整个请求在 mu 里跑完 —— 这就是 §3 说的"同插件串行"。超时不放手等它：
// kill 掉进程让阻塞中的读自己醒过来，转入退避重启；一个不回话的进程没有留着等的价值。
func (p *process) call(req wireRequest, timeout time.Duration) (*wireResponse, error) {
	p.mu.Lock()
	if !p.live {
		err := p.lastErr
		if err == "" {
			err = "插件进程当前没在运行"
		}
		p.mu.Unlock()
		return nil, fmt.Errorf("%s", err)
	}
	p.seq++
	req.ID = p.seq
	stdin, stdout := p.stdin, p.stdout
	type outcome struct {
		resp *wireResponse
		err  error
	}
	done := make(chan outcome, 1)

	go func() {
		raw, err := json.Marshal(req)
		if err != nil {
			done <- outcome{err: fmt.Errorf("编码请求失败: %w", err)}
			return
		}
		if _, err := stdin.Write(append(raw, '\n')); err != nil {
			done <- outcome{err: fmt.Errorf("写 stdin 失败: %w", err)}
			return
		}
		line, err := readCappedLine(stdout)
		if err != nil {
			done <- outcome{err: fmt.Errorf("读 stdout 失败: %w", err)}
			return
		}
		var resp wireResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			done <- outcome{err: fmt.Errorf("插件回的不是 JSON: %w", err)}
			return
		}
		if resp.ID != req.ID {
			// 串了号的回答比不回答更坏：A 的面板会显示 B 的数
			done <- outcome{err: fmt.Errorf("响应 id 回显错误：请求 %d，回来 %d", req.ID, resp.ID)}
			return
		}
		done <- outcome{resp: &resp}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-done:
		if res.err != nil {
			// 读写任一边断了都说明这个进程已经不讲道理了：当场终止，交给退避重启
			p.killLocked()
			p.mu.Unlock()
			return nil, res.err
		}
		p.mu.Unlock()
		return res.resp, nil
	case <-timer.C:
		p.killLocked()
		p.mu.Unlock()
		return nil, fmt.Errorf("插件 %s 在 %s 内没有回答，已终止该进程", p.id, timeout)
	}
}

// killLocked 终止当前进程（调用方持 mu）。watch 会把状态推进退避。
func (p *process) killLocked() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// readCappedLine 读一行，超过 MaxResponseBytes 直接拒（不但不吃内存，
// 也顺手挡掉一个坏插件用一行无限的输出拖死主程序的写法）。
func readCappedLine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice('\n')
		out = append(out, chunk...)
		if err == bufio.ErrBufferFull {
			if len(out) > MaxResponseBytes {
				return nil, fmt.Errorf("响应单行超过 %d 字节上限，已拒绝", MaxResponseBytes)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(out) > MaxResponseBytes {
			return nil, fmt.Errorf("响应单行超过 %d 字节上限，已拒绝", MaxResponseBytes)
		}
		return out, nil
	}
}

// tailBuffer 环形地只保留最后 N 字节的写入口（io.Writer，给 cmd.Stderr 用）。
// 锁是必需的：子进程随时可能在别的 goroutine 里往里写。
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newTailBuffer(max int) *tailBuffer {
	if max <= 0 {
		max = 4 << 10
	}
	return &tailBuffer{max: max}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

// String 目前攒下的尾巴。插件的日志不该由主程序落盘，只在面板上给一眼。
func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// truncateReason 把任何要进界面/日志的原因压到一行短文本里。
// 插件自报的错误字符串是外来输入：截断 + 去换行，不然大段文字会把警示卡撑爆。
func truncateReason(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	const keep = 200
	if len([]rune(s)) > keep {
		return string([]rune(s)[:keep]) + "…"
	}
	return s
}
