package model

import "testing"

// kind 是收口枚举：取值合法 != 有路径能产出它。
// system_cc 目前是"字段就位、写入路径为空"的预留位，等班主任角色与
// 班级→班主任映射真存在了才允许有产出它的代码。
func TestNormalizeMessageKind(t *testing.T) {
	if v, ok := NormalizeMessageKind(""); !ok || v != MessageKindHuman {
		t.Errorf("空值应归一为 human（与 GORM default:'human' 一致），实际 %q ok=%v", v, ok)
	}
	if v, ok := NormalizeMessageKind("  human  "); !ok || v != MessageKindHuman {
		t.Errorf("human 带空格应归一成功，实际 %q ok=%v", v, ok)
	}
	if v, ok := NormalizeMessageKind(MessageKindSystemCC); !ok || v != MessageKindSystemCC {
		t.Errorf("system_cc 应被接受为合法取值，实际 %q ok=%v", v, ok)
	}

	for _, bad := range []string{"cc", "系统", "notice", "systemcc", "HUMAN"} {
		if v, ok := NormalizeMessageKind(bad); ok {
			t.Errorf("非法取值 %q 竟被接受（归一为 %q），脏数据会绕过判定", bad, v)
		}
	}
}
