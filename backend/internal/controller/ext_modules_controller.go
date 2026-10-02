package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/middleware"
	"xgh-system/internal/modules"
)

// ExtController 扩展模块清单（插件地基）。
type ExtController struct{}

// Manifest 返回当前身份能看见的模块清单：GET /api/v1/ext/modules
//
// 这个接口本身不扩任何数据权限——它只描述"有哪些组件、数据在哪个已有接口上"。
// 某角色能不能看见某个模块，是拿它对这些已有接口的 Casbin 策略推出来的，
// 所以"清单里给了入口、点进去 403"在结构上不会发生（有反向断言的测试守着）。
//
// 鉴权引擎未就绪时返回空清单而不是全量：与 CasbinRBACMiddleware 同一个失败关闭口径。
func (e *ExtController) Manifest(c *gin.Context) {
	roleVal, exists := c.Get("role")
	roleStr, _ := roleVal.(string)
	if !exists || roleStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "未获取到用户角色信息"})
		return
	}

	allowed := func(role, method, path string) bool {
		if middleware.Enforcer == nil {
			return false
		}
		ok, err := middleware.Enforcer.Enforce("role:"+role, path, method)
		if err != nil {
			// 判定出错就当作读不到：清单宁缺勿滥，也不能把没授权的入口放出去
			return false
		}
		return ok
	}

	c.JSON(http.StatusOK, gin.H{
		"manifest_version": modules.ManifestVersion,
		"role":             roleStr,
		"modules":          modules.ManifestForRole(roleStr, allowed),
	})
}
