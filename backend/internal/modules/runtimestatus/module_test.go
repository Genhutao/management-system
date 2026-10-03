package runtimestatus

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/middleware"
	"xgh-system/internal/modules"
)

// 这个包是第一个"清单驱动"的真模块，所以测试要同时钉住三件事：
// 描述符本身、描述符与路由接线是否同一套字符串、以及处理器给出去的值是不是真算出来的。
// 第三件最容易被样板代码蒙过去 —— 一个写死 "1 个模块" 的处理器也能让形状断言全绿。

func callRuntime(t *testing.T) (int, modules.DataPayload) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, DataEndpoint, nil)
	(&Handler{}).Runtime(c)

	var payload modules.DataPayload
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不符合数据契约: %v / %s", err, w.Body.String())
	}
	return w.Code, payload
}

// 描述符必须真的注册进来（init() 有没有被 import 拉起来，是这套架构最容易失效的一环：
// 忘记在 main.go 里 import，模块就静默地不在清单里，界面少一个入口，日志一个字不多）。
func TestDescriptorIsRegisteredWithStatWidgets(t *testing.T) {
	var found *modules.Module
	mods := modules.Modules()
	for i := range mods {
		if mods[i].ID == "runtimestatus" {
			found = &mods[i]
		}
	}
	if found == nil {
		t.Fatal("runtimestatus 没有出现在注册表里：init() 未执行，或 Register 被改坏了")
	}
	// 前端把 tab 拼成 panel-<tab> 与 sidebar-nav-<tab>，所以它必须与 id 同名且合规
	if found.Tab != found.ID {
		t.Errorf("tab 应等于 id（前端按它生成 panel-<tab>），实际 tab=%q id=%q", found.Tab, found.ID)
	}
	if strings.TrimSpace(found.Title) == "" || strings.TrimSpace(found.Group) == "" {
		t.Error("标题与分组都不能为空：侧栏会是一个认不出来的图标")
	}
	// 本模块同时是 M2 三种只读组件的对照物：key → 期望类型写死在这里，
	// 谁加了一类却忘了改数据端口，下面那条覆盖测试会挂。
	want := map[string]string{
		"uptime":     modules.WidgetStat,
		"timezone":   modules.WidgetStat,
		"gin_mode":   modules.WidgetStat,
		"registry":   modules.WidgetStat,
		"provenance": modules.WidgetNote,
		"process":    modules.WidgetList,
		"registered": modules.WidgetTable,
	}
	if len(found.Widgets) != len(want) {
		t.Fatalf("应有 %d 个组件，实际 %d 个", len(want), len(found.Widgets))
	}
	for _, w := range found.Widgets {
		typ, ok := want[w.Key]
		if !ok {
			t.Errorf("组件 key %q 不在预期之内", w.Key)
			continue
		}
		delete(want, w.Key)
		if w.Type != typ {
			t.Errorf("组件 %q 类型为 %q，期望 %q", w.Key, w.Type, typ)
		}
		if strings.TrimSpace(w.Label) == "" {
			t.Errorf("组件 %q 没有标签", w.Key)
		}
		if w.DataEndpoint != DataEndpoint {
			t.Errorf("组件 %q 的端口应为 %s，实际 %s", w.Key, DataEndpoint, w.DataEndpoint)
		}
	}
	if len(want) != 0 {
		t.Errorf("缺少组件 key: %v", want)
	}
}

// 清单里的完整路径与 main.go 注册的组内路径必须同源，否则前端拿到的是一个 404 的 data_endpoint。
func TestDataEndpointIsBuiltFromRoutePath(t *testing.T) {
	if RoutePath != "/runtimestatus" {
		t.Errorf("RoutePath 变了要同时改 Casbin 策略与清单：实际 %q", RoutePath)
	}
	if DataEndpoint != "/api/v1/mod"+RoutePath {
		t.Errorf("DataEndpoint 必须由 /api/v1/mod + RoutePath 拼出，实际 %q", DataEndpoint)
	}
}

func TestRuntimeReturnsRealComputedValues(t *testing.T) {
	code, payload := callRuntime(t)
	if code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", code)
	}
	if payload.Values == nil {
		t.Fatal("values 不能是 null：前端按对象取键")
	}

	for _, key := range []string{"uptime", "timezone", "gin_mode", "registry"} {
		v, ok := payload.Values[key]
		if !ok {
			t.Errorf("缺少组件 %q 的取值：这个端口本该给得出，缺了前端就只显示\"无数据\"", key)
			continue
		}
		if strings.TrimSpace(v.Text) == "" {
			t.Errorf("组件 %q 的 text 为空", key)
		}
		if v.Level != "" && v.Level != "ok" && v.Level != "warn" {
			t.Errorf("组件 %q 的 level=%q，契约只认 ok / warn（空串按 ok 处理）", key, v.Level)
		}
	}

	// 运行模式取的是进程真实生效值：环境变量优先，没设则取 gin 自己的判定
	wantMode := strings.TrimSpace(os.Getenv("GIN_MODE"))
	if wantMode == "" {
		wantMode = gin.Mode()
	}
	if got := payload.Values["gin_mode"].Text; got != wantMode {
		t.Errorf("gin_mode 应为生效模式 %q，实际 %q（写死一个字符串会让上线时看不出还在 debug）", wantMode, got)
	}

	// 时区同理：直接和 time.Now().Zone() 的偏移比对
	_, offset := time.Now().Zone()
	if got, want := payload.Values["timezone"].Text, time.Now().Format("MST")+" (UTC"+utcOffsetText(offset)+")"; got != want {
		t.Errorf("timezone 应为 %q，实际 %q", want, got)
	}

	// 注册数必须是活的。做法是先记下当前数字，再登记一个探针模块，然后要求这个数字变大且
	// 等于注册表长度 —— 一个把模块数写死的处理器在"只注册了一个模块"时也能通过形状检查，
	// 但它过不了这一条。探针模块留在测试进程的注册表里即可：本包的测试二进制是独立的。
	firstRegistryText := payload.Values["registry"].Text
	probeID := "probe-registry-count"
	_, probeExisted := findModule(probeID)
	if !probeExisted {
		modules.Register(modules.Module{
			ID: probeID, Title: "探针", Tab: probeID,
			MinManifestVersion: modules.ManifestVersion,
			Widgets:            []modules.Widget{{Key: "k", Type: modules.WidgetStat, Label: "L", DataEndpoint: "/api/v1/dashboard/summary"}},
		})
	}
	_, after := callRuntime(t)
	want := strconv.Itoa(len(modules.Modules())) + " 个模块"
	if got := after.Values["registry"].Text; got != want {
		t.Errorf("registry 应等于注册表当前长度（期望 %q，实际 %q）", want, got)
	}
	if !probeExisted && after.Values["registry"].Text == firstRegistryText {
		t.Errorf("多注册了一个模块而数字没变（都是 %q）：这个数字是写死的", firstRegistryText)
	}
	hint := after.Values["registry"].Hint
	if !strings.Contains(hint, "manifest_version="+strconv.Itoa(modules.ManifestVersion)) {
		t.Errorf("hint 里应带上清单契约版本，方便排查前后端版本不匹配，实际 %q", hint)
	}
	if !strings.Contains(hint, runtime.Version()) {
		t.Errorf("hint 里应带上 Go 版本，实际 %q", hint)
	}
}

// 声明了什么类型，端口就得回什么形状。文件期插件由服务端在出口处校验并整键降级
// （internal/plugins/values.go），编译期模块没人校验，所以这一条就是它唯一的兜底：
// 清单说 process 是 list 而值里只有 text，前端会画出一张空列表卡，
// 看上去像"这个模块今天没有数据"，而真实情况是有人改了处理器忘了改清单。
func TestEveryDeclaredWidgetHasMatchingShape(t *testing.T) {
	mod, ok := findModule("runtimestatus")
	if !ok {
		t.Fatal("runtimestatus 不在注册表里")
	}
	_, payload := callRuntime(t)
	for _, w := range mod.Widgets {
		v, ok := payload.Values[w.Key]
		if !ok {
			t.Errorf("组件 %q 在清单里，端口却没回这一键：界面只会显示\"无数据\"", w.Key)
			continue
		}
		switch w.Type {
		case modules.WidgetStat, modules.WidgetNote:
			if strings.TrimSpace(v.Text) == "" {
				t.Errorf("%q（%s）的 text 为空", w.Key, w.Type)
			}
			if len(v.Items) > 0 || len(v.Columns) > 0 || len(v.Rows) > 0 {
				t.Errorf("%q（%s）里混进了列表/表格字段", w.Key, w.Type)
			}
		case modules.WidgetList:
			if v.Text != "" || len(v.Columns) > 0 || len(v.Rows) > 0 {
				t.Errorf("%q（list）里混进了 text/columns/rows：%+v", w.Key, v)
			}
			for i, it := range v.Items {
				if strings.TrimSpace(it.Label) == "" {
					t.Errorf("%q 第 %d 行没有标题：界面会多出一条没人知道说的是什么行", w.Key, i+1)
				}
			}
		case modules.WidgetTable:
			if v.Text != "" || len(v.Items) > 0 {
				t.Errorf("%q（table）里混进了 text/items：%+v", w.Key, v)
			}
			if len(v.Columns) == 0 {
				t.Fatalf("%q 没有表头", w.Key)
			}
			for i, r := range v.Rows {
				if len(r) != len(v.Columns) {
					t.Errorf("%q 第 %d 行有 %d 格，表头 %d 列：前端会渲染出一张错位的表", w.Key, i+1, len(r), len(v.Columns))
				}
			}
		default:
			t.Errorf("组件 %q 类型 %q 不在本模块的预期之内（加了类型也要加这条检查）", w.Key, w.Type)
		}
	}
}

// 鉴权引擎没就绪时，策略条数是"读不到"而不是 0 —— 这是本项目一贯的口径。
func TestRuntimeSaysReadFailedInsteadOfZero(t *testing.T) {
	prev := middleware.Enforcer
	middleware.Enforcer = nil
	t.Cleanup(func() { middleware.Enforcer = prev })

	code, payload := callRuntime(t)
	if code != http.StatusOK {
		t.Fatalf("应 200（自诊接口本身不该因为依赖缺失而报错），实际 %d", code)
	}
	v := payload.Values["registry"]
	if v.Level != "warn" {
		t.Errorf("引擎缺失时 registry 应标 warn，实际 %q", v.Level)
	}
	if !strings.Contains(v.Hint, "读不到") {
		t.Errorf("hint 应明写读不到，而不是显示\"共 0 条\"，实际 %q", v.Hint)
	}
}

func TestHumanDurationAndUtcOffsetText(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "0 分"},
		{9 * time.Minute, "9 分"},
		{2*time.Hour + 13*time.Minute, "2 小时 13 分"},
		{50 * time.Hour, "2 天 2 小时"},
		{-3 * time.Hour, "0 分"}, // 计时起点在将来也不该报负数
	}
	for _, c := range cases {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}

	offsets := []struct {
		seconds int
		want    string
	}{
		{8 * 3600, "+8:00"},
		{0, "+0:00"},
		{-5 * 3600, "-5:00"},
		{5*3600 + 30*60, "+5:30"},
	}
	for _, c := range offsets {
		if got := utcOffsetText(c.seconds); got != c.want {
			t.Errorf("utcOffsetText(%d) = %q，期望 %q", c.seconds, got, c.want)
		}
	}

	if statLevelWarnIf(false) != "ok" || statLevelWarnIf(true) != "warn" {
		t.Error("statLevelWarnIf 的映射被改动了")
	}
}

// humanBytes 的档位错位在这一条上最容易溜过去：单位数组从 KB 起、循环次数从 0 起，
// 差一格就把 4 MB 报成 4 GB —— 数字本身看着还"挺正常"，没人会去质疑一个 4.0。
func TestHumanBytesUnits(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1023, "1023 B"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{4212936, "4.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{40 * 1024 * 1024, "40.0 MB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func findModule(id string) (*modules.Module, bool) {
	mods := modules.Modules()
	for i := range mods {
		if mods[i].ID == id {
			return &mods[i], true
		}
	}
	return nil, false
}
