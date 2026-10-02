package middleware

import "testing"

// 插件地基的两段路径：/api/v1/ext/* 是清单本身（人人可读，它只说"有哪些组件"），
// /api/v1/mod/* 是模块自带的数据端口（当前只有 runtimestatus，运行状态自诊）。
//
// 这里最要紧的是**反向**那一半：mod 段只给技术维护组，其余四个角色必须一条都没有。
// 清单可见性正是拿这些策略推出来的，多给一条就等于给那个角色长出一个本不该有的入口；
// 少给一条（ext 段）则是所有人的扩展模块一起消失。两种都只会在界面上显形，所以钉在策略层。
func TestCasbinExtAndModuleRoutes(t *testing.T) {
	if err := newCasbinEnforcer(t); err != nil {
		t.Fatalf("初始化 Casbin 引擎失败: %v", err)
	}
	if Enforcer == nil {
		t.Fatal("Enforcer 未就绪")
	}

	allRoles := []string{"dorm_manager", "member", "minister", "viewer_export", "tech_admin"}

	// 清单读取：五角色都要能读到。它本身不带业务数据，读不到清单才是问题。
	for _, role := range allRoles {
		ok, err := Enforcer.Enforce("role:"+role, "/api/v1/ext/modules", "GET")
		if err != nil {
			t.Fatalf("策略求值出错 (%s): %v", role, err)
		}
		if !ok {
			t.Errorf("角色 %s 应能读取扩展清单 GET /api/v1/ext/modules（漏这条 = 该角色的扩展入口整体消失）", role)
		}
	}

	// 模块数据段：只给技术维护组，写显式一条而不是靠 /api/v1/* 兜底，
	// 这样"这个入口是谁放开的"能在 casbin_rule 里查到。
	for _, role := range allRoles {
		ok, err := Enforcer.Enforce("role:"+role, "/api/v1/mod/runtimestatus", "GET")
		if err != nil {
			t.Fatalf("策略求值出错 (%s): %v", role, err)
		}
		shouldRead := role == "tech_admin"
		if ok != shouldRead {
			t.Errorf("角色 %s 对 /api/v1/mod/runtimestatus 的可读性应为 %v，实际 %v", role, shouldRead, ok)
		}
	}

	// 段内没给过的动作一律不放。这里刻意不含技术维护组：它按角色设计持有 /api/v1/* 全方法，
	// 断言它"写不了 mod 段"必然是假的，也毫无意义 —— 要防的是另外四个角色顺手拿到写权限。
	for _, role := range []string{"dorm_manager", "member", "minister", "viewer_export"} {
		for _, act := range []string{"POST", "PUT", "DELETE"} {
			ok, err := Enforcer.Enforce("role:"+role, "/api/v1/mod/runtimestatus", act)
			if err != nil {
				t.Fatalf("策略求值出错 (%s/%s): %v", role, act, err)
			}
			if ok {
				t.Errorf("角色 %s 不应拿到 %s /api/v1/mod/runtimestatus —— 扩展模块目前全部只读，写权限要显式加策略", role, act)
			}
		}
	}
}
