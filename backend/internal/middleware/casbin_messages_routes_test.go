package middleware

import "testing"

// 站内信的策略最容易写错的地方是"集合路径"：keyMatch 与 keyMatch2 的 /* 都匹配不到
// 无斜杠的 /api/v1/messages 本身，少一条就是所有非技术角色点收件箱 403，
// 而这在前端只有登录后点进去才看得见。这里逐条点名。
func TestCasbinMessageRoutes(t *testing.T) {
	if err := newCasbinEnforcer(t); err != nil {
		t.Fatalf("初始化 Casbin 引擎失败: %v", err)
	}
	if Enforcer == nil {
		t.Fatal("Enforcer 未就绪")
	}

	sendRoles := []string{"dorm_manager", "member", "minister", "tech_admin"}
	allRoles := append([]string{"viewer_export"}, sendRoles...)

	// 人人可读：收件箱/发件箱（集合路径）、未读数、选人列表
	readables := []struct{ path, act string }{
		{"/api/v1/messages", "GET"},
		{"/api/v1/messages/unread-count", "GET"},
		{"/api/v1/messages/contacts", "GET"},
		{"/api/v1/messages/7/read", "PUT"},
	}
	for _, role := range allRoles {
		for _, rt := range readables {
			ok, err := Enforcer.Enforce("role:"+role, rt.path, rt.act)
			if err != nil {
				t.Fatalf("策略求值出错 (%s): %v", rt.path, err)
			}
			if !ok {
				t.Errorf("角色 %s 应可 %s %s —— 漏策略的表现是登录后点收件箱 403", role, rt.act, rt.path)
			}
		}
	}

	// 只有需要发信的角色才有集合路径上的 POST
	for _, role := range sendRoles {
		if ok, _ := Enforcer.Enforce("role:"+role, "/api/v1/messages", "POST"); !ok {
			t.Errorf("角色 %s 应可发站内信（POST /api/v1/messages）", role)
		}
	}

	// 信息查看下载岗只收不发
	if ok, _ := Enforcer.Enforce("role:viewer_export", "/api/v1/messages", "POST"); ok {
		t.Error("查看下载岗不应获得发信权限（口径：只收不发）")
	}

	// 反向兜底：段内没给过的动作一律不放，别把 /* 当成万能钥匙。
	// 发送只走集合路径；子路径上的 POST/DELETE 当前一条策略都没有。
	for _, role := range []string{"dorm_manager", "member", "minister"} {
		if ok, _ := Enforcer.Enforce("role:"+role, "/api/v1/messages/unread-count", "POST"); ok {
			t.Errorf("角色 %s 不应在子路径上获得 POST", role)
		}
		if ok, _ := Enforcer.Enforce("role:"+role, "/api/v1/messages/7", "DELETE"); ok {
			t.Errorf("角色 %s 不应能删站内信（尚未设计删除口径，宁可 403）", role)
		}
	}
	if ok, _ := Enforcer.Enforce("role:stranger", "/api/v1/messages", "GET"); ok {
		t.Error("未知角色不应被放行")
	}
}
