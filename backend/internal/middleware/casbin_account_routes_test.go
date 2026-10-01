package middleware

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// 自助安全中心是"人人可用"的段落：漏一条策略的表现是除技术维护组外全部 403，
// 而前端只在点按钮时才看得见。这里把四角色 × 全部自助动作一次点齐。

func locateModelConf(t *testing.T) string {
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
	t.Skip("未找到 rbac_model.conf，跳过策略断言")
	return ""
}

func newCasbinEnforcer(t *testing.T) error {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:casbin_account_routes?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if _, err := InitCasbin(db, locateModelConf(t)); err != nil {
		return err
	}
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return nil
}

func TestCasbinAccountSelfServiceRoutes(t *testing.T) {
	if err := newCasbinEnforcer(t); err != nil {
		t.Fatalf("初始化 Casbin 引擎失败: %v", err)
	}
	if Enforcer == nil {
		t.Fatal("Enforcer 未就绪")
	}

	roles := []string{"dorm_manager", "member", "minister", "viewer_export", "tech_admin"}
	// 路径与 main.go 里 /api/v1/account 路由组一一对应
	routes := []struct{ path, act string }{
		{"/api/v1/account/security", "GET"},
		{"/api/v1/account/sessions/revoke", "POST"},
		{"/api/v1/account/sessions/12", "DELETE"},
		{"/api/v1/account/environment/confirm", "POST"},
		{"/api/v1/account/totp/setup", "POST"},
		{"/api/v1/account/totp/enable", "POST"},
		{"/api/v1/account/totp/disable", "POST"},
	}

	for _, role := range roles {
		for _, rt := range routes {
			ok, err := Enforcer.Enforce("role:"+role, rt.path, rt.act)
			if err != nil {
				t.Fatalf("策略求值出错 (%s): %v", rt.path, err)
			}
			if !ok {
				t.Errorf("角色 %s 应可自助访问 %s %s —— 漏了策略就会 403", role, rt.act, rt.path)
			}
		}
	}

	// 反向兜底：不在枚举里的身份不该被通配符放进来
	for _, rt := range routes {
		ok, _ := Enforcer.Enforce("role:stranger", rt.path, rt.act)
		if ok {
			t.Errorf("未知角色 %s %s 不应被放行", rt.act, rt.path)
		}
	}

	// 自助段之外的高权入口仍未被放宽
	if ok, _ := Enforcer.Enforce("role:member", "/api/v1/tech/db/tables", "GET"); ok {
		t.Error("部员不应能读技术组数据库管理器")
	}
	if ok, _ := Enforcer.Enforce("role:dorm_manager", "/api/v1/deductions", "POST"); ok {
		t.Error("宿管不应获得打表权限")
	}
}
