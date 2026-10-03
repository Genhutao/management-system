package plugins

import (
	"strings"
	"testing"
	"unicode/utf8"

	"xgh-system/internal/modules"
)

// values_test.go —— M2 的服务端出口校验。
// 这里测的不是"能不能裁短"，而是**裁过 / 丢过之后有没有说实话**：
// 界面上那张卡说的必须恰好是插件真正回的内容，多一个字（替它编）或少一个字（悄悄裁）都算骗人。

func strBytes(n int) string {
	return strings.Repeat("字", n) // 一个汉字 3 字节，用来测"不切断一个字符"
}

func typesOf(pairs ...string) map[string]string {
	t := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		t[pairs[i]] = pairs[i+1]
	}
	return t
}

func TestNormalizeKeepsWellFormedValues(t *testing.T) {
	in := map[string]modules.Value{
		"uptime": {Text: "3 天 4 小时", Hint: "自上次重启", Level: "ok"},
		"notes":  {Text: "第一行\n第二行"},
		"todo":   {Items: []modules.Item{{Label: "待审核播报", Value: "2"}, {Label: "空值也算一行", Value: ""}}},
		"grid": {
			Columns: []modules.Column{{Key: "name", Label: "插件"}, {Key: "state", Label: "状态"}},
			Rows:    [][]string{{"diskusage", "运行中"}},
		},
	}
	out := normalizeValues(typesOf(
		"uptime", modules.WidgetStat, "notes", modules.WidgetNote,
		"todo", modules.WidgetList, "grid", modules.WidgetTable), in)

	for _, k := range []string{"uptime", "notes", "todo", "grid"} {
		v, ok := out[k]
		if !ok {
			t.Fatalf("%s 被丢掉了：合规的数据不该在校验这一步消失", k)
		}
		if v.Level == "warn" {
			t.Errorf("%s 被标成 warn（%s）：正常的取值不该配一句警告", k, v.Hint)
		}
	}
	if out["notes"].Text != "第一行\n第二行" {
		t.Errorf("note 的换行被改写了：%q", out["notes"].Text)
	}
	if len(out["grid"].Rows) != 1 || out["grid"].Rows[0][1] != "运行中" {
		t.Errorf("table 的行被改动：%v", out["grid"].Rows)
	}
}

func TestNormalizeDropsUndeclaredAndActionKeys(t *testing.T) {
	types := typesOf("shown", modules.WidgetStat)
	out := normalizeValues(types, map[string]modules.Value{
		"shown":  {Text: "在清单里"},
		"ghost":  {Text: "清单没声明这个键"},
		"submit": {Text: "这是个按钮，不该有取值"},
	})
	if len(out) != 1 {
		t.Fatalf("只该留下清单声明过的那一键，实际 %v", out)
	}
	if _, ok := out["shown"]; !ok {
		t.Error("声明过的键反而没了")
	}
}

func TestNormalizeShapeMismatchNamesBothSides(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		in       modules.Value
		wantIn   string // hint 里必须出现的字样
	}{
		{"stat 回了列表", modules.WidgetStat, modules.Value{Items: []modules.Item{{Label: "甲"}}}, "是 stat"},
		{"list 回了文本", modules.WidgetList, modules.Value{Text: "一句话"}, "是 list"},
		{"table 回了列表", modules.WidgetTable, modules.Value{Items: []modules.Item{{Label: "甲"}}}, "是 table"},
		{"table 没有表头", modules.WidgetTable, modules.Value{Rows: [][]string{{"甲"}}}, "rows(1)"},
		{"stat 什么都没填", modules.WidgetStat, modules.Value{Hint: "只有一句说明"}, "（什么字段都没填）"},
		{"stat 只回空白", modules.WidgetStat, modules.Value{Text: "   \n  "}, "（什么字段都没填）"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := normalizeValues(typesOf("w", c.declared), map[string]modules.Value{"w": c.in})["w"]
			if v.Level != "warn" {
				t.Fatalf("形状不符必须整键 warn，实际 level=%q", v.Level)
			}
			if v.Text != "数据形状不对" {
				t.Errorf("卡片正文要能说清是形状问题，实际 %q", v.Text)
			}
			if !strings.Contains(v.Hint, c.wantIn) {
				t.Errorf("hint 里要有 %q，实际 %q", c.wantIn, v.Hint)
			}
			// 关键一条：形状不符时不能把插件回的内容原样留着，否则前端按 list 渲染时 Items 还在。
			if len(v.Items) != 0 || len(v.Rows) != 0 || len(v.Columns) != 0 {
				t.Errorf("warn 卡里还带着原始数据：%+v", v)
			}
		})
	}
}

func TestNormalizeTextLimitsClipAndTell(t *testing.T) {
	// 一个汉字 3 字节：500 不是 3 的倍数，正好让朴素的按字节裁切在汉字中间断掉。
	long := strBytes(400)
	v := normalizeValues(typesOf("w", modules.WidgetStat), map[string]modules.Value{
		"w": {Text: long, Hint: strBytes(200)},
	})["w"]
	if len(v.Text) > modules.MaxValueText {
		t.Errorf("Text 没裁到上限：%d 字节 > %d", len(v.Text), modules.MaxValueText)
	}
	if !utf8Valid(v.Text) {
		t.Errorf("裁在半个汉字上，界面会收到替换符：%q", v.Text)
	}
	if !strings.Contains(v.Hint, "文本超出上限") {
		t.Errorf("裁了正文却不说，实际 hint=%q", v.Hint)
	}
	if v.Level != "warn" {
		t.Errorf("被动过刀子必须标 warn，实际 %q", v.Level)
	}
}

func TestNormalizeListLimits(t *testing.T) {
	items := make([]modules.Item, 0, modules.MaxListItems+7)
	items = append(items, modules.Item{Label: "   ", Value: "没人知道这行说的是什么"})
	items = append(items, modules.Item{Label: strBytes(200), Value: "标签超长的行"})
	for i := 0; i < modules.MaxListItems+7; i++ {
		items = append(items, modules.Item{Label: "行", Value: "1"})
	}
	v := normalizeValues(typesOf("w", modules.WidgetList), map[string]modules.Value{"w": {Items: items}})["w"]
	// 上限管的是"看过多少行"（前 50 行），空标题管的是"这一行配不占一行"：
	// 前 50 行里有一条被丢掉，所以显示 49 条，而两句话都要在 hint 上写着。
	if len(v.Items) != modules.MaxListItems-1 {
		t.Fatalf("只该留前 %d 行里的合规行，实际 %d", modules.MaxListItems, len(v.Items))
	}
	for _, want := range []string{"超过上限", "没写标题", "内容超出上限，已裁短"} {
		if !strings.Contains(v.Hint, want) {
			t.Errorf("hint 里缺 %q，实际 %q", want, v.Hint)
		}
	}
	if v.Level != "warn" {
		t.Errorf("裁过又丢了行，必须是 warn，实际 %q", v.Level)
	}
	for _, it := range v.Items {
		if len(it.Label) > modules.MaxCellBytes {
			t.Fatalf("单元格没管住上限：%d 字节", len(it.Label))
		}
		if !utf8Valid(it.Label) {
			t.Fatalf("裁在半个汉字上：%q", it.Label)
		}
	}
}

func TestNormalizeEmptyListIsAFactNotAWarning(t *testing.T) {
	v := normalizeValues(typesOf("w", modules.WidgetList), map[string]modules.Value{"w": {Items: []modules.Item{}}})["w"]
	if v.Level == "warn" {
		t.Errorf("「今天一条都没有」是插件说出来的事实，不该配警告：%q", v.Hint)
	}
	if len(v.Items) != 0 {
		t.Errorf("空列表被填出了内容：%v", v.Items)
	}
	// 与"这个键没回"区分开：没回是前端显示"无数据"，这里回了一个空 items。
}

func TestNormalizeTableKeepsCellsUnderTheRightHeader(t *testing.T) {
	// 第二列表头是空的：那一列整列不显示，但第三列的格子必须还挂在第三列名下。
	v := normalizeValues(typesOf("w", modules.WidgetTable), map[string]modules.Value{"w": {
		Columns: []modules.Column{{Key: "a", Label: "名字"}, {Key: "b", Label: " "}, {Key: "c", Label: "状态"}},
		Rows:    [][]string{{"diskusage", "被丢掉的那格", "运行中"}},
	}})["w"]
	if len(v.Columns) != 2 || v.Columns[1].Label != "状态" {
		t.Fatalf("表头映射错了：%+v", v.Columns)
	}
	if len(v.Rows) != 1 || len(v.Rows[0]) != 2 || v.Rows[0][1] != "运行中" {
		t.Fatalf("格子跟着列错位了：%v", v.Rows)
	}
	if !strings.Contains(v.Hint, "表头是空的") || v.Level != "warn" {
		t.Errorf("丢了一列必须说：%q", v.Hint)
	}
}

func TestNormalizeTableRaggedRowNamesTheRow(t *testing.T) {
	v := normalizeValues(typesOf("w", modules.WidgetTable), map[string]modules.Value{"w": {
		Columns: []modules.Column{{Key: "a", Label: "甲"}, {Key: "b", Label: "乙"}},
		Rows:    [][]string{{"1", "2"}, {"3"}},
	}})["w"]
	if v.Level != "warn" || !strings.Contains(v.Hint, "第 2 行回了 1 格") {
		t.Errorf("要说得出是哪一行对不上，实际 %q", v.Hint)
	}
	if len(v.Rows) != 0 {
		t.Errorf("形状不符时不该留下一半表格：%v", v.Rows)
	}
}

func TestNormalizeTableColumnLimit(t *testing.T) {
	cols := make([]modules.Column, 0, modules.MaxTableColumns+3)
	for i := 0; i < modules.MaxTableColumns+3; i++ {
		cols = append(cols, modules.Column{Key: "c", Label: "列"})
	}
	rows := make([][]string, 0, modules.MaxTableRows+5)
	for i := 0; i < modules.MaxTableRows+5; i++ {
		row := make([]string, len(cols))
		for j := range row {
			row[j] = "格"
		}
		rows = append(rows, row)
	}
	v := normalizeValues(typesOf("w", modules.WidgetTable), map[string]modules.Value{"w": {Columns: cols, Rows: rows}})["w"]
	if len(v.Columns) != modules.MaxTableColumns || len(v.Rows) != modules.MaxTableRows {
		t.Fatalf("上限没管住：%d 列 / %d 行", len(v.Columns), len(v.Rows))
	}
	for _, r := range v.Rows {
		if len(r) != len(v.Columns) {
			t.Fatalf("裁完列没裁格子，前端会渲染出歪的表格：%d 格 vs %d 列", len(r), len(v.Columns))
		}
	}
	for _, want := range []string{"列，超过上限", "行，超过上限"} {
		if !strings.Contains(v.Hint, want) {
			t.Errorf("hint 里缺 %q，实际 %q", want, v.Hint)
		}
	}
}

func TestNormalizeLevelOnlyKnowsOkAndWarn(t *testing.T) {
	cases := map[string]string{"error": "error", "critical": "critical", "": ""}
	for bad, label := range cases {
		v := normalizeValues(typesOf("w", modules.WidgetStat), map[string]modules.Value{
			"w": {Text: "一个数", Level: bad},
		})["w"]
		if bad == "" {
			if v.Level != "" {
				t.Errorf("空等级不该被改写：%q", v.Level)
			}
			continue
		}
		if v.Level != "warn" {
			t.Errorf("%s 等级应折成 warn，实际 %q", label, v.Level)
		}
		// 判成 ok 会把异常数涂成正常色；判成别的又得让前端猜一个新状态长什么样。
		if !strings.Contains(v.Hint, "只认 ok / warn") {
			t.Errorf("要写明只认哪两个，实际 %q", v.Hint)
		}
	}
}

func TestNormalizeStripsControlChars(t *testing.T) {
	v := normalizeValues(typesOf("w", modules.WidgetStat), map[string]modules.Value{
		"w": {Text: "正\r\n常\t文本\x00\x1b[31m红\x1a"},
	})["w"]
	for _, bad := range []string{"\x00", "\x1b", "\x1a"} {
		if strings.Contains(v.Text, bad) {
			t.Errorf("控制字符进了将要写入 DOM 的文本：%q", v.Text)
		}
	}
	if !strings.Contains(v.Text, "正\r\n常\t文本") {
		t.Errorf("换行和制表被一起清掉了：%q", v.Text)
	}
}

func TestNormalizeUnknownWidgetType(t *testing.T) {
	// 白名单漂移（清单加了类型、这里没跟上）必须显式说话，不能猜一张卡渲染。
	v := normalizeValues(typesOf("w", "sparkline"), map[string]modules.Value{"w": {Text: "随便什么都行"}})["w"]
	if v.Level != "warn" || !strings.Contains(v.Hint, "sparkline") {
		t.Errorf("不认识的类型要说出来，实际 %+v", v)
	}
}

func TestNormalizeEmptyValuesPassThrough(t *testing.T) {
	if got := normalizeValues(typesOf("w", modules.WidgetStat), nil); len(got) != 0 {
		t.Errorf("没回任何键时不该造出内容：%v", got)
	}
}

func utf8Valid(s string) bool {
	return utf8.ValidString(s) && !strings.Contains(s, "\uFFFD")
}
