package plugins

// action_scope.go —— 插件能发起哪些写动作：**由服务端决定，不由清单决定**
// （计划《学管会系统_插件能力扩展计划.md》§5.1 / §9 决策⑥⑦）。
//
// 这里放两份清单，都在代码里可查、可评审：
//
//	declarableActions  可声明路径白名单。不在上面的动作，manifest 写了就在扫描期被判校验失败。
//	forcedStepUp       高危清单。命中它的动作即使插件自己写 confirm:"none"，服务端也强制改成
//	                    要口令——"哪些接口算高危"是学校的口径，不能交给插件作者决定（决策⑦）。
//
// 为什么是两份显式清单而不是"从路由表生成"：路由表里 130 多条几乎每一条都能被写成
// "看起来合理"的动作，生成出来的白名单等于没有白名单。加一条动作的正确姿势是
// 改这个文件 + 过一次评审 + 重新签名 + 重新授权，四步缺一不可。
//
// 首批范围按使用方拍板走"**新增类**"起步：只放开"插入一条记录、且事后还能撤回/删掉"的接口，
// 不放"修改或删除既有业务数据"。后面要加，一条一条加，每条都得写下为什么。

import (
	"fmt"
	"strings"
)

// declarableActions 可声明的动作路径。key = "METHOD /path"（与 Casbin 的 obj+act 同形，
// 便于把这条声明和它的策略对着看）。
var declarableActions = map[string]string{
	// 广播台新闻稿：新增一条。宣传组日常就在做的动作，且 POST /publicity/broadcast/news
	// 之后有 PUT .../:id/toggle 能把这条下线——写错了有撤的路，这是"新增类"的硬要求。
	"POST /api/v1/publicity/broadcast/news": "新增一条播音稿（可在同一模块里下线，写错了撤得回）",

	// 值班时段配置：新增一条。技术维护组专属接口，配套有 DELETE /tech/task-slots/:id，
	// 所以同样是可撤回的写入。列进来还有一个用处：它是下面 forcedStepUp 里那条高危项，
	// 能当面验一次"插件自己声明免口令也不给免"。
	"POST /api/v1/tech/task-slots": "新增一条查寝值班时段（改的是全校当晚的值班口径，属高危）",
}

// forcedStepUp 高危清单：命中即要求操作者当场输登录口令（走 requireStepUp 的 bcrypt 与限流），
// 与插件在 manifest 里写的 confirm 无关。
//
// 值是那句话说给人看的——授权界面与审计都要引用它，所以别写成"危险"这种半年后看不懂的话。
var forcedStepUp = map[string]string{
	"POST /api/v1/tech/task-slots": "值班时段是全校当晚查寝的依据，配错一晚的值班记录会整体挂错",
}

// forbiddenActionPrefixes 明确点名拒绝的几段。它们本来就不在可声明白名单里，
// 单独列一条只为了**报错要说得出为什么**：运维看到"路径不在白名单"会继续往里加，
// 看到"这条路径等于把后台数据编辑器包一层皮"才知道这条路根本不该走。
var forbiddenActionPrefixes = map[string]string{
	"/api/v1/tech/db/": "那是后台通用数据编辑器，插件动作指过来等于给它包一层皮、绕过所有业务校验",
	"/api/v1/mod/":     "那是插件自己的数据端点：动作再回头打它，会在同一个插件的串行管道上 self-deadlock",
	"/api/v1/auth/":    "登录、改密、会话这类账号动作由「账户安全中心」承担，不经插件之手",
}

// actionAllowedScope 判一条动作声明能不能进可授权列表。
// 返回的 error 就是面板上那句原因，必须指名道姓说清是"没在白名单"还是"这段就是禁的"。
func actionAllowedScope(method, path string) error {
	key := method + " " + path
	if _, ok := declarableActions[key]; ok {
		return nil
	}
	for prefix, why := range forbiddenActionPrefixes {
		if strings.HasPrefix(path, prefix) {
			return fmt.Errorf("动作路径 %s 落在 %s 这一段：%s", path, prefix, why)
		}
	}
	return fmt.Errorf("动作路径 %s %s 不在服务端可声明清单里：加动作要改 internal/plugins/action_scope.go 并过评审，光靠 manifest 写了不算", method, path)
}

// effectiveConfirm 服务端认定的二次确认方式。
// 插件写 stepup 就 stepup；命中高危清单的话，它写 none 也不算——这条不能交给作者决定。
func effectiveConfirm(method, path, claimed string) (string, string) {
	if why, ok := forcedStepUp[method+" "+path]; ok {
		return "stepup", why
	}
	return claimed, ""
}

// DeclarableNote 这条接口在**服务端**那本账上是怎么写的（S5 批准界面要用）。
//
// 它和作者在 manifest 里写的 reason 是两栏，不是一栏：批准一条写通道时，
// 人需要同时看到"申请人说为什么要改"和"这台服务知道这条接口是干什么的、错了怎么撤"。
// 只留前者，批准就退化成了信一个人的一句话。
//
// 查不到（不在白名单）时回空串——那种动作在扫描期就已经被拒，走不到批准界面。
// 这里刻意**不**兜底一句"没有说明"：那会让一条没登记的接口在界面上看起来
// 和一条登记了但没写说明的接口长一个样，而这两件事的处理方式完全不同。
func DeclarableNote(method, path string) string {
	return declarableActions[method+" "+path]
}
