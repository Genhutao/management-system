package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/middleware"
	"xgh-system/internal/modules"

	// 真模块（插件 M1）：只要 import 进来，它的 init() 就把描述符登记进全局注册表。
	// 下面这条用例验的正是"真模块 + 真 Casbin 策略"推导出来的清单，而不是测试自己造的假描述符。
	_ "xgh-system/internal/modules/runtimestatus"
)

// 这套测试用的不是假判定，而是**真的 Casbin 引擎 + 仓库里那份 rbac_model.conf**。
// 清单可见性既然是从 Casbin 推出来的，就必须拿 Casbin 来验，否则测的是我自己写的桩。

func locateExtModelConf(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"../../rbac_model.conf", "../rbac_model.conf", "rbac_model.conf"} {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}
	t.Skip("未找到 rbac_model.conf，跳过清单策略断言")
	return ""
}

func setupExtEnforcer(t *testing.T) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "_casbin"
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if _, err := middleware.InitCasbin(db, locateExtModelConf(t)); err != nil {
		t.Fatalf("初始化 Casbin 引擎失败: %v", err)
	}
	prev := middleware.Enforcer
	t.Cleanup(func() {
		middleware.Enforcer = prev
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
}

func extManifest(t *testing.T, role string) (int, map[string]interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/ext/modules", nil)
	if role != "" {
		c.Set("role", role)
	}
	(&ExtController{}).Manifest(c)

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("清单响应不是合法 JSON: %v / %s", err, w.Body.String())
	}
	return w.Code, body
}

func manifestIDs(body map[string]interface{}) map[string]bool {
	out := map[string]bool{}
	raw, _ := json.Marshal(body["modules"])
	var list []modules.Module
	if err := json.Unmarshal(raw, &list); err != nil {
		return out
	}
	for _, m := range list {
		out[m.ID] = true
	}
	return out
}

// 清单里出现的每个组件，其数据端口都必须对该角色真的可过 Casbin。
// 这条是通用防漂移断言：以后真模块进来，它会自动替我们守住"入口出来了、点进去 403"。
func assertManifestMatchesCasbin(t *testing.T, role string, body map[string]interface{}) {
	t.Helper()
	if middleware.Enforcer == nil {
		t.Fatal("Enforcer 未就绪")
	}
	raw, _ := json.Marshal(body["modules"])
	var list []modules.Module
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("modules 解析失败: %v", err)
	}
	for _, m := range list {
		for _, w := range m.Widgets {
			ok, err := middleware.Enforcer.Enforce("role:"+role, w.DataEndpoint, "GET")
			if err != nil {
				t.Fatalf("策略求值出错 %s: %v", w.DataEndpoint, err)
			}
			if !ok {
				t.Errorf("清单给了 %s 一个读不到的入口：模块 %s 的组件 %s -> %s", role, m.ID, w.Key, w.DataEndpoint)
			}
		}
	}
}

// 模块对某角色是否可见，取决于它对这些已有接口的策略：
// 挂在人人可读的 summary 上的模块五角色都看得见；挂在技术组专有接口上的只有技术组看得见。
func TestExtManifestDerivesVisibilityFromCasbin(t *testing.T) {
	setupExtEnforcer(t)

	modules.Register(modules.Module{
		ID: "readable-by-all", Title: "人人可读", Tab: "readable-by-all",
		MinManifestVersion: modules.ManifestVersion,
		Widgets:            []modules.Widget{{Key: "w", Type: modules.WidgetStat, Label: "计数", DataEndpoint: "/api/v1/dashboard/summary"}},
	})
	modules.Register(modules.Module{
		ID: "tech-only", Title: "技术组专属", Tab: "tech-only",
		MinManifestVersion: modules.ManifestVersion,
		Widgets:            []modules.Widget{{Key: "w", Type: modules.WidgetStat, Label: "表数", DataEndpoint: "/api/v1/tech/db/tables"}},
	})

	roles := []string{"dorm_manager", "member", "minister", "viewer_export", "tech_admin"}
	for _, role := range roles {
		code, body := extManifest(t, role)
		if code != http.StatusOK {
			t.Fatalf("%s 取清单应 200，实际 %d: %v", role, code, body)
		}
		if got := body["manifest_version"]; got != float64(modules.ManifestVersion) {
			t.Errorf("%s 的 manifest_version 应为 %d，实际 %v", role, modules.ManifestVersion, got)
		}
		ids := manifestIDs(body)
		if !ids["readable-by-all"] {
			t.Errorf("%s 应看见挂在 summary 上的模块，实际清单 %v", role, ids)
		}
		if role == "tech_admin" && !ids["tech-only"] {
			t.Errorf("技术维护组应看见挂在 tech 段上的模块（它有 /api/v1/* 通配）")
		}
		if role != "tech_admin" && ids["tech-only"] {
			t.Errorf("%s 不该看见只有技术组读得到的模块，实际清单 %v", role, ids)
		}
		assertManifestMatchesCasbin(t, role, body)
	}
}

// 清单本身必须是数组而不是 null，前端遍历才不会当场崩。
func TestExtManifestSerializesEmptyArrayNotNull(t *testing.T) {
	setupExtEnforcer(t)
	middleware.Enforcer = nil
	t.Cleanup(func() { middleware.Enforcer = nil })

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/ext/modules", nil)
	c.Set("role", "member")
	(&ExtController{}).Manifest(c)

	// 引擎未就绪：接口仍可 200，但清单必须空 —— 失败关闭，不能因为拿不到判定就全量放行
	if w.Code != http.StatusOK {
		t.Fatalf("引擎缺失时应返回空清单而非 500，实际 %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got == "" {
		t.Fatal("响应不应为空体")
	} else if !json.Valid([]byte(got)) {
		t.Fatalf("响应不是合法 JSON: %s", got)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	list, ok := body["modules"].([]interface{})
	if !ok {
		t.Fatalf("modules 字段应为数组，实际类型 %T: %s", body["modules"], w.Body.String())
	}
	if len(list) != 0 {
		t.Errorf("引擎未就绪时不应放出任何模块，实际 %d 项", len(list))
	}
}

// 没拿到角色就拒绝，别用空角色去问 Casbin。
func TestExtManifestRequiresRole(t *testing.T) {
	setupExtEnforcer(t)
	code, body := extManifest(t, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("缺角色应 401，实际 %d: %v", code, body)
	}
}

// 第一个真模块（runtimestatus）的可见性：它的数据端口在 mod 段只显式给了技术维护组，
// 所以其余四个角色的清单里必须**根本没有**这个模块，而不是"列出来了、点进去 403"。
// 这条是插件架构的核心承诺，也是前端"零改动"能成立的前提——前端不看角色，只看清单。
func TestExtManifestHidesRealModuleFromNonTechRoles(t *testing.T) {
	setupExtEnforcer(t)

	for _, role := range []string{"dorm_manager", "member", "minister", "viewer_export"} {
		code, body := extManifest(t, role)
		if code != http.StatusOK {
			t.Fatalf("%s 取清单应 200，实际 %d: %v", role, code, body)
		}
		if ids := manifestIDs(body); ids["runtimestatus"] {
			t.Errorf("%s 的清单里出现了 runtimestatus：mod 段没给它任何策略，列出来就等于前端多一个 403 入口", role)
		}
		assertManifestMatchesCasbin(t, role, body)
	}

	code, body := extManifest(t, "tech_admin")
	if code != http.StatusOK {
		t.Fatalf("技术维护组取清单应 200，实际 %d: %v", code, body)
	}
	raw, _ := json.Marshal(body["modules"])
	var list []modules.Module
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("modules 解析失败: %v", err)
	}
	var found *modules.Module
	for i := range list {
		if list[i].ID == "runtimestatus" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatal("技术维护组的清单里应有 runtimestatus")
	}
	// 前端只要拿到这份清单就能长出一整页，不需要再读别的：标题、图标、分组、tab、四个组件都要齐
	if found.Title == "" || found.Tab == "" || found.Group == "" {
		t.Errorf("描述符字段不全: %+v", found)
	}
	if len(found.Widgets) != 4 {
		t.Fatalf("应有 4 个组件，实际 %d", len(found.Widgets))
	}
	for _, w := range found.Widgets {
		if w.Key == "" || w.Label == "" || w.Type != modules.WidgetStat {
			t.Errorf("组件字段不全: %+v", w)
		}
		if w.DataEndpoint != "/api/v1/mod/runtimestatus" {
			t.Errorf("组件 %q 的端口应为 /api/v1/mod/runtimestatus，实际 %q", w.Key, w.DataEndpoint)
		}
		// 前端拿到完整路径后要剥掉 API_BASE 再请求，端口带前缀这条约定不能改
		if !strings.HasPrefix(w.DataEndpoint, "/api/v1/") {
			t.Errorf("组件 %q 的端口缺少 /api/v1/ 前缀: %q", w.Key, w.DataEndpoint)
		}
	}
	assertManifestMatchesCasbin(t, "tech_admin", body)
}
