// Package runtimestatus 是第一个清单驱动的扩展模块（插件 M1）。
//
// 选它当第一个真模块有两个理由：
//  1. 只读、不碰任何业务数据，加错也不会毁掉已有功能；
//  2. 它的数据端口只对技术维护组开放，正好实地验证一次"清单可见性由 Casbin 推导"——
//     其它四个角色拿到的清单里根本不该出现这个模块，而不是"出现了但点进去 403"。
//
// 接线形态：本模块自带处理器，所以 main.go 注册路由那一行 import 就已经让 init() 生效，
// 不需要 internal/modules/modules.go 的 blank import。只有"不新增路由、只挂已有接口"的
// 纯清单模块才需要那个汇总文件。
package runtimestatus

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/middleware"
	"xgh-system/internal/modules"
)

// RoutePath 是本模块数据端口在 /api/v1/mod 组下注册的那一段，main.go 直接引用它。
// 之所以拆出来：清单里写的是完整路径，路由注册写的是组内路径，两处各写一遍字面量迟早会漂。
// 漂了以后前端拿到的是一个 404 的 data_endpoint，表现是"扩展模块点进去读不到数"。
const RoutePath = "/runtimestatus"

// DataEndpoint 是模块自己的数据端口，路径段 /api/v1/mod/ 专门放模块接口。
const DataEndpoint = "/api/v1/mod" + RoutePath

// startedAt 取包初始化时刻：与进程启动相差不到几十毫秒（init 在 main 之前跑），
// 而真正的服务就绪时间还差一个"监听成功"的日志可查，这里不冒充精确值。
var startedAt = time.Now()

func init() {
	modules.Register(modules.Module{
		ID:                 "runtimestatus",
		Title:              "服务端运行状态",
		Icon:               "fa-solid fa-server",
		Group:              "运维",
		Tab:                "runtimestatus",
		MinManifestVersion: modules.ManifestVersion,
		Widgets: []modules.Widget{
			{Key: "uptime", Type: modules.WidgetStat, Label: "运行时长", DataEndpoint: DataEndpoint},
			{Key: "timezone", Type: modules.WidgetStat, Label: "服务端时区", DataEndpoint: DataEndpoint},
			{Key: "gin_mode", Type: modules.WidgetStat, Label: "运行模式", DataEndpoint: DataEndpoint},
			{Key: "registry", Type: modules.WidgetStat, Label: "清单与模块", DataEndpoint: DataEndpoint},
		},
	})
}

// Handler 提供本模块的数据。刻意放在模块包里：以后加模块就是加一个包 + 一行路由注册，
// 不再往已经 3.3k 行的 controller 目录里塞文件。
type Handler struct{}

// Runtime GET /api/v1/mod/runtimestatus
//
// 四条值全部现算，没有一条是写死的样板数字；读不到的东西就明写读不到
// （同本项目"取不到不许兜底成 0 或空"的口径）。
func (h *Handler) Runtime(c *gin.Context) {
	payload := modules.DataPayload{Values: map[string]modules.StatValue{}}

	payload.Values["uptime"] = modules.StatValue{
		Text: humanDuration(time.Since(startedAt)),
		Hint: "进程内计时起点 " + startedAt.Format("2006-01-02 15:04:05"),
	}

	// 时区是上线三坑之一：容器里往往是 UTC，扣分统计的"今天/本周"就会错一天。
	_, offset := time.Now().Zone()
	label := time.Now().Format("MST")
	payload.Values["timezone"] = modules.StatValue{
		Text:  label + " (UTC" + utcOffsetText(offset) + ")",
		Hint:  "系统时区设置不对会让\"本周\"少算一天；容器内需显式设 TZ=Asia/Shanghai",
		Level: statLevelWarnIf(offset != 8*3600),
	}

	mode := strings.TrimSpace(os.Getenv("GIN_MODE"))
	if mode == "" {
		mode = gin.Mode()
		payload.Values["gin_mode"] = modules.StatValue{
			Text:  mode,
			Hint:  "未显式设置 GIN_MODE，取的是当前值；生产应为 release",
			Level: statLevelWarnIf(mode != "release"),
		}
	} else {
		payload.Values["gin_mode"] = modules.StatValue{
			Text:  mode,
			Hint:  "来自环境变量 GIN_MODE",
			Level: statLevelWarnIf(mode != "release"),
		}
	}

	policyNote := "策略条数读不到（鉴权引擎未就绪）"
	policyLevel := "warn"
	if middleware.Enforcer != nil {
		if list, err := middleware.Enforcer.GetPolicy(); err == nil {
			policyNote = "casbin_rule 共 " + strconv.Itoa(len(list)) + " 条 p 规则"
			policyLevel = "ok"
		}
	}
	// 模块数包含本模块自身；清单里对当前角色可见的数量由 /ext/modules 决定，这里报的是注册总量。
	payload.Values["registry"] = modules.StatValue{
		Text: strconv.Itoa(len(modules.Modules())) + " 个模块",
		Hint: "manifest_version=" + strconv.Itoa(modules.ManifestVersion) +
			" · Go " + runtime.Version() + " · " + policyNote,
		Level: policyLevel,
	}

	c.JSON(http.StatusOK, payload)
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return strconv.Itoa(days) + " 天 " + strconv.Itoa(hours) + " 小时"
	case hours > 0:
		return strconv.Itoa(hours) + " 小时 " + strconv.Itoa(minutes) + " 分"
	default:
		return strconv.Itoa(minutes) + " 分"
	}
}

func utcOffsetText(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	// 分钟必须补零：拼成 "+8:0" 这种半截写法在界面上像个坏掉的数，
	// 而这一栏正是给人核对时区对不对用的（TZ 设错会让"本周"少算一天）。
	return fmt.Sprintf("%s%d:%02d", sign, seconds/3600, (seconds%3600)/60)
}

func statLevelWarnIf(bad bool) string {
	if bad {
		return "warn"
	}
	return "ok"
}
