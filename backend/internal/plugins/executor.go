package plugins

// executor.go —— 宿主代执行（计划 §2 / §5.3）。
//
// 一句话口径：**插件只写"该发哪个请求"，发的是宿主**。
// 这样权限模型一个字都不用改：Casbin 判定用的就是那个点按钮的人的身份，
// 插件能改的东西恰好等于他自己能改的东西，不多一条。这句话下面有测试钉着（验收①）。
//
// 三段职责分开，各在一个函数里：
//
//	ValidateActionParams  客户端传上来的参数必须逐字落在清单声明过的那几个键上
//	PrepareAction         问插件要请求 + 把请求与签名清单逐字比对（越界就拒）
//	ExecuteAction         带操作者自己的会话打本机回环，把结果原样带回去
//
// 为什么比对放在执行之前、且不解析 body：body 是插件的业务语义，宿主看不懂也不该看懂，
// 看不懂又要负责，唯一站得住的做法就是把"能发到哪"这件事钉死在签名清单上。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"xgh-system/internal/modules"
)

const (
	// ActionDedupWindow 去重窗口（验收④"连点两次只一笔"）。
	// 这是防手滑，不是业务幂等：本项目请假审批重复加分这类洞还在（API 文档 §9），
	// 一个 60 秒的窗口不会让它变好，所以真正兜底的是"能发哪条接口"那份白名单。
	ActionDedupWindow = 60 * time.Second

	// MaxActionBodyBytes 插件回的请求体上限。256KB 是整行响应的上限，这里再收一道：
	// 一个动作要塞进去几 MB JSON，多半不是动作而是导入。
	MaxActionBodyBytes = 32 << 10

	// MaxActionParamBytes 单个参数的默认上限（清单里那一条写了更小的按更小的）。
	MaxActionParamBytes = modules.MaxActionArgBytes

	// selfCallTimeout 回环那一跳的客户端超时（计划 §5.3 代价 2）。
	selfCallTimeout = 10 * time.Second

	// PluginActedForHeader 内部标记头：值由宿主自己塞，中间件对非回环来源一律剥掉。
	PluginActedForHeader = "X-Plugin-Acted-For"
)

// ActionCredentials 操作者自己的会话凭据。
// 两条都带着：AuthMiddleware 优先看 Authorization，再看会话 Cookie（auth.go:124），
// Web 端与 APK/客户端走的是不同的那条路，这里不替它选。
type ActionCredentials struct {
	Authorization string
	Cookie        string
}

// PreparedAction 一次将要被代执行的动作。
type PreparedAction struct {
	Plugin string
	Action modules.Action
	Method string
	Path   string
	Body   []byte
}

// ValidateActionParams 把客户端传来的参数按清单收一遍。
//
// 三条硬规则，都是"少一条就会出现没人解释得了的行为"：
//   - 键必须**全部**在 fields 里声明过。多一个键就整笔拒收，不"顺手带上"：
//     那等于让浏览器替插件决定 stdin 里有什么；
//   - 声明过的键**都得给**（值不为空）。清单说这次要收标题和正文，就不能只给一个，
//     少给一个字段而让插件自己兜底，界面上看到的就是"提交成功"但其实填了个默认值；
//   - 值必须是字符串且不超长。数字/布尔/嵌套一律拒，理由同上：形状由清单决定。
func ValidateActionParams(a modules.Action, got map[string]any) (map[string]string, error) {
	max := map[string]int{}
	for _, f := range a.Fields {
		limit := f.MaxBytes
		if limit <= 0 {
			limit = MaxActionParamBytes
		}
		max[f.Key] = limit
	}
	out := make(map[string]string, len(got))
	for k, v := range got {
		limit, declared := max[k]
		if !declared {
			return nil, fmt.Errorf("参数 %q 没有在清单里声明过：动作能收什么由签过名的 manifest 决定，不由调用方决定", k)
		}
		s, isString := v.(string)
		if !isString {
			return nil, fmt.Errorf("参数 %q 必须是文本：清单里它是一栏给人填的东西，不是结构体", k)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("参数 %q 是空的：这条动作声明了它就得填上，不许留空让插件自己兜底", k)
		}
		if len(s) > limit {
			return nil, fmt.Errorf("参数 %q 超过 %d 字节上限（实际 %d）", k, limit, len(s))
		}
		out[k] = s
	}
	// 少给一个字段单独报，而且排在上面那段之后：先说清"多传的那个不许传"，
	// 再说"少传的那个必须给"——反过来会让人以为去掉多余键就够了。
	missing := make([]string, 0, len(max))
	for k := range max {
		if _, ok := out[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("缺参数 %s：这条动作声明了 %d 栏（%s），一栏都不能少",
			strings.Join(missing, "、"), len(max), strings.Join(keysOf(max), "、"))
	}
	return out, nil
}

// PrepareAction 问插件要"这次点击该发的那个请求"，并把它与签名清单逐字比对。
//
// 越界（path/method 与清单不符）不是"重试一下"的错误，是**签名承诺被违反**：
// 回一句明确的话，并让调用方把它记进审计（验收②）。这里不 kill 进程——
// 插件回错一次可能是 bug，把它判死会让面板上突然出现"已放弃"，那是另一种误导。
func (m *Manager) PrepareAction(id string, a modules.Action, params map[string]string, userID uint, role string) (*PreparedAction, error) {
	proc := m.processFor(id)
	if proc == nil {
		return nil, fmt.Errorf("插件 %s 没在运行（%s）", id, m.absentReason(id))
	}
	req, selfErr, err := proc.Action(a.Key, params, userID, role)
	switch {
	case err != nil:
		return nil, err
	case selfErr != "":
		return nil, fmt.Errorf("插件自己拒绝了这次动作：%s", selfErr)
	}
	gotMethod := strings.ToUpper(strings.TrimSpace(req.Method))
	gotPath := strings.TrimSpace(req.Path)
	if gotMethod != a.Method || gotPath != a.Path {
		return nil, fmt.Errorf("插件越界：签名清单里这条动作是 %s %s，它回的是 %s %s。请求未发出，一次写都没发生",
			a.Method, a.Path, gotMethod, gotPath)
	}
	body := []byte(req.Body)
	if len(body) > MaxActionBodyBytes {
		return nil, fmt.Errorf("插件回的请求体 %d 字节，超过动作上限 %d：一个按钮要塞这么多内容，多半不是动作而是导入",
			len(body), MaxActionBodyBytes)
	}
	// 这里**不**再验一遍 body 是不是合法 JSON：req.Body 是 json.RawMessage，
	// 能被解出来就说明整行 JSON 里它是合式的一段，再验一遍是给不存在的场景写分支。
	return &PreparedAction{
		Plugin: id, Action: a, Method: gotMethod, Path: gotPath,
		Body: body,
	}, nil
}

// ExecuteAction 带操作者自己的会话向本机回环发那一个请求，把结果原样带回来。
//
// 三处刻意不做：
//   - 不重试：这是写操作，超时之后"到底写没写进去"没人知道，重试可能写两笔；
//   - 不改写状态码：内层业务接口说 403 就是 403，界面上那句话必须是真的；
//   - 不带上插件回的任何 header（含它自己塞的 Authorization / Cookie / 口令头）。
type ExecuteResult struct {
	Status      int
	Body        []byte
	ContentType string
}

func (m *Manager) ExecuteAction(prepared *PreparedAction, cred ActionCredentials) (*ExecuteResult, error) {
	if cred.Authorization == "" && cred.Cookie == "" {
		// 一个凭据都没有就别发出去：那趟请求一定会在 AuthMiddleware 上拿 401，
		// 而那句 401 会被界面读成"插件的动作失败了"，把"会话没带上"这个真因盖掉。
		return nil, fmt.Errorf("操作者的会话凭据没带上来，动作未执行")
	}
	baseURL := m.SelfBaseURL()
	if baseURL == "" {
		return nil, fmt.Errorf("没有配本机回环地址，代执行无法进行（检查 PORT 是否与监听端口一致）")
	}
	var reader io.Reader
	if len(prepared.Body) > 0 {
		reader = bytes.NewReader(prepared.Body)
	}
	req, err := http.NewRequest(prepared.Method, baseURL+prepared.Path, reader)
	if err != nil {
		return nil, fmt.Errorf("构造回环请求失败: %w", err)
	}
	if cred.Authorization != "" {
		req.Header.Set("Authorization", cred.Authorization)
	} else if cred.Cookie != "" {
		req.Header.Set("Cookie", cred.Cookie)
	} else {
		// 一个凭据都没有就别发出去：那趟请求一定会在 AuthMiddleware 上拿 401，
		// 而那句 401 会被界面读成"插件的动作失败了"，把"会话没带上"这个真因盖掉。
		return nil, fmt.Errorf("操作者的会话凭据没带上来，动作未执行")
	}
	if len(prepared.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	// 值由宿主拼，插件与浏览器都改不动：中间件对非回环来源会把这个头剥掉。
	req.Header.Set(PluginActedForHeader, fmt.Sprintf("plugin=%s action=%s", prepared.Plugin, prepared.Action.Key))

	client := &http.Client{Timeout: selfCallTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// 超时与连不上都到不了"状态码"这一层。这句话不许含糊成"操作失败"：
		// 写操作超时意味着"可能已经写进去了"，运维需要知道的是这件事。
		return nil, fmt.Errorf("回环调用没走通（%s %s）：%v。这一步的结果是未知的——可能一次都没写进去，"+
			"也可能已经写成而回话断了。先去对应页面核对，确认没写成再重试，别直接点第二下",
			prepared.Method, prepared.Path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxActionBodyBytes*4))
	if err != nil {
		return nil, fmt.Errorf("读回环响应失败: %w", err)
	}
	return &ExecuteResult{
		Status:      resp.StatusCode,
		Body:        raw,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// ParamsFingerprint 一次动作的参数摘要，去重窗口用它当键（见 ClaimAction）。
// 键排序后再拼：map 的遍历顺序每次都不同，不排序的话"同一个动作"会产生两个摘要，
// 去重窗口就形同虚设。
func ParamsFingerprint(plugin, key string, params map[string]string) string {
	names := make([]string, 0, len(params))
	for k := range params {
		names = append(names, k)
	}
	sort.Strings(names)
	var buf strings.Builder
	buf.WriteString(plugin)
	buf.WriteByte('\n')
	buf.WriteString(key)
	for _, k := range names {
		buf.WriteByte('\n')
		buf.WriteString(k)
		buf.WriteByte('=')
		buf.WriteString(params[k])
	}
	sum := sha256.Sum256([]byte(buf.String()))
	return hex.EncodeToString(sum[:])
}

// ClaimAction 60 秒去重窗口，**按人分窗**：判"最近有没有做过"用的是 (操作者, 插件, 动作, 参数摘要)。
//
// 为什么按人而不是全校一把：这条窗口要治的是"同一个人连点两下产生两笔"。
// 上一轮端到端就撞见了全局窗口的副作用——宿管绕过界面打了一次、被内层 403 挡下，
// 那次失败的认领把窗口占住，紧接着部员点自己那一份完全正常的稿子收到的是
// "已经发起过一次"。他被挡住的原因不在他身上，这句话就没有指错方向的余地了。
// 跨人去重也不是安全边界：真正的重复由业务自己的规则管。
//
// 认领仍然在真正执行**之前**落下，且**失败的认领保留**：写操作超时之后结果未知，
// 这时释放窗口等于鼓励同一个人再点一次。要立刻重做的人可以等 60 秒，或者去对应页面核对。
//
// 窗口只在进程内存里（重启即清），这是刻意的：一次重启不该让某个动作被永久锁住。
func (m *Manager) ClaimAction(fingerprint string, operatorID uint) error {
	claimKey := strconv.FormatUint(uint64(operatorID), 10) + "|" + fingerprint
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claims == nil {
		m.claims = map[string]time.Time{}
	}
	now := time.Now()
	// 顺手清掉过期的，不然这个 map 会跟着服务一直开着慢慢长（一条 60 秒的记录留一辈子）。
	for k, at := range m.claims {
		if now.Sub(at) > ActionDedupWindow {
			delete(m.claims, k)
		}
	}
	if at, ok := m.claims[claimKey]; ok && now.Sub(at) <= ActionDedupWindow {
		return fmt.Errorf("你已经发起过一次同样的动作了（%s 之内，还有 %s 到窗口结束）：这次没有执行，避免连点产生两笔",
			ActionDedupWindow, (ActionDedupWindow - now.Sub(at)).Round(time.Second))
	}
	m.claims[claimKey] = now
	return nil
}

func keysOf(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for k := range counts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
