package controller

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
	"xgh-system/internal/plugins"
)

// plugin_action_controller.go —— 插件动作端点：一次点击怎么变成一次代执行。
//
// 这条路由是整套扩权设计里唯一真正"写数据"的地方，所以顺序就是一条安全清单，
// 少一步都会留下一个能被利用的缝：
//
//  1. 路由只在本插件**声明过动作**时才挂（main.go 启动期决定），点得到一个没动作的插件不可能；
//  2. 动作 key 必须在**签名过的那份 manifest** 里，且这个插件必须已经授权过
//     （客户端传来的 method/path 一概不采信；未授权的插件在要口令之前就被挡下，见 Manager.ActionGate）；
//  3. 这条动作必须**被单独批准过**（S5）。插件已授权 ≠ 它可以写：批的是哪几条由超管在申请单上
//     逐条勾出来，没勾的那条在这里回 409 并留一条审计——一次被挡下的写尝试恰恰最该留痕；
//  4. 需要二次确认的，先过 requireStepUp（bcrypt + 未改初始口令拦截 + 失败限流），
//     **口令没过的请求不该让插件进程被问一次**；
//  5. 参数必须逐字落在清单声明过的那几栏上；
//  6. 去重窗口（连点两次只一笔）；
//  7. 问插件要请求，并与签名清单逐字比对 —— 不符就是签名承诺被违反，当场拒掉；
//  8. 带**操作者自己的会话**打本机回环：Casbin、token_version、jti、账号停用全部照常生效；
//  9. 无论成败都留一条 plugin.action 审计，里面是真实来源 IP。
//
// 第 7 步是"插件不可能越操作者的权"这句话的落点，所以它有测试（验收①）。

// PluginActionController 动作端点的处理器。一个插件一个 handler，由 main.go 在启动期挂好。
type PluginActionController struct {
	mgr *plugins.Manager
}

func NewPluginActionController(mgr *plugins.Manager) *PluginActionController {
	return &PluginActionController{mgr: mgr}
}

type pluginActionBody struct {
	Action string         `json:"action"`
	Params map[string]any `json:"params"`
}

// Handle POST /api/v1/mod/:id/action。
func (ctl *PluginActionController) Handle(id string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body pluginActionBody
		// DisallowUnknownFields 是刻意的：多一个 "path" 或 "method" 键如果被判"没写"，
		// 表现就是"我明明指了那个接口它却没动"，而真正的原因（客户端在替清单做主）会被掩盖。
		dec := json.NewDecoder(c.Request.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体读不懂：" + err.Error()})
			return
		}
		key := strings.TrimSpace(body.Action)
		if key == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 action：要发起哪个动作由签过名的清单决定，这里只能给它的 key"})
			return
		}
		declared, ok := ctl.mgr.DeclaredAction(id, key)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "插件 " + id + " 的签名清单里没有声明过动作 " + key})
			return
		}
		// 信任门放在**要口令之前**：一个没授权的插件不该让人先输一遍登录口令才听到"未授权"。
		if why := ctl.mgr.ActionGate(id); why != "" {
			// 409 而不是 403：403 在这个系统里说的是"你的角色不够"，而这里不够的是插件的状态。
			c.JSON(http.StatusConflict, gin.H{"error": why})
			return
		}

		operator, ok := operatorFromContext(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
			return
		}
		// 逐条批准门（S5），同样在要口令之前，但它**留审计**：走到这一步插件是已授权的，
		// "有人试图发起一条没被批准的写动作"正是这条门最需要留下痕迹的那种尝试。
		if why := ctl.mgr.GrantGate(id, declared.Key); why != "" {
			ctl.audit(c, operator, id, declared, "未批准", why, nil)
			c.JSON(http.StatusConflict, gin.H{"error": why})
			return
		}
		// confirm 存进来时已经按服务端高危清单归一化过，所以这里信这一个字段就行。
		if declared.Confirm == "stepup" {
			if !requireStepUp(c, operator) {
				return
			}
		}

		params, err := plugins.ValidateActionParams(declared, body.Params)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// 参数摘要与认领：去重窗口**按人分**——另一个人刚才提交过一模一样的内容，
		// 不该让这个人收到"你已经发起过一次"。
		fingerprint := plugins.ParamsFingerprint(id, declared.Key, params)
		if err := ctl.mgr.ClaimAction(fingerprint, operator.ID); err != nil {
			// 429 而不是 400：这不是请求写错了，是来太快了，客户端等一会儿重试是对的。
			c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error()})
			return
		}

		prepared, err := ctl.mgr.PrepareAction(id, declared, params, operator.ID, operator.Role)
		if err != nil {
			ctl.audit(c, operator, id, declared, "未执行", err.Error(), params)
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		res, err := ctl.mgr.ExecuteAction(prepared, credentialOf(c))
		if err != nil {
			ctl.audit(c, operator, id, declared, "未送达", err.Error(), params)
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		// 审计里写状态码原值，不写 http.StatusText：那句是英文（"Forbidden"/"Created"），
		// 夹在中文详情里既不好读也容易被当成某个业务错误码。
		ctl.audit(c, operator, id, declared, "HTTP "+strconv.Itoa(res.Status), "", params)
		// 内层业务接口的状态码原样转出去。改成 200 就是替插件撒谎：
		// 403 说成成功，界面上会显示"已完成"，而库里什么都没变。
		ctype := res.ContentType
		if ctype == "" {
			ctype = "application/json; charset=utf-8"
		}
		c.Data(res.Status, ctype, res.Body)
	}
}

// credentialOf 把操作者自己的会话凭据原样取出来交给代执行。
// 两条路都认（AuthMiddleware 也是两条都认）：APK 走 Authorization，Web 走会话 Cookie。
func credentialOf(c *gin.Context) plugins.ActionCredentials {
	return plugins.ActionCredentials{
		Authorization: c.GetHeader("Authorization"),
		Cookie:        c.GetHeader("Cookie"),
	}
}

// audit 一条 plugin.action 留痕。
//
// 为什么每次尝试都要写、包括没执行成的那一次：验收清单里"部员点了只有部长能做的动作
// ⇒ 403，且这条 403 出现在审计里"要的就是这条记录 —— 一次被拦下来的越权尝试，
// 恰恰是最该留下痕迹的那一次。
//
// IP 用的是**外层请求**的 ClientIP，也就是浏览器/客户端真正的来源。内层那一跳是回环，
// 业务处理器自己写的审计里 IP 只会是 127.0.0.1（计划 §5.3 代价 1）；两句话都在，
// 才对得上"谁在哪个 IP 上点了这个按钮、系统替他改了哪条接口"。
//
// 参数值不进审计：动作参数经常是人名、事由这类内容，审计要回答的是
// "谁对哪条接口发起了什么动作、结果如何"，不是把业务内容抄一份第二遍。
func (ctl *PluginActionController) audit(c *gin.Context, operator model.User, id string, a modules.Action, outcome, why string, params map[string]string) {
	names := make([]string, 0, len(params))
	for k := range params {
		names = append(names, k)
	}
	sort.Strings(names)

	detail := "插件 " + id + " 动作 " + a.Key + "（" + a.Label + "） · 宿主代执行 " + a.Method + " " + a.Path +
		" · 参数 " + strings.Join(names, "、") + " · 结果 " + outcome
	if why != "" {
		detail += " · 原因 " + why
	}
	if a.Confirm == "stepup" {
		detail += " · 已二次确认登录口令"
	}
	logOperationAs(c, operator, "plugin.action", "plugin", 0, detail)
}
