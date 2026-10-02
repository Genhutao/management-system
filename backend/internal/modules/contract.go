package modules

// 这一对类型是**清单组件的数据契约**：清单只说"有哪些组件、数据从哪个端口取"，
// 真正的数值由模块自己的数据端口按下面的形状返回。
//
// 刻意把契约放在 modules 包而不是各模块包里：前端渲染器只认这一个形状，
// 模块各写各的响应结构就等于把契约拆散到 N 个地方，日后谁都对不上谁。

// StatValue 一个 stat 组件的取值。
// Text 必填；Hint 是一句话说明（口径、来源、为什么是这个数）；
// Level 只认 ok / warn，界面据此配色——warn 不是错误，而是"这个数值得看一眼"。
type StatValue struct {
	Text  string `json:"text"`
	Hint  string `json:"hint,omitempty"`
	Level string `json:"level,omitempty"`
}

// DataPayload 数据端口的响应体。Values 按组件 key 索引，
// 一个端口可以同时供多个组件（本模块的四个 stat 就共用一个端口）。
//
// 取不到的 key 应该整个不放进 Values，由界面显示"无数据"；
// 不要填 0 或空串——那会被读成"真的是 0"。
type DataPayload struct {
	Values map[string]StatValue `json:"values"`
}
