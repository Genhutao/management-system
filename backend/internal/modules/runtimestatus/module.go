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
			// M2 的三种只读组件在这里当对照物：同一个数据端口供七种组件，
			// 前端按清单里声明的类型各取渲染器。刻意选这个模块而不是再造一个演示模块——
			// 这些内容（数是怎么来的、进程在哪、注册表里有谁）技术维护组本来就要核对。
			{Key: "provenance", Type: modules.WidgetNote, Label: "这些数是怎么来的", DataEndpoint: DataEndpoint},
			{Key: "process", Type: modules.WidgetList, Label: "进程与环境", DataEndpoint: DataEndpoint},
			{Key: "registered", Type: modules.WidgetTable, Label: "已登记的模块", DataEndpoint: DataEndpoint},
		},
	})
}

// Handler 提供本模块的数据。刻意放在模块包里：以后加模块就是加一个包 + 一行路由注册，
// 不再往已经 3.3k 行的 controller 目录里塞文件。
type Handler struct{}

// Runtime GET /api/v1/mod/runtimestatus
//
// 每个组件的值全部现算，没有一条是写死的样板数字；读不到的东西就明写读不到
// （同本项目"取不到不许兜底成 0 或空"的口径）。
func (h *Handler) Runtime(c *gin.Context) {
	payload := modules.DataPayload{Values: map[string]modules.Value{}}

	payload.Values["uptime"] = modules.Value{
		Text: humanDuration(time.Since(startedAt)),
		Hint: "进程内计时起点 " + startedAt.Format("2006-01-02 15:04:05"),
	}

	// 时区是上线三坑之一：容器里往往是 UTC，扣分统计的"今天/本周"就会错一天。
	_, offset := time.Now().Zone()
	label := time.Now().Format("MST")
	payload.Values["timezone"] = modules.Value{
		Text:  label + " (UTC" + utcOffsetText(offset) + ")",
		Hint:  "系统时区设置不对会让\"本周\"少算一天；容器内需显式设 TZ=Asia/Shanghai",
		Level: statLevelWarnIf(offset != 8*3600),
	}

	mode := strings.TrimSpace(os.Getenv("GIN_MODE"))
	if mode == "" {
		mode = gin.Mode()
		payload.Values["gin_mode"] = modules.Value{
			Text:  mode,
			Hint:  "未显式设置 GIN_MODE，取的是当前值；生产应为 release",
			Level: statLevelWarnIf(mode != "release"),
		}
	} else {
		payload.Values["gin_mode"] = modules.Value{
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
	payload.Values["registry"] = modules.Value{
		Text: strconv.Itoa(len(modules.Modules())) + " 个模块",
		Hint: "manifest_version=" + strconv.Itoa(modules.ManifestVersion) +
			" · Go " + runtime.Version() + " · " + policyNote,
		Level: policyLevel,
	}

	// --- M2 的三种只读组件 ---------------------------------------------------

	// 说明卡：这一屏的数为什么会是这个样子。写在这里而不是写在界面里——
	// 界面是所有人共用的，口径却属于这个模块，塞进前端就成了"哪个模块来了都是这几句"。
	payload.Values["provenance"] = modules.Value{Text: strings.Join([]string{
		"本页每个数字都是这一次请求里现算的，模块不缓存，也没有写死的样板值。",
		"运行时长从进程初始化开始计时，比\"开始监听\"那行日志早几十毫秒，不是服务就绪时刻。",
		"模块数是注册表里的总量（含本模块自身）；某个账号能看到几个入口，是那次请求按 Casbin 现推的。",
		"取不到的那一项界面写\"无数据\"，不写 0——0 会被读成\"真的是 0\"。",
	}, "\n")}

	procItems := []modules.Item{
		{Label: "进程号", Value: strconv.Itoa(os.Getpid())},
		{Label: "启动时刻", Value: startedAt.Format("2006-01-02 15:04:05")},
		{Label: "Go 版本", Value: runtime.Version()},
		{Label: "系统 / 架构", Value: runtime.GOOS + " / " + runtime.GOARCH},
		{Label: "可用核数", Value: strconv.Itoa(runtime.NumCPU())},
	}
	// 工作目录是排查"相对路径下的数据库到底打开的是哪一个"时第一眼要看的东西，
	// 读不到就写读不到，不留一行空白让人以为目录是空字符串。
	if cwd, err := os.Getwd(); err == nil {
		procItems = append(procItems, modules.Item{Label: "工作目录", Value: cwd})
	} else {
		procItems = append(procItems, modules.Item{Label: "工作目录", Value: "读不到", Hint: err.Error(), Level: "warn"})
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	procItems = append(procItems, modules.Item{
		Label: "堆内存占用",
		Value: humanBytes(mem.HeapAlloc),
		Hint:  "Go 运行时报告的 HeapAlloc，未扣 GC 尚未回收的部分",
	})
	payload.Values["process"] = modules.Value{Items: procItems}

	// 登记表：每个模块的数从哪个接口来。技术维护组核对"这个数是哪来的"时，
	// 需要的就是这一张表——它比把八个路径名平铺在页面上好读，也比让人去翻代码快。
	registered := modules.Modules()
	cols := []modules.Column{
		{Key: "title", Label: "模块"},
		{Key: "group", Label: "分组"},
		{Key: "widgets", Label: "组件"},
		{Key: "source", Label: "数据取自"},
	}
	rows := make([][]string, 0, len(registered))
	for _, m := range registered {
		// 按钮上的端点是"点下去要 POST 的那个"，不是数据来源；混进这一列会让这张表
		// 把一次写说成一次读。
		endpoints := []string{}
		for _, w := range m.Widgets {
			if w.Type == modules.WidgetAction || w.DataEndpoint == "" {
				continue
			}
			seen := false
			for _, e := range endpoints {
				if e == w.DataEndpoint {
					seen = true
					break
				}
			}
			if !seen {
				endpoints = append(endpoints, w.DataEndpoint)
			}
		}
		source := "没有数据端口"
		if len(endpoints) == 1 {
			source = endpoints[0]
		} else if len(endpoints) > 1 {
			source = endpoints[0] + fmt.Sprintf(" 等 %d 个接口", len(endpoints))
		}
		group := m.Group
		if group == "" {
			group = "（未分组）"
		}
		rows = append(rows, []string{m.Title + "（" + m.ID + "）", group, strconv.Itoa(len(m.Widgets)), source})
	}
	payload.Values["registered"] = modules.Value{
		Columns: cols,
		Rows:    rows,
		Hint:    "含由插件加载器登记的模块；本表按模块 id 排序",
	}

	c.JSON(http.StatusOK, payload)
}

// humanBytes 把字节数说成人能读的那一句。刻意用 1024 进制并保留一位小数：
// 这一栏是拿来看"有没有异常大"的，不是拿来精确对账的。
func humanBytes(n uint64) string {
	const unit = 1024
	suffixes := []string{"KB", "MB", "GB", "TB", "PB"}
	value := float64(n)
	steps := 0
	for value >= unit && steps < len(suffixes) {
		value /= unit
		steps++
	}
	if steps == 0 {
		return strconv.FormatUint(n, 10) + " B"
	}
	return fmt.Sprintf("%.1f %s", value, suffixes[steps-1])
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
