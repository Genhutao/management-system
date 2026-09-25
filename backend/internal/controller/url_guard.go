package controller

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// validatePublicURL 校验上游地址仅允许 http/https 且不指向本机或内网网段。
// 福利网关的 base_url 由用户填写，服务端会带着存储的密钥向它发请求——
// 不做校验就等于把"探测内网"的能力交给了任何登录用户（SSRF）。
func validatePublicURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("地址格式无效：%s", trimmed)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("上游地址仅允许 http/https 协议")
	}

	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return "", fmt.Errorf("无法解析上游主机 %s: %w", u.Hostname(), err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return "", fmt.Errorf("上游地址 %s 指向本机或内网网段，已拒绝", trimmed)
		}
	}
	return trimmed, nil
}
