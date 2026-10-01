package main

import "testing"

// TRUSTED_PROXIES 的解析口径：留空 = 不信任任何代理（返回 nil，Gin 只采信直连地址）；
// 空白项与多余空格一律丢弃，避免把 "" 交给 Gin 去做 CIDR 解析而整条启动失败。

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"未配置", "", nil},
		{"只有空格与逗号", " , , ", nil},
		{"单个地址", "10.0.0.1", []string{"10.0.0.1"}},
		{"网段与地址混排带空格", " 10.0.0.0/8, 172.16.0.1 ", []string{"10.0.0.0/8", "172.16.0.1"}},
		{"尾随逗号", "192.168.1.1,", []string{"192.168.1.1"}},
	}
	for _, c := range cases {
		got := parseTrustedProxies(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("%s: 期望 %d 项，实际 %d 项 %q", c.name, len(c.want), len(got), got)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: 第 %d 项期望 %q，实际 %q", c.name, i, c.want[i], got[i])
			}
		}
	}
}
