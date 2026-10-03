package middleware

import (
	"log"
	"net/http"

	"github.com/casbin/casbin/v3"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Enforcer 用 SyncedEnforcer 而不是普通 Enforcer：插件授权/撤销要在运行期增删策略
// （internal/plugins/policy.go），而请求线程每来一个都在读它做判定。普通 Enforcer
// 内部没有读写锁，并发下是数据竞争；SyncedEnforcer 的 Enforce/AddPolicy/RemovePolicy
// 都带锁，方法与调用点完全兼容（runtimestatus 读的 GetPolicy 也在）。
var Enforcer *casbin.SyncedEnforcer

// InitCasbin 使用 GORM 适配器初始化 Casbin RBAC 权限隔离引擎
func InitCasbin(db *gorm.DB, modelPath string) (*casbin.SyncedEnforcer, error) {
	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return nil, err
	}

	e, err := casbin.NewSyncedEnforcer(modelPath, adapter)
	if err != nil {
		return nil, err
	}

	if err := e.LoadPolicy(); err != nil {
		return nil, err
	}

	Enforcer = e
	seedCasbinRules(e)

	// 播种写完就把自动持久化关掉，之后不再打开：casbin 在 autoSave 开启时，
	// AddPolicy 会顺手写 casbin_rule 表，而插件策略是**只该活在内存里**的
	// （插件下线要删得干净，表里不能留残；表仍只归上面的 seed 管）。
	// 关掉以后，运行期任何一次策略增删都碰不到库；要落库只能显式 SavePolicy()，
	// 而现在代码里显式调它的只有 seedCasbinRules 这一处。
	e.EnableAutoSave(false)
	return e, nil
}

// seedCasbinRules 注入符合 GitHub 开源规范的五大身份细粒度权限隔离规则
func seedCasbinRules(e *casbin.SyncedEnforcer) {
	// 规则格式: sub (角色), obj (API 路径), act (HTTP 方法)
	policies := [][]string{
		// 1. 宿管 (dorm_manager): 极简工作台、拍照上传、历史违规照片查看、今日工作待办、查寝联动查学生
		{"role:dorm_manager", "/api/v1/dorm/*", "(GET)|(POST)"},
		{"role:dorm_manager", "/api/v1/schedules/today", "GET"},
		{"role:dorm_manager", "/api/v1/auth/profile", "GET"},
		{"role:dorm_manager", "/api/v1/auth/security-settings", "PUT"},
		{"role:dorm_manager", "/api/v1/students/room-members", "GET"},

		// 2. 学管会部员 (member): 上工日历、个人积分流水、请假快速申报、在线答题自测、查寝联动查学生、组织部打表扣分、播音/宣传、AI福利
		{"role:member", "/api/v1/member/*", "(GET)|(POST)"},
		{"role:member", "/api/v1/exam/*", "(GET)|(POST)"},
		{"role:member", "/api/v1/schedules/*", "GET"},
		{"role:member", "/api/v1/auth/profile", "GET"},
		{"role:member", "/api/v1/auth/security-settings", "PUT"},
		{"role:member", "/api/v1/students/room-members", "GET"},
		// 打表权限的唯一判定在 controller 层：Casbin 主体只有 role，表达不了"技术部 + 副部长"这类部门与职务属性
		{"role:member", "/api/v1/deductions", "(GET)|(POST)"},
		{"role:member", "/api/v1/deductions/*", "(GET)|(POST)"},
		{"role:member", "/api/v1/publicity/*", "(GET)|(POST)|(PUT)"},
		{"role:member", "/api/v1/welfare/*", "(GET)|(POST)"},

		// 3. 学管会部长 (minister): 审批请假、排班轮换生成调度、AI对话排表、答题试卷、学生名册、打表查阅导出、宣传、福利
		{"role:minister", "/api/v1/member/*", "(GET)|(POST)"},
		{"role:minister", "/api/v1/minister/*", "(GET)|(POST)|(PUT)|(DELETE)"},
		{"role:minister", "/api/v1/schedules/*", "(GET)|(POST)|(PUT)|(DELETE)"},
		{"role:minister", "/api/v1/exam/*", "(GET)|(POST)|(PUT)|(DELETE)"},
		{"role:minister", "/api/v1/dorm/inspections", "GET"},
		{"role:minister", "/api/v1/auth/profile", "GET"},
		{"role:minister", "/api/v1/auth/security-settings", "PUT"},
		{"role:minister", "/api/v1/students/*", "(GET)|(POST)|(DELETE)"},
		{"role:minister", "/api/v1/deductions", "(GET)|(POST)"},
		{"role:minister", "/api/v1/deductions/*", "(GET)|(POST)"},
		{"role:minister", "/api/v1/publicity/*", "(GET)|(POST)|(PUT)"},
		{"role:minister", "/api/v1/welfare/*", "(GET)|(POST)|(PUT)|(DELETE)"},

		// 4. 技术维护组 (tech_admin): AI 中枢、宿管花名册预置、系统管理、学生名册特征识别导入
		{"role:tech_admin", "/api/v1/tech/*", "(GET)|(POST)|(PUT)|(DELETE)"},
		{"role:tech_admin", "/api/v1/*", "(GET)|(POST)|(PUT)|(DELETE)"},

		// 5. 信息查看下载管理 (viewer_export): 只读与全量数据多维筛选、报表导出、学生名册查阅
		{"role:viewer_export", "/api/v1/export/*", "(GET)|(POST)"},
		{"role:viewer_export", "/api/v1/dorm/inspections", "GET"},
		{"role:viewer_export", "/api/v1/schedules/*", "GET"},
		{"role:viewer_export", "/api/v1/auth/profile", "GET"},
		{"role:viewer_export", "/api/v1/auth/security-settings", "PUT"},
		{"role:viewer_export", "/api/v1/students/*", "GET"},
		{"role:viewer_export", "/api/v1/deductions", "GET"},
		{"role:viewer_export", "/api/v1/deductions/*", "GET"},

		// 6. 服务端登出：漏了这条的话除技术维护组外点「退出登录」会被 403 拦掉，
		// UI 回到壁纸页但会话 Cookie 仍然有效，公网机车上等于没退出。
		{"role:dorm_manager", "/api/v1/auth/logout", "POST"},
		{"role:member", "/api/v1/auth/logout", "POST"},
		{"role:minister", "/api/v1/auth/logout", "POST"},
		{"role:viewer_export", "/api/v1/auth/logout", "POST"},

		// 7. 登录后总览。放行四个非技术角色（tech_admin 已被 /api/v1/* 覆盖）。
		// 这条不是新的授权通道：handler 按角色分支，只返回该角色本来就有权限读到的数，
		// 例如宿管拿不到违纪统计（其策略里没有 /deductions），查看岗拿不到部员积分。
		{"role:dorm_manager", "/api/v1/dashboard/summary", "GET"},
		{"role:member", "/api/v1/dashboard/summary", "GET"},
		{"role:minister", "/api/v1/dashboard/summary", "GET"},
		{"role:viewer_export", "/api/v1/dashboard/summary", "GET"},

		// 8. 账户安全中心（自助段）。放行四个非技术角色（tech_admin 已被 /api/v1/* 覆盖）。
		// 整段通配不会造成横向越权：段内每个处理器都只以当前登录账号本人的 user_id
		// 取数与写入，会话吊销还额外按 user_id 收窄了查询条件。
		{"role:dorm_manager", "/api/v1/account/*", "(GET)|(POST)|(DELETE)"},
		{"role:member", "/api/v1/account/*", "(GET)|(POST)|(DELETE)"},
		{"role:minister", "/api/v1/account/*", "(GET)|(POST)|(DELETE)"},
		{"role:viewer_export", "/api/v1/account/*", "(GET)|(POST)|(DELETE)"},

		// 9. 站内信。整段通配不会造成横向越权：收件箱、发件箱、未读数和"清未读"
		// 一律以当前登录账号的 user_id 收窄查询，发送还要过 controller 层的收发矩阵。
		// 集合路径 /api/v1/messages 必须单列一条：keyMatch 与 keyMatch2 的 /* 都匹配不到
		// 无斜杠的集合路径（同 /deductions 那两条的写法）。
		// 技术维护组已被 /api/v1/* 覆盖，不再单列。
		{"role:dorm_manager", "/api/v1/messages", "(GET)|(POST)"},
		{"role:dorm_manager", "/api/v1/messages/*", "(GET)|(PUT)"},
		{"role:member", "/api/v1/messages", "(GET)|(POST)"},
		{"role:member", "/api/v1/messages/*", "(GET)|(PUT)"},
		{"role:minister", "/api/v1/messages", "(GET)|(POST)"},
		{"role:minister", "/api/v1/messages/*", "(GET)|(PUT)"},
		// 信息查看下载岗：只收不发。有收件箱和清未读，没有发送权限，
		// 集合路径上也不给 POST —— 部员发给它都不行，它更发不出去。
		{"role:viewer_export", "/api/v1/messages", "GET"},
		{"role:viewer_export", "/api/v1/messages/*", "(GET)|(PUT)"},

		// 10. 扩展模块清单（插件地基）。读的是"有哪些组件、各自的数据在哪个已有接口上"，
		// 本身不放行任何业务数据：某角色能看见哪些模块，是拿它对这些已有接口的策略推出来的。
		// 段内目前没有别的端点，所以放行 GET 只等于放开清单读取。技术维护组已被 /api/v1/* 覆盖。
		{"role:dorm_manager", "/api/v1/ext/*", "GET"},
		{"role:member", "/api/v1/ext/*", "GET"},
		{"role:minister", "/api/v1/ext/*", "GET"},
		{"role:viewer_export", "/api/v1/ext/*", "GET"},

		// 11. 插件模块自带的数据段 /api/v1/mod/*。
		// 这里单独列一条而不是躺在 tech_admin 的 /api/v1/* 里兜底：模块清单的可见性是
		// 拿"这个角色能不能读它的数据端点"推出来的，显式一条才能回答"这个入口是谁放开的"。
		// 其余四个角色刻意不写任何一条 —— 少一条策略就等于前端少一个模块，
		// 这正是"不给权限就不显示"的机制本身，不是漏写。
		// 段内目前只有 runtimestatus（运行状态自诊），它对部员/部长/宿管/查看岗没有业务意义。
		{"role:tech_admin", "/api/v1/mod/*", "GET"},
	}

	for _, p := range policies {
		has, _ := e.HasPolicy(p)
		if !has {
			_, _ = e.AddPolicy(p)
		}
	}

	// 策略持久化在 casbin_rule 表中且只做增量补齐，因此必须显式撤销历史写入的过宽记录，
	// 否则对已上线数据库收紧策略不会生效。
	stalePolicies := [][]string{
		{"role:member", "/api/v1/deductions/*", "(GET)|(POST)|(DELETE)"},
		{"role:minister", "/api/v1/deductions/*", "(GET)|(POST)|(DELETE)"},
		{"role:member", "/api/v1/deductions/*", "(GET)|(DELETE)"},
		{"role:minister", "/api/v1/deductions/*", "(GET)|(DELETE)"},
	}
	for _, p := range stalePolicies {
		if has, _ := e.HasPolicy(p); has {
			_, _ = e.RemovePolicy(p)
		}
	}

	_ = e.SavePolicy()
	log.Println("[Casbin] RBAC authorization rules successfully synchronized from template!")
}

// CasbinRBACMiddleware Casbin 权限鉴权拦截中间件
func CasbinRBACMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		roleVal, exists := c.Get("role")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "未获取到用户角色信息"})
			c.Abort()
			return
		}

		sub := "role:" + roleVal.(string)
		obj := c.Request.URL.Path
		act := c.Request.Method

		// 鉴权引擎不可用时失败关闭：宁可拒绝请求，也不能让全部角色互通
		if Enforcer == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "权限校验引擎未就绪，请求被拒绝"})
			c.Abort()
			return
		}

		ok, err := Enforcer.Enforce(sub, obj, act)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "权限验证服务异常: " + err.Error()})
			c.Abort()
			return
		}

		if !ok {
			c.JSON(http.StatusForbidden, gin.H{
				"error":    "Casbin 权限拦截：您所在的身份角色无权操作此资源",
				"role":     roleVal.(string),
				"resource": obj,
				"action":   act,
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
