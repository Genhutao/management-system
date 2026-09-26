package middleware

import (
	"log"
	"net/http"

	"github.com/casbin/casbin/v3"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var Enforcer *casbin.Enforcer

// InitCasbin 使用 GORM 适配器初始化 Casbin RBAC 权限隔离引擎
func InitCasbin(db *gorm.DB, modelPath string) (*casbin.Enforcer, error) {
	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return nil, err
	}

	e, err := casbin.NewEnforcer(modelPath, adapter)
	if err != nil {
		return nil, err
	}

	if err := e.LoadPolicy(); err != nil {
		return nil, err
	}

	Enforcer = e
	seedCasbinRules(e)
	return e, nil
}

// seedCasbinRules 注入符合 GitHub 开源规范的五大身份细粒度权限隔离规则
func seedCasbinRules(e *casbin.Enforcer) {
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
					{"role:minister", "/api/v1/welfare/*", "(GET)|(POST)"},

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
				"error":     "Casbin 权限拦截：您所在的身份角色无权操作此资源",
				"role":      roleVal.(string),
				"resource":  obj,
				"action":    act,
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
