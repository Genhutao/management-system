package modules

// 这一对类型是**清单组件的数据契约**：清单只说"有哪些组件、数据从哪个端口取"，
// 真正的数值由模块自己的数据端口按下面的形状返回。
//
// 刻意把契约放在 modules 包而不是各模块包里：前端渲染器只认这一个形状，
// 模块各写各的响应结构就等于把契约拆散到 N 个地方，日后谁都对不上谁。

// Value 一个组件的取值。
//
// 它是**按声明类型选用的并集**，不是一份"什么都能塞"的自由 JSON：清单说这个组件是 list，
// 数据端口就只能回 Items；回了 Text 会被服务端在出口处整键降级成一句 warn
// （见 internal/plugins/values.go）。理由和信任门同源——界面按声明长卡片，
// 回的形状跟声明不符时，猜着渲染出来的那张卡是在骗人。
//
//   - Text   ：stat / note 用。stat 是一个数或一句话，note 是一段可以换行的说明文字。
//   - Items  ：list 用。每条一行，Label 是那一行说的事，Value 是它的数（可空）。
//   - Columns / Rows：table 用。Rows 的每一行长度必须等于 Columns 的长度，
//     单元格一律纯文本——回一段 HTML 进去就等于让插件往页面里写脚本。
//   - Hint   ：一句话说明（口径、来源、为什么是这个数）。
//   - Level  ：只认 ok / warn。warn 不是错误，而是"这块值得看一眼"，界面据此配色。
type Value struct {
	Text    string     `json:"text,omitempty"`
	Hint    string     `json:"hint,omitempty"`
	Level   string     `json:"level,omitempty"`
	Items   []Item     `json:"items,omitempty"`
	Columns []Column   `json:"columns,omitempty"`
	Rows    [][]string `json:"rows,omitempty"`
}

// Item list 里的一行。Value 为空是合法的（"这件事今天没有数"），Label 为空不是。
type Item struct {
	Label string `json:"label"`
	Value string `json:"value,omitempty"`
	Hint  string `json:"hint,omitempty"`
	Level string `json:"level,omitempty"`
}

// Column table 的一列表头。
type Column struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// 组件取值的上限。为什么要设：这些字符串是外来进程写的，它会直接进 DOM、进审计、
// 进一次 HTTP 响应体。没有上限，一个插件就能让面板滚一百屏、让一次 GET 回几十 MB。
// 上限的作用是逼"要摆一万行"的东西走分页接口，而不是塞进一张卡片。
const (
	MaxValueText    = 500 // stat / note 的 Text
	MaxValueHint    = 200 // 一句 hint
	MaxListItems    = 50  // list 的行数
	MaxTableRows    = 20  // table 的行数
	MaxTableColumns = 8   // table 的列数
	MaxCellBytes    = 200 // list 的每个字段、table 的每个单元格
	MaxColumnLabel  = 40
)

// DataPayload 数据端口的响应体。Values 按组件 key 索引，
// 一个端口可以同时供多个组件（本模块的四个 stat 就共用一个端口）。
//
// 取不到的 key 应该整个不放进 Values，由界面显示"无数据"；
// 不要填 0 或空串——那会被读成"真的是 0"。
type DataPayload struct {
	Values map[string]Value `json:"values"`
}
