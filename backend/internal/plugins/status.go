package plugins

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/modules"
)

// status.go —— 加载器自带的信息模块（方案 §7.1，也是使用方那两条新要求里的第一条：
// "加载器自带 mod 信息显示，仅 tech_admin"）。
//
// 它是一个**编译期**模块（形态与 runtimestatus 完全一样），描述符在 init() 里登记，
// 数据端口现算四个数。选这个形态的理由：
//   - 可见性不用自己写判定：tech_admin 有 seed 里那条 /api/v1/mod/* 显式策略，
//     其余四个角色没有 —— 清单里出不出现，是 Casbin 推出来的，不是这里 if 出来的；
//   - 前端零改动：侧栏入口、面板、卡片全走 M1 已经跑通的清单驱动路径。

// StatusRoutePath / StatusDataEndpoint 与 runtimestatus 同一套拆法：清单写全路径、
// 路由写组内路径，只在一处写字面量，免得两处漂移成"清单里点进去 404"。
// 带 Status 前缀是因为 loader 里那两个名字属于"按插件 id 推导"的函数（DataEndpoint(id)）。
const (
	StatusRoutePath    = "/pluginstatus"
	StatusDataEndpoint = "/api/v1/mod" + StatusRoutePath
)

const (
	widgetLoaded    = "loaded"
	widgetRunning   = "running"
	widgetPending   = "pending"
	widgetAttention = "attention"
)

func init() {
	modules.Register(modules.Module{
		ID:                 "pluginstatus",
		Title:              "插件加载器",
		Icon:               "fa-solid fa-puzzle-piece",
		Group:              "运维",
		Tab:                "pluginstatus",
		MinManifestVersion: modules.ManifestVersion,
		Widgets: []modules.Widget{
			{Key: widgetLoaded, Type: modules.WidgetStat, Label: "已授权已登记", DataEndpoint: StatusDataEndpoint},
			{Key: widgetRunning, Type: modules.WidgetStat, Label: "进程运行中", DataEndpoint: StatusDataEndpoint},
			{Key: widgetPending, Type: modules.WidgetStat, Label: "待授权", DataEndpoint: StatusDataEndpoint},
			{Key: widgetAttention, Type: modules.WidgetStat, Label: "要处理的", DataEndpoint: StatusDataEndpoint},
		},
	})
}

// HandleStatus GET /api/v1/mod/pluginstatus。
//
// 四个数全部现算，没有一条是缓存或写死的（同本项目"取不到不许兜底成 0"的口径）：
// 读不到的东西明写读不到，比给一个看着正常的数字有用。
func (m *Manager) HandleStatus(c *gin.Context) {
	s := m.Stats()
	views := m.Views()

	payload := modules.DataPayload{Values: map[string]modules.Value{}}
	payload.Values[widgetLoaded] = statCard(s.Loaded,
		"已授权且已登记进清单；进程此刻活不活看\"进程运行中\"那张卡",
		false, "")
	payload.Values[widgetRunning] = statCard(s.Running,
		"子进程当前活着（正在退避重启的那些不算）",
		s.Loaded > 0 && s.Running < s.Loaded,
		"有已授权的插件进程没在跑，去管理卡看最近错误")
	payload.Values[widgetPending] = statCard(s.Pending, pendingHint(views),
		s.Pending > 0, "待授权的东西不会自己上线")
	payload.Values[widgetAttention] = statCard(s.Attention, attentionHint(views),
		s.Attention > 0, "校验失败 / 未签名 / 签名不符 / 需重新授权 / 已放弃，都需要人看一眼")
	c.JSON(http.StatusOK, payload)
}

// statCard 一张数卡。warn 不是错误，是"这个数值得看一眼"，
// 所以亮 warn 时必须把下一步说给人听，而不是只把数字涂成琥珀色。
func statCard(n int, hint string, bad bool, why string) modules.Value {
	level := "ok"
	if bad {
		level = "warn"
		hint = hint + " · " + why
	}
	return modules.Value{Text: strconv.Itoa(n) + " 个", Hint: hint, Level: level}
}

func pendingHint(views []View) string {
	ids := collectByState(views, StatePending)
	if len(ids) == 0 {
		return "plugins/ 下没有等着授权的目录"
	}
	// 数出来"有 1 个待授权"之后，下一个问题一定是"去哪儿点"——这句话不说清，
	// 人就会以为授权按钮长在这个页面上（授权表确实就在本页下方，由宿主 app.js 拼）。
	return "等授权：" + strings.Join(ids, "、") + " · 授权入口：本页下方「插件管理」表"
}

func attentionHint(views []View) string {
	ids := collectByState(views, StateInvalid)
	ids = append(ids, collectByState(views, StateUnsigned)...)
	ids = append(ids, collectByState(views, StateSigBroken)...)
	ids = append(ids, collectByState(views, StateStale)...)
	ids = append(ids, collectByState(views, StateGaveUp)...)
	if len(ids) == 0 {
		return "没有校验失败、没有未签名或签名不符、没有换过文件、也没有放弃重启的插件"
	}
	return "要看一眼：" + strings.Join(ids, "、") + " · 原因见本页下方「插件管理」表的状态列"
}

func collectByState(views []View, st State) []string {
	out := []string{}
	for _, v := range views {
		if v.State == st {
			out = append(out, v.ID)
		}
	}
	return out
}
