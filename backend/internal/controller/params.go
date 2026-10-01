package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// pathID 把 URL 路径参数强制收敛成正整数主键。
//
// 这不是代码风格问题，而是实打实的注入面：GORM 的 First(&x, v) 只有在 v 能转成数字时
// 才按主键查询，否则会把整个字符串当成原生 SQL 片段拼进 WHERE。于是
// /papers/1+OR+1=1 这类路径可以直接改写查询条件；放在完全公开的路由上，
// 200/404 就是一个布尔预言机，未认证者可以据此盲注逐位拖库。
//
// 所有来自路径的 id 都必须先过这里，再进数据库。
func pathID(c *gin.Context, name string) (uint, bool) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param(name)), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "路径参数 " + name + " 必须是正整数"})
		return 0, false
	}
	return uint(id), true
}
