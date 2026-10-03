package plugins

// values.go —— 插件回的数据形状必须与清单声明的组件类型对得上（M2）。
//
// 为什么在服务端出口处判、不交给前端：清单说这一格是 list，前端就长一张列表卡；
// 插件要是回了 Text，前端只有两种选择——要么空白（看起来像"这个模块坏了"），
// 要么自己猜一个样子渲染（把"声明"和"实际"对不上这件事藏掉了）。两种都是在骗人。
// 整键换成一张 warn 卡、并把"声明是什么、回来的是什么"写在上面，才是能被下一步处理的样子。
//
// 顺带在这里管住长度：这些字符串由外来进程写，会直接进 DOM。没有上限，
// 一个插件就能让面板滚一百屏。超了不静默截掉，要在 hint 上说明截了多少。

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"xgh-system/internal/modules"
)

// shapeOf 一份取值实际"装了哪些字段"，用来在报错里点名。
// 说"形状不对"而不说"哪一格不对"，人会去查数据，而真正要做的是改插件或改清单。
func shapeOf(v modules.Value) string {
	parts := make([]string, 0, 4)
	if strings.TrimSpace(stripCtl(v.Text)) != "" {
		parts = append(parts, "text")
	}
	if len(v.Items) > 0 {
		parts = append(parts, fmt.Sprintf("items(%d)", len(v.Items)))
	}
	if len(v.Columns) > 0 {
		parts = append(parts, fmt.Sprintf("columns(%d)", len(v.Columns)))
	}
	if len(v.Rows) > 0 {
		parts = append(parts, fmt.Sprintf("rows(%d)", len(v.Rows)))
	}
	if len(parts) == 0 {
		return "（什么字段都没填）"
	}
	return strings.Join(parts, "+")
}

func mismatch(key, declared string, v modules.Value) modules.Value {
	return modules.Value{
		Text:  "数据形状不对",
		Hint:  fmt.Sprintf("清单声明组件 %q 是 %s，插件回的却是 %s：这一键不渲染", key, declared, shapeOf(v)),
		Level: "warn",
	}
}

// cutBytes 按字节上限裁，但不把一个汉字的字节切断。
// 切在半个字上，界面收到的就是一个 U+FFFD——那看起来像插件坏了，而问题是我们的上限。
func cutBytes(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut], true
}

// addWarn 把一句"这份数据被动过 / 不合式"的说明并进 hint，并把这一键标成 warn。
// 只裁不说是界面在替插件隐瞒它回过多少东西。
func addWarn(v *modules.Value, why string) {
	if v.Hint == "" {
		v.Hint = why
	} else if !strings.Contains(v.Hint, why) {
		v.Hint = v.Hint + " · " + why
	}
	if v.Level != "warn" {
		v.Level = "warn"
	}
}

// stripCtl 剥掉控制字符与非法字节序列，留下换行、制表和回车。
// 一个 \x00 或方向控制符能让后面半张卡片的排版说话不对人；非法 UTF-8 更糟，
// 它在 JSON 编码阶段就被换成替换符，到界面上是一个谁都读不懂的黑方块。
// 这里清过一遍，cutBytes 的"按字节裁但不切断一个汉字"才有意义。
func stripCtl(s string) string {
	s = strings.ToValidUTF8(s, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// clipInto 裁到 max 字节，真裁过就在这一键的 hint 上写明裁的是什么。
// 裁本身挡不住恶意插件（上限已经挡住了），写明才是关键：只裁不说，等于界面替插件
// 隐瞒它回过多少东西，看到的人以为插件本来就只回了这么一句。
func clipInto(s *string, max int, v *modules.Value, what string) {
	out, cut := cutBytes(stripCtl(*s), max)
	*s = out
	if cut {
		addWarn(v, what+"超出上限，已裁到前一段")
	}
}

// clipCell 裁 list / table 里的小字段，只回答"裁没裁"，由调用方汇总成一句 warn。
// 一格一句会把它变成一句塞满自己的说明，反而看不清这一键到底怎么了。
func clipCell(s string, max int) (string, bool) {
	out, cut := cutBytes(stripCtl(s), max)
	return out, cut
}

// normalizeValues 按 types（组件 key → 清单声明的类型）逐键校正一份数据。
// 清单里没有的键直接丢掉：没有组件就没有卡片，留着它只会让响应体里多一段没人读的内容。
func normalizeValues(types map[string]string, values map[string]modules.Value) map[string]modules.Value {
	if len(values) == 0 {
		return values
	}
	out := make(map[string]modules.Value, len(values))
	for key, v := range values {
		declared, ok := types[key]
		if !ok {
			continue
		}
		v = cleanLevel(v)
		switch declared {
		case modules.WidgetStat, modules.WidgetNote:
			out[key] = normalizeTextValue(key, declared, v)
		case modules.WidgetList:
			out[key] = normalizeList(key, v)
		case modules.WidgetTable:
			out[key] = normalizeTable(key, v)
		default:
			// 清单里出现了这里不认识类型：扫描期本该拦住（widgetTypes 白名单），
			// 走到这里说明两处口径漂移了，明写出来而不是渲染一张猜的卡。
			out[key] = modules.Value{Text: "组件类型不认识", Hint: "清单声明的是 " + declared + "：界面没有这个渲染器", Level: "warn"}
		}
	}
	return out
}

// cleanLevel 只认 ok / warn。别的取值（包括插件自己写的 "error"）一律按 warn 处理并说明：
// 判成"没事"会把一个异常数涂成正常色，判成错误态又得让前端去猜一个新状态长什么样。
func cleanLevel(v modules.Value) modules.Value {
	switch v.Level {
	case "", "ok", "warn":
		return v
	}
	bad := v.Level
	v.Level = "warn"
	addWarn(&v, "等级回的是 "+bad+"，只认 ok / warn")
	return v
}

// normalizeTextValue stat 与 note：只要 Text。
// note 允许多行（换行在渲染时按纯文本折行），两者共用同一个文本上限。
func normalizeTextValue(key, declared string, v modules.Value) modules.Value {
	if len(v.Items) > 0 || len(v.Columns) > 0 || len(v.Rows) > 0 {
		return mismatch(key, declared, v)
	}
	if strings.TrimSpace(stripCtl(v.Text)) == "" {
		// 空白 Text 和"这一键没回"在界面上是同一个样子，所以按没回处理更诚实：
		// 让插件自己去看这句"声明的是 stat，回的却是（什么字段都没填）"。
		return mismatch(key, declared, v)
	}
	// 先裁插件自己写的说明，再往上叠我们的话：反过来的话，后面这几句"被动过刀子"的说明
	// 会被上一句的裁切一起切掉 —— 只剩一个 warn 色，没人知道为什么。
	clipInto(&v.Hint, modules.MaxValueHint, &v, "说明")
	clipInto(&v.Text, modules.MaxValueText, &v, "文本")
	return v
}

func normalizeList(key string, v modules.Value) modules.Value {
	// 空列表是合法事实（"今天一条都没有"），和"这个键没回"是两回事：
	// 前者按 Items 为空正常渲染，后者由界面显示"无数据"。
	if strings.TrimSpace(stripCtl(v.Text)) != "" || len(v.Columns) > 0 || len(v.Rows) > 0 {
		return mismatch(key, modules.WidgetList, v)
	}
	clipInto(&v.Hint, modules.MaxValueHint, &v, "说明")

	items := v.Items
	if len(items) > modules.MaxListItems {
		addWarn(&v, fmt.Sprintf("列表回了 %d 行，超过上限，只列前 %d 行", len(items), modules.MaxListItems))
		items = items[:modules.MaxListItems]
	}
	out := make([]modules.Item, 0, len(items))
	var dropped, cut int
	for _, it := range items {
		label, c1 := clipCell(it.Label, modules.MaxCellBytes)
		value, c2 := clipCell(it.Value, modules.MaxCellBytes)
		hint, c3 := clipCell(it.Hint, modules.MaxValueHint)
		if c1 || c2 || c3 {
			cut++
		}
		if strings.TrimSpace(label) == "" {
			dropped++
			continue
		}
		out = append(out, modules.Item{
			Label: label,
			Value: value,
			Hint:  hint,
			Level: levelOf(it.Level),
		})
	}
	if dropped > 0 {
		// 一行连"说的是什么"都不配占一行；但删了几行必须说，否则看到的人以为插件就回了这么多。
		addWarn(&v, fmt.Sprintf("有 %d 行没写标题，已不显示", dropped))
	}
	if cut > 0 {
		addWarn(&v, fmt.Sprintf("有 %d 行的内容超出上限，已裁短", cut))
	}
	v.Items = out
	v.Text = ""
	v.Columns = nil
	v.Rows = nil
	return v
}

func normalizeTable(key string, v modules.Value) modules.Value {
	if strings.TrimSpace(stripCtl(v.Text)) != "" || len(v.Items) > 0 {
		return mismatch(key, modules.WidgetTable, v)
	}
	clipInto(&v.Hint, modules.MaxValueHint, &v, "说明")

	cols := v.Columns
	// declaredCols 是插件自己说有几列，裁上限之前的那个数：行数对不上要报错，对的是这个数，
	// 不是显示用的列数 —— 插件老老实实回了 11 列、被我们的上限裁成 8 列，
	// 这时候说它"形状不对"是在怪它听话。
	declaredCols := len(cols)
	if len(cols) > modules.MaxTableColumns {
		addWarn(&v, fmt.Sprintf("回了 %d 列，超过上限，只列前 %d 列", len(cols), modules.MaxTableColumns))
		cols = cols[:modules.MaxTableColumns]
	}
	// kept[i] 是"显示出来的第 i 列取自原始第 src 列"。空表头的列整列去掉后，行里的格子
	// 必须按同一份映射取：直接按顺序对齐会让每一格填到隔壁那列的名下，那比不显示更坏。
	type col struct {
		src   int
		key   string
		label string
	}
	kept := make([]col, 0, len(cols))
	var droppedCols int
	for i, c := range cols {
		label, _ := clipCell(c.Label, modules.MaxColumnLabel)
		if strings.TrimSpace(label) == "" {
			// 没有表头的一列在网格里是一根没人知道是什么的竖条，整列连同数据一起去掉。
			droppedCols++
			continue
		}
		keyName, _ := clipCell(c.Key, modules.MaxColumnLabel)
		kept = append(kept, col{src: i, key: keyName, label: label})
	}
	if len(kept) == 0 {
		return mismatch(key, modules.WidgetTable, v)
	}
	if droppedCols > 0 {
		addWarn(&v, fmt.Sprintf("有 %d 列表头是空的，已连同它的数据一起不显示", droppedCols))
	}

	rows := v.Rows
	if len(rows) > modules.MaxTableRows {
		addWarn(&v, fmt.Sprintf("回了 %d 行，超过上限，只列前 %d 行", len(rows), modules.MaxTableRows))
		rows = rows[:modules.MaxTableRows]
	}
	out := make([][]string, 0, len(rows))
	var cut int
	for i, r := range rows {
		// 格子数和表头对不上就不裁也不补：补空格是替插件造数据，裁掉是替它改数据，
		// 两种都会让那张表说的不是插件真正回的话。整键换成一句形状不符，
		// 并且点明是第几行、几格对几列 —— 要改的是插件那一行，不是这个数。
		if len(r) != declaredCols {
			return modules.Value{
				Text:  "数据形状不对",
				Hint:  fmt.Sprintf("清单声明组件 %q 是 table，第 %d 行回了 %d 格，表头却有 %d 列：这一键不渲染", key, i+1, len(r), declaredCols),
				Level: "warn",
			}
		}
		row := make([]string, len(kept))
		for j, kc := range kept {
			cell, wasCut := clipCell(r[kc.src], modules.MaxCellBytes)
			if wasCut {
				cut++
			}
			row[j] = cell
		}
		out = append(out, row)
	}
	if cut > 0 {
		addWarn(&v, fmt.Sprintf("有 %d 个单元格超出上限，已裁短", cut))
	}
	v.Columns = make([]modules.Column, len(kept))
	for i, kc := range kept {
		v.Columns[i] = modules.Column{Key: kc.key, Label: kc.label}
	}
	v.Rows = out
	v.Text = ""
	v.Items = nil
	return v
}

func levelOf(level string) string {
	switch level {
	case "warn":
		return "warn"
	default:
		return ""
	}
}
