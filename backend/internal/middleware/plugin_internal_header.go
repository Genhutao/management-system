package middleware

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// PluginActedForHeader 与 plugins.PluginActedForHeader 同一个字面量。
// 这里不 import plugins 包：那条依赖会绕成一个环（plugins→controller→middleware），
// 所以常量在这一侧重复一次，并由测试把两处钉在一起 —— 两处拼写不一致的后果是
// "外部请求能自己带一个伪造的内部头进来"，而那正是这个头存在的理由。
const PluginActedForHeader = "X-Plugin-Acted-For"

// StripPluginInternalHeader 把内部标记头从**非回环来源**的请求上剥掉。
//
// 代执行那一跳是宿主自己对本机端口发的，它会带上这个头（值由宿主拼），
// 用来让内层业务处理器与日志能看出"这趟不是人直接打的，是某个插件动作代出来的"。
// 如果不剥，任何一个从公网进来的请求都能自己写一头 `X-Plugin-Acted-For: plugin=x action=y`，
// 于是"这条写请求是插件代出来的"这个事实就变成了调用方可以随便声称的东西——
// 审计与排查会跟着一起被骗。
//
// 判定用的是直连地址而不是 X-Forwarded-For：本项目的可信代理是显式配置的
// （parseTrustedProxies），代理转发的真实客户端 IP 与"这趟请求是不是本机自己发的"
// 是两件事，后者只能看 TCP 直连方。
func StripPluginInternalHeader() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader(PluginActedForHeader) != "" && !isLoopback(c.Request.RemoteAddr) {
			c.Request.Header.Del(PluginActedForHeader)
		}
		c.Next()
	}
}

// isLoopback 直连方是不是本机。
// RemoteAddr 形如 "127.0.0.1:54321" 或 "[::1]:54321"，也可能在没有真实连接时（httptest 之外）
// 是空串——空串按"不是回环"处理，宁可不给内部头。
func isLoopback(remoteAddr string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	} else {
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	// ::1 与 127.0.0.0/8 之外，Windows 上偶尔还会见到 ::ffff:127.0.0.1 这种映射写法。
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
