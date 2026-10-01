// Package rosterparse 把名单从任意常见文本形态（TSV/CSV/竖线/空格分隔、UTF-8/GBK/UTF-16、
// 带或不带表头）解析成结构化记录，并给出**逐行**的可解释诊断。
//
// 这里刻意不碰数据库、不碰 HTTP：控制器只负责把字节喂进来、把结果映射成 model.Student。
// 解析这一层的价值全在"如实"二字上——旧实现读不到就编默认值、读断了不报错、
// 一行没解析出来就静默消失，导进来的名单因此没法追责。
package rosterparse

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	xtextunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Field 是解析器认识的名单字段。
type Field string

const (
	FieldRealName   Field = "real_name"
	FieldGrade      Field = "grade"
	FieldClassName  Field = "class_name"
	FieldBuilding   Field = "building"
	FieldRoomNumber Field = "room_number"
	FieldBedNumber  Field = "bed_number"
	FieldStudentNo  Field = "student_no"
	FieldGender     Field = "gender"
	FieldPhone      Field = "phone"
)

// fieldLabel 用于诊断文案，让报错说人话而不是报字段名。
var fieldLabel = map[Field]string{
	FieldRealName:   "姓名",
	FieldGrade:      "年级",
	FieldClassName:  "班级",
	FieldBuilding:   "楼栋",
	FieldRoomNumber: "寝室",
	FieldBedNumber:  "床位",
	FieldStudentNo:  "学号",
	FieldGender:     "性别",
	FieldPhone:      "联系电话",
}

func (f Field) Label() string {
	if s, ok := fieldLabel[f]; ok {
		return s
	}
	return string(f)
}

// SupportedFields 是解析器认识的全部字段，顺序即界面逐列下拉的展示顺序。
// 导出是为了让前端不必再抄一份字段清单——两边各抄一份迟早会漂移。
var SupportedFields = []Field{
	FieldRealName,
	FieldStudentNo,
	FieldGrade,
	FieldClassName,
	FieldBuilding,
	FieldRoomNumber,
	FieldBedNumber,
	FieldGender,
	FieldPhone,
}

// DefaultRequired 是入库的最低要求。
//
// 年级不在其中：年级能从班级推出来，属于换算而不是编造。
var DefaultRequired = []Field{FieldRealName, FieldClassName, FieldBuilding, FieldRoomNumber}

// 诊断码。前端按码决定是标红还是标黄。
const (
	CodeMissingField    = "missing_field"    // 必填缺失，该行不得入库
	CodeBadValue        = "bad_value"        // 值格式可疑（手机号位数不对、姓名被脱敏等）
	CodeAmbiguous       = "ambiguous"        // 同一行里无法判定（如 4 位数既像寝室又像学号）
	CodeDuplicate       = "duplicate"        // 与另一行重复
	CodeSkipped         = "skipped"          // 整行没解析出任何字段
	CodeDerived         = "derived"          // 这个值是换算出来的，不是表里写的原文
	CodeFlatTable       = "flat_table"       // 整份内容只有一个物理行
	CodeHeaderNote      = "header"           // 表头识别结论
	CodeEncoding        = "encoding"         // 编码归一结论
	CodeColumnCollision = "column_collision" // 两列抢同一个字段
	CodeUnmappedColumns = "unmapped_columns" // 有列没认出来
	CodeSensitive       = "sensitive"        // 检测到敏感列，按最小必要原则不导入
	CodeSheet           = "sheet"            // xlsx 实际读取了哪个工作表
	CodeSummaryRow      = "summary"          // 合计/统计行，不是学生记录
)

// Issue 是一条诊断。LineNo 为 0 表示与具体行无关（编码、表头层面的结论）。
type Issue struct {
	LineNo  int    `json:"line_no,omitempty"`
	Code    string `json:"code"`
	Field   Field  `json:"field,omitempty"`
	Message string `json:"message"`
	Text    string `json:"text,omitempty"`
}

// Record 是一条解析结果。Missing 非空即表示该行被判定为不可入库。
//
// JSON 键与既有前端契约保持一致，控制器可以直接序列化返回。
type Record struct {
	LineNo     int     `json:"line_no"`
	RealName   string  `json:"real_name"`
	Grade      string  `json:"grade"`
	ClassName  string  `json:"class_name"`
	Building   string  `json:"building"`
	RoomNumber string  `json:"room_number"`
	BedNumber  string  `json:"bed_number"`
	StudentNo  string  `json:"student_no"`
	Gender     string  `json:"gender"`
	Phone      string  `json:"phone"`
	SourceLine string  `json:"source_line"`
	Missing    []Field `json:"missing,omitempty"`
}

// Blocked 表示该行缺了必填字段，不得入库。
func (r Record) Blocked() bool { return len(r.Missing) > 0 }

// Options 控制解析口径。
type Options struct {
	// Separator 强制分隔符（"\t" / "," / ";" / "|" / " "），留空则自动推断。
	Separator string
	// Required 覆盖必填字段集合，留 nil 用 DefaultRequired。
	Required []Field
	// Now 是年级换算的基准时刻，零值取当前时间。
	// 毕业年份要换算成"高一/高二/高三"，没有基准时刻这条口径就不可测。
	Now time.Time
	// ColumnMap 是人工指定的"字段 → 列下标"，非 nil 即跳过自动表头识别。
	// 值为 -1 表示这一列不导入——界面上的「不导入」选项走这里。
	ColumnMap map[Field]int
	// HeaderLine 人工指定的表头所在行（1 起），0 表示这份表没有表头。仅在给了 ColumnMap 时生效。
	HeaderLine int
}

// Header 是表头识别结果，nil 表示这份名单没有表头、走的是特征识别。
type Header struct {
	LineNo      int           `json:"line_no"`
	Separator   string        `json:"separator"`
	Columns     map[Field]int `json:"columns"`
	Cells       []string      `json:"cells"`
	Unmapped    []string      `json:"unmapped,omitempty"`
	Fingerprint string        `json:"fingerprint"`

	// sensitiveIdx 是表头里敏感列的下标，不参与序列化：
	// 这些单元格连"残余"都不算，否则未脱敏的 18 位身份证号会被数字回退捡去当学号。
	sensitiveIdx map[int]bool
}

// Stats 是汇总口径，供界面显示。
type Stats struct {
	LinesTotal  int            `json:"lines_total"`
	DataRows    int            `json:"data_rows"`
	Recognized  int            `json:"recognized"`
	Blocked     int            `json:"blocked"`
	Skipped     int            `json:"skipped"`
	GradeCounts map[string]int `json:"grade_counts"`
}

// Result 是一次解析的完整产出。
type Result struct {
	Records []Record `json:"records"`
	Issues  []Issue  `json:"issues"`
	Header  *Header  `json:"header"`
	Stats   Stats    `json:"stats"`
	// Fingerprint 是"这张表"的指纹：认出表头时取表头行，认不出时取第一行。
	// 没有它，自动识别失败的表就永远不会去查记住的列映射——而那恰恰最需要复用。
	Fingerprint string `json:"fingerprint"`
}

// errEmptyInput 表示解码之后一行内容都没有——是输入问题，不是解析问题。
var errEmptyInput = errors.New("名单内容为空：解码后没有读到任何一行文本")

// sheetRow 是内核看到的一行。Cells 为 nil 表示这行还是纯文本、要按分隔符现切；
// xlsx 这类结构化输入直接带着切好的单元格进来，文本与表格因此共用同一套解析。
type sheetRow struct {
	No    int
	Text  string
	Cells []string
}

func (r sheetRow) cells(sep string) []string {
	if r.Cells != nil {
		return r.Cells
	}
	return splitCells(r.Text, sep)
}

// bestCells 让表头识别按这一行自己的形态选分隔符。
func (r sheetRow) bestCells() []string {
	if r.Cells != nil {
		return r.Cells
	}
	return splitCells(r.Text, bestSeparatorFor(r.Text))
}

// Parse 解析原始字节（粘贴文本或 CSV/TSV 文件）。只有"完全读不出内容"才算 error，
// 单行解析失败一律进 Issues，不中断整批。
func Parse(raw []byte, opts Options) (*Result, error) {
	text, encodingIssues := Decode(raw)
	rows := splitRows(text)
	res, err := parseRows(rows, opts, len(raw))
	if res != nil {
		res.Issues = append(encodingIssues, res.Issues...)
	}
	if err != nil && len(encodingIssues) > 0 {
		return nil, errors.New(encodingIssues[0].Message)
	}
	return res, err
}

// ParseCells 解析已经切成单元格的表（xlsx 等结构化输入）。
//
// 刻意不走"把单元格拼回字符串再切一遍"那条路：单元格里的制表符或逗号
// 会被再切一次，列就错位了。
func ParseCells(rows [][]string, opts Options) (*Result, error) {
	prepared := make([]sheetRow, 0, len(rows))
	for i, cells := range rows {
		if isBlankRow(cells) {
			continue
		}
		prepared = append(prepared, sheetRow{
			No:    i + 1,
			Text:  strings.Join(cells, "\t"),
			Cells: cells,
		})
	}
	return parseRows(prepared, opts, 0)
}

func isBlankRow(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// parseRows 是两种输入共用的主流程。
func parseRows(rows []sheetRow, opts Options, rawBytes int) (*Result, error) {
	res := &Result{Records: []Record{}, Issues: []Issue{}}
	res.Stats.GradeCounts = map[string]int{"高一": 0, "高二": 0, "高三": 0, "其他": 0}

	res.Stats.LinesTotal = len(rows)
	if len(rows) == 0 {
		return nil, errEmptyInput
	}

	if len(rows) == 1 && rawBytes > 64<<10 {
		res.Issues = append(res.Issues, Issue{
			LineNo:  rows[0].No,
			Code:    CodeFlatTable,
			Message: fmt.Sprintf("整份内容只有一个物理行（约 %d KB），换行符可能已丢失或表格被压平；这样最多只能解析出 1 条记录，其余内容会被丢弃", rawBytes>>10),
			Text:    clip(strings.TrimSpace(rows[0].Text), 80),
		})
	}

	required := opts.Required
	if required == nil {
		required = DefaultRequired
	}

	sep := opts.Separator
	if sep == "" {
		sep = detectSeparator(rows)
	}

	header := buildHeader(rows, opts, sep)
	if header != nil && header.Separator != "" && opts.Separator == "" {
		sep = header.Separator
	}
	res.Header = header

	// 指纹是"记住列映射"的键：认出表头就用表头行，认不出就用第一行——
	// 否则最需要复用的那种表（自动识别失败的）永远拿不到复用机会。
	if header != nil {
		res.Fingerprint = header.Fingerprint
	} else {
		res.Fingerprint = HeaderFingerprint(rows[0].cells(sep))
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	if header != nil {
		names, _ := sensitiveCells(header.Cells)
		for _, col := range names {
			res.Issues = append(res.Issues, Issue{
				LineNo: header.LineNo, Code: CodeSensitive,
				Message: "检测到敏感列「" + col + "」，系统不需要也不导入该字段",
			})
		}
	}

	start := 0
	if header != nil {
		start = header.LineNo // LineNo 从 1 起，正好等于要跳过的行数
	}

	for _, row := range rows[start:] {
		trimmed := strings.TrimSpace(row.Text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// 数据区里再次出现的表头（多段表格拼接、Excel 每页重复表头）安静跳过，
		// 报成"没解析出任何字段"会把人引去查那几行数据。
		if isHeaderRow(row) {
			res.Issues = append(res.Issues, Issue{
				LineNo: row.No, Code: CodeHeaderNote,
				Message: "这是一行表头，已跳过",
				Text:    clip(trimmed, 80),
			})
			continue
		}
		if isSummaryRow(row.bestCells()) {
			res.Issues = append(res.Issues, Issue{
				LineNo: row.No, Code: CodeSummaryRow,
				Message: "这是一行合计/统计，不是学生记录，已跳过",
				Text:    clip(trimmed, 80),
			})
			continue
		}
		rec, issues := parseRow(row, header, sep, now)
		if rec == nil {
			res.Stats.Skipped++
			res.Issues = append(res.Issues, Issue{
				LineNo:  row.No,
				Code:    CodeSkipped,
				Message: "这一行没有解析出任何有效字段，已跳过",
				Text:    clip(trimmed, 80),
			})
			continue
		}
		res.Stats.DataRows++

		missing := checkRequired(*rec, required)
		rec.Missing = missing
		for _, f := range missing {
			res.Issues = append(res.Issues, Issue{
				LineNo:  row.No,
				Code:    CodeMissingField,
				Field:   f,
				Message: "缺少" + f.Label() + "，该行不会入库，请补齐后重导",
				Text:    clip(trimmed, 80),
			})
		}
		res.Issues = append(res.Issues, issues...)

		if len(missing) == 0 {
			res.Stats.Recognized++
			res.Stats.GradeCounts[gradeBucket(rec.Grade)]++
		} else {
			res.Stats.Blocked++
		}
		res.Records = append(res.Records, *rec)
	}

	res.Issues = append(res.Issues, detectDuplicates(res.Records)...)
	return res, nil
}

// gradeBucket 把年级归到统计口径的四档里。
func gradeBucket(g string) string {
	switch g {
	case "高一", "高二", "高三":
		return g
	}
	return "其他"
}

func checkRequired(r Record, required []Field) []Field {
	get := func(f Field) string {
		switch f {
		case FieldRealName:
			return r.RealName
		case FieldGrade:
			return r.Grade
		case FieldClassName:
			return r.ClassName
		case FieldBuilding:
			return r.Building
		case FieldRoomNumber:
			return r.RoomNumber
		case FieldBedNumber:
			return r.BedNumber
		case FieldStudentNo:
			return r.StudentNo
		case FieldGender:
			return r.Gender
		case FieldPhone:
			return r.Phone
		}
		return ""
	}
	var missing []Field
	for _, f := range required {
		if strings.TrimSpace(get(f)) == "" {
			missing = append(missing, f)
		}
	}
	return missing
}

// ---------------------------------------------------------------------------
// 编码归一
// ---------------------------------------------------------------------------

// Decode 把任意常见导出编码的字节统一成 UTF-8 文本。
//
// 学校侧的表绝大多数是 Excel 导出的 GBK，旧实现完全不处理，
// 结果是整份名单变成乱码还能"识别成功"。这里失败时不猜，如实报 encoding 诊断。
func Decode(raw []byte) (string, []Issue) {
	if len(raw) == 0 {
		return "", nil
	}

	// UTF-16 带 BOM：Excel 的"Unicode 文本"格式
	if len(raw) >= 2 {
		if (raw[0] == 0xFF && raw[1] == 0xFE) || (raw[0] == 0xFE && raw[1] == 0xFF) {
			out, _, err := transform.Bytes(xtextunicode.UTF16(xtextunicode.LittleEndian, xtextunicode.UseBOM).NewDecoder(), raw)
			if err == nil && utf8.Valid(out) {
				return string(out), []Issue{{Code: CodeEncoding, Message: "检测到 UTF-16 BOM，已转成 UTF-8 解析"}}
			}
			return "", []Issue{{Code: CodeEncoding, Message: "UTF-16 解码失败，文件可能已损坏"}}
		}
	}

	// 二进制特征检查：GBK 解码器对 xlsx/zip 这类字节常常"解码成功"并产出合法乱码，
	// 所以必须在解码之前就按字节特征拒绝，否则一份 Excel 表会变成一份"识别成功"的名单。
	if looksBinary(raw) {
		return "", []Issue{{Code: CodeEncoding, Message: "这份文件既不是文本也不是可解析的表格。名单请上传 .xlsx（Excel 工作表）、或 .csv/.txt/.tsv；PDF、Word、图片无法直接导入，请先在 Excel 里整理成表格"}}
	}

	// UTF-8 BOM
	text := strings.TrimPrefix(string(raw), "\xEF\xBB\xBF")
	b := []byte(text)
	if utf8.Valid(b) {
		return text, nil
	}

	out, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), b)
	if err == nil && utf8.Valid(out) && !strings.ContainsRune(string(out), utf8.RuneError) {
		return string(out), []Issue{{Code: CodeEncoding, Message: "检测到 GBK 编码，已转成 UTF-8 解析"}}
	}
	return "", []Issue{{Code: CodeEncoding, Message: "既不是 UTF-8 也不是 GBK，无法解码该文件"}}
}

// looksBinary 按字节特征判断"这不是文本"。
// NUL 是最强信号；此外控制字符占比过高也拒绝（\t \n \r 除外）。
func looksBinary(raw []byte) bool {
	limit := len(raw)
	if limit > 4096 {
		limit = 4096
	}
	controls := 0
	for i := 0; i < limit; i++ {
		c := raw[i]
		if c == 0x00 {
			return true
		}
		if c < 0x09 || (c > 0x0D && c < 0x20) {
			controls++
		}
	}
	return limit > 0 && controls*10 > limit
}

// splitRows 把文本切成非空物理行，保留原始行号——
// 诊断必须指得回用户眼前的那一行，重排过就没法对照了。
func splitRows(text string) []sheetRow {
	var out []sheetRow
	no := 0
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		no++
		l = strings.TrimRight(l, "\r")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, sheetRow{No: no, Text: l})
	}
	return out
}

// ---------------------------------------------------------------------------
// 分隔符与单元格
// ---------------------------------------------------------------------------

var separatorCandidates = []string{"\t", ",", ";", "|"}

// detectSeparator 在整份名单上投票，而不是只看第一行：
// 只有一行的粘贴、或首行是"XX中学住宿名单"这类标题时，单行判断会选错分隔符。
func detectSeparator(rows []sheetRow) string {
	type vote struct {
		sep  string
		hits int
	}
	var votes []vote
	for _, cand := range separatorCandidates {
		hits := 0
		for _, ln := range rows {
			if strings.Contains(ln.Text, cand) {
				hits++
			}
		}
		votes = append(votes, vote{cand, hits})
	}
	best := vote{}
	for _, v := range votes {
		if v.hits > best.hits {
			best = v
		}
	}
	if best.hits > 0 {
		return best.sep
	}
	return " "
}

// splitCells 切一行成单元格。制表符/逗号/分号/竖线都支持引号包裹，
// 旧实现只有逗号走 csv.Reader，于是 "1号楼,301" 之外的带引号表都解析错位。
func splitCells(line, sep string) []string {
	line = strings.TrimRight(line, "\r")
	if sep == "" || sep == " " {
		return strings.Fields(line)
	}
	if !strings.Contains(line, `"`) {
		if sep == "  " {
			return strings.Fields(line)
		}
		return strings.Split(line, sep)
	}

	r := csv.NewReader(strings.NewReader(line))
	r.Comma = rune(sep[0])
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	rec, err := r.Read()
	if err != nil {
		return strings.Split(line, sep)
	}
	return rec
}

// ---------------------------------------------------------------------------
// 表头识别
// ---------------------------------------------------------------------------

// headerRejectWords 含这些词的单元格是数据或说明，不是表头。
var headerRejectWords = []string{"班主任", "辅导员", "宿管", "联系电话人", "备注说明"}

// classifyHeaderCell 把一个表头单元格归到某个字段。
//
// 判定顺序是刻意的：先"学号"再"寝室"，因为"房号/编号"都含"号"；
// 先"楼栋"再"寝室"，因为"宿舍楼"既含"宿舍"又含"楼"，它指的是楼。
func classifyHeaderCell(raw string) (Field, bool) {
	c := ToHalfWidth(strings.ToLower(strings.Join(strings.Fields(raw), "")))
	if c == "" {
		return "", false
	}
	for _, bad := range headerRejectWords {
		if strings.Contains(c, ToHalfWidth(strings.ToLower(bad))) {
			return "", false
		}
	}
	// 含数字的一律是数据值（"8栋""高三(5)班""20230501"），不是标签。
	// 缺这道判断时，只粘贴一行数据会被当成表头整行丢弃，识别结果直接归零。
	if strings.IndexFunc(c, unicode.IsDigit) >= 0 {
		return "", false
	}

	switch {
	case containsAny(c, "学号", "学籍号", "编号", "studentno", "studentid", "no", "id"):
		return FieldStudentNo, true
	case containsAny(c, "性别", "sex", "gender"):
		return FieldGender, true
	case containsAny(c, "手机", "电话", "phone", "mobile", "tel", "联系"):
		return FieldPhone, true
	case containsAny(c, "床位", "床号", "床", "bed"):
		return FieldBedNumber, true
	case containsAny(c, "姓名", "名字", "name"):
		return FieldRealName, true
	case containsAny(c, "年级", "grade"):
		return FieldGrade, true
	case containsAny(c, "班级", "class"):
		return FieldClassName, true
	case containsAny(c, "楼", "栋", "公寓", "building", "dormitory"):
		return FieldBuilding, true
	case containsAny(c, "寝", "宿舍", "舍", "室", "房", "room", "dorm"):
		return FieldRoomNumber, true
	}
	return "", false
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// buildHeader 决定用哪一套列映射：人工指定优先于自动识别。
// 自动识别永远会有认错的时候，认错了就由使用者直接指定。
func buildHeader(rows []sheetRow, opts Options, sep string) *Header {
	if opts.ColumnMap == nil {
		return findHeader(rows, opts.Separator)
	}

	columns := map[Field]int{}
	for f, idx := range opts.ColumnMap {
		if idx >= 0 {
			columns[f] = idx
		}
	}
	if len(columns) == 0 {
		return nil
	}

	var cells []string
	lineNo := opts.HeaderLine
	if lineNo > 0 {
		if lineNo > len(rows) {
			lineNo = len(rows)
		}
		cells = rows[lineNo-1].cells(sep)
	}
	return newHeader(lineNo, sep, columns, cells, nil)
}

// HeaderFingerprint 由表头列名序列算出稳定指纹，作为"记住列映射并自动复用"的键。
//
// 只裁掉尾部空列，保留中间空列——列下标要对齐，否则复用时会整体错位。
// 同一张表换一批人，指纹不变，映射就还能命中。
func HeaderFingerprint(cells []string) string {
	out := append([]string(nil), cells...)
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		return ""
	}
	norm := make([]string, 0, len(out))
	for _, c := range out {
		norm = append(norm, strings.ToLower(strings.Join(strings.Fields(ToHalfWidth(c)), "")))
	}
	sum := sha256.Sum256([]byte(strings.Join(norm, "\x1f")))
	return hex.EncodeToString(sum[:])
}

// findHeader 在前若干行里找表头，而不是死认第一行——
// 学校表常带"XX中学 2025 级住宿名单"这样的标题行。
func findHeader(rows []sheetRow, forcedSep string) *Header {
	const scanRows = 6
	limit := len(rows)
	if limit > scanRows {
		limit = scanRows
	}

	for i := 0; i < limit; i++ {
		sep := forcedSep
		if sep == "" && rows[i].Cells == nil {
			sep = bestSeparatorFor(rows[i].Text)
		}
		cells := rows[i].bestCells()
		if len(cells) < 2 {
			continue
		}

		columns := map[Field]int{}
		var unmapped []string
		hits := 0
		for idx, cell := range cells {
			f, ok := classifyHeaderCell(cell)
			if !ok {
				if strings.TrimSpace(cell) != "" {
					unmapped = append(unmapped, strings.TrimSpace(cell))
				}
				continue
			}
			if _, taken := columns[f]; taken {
				continue
			}
			columns[f] = idx
			hits++
		}
		// 至少认到 2 个不同字段才算表头，避免把一行普通数据误判成表头后整批丢光。
		if hits >= 2 {
			if _, ok := columns[FieldRealName]; !ok {
				continue // 认不出姓名列的表头没有意义，交给特征识别
			}
			return newHeader(i+1, sep, columns, cells, unmapped)
		}
	}
	return nil
}

// sensitiveWords 是系统不需要、也不应该入库的列。
// 学号/学籍号不在其中：那是打表绑定要用的最小必要信息。
var sensitiveWords = []string{"身份证", "户口", "家庭住址", "住址", "银行卡", "监护人", "家长联系", "残疾", "疾病", "病史"}

// sensitiveCells 挑出表头里的敏感列，返回列名与列下标。
//
// 列名用来明确表态"看到了但不导入"（默默丢列会让导入者以为数据已经进去了）；
// 下标也必须拿出去——身份证号列如果没脱敏就是 18 位纯数字，
// 只要还留在"未被认领的残余单元格"里，就会被数字回退捡去当成学号入库。
func sensitiveCells(cells []string) ([]string, map[int]bool) {
	var names []string
	idx := map[int]bool{}
	for i, c := range cells {
		n := ToHalfWidth(c)
		for _, w := range sensitiveWords {
			if strings.Contains(n, w) {
				if t := strings.TrimSpace(c); t != "" {
					names = append(names, t)
				}
				idx[i] = true
				break
			}
		}
	}
	return names, idx
}

// isHeaderRow 判定一行是否"又是一行表头"。
// classifyHeaderCell 已经排除了含数字的单元格，所以真实数据行不会被误判成表头。
func isHeaderRow(row sheetRow) bool {
	cells := row.bestCells()
	if len(cells) < 2 {
		return false
	}
	hits := 0
	for _, c := range cells {
		if _, ok := classifyHeaderCell(c); ok {
			hits++
		}
	}
	return hits >= 2
}

// summaryWords 是表尾统计行的整格写法。
//
// 只接受"整个单元格就等于这些词"：备注里写着"已合计"的真实学生行不该被跳过。
var summaryWords = []string{"合计", "总计", "小计", "共计", "累计", "总人数", "合计人数", "统计"}

// isSummaryRow 判定一行是否是表尾的合计/统计行。
//
// 住宿部总表末尾的"合计 6"如果按学生行处理，会变成一条"缺班级、缺楼栋"的记录挂在预览里，
// 使用者只会以为是名单写坏了——它其实压根不是人。
func isSummaryRow(cells []string) bool {
	for _, c := range cells {
		t := strings.TrimSpace(ToHalfWidth(c))
		if t == "" {
			continue
		}
		for _, w := range summaryWords {
			if t == w {
				return true
			}
		}
	}
	return false
}

// bestSeparatorFor 给单行挑命中数最多的分隔符，全都没有则按空白切。
func bestSeparatorFor(line string) string {
	best, bestHits := "", 0
	for _, cand := range separatorCandidates {
		hits := strings.Count(line, cand)
		if hits > bestHits {
			best, bestHits = cand, hits
		}
	}
	if bestHits > 0 {
		return best
	}
	if strings.Contains(line, "  ") {
		return "  "
	}
	return " "
}

// ---------------------------------------------------------------------------
// 单行解析
// ---------------------------------------------------------------------------

// mapped 表示这一列是否已被表头认领。
func (h *Header) mapped(f Field) bool {
	if h == nil {
		return false
	}
	_, ok := h.Columns[f]
	return ok
}

// newHeader 组装表头结果：自动识别与人工映射两条路共用，顺带记下指纹与敏感列下标。
func newHeader(lineNo int, sep string, columns map[Field]int, cells, unmapped []string) *Header {
	_, idx := sensitiveCells(cells)
	return &Header{
		LineNo:       lineNo,
		Separator:    sep,
		Columns:      columns,
		Cells:        cells,
		Unmapped:     unmapped,
		Fingerprint:  HeaderFingerprint(cells),
		sensitiveIdx: idx,
	}
}

// residualText 返回未被表头认领、且不属于敏感列的那些单元格文本。
// 特征补齐只能看这些残余，否则"年级=2027"会被再捡一遍当成学号。
func (h *Header) residualText(cells []string) string {
	if h == nil {
		return strings.Join(cells, " ")
	}
	claimed := map[int]bool{}
	for _, idx := range h.Columns {
		claimed[idx] = true
	}
	var rest []string
	for i, c := range cells {
		if claimed[i] || h.sensitiveIdx[i] || strings.TrimSpace(c) == "" {
			continue
		}
		rest = append(rest, c)
	}
	return strings.Join(rest, " ")
}

func parseRow(row sheetRow, header *Header, sep string, now time.Time) (*Record, []Issue) {
	rec := &Record{LineNo: row.No, SourceLine: strings.TrimSpace(row.Text)}
	var issues []Issue

	var featureSource string
	if header != nil {
		cells := row.cells(sep)
		applyColumns(rec, cells, header)
		featureSource = header.residualText(cells)
	} else {
		featureSource = row.Text
	}

	// 表头没给全、或压根没有表头时，用特征识别补齐**表头没有的**字段。
	fillFromFeatures(rec, &issues, featureSource, func(f Field) bool {
		return !header.mapped(f)
	})

	if rec.RealName == "" && rec.RoomNumber == "" && rec.ClassName == "" && rec.Building == "" {
		return nil, issues
	}

	normalizeRecord(rec, &issues, now)
	return rec, issues
}

// normalizeRecord 把取到的字段统一成系统口径。
//
// 表头映射进来的单元格同样要归一：楼栋 "N" 与打表侧的 "N号楼" 必须落在同一口径上，
// 否则同一个物理楼会在库里裂成好几条，寝室评优按楼栋聚合时会漏。
func normalizeRecord(rec *Record, issues *[]Issue, now time.Time) {
	if v := NormalizeBuilding(rec.Building); v != "" {
		rec.Building = v
	}
	if rec.RoomNumber != "" {
		rec.RoomNumber = NormalizeRoom(rec.RoomNumber)
		// 寝室号常自带楼栋前缀（N 号楼的 N101）。前缀与楼栋重复时剥掉，
		// 否则打表填 "101" 匹配不上导进来的 "N101"。
		if prefix := strings.TrimSuffix(rec.Building, "号楼"); prefix != "" && len(prefix) <= 2 {
			if rest := strings.TrimPrefix(rec.RoomNumber, prefix); rest != rec.RoomNumber && isAllDigits(rest) && rest != "" {
				*issues = append(*issues, Issue{
					LineNo: rec.LineNo, Code: CodeDerived, Field: FieldRoomNumber,
					Message: "寝室号 " + rec.RoomNumber + " 已剥掉与楼栋重复的前缀 " + prefix + "，改记为 " + rest,
				})
				rec.RoomNumber = rest
			}
		}
	}
	if rec.Gender != "" {
		g := NormalizeGender(rec.Gender)
		if g == "" {
			*issues = append(*issues, Issue{
				LineNo: rec.LineNo, Code: CodeBadValue, Field: FieldGender,
				Message: "性别值 " + rec.Gender + " 无法识别为男/女，已保留原值",
			})
		} else {
			rec.Gender = g
		}
	}
	if rec.Phone != "" {
		rec.Phone = NormalizePhone(rec.Phone)
	}
	if rec.BedNumber != "" {
		if b := NormalizeBed(rec.BedNumber); b != "" {
			rec.BedNumber = b
		} else {
			rec.BedNumber = strings.TrimSpace(ToHalfWidth(rec.BedNumber))
		}
	}
	if rec.StudentNo != "" {
		rec.StudentNo = NormalizeStudentNo(rec.StudentNo)
	}

	// 年级：学校表普遍写"毕业年份"（如 2027）而不是"高一/高二/高三"。
	if rec.Grade != "" && !isGradeWord(rec.Grade) {
		if y, ok := parseYear(rec.Grade); ok {
			if g, valid := GradeFromGraduationYear(y, now); valid {
				*issues = append(*issues, Issue{
					LineNo: rec.LineNo, Code: CodeDerived, Field: FieldGrade,
					Message: "年级 " + strconv.Itoa(y) + " 按毕业年份换算为 " + g,
				})
				rec.Grade = g
			} else {
				*issues = append(*issues, Issue{
					LineNo: rec.LineNo, Code: CodeBadValue, Field: FieldGrade,
					Message: "毕业年份 " + strconv.Itoa(y) + " 换算不出在读年级（可能已毕业或年份有误），请核对该行",
				})
			}
		}
	}

	// 班级：学校表的"班级"列常常只有 1、2、3 这样的序号，要和年级合成完整班名。
	if c := strings.TrimSpace(ToHalfWidth(rec.ClassName)); c != "" {
		if isAllDigits(c) && rec.Grade != "" {
			n := strings.TrimLeft(c, "0")
			if n == "" {
				n = "0"
			}
			rec.ClassName = rec.Grade + "(" + n + ")班"
		} else if isAllDigits(c) {
			*issues = append(*issues, Issue{
				LineNo: rec.LineNo, Code: CodeMissingField, Field: FieldGrade,
				Message: "班级只有序号 " + c + " 但没有年级，无法拼出完整班名",
			})
			rec.ClassName = ""
		} else {
			rec.ClassName = NormalizeClassName(c)
		}
	}

	// 年级仍空时从班级换算——这是推导，不是编造。
	if rec.Grade == "" && rec.ClassName != "" {
		rec.Grade = GradeFromClass(rec.ClassName)
	}

	if strings.ContainsAny(rec.RealName, "*＊") {
		*issues = append(*issues, Issue{
			LineNo: rec.LineNo, Code: CodeBadValue, Field: FieldRealName,
			Message: "姓名 " + rec.RealName + " 含脱敏字符，打表与档案按姓名匹配会失效，建议改用完整姓名或学号",
		})
	}
}

// isGradeWord 判断是否已经是系统口径的年级。
func isGradeWord(g string) bool {
	switch strings.TrimSpace(g) {
	case "高一", "高二", "高三":
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func parseYear(s string) (int, bool) {
	digits := compactDigits(s)
	if len(digits) != 4 {
		return 0, false
	}
	y, err := strconv.Atoi(digits)
	if err != nil || y < 1990 || y > 2100 {
		return 0, false
	}
	return y, true
}

// AcademicYear 返回给定时刻所处的学年（9 月开学进位）。
func AcademicYear(t time.Time) int {
	if int(t.Month()) >= 9 {
		return t.Year()
	}
	return t.Year() - 1
}

// GradeFromGraduationYear 把毕业年份换算成年级。
//
// 高中学制三年：入学级 = 毕业年 - 3；当前学年 - 入学级 + 1 即第几年。
// 换算不出来（已毕业、年份明显有误）时返回 false，由调用方报诊断，不猜一个年级蒙混。
func GradeFromGraduationYear(graduationYear int, now time.Time) (string, bool) {
	switch AcademicYear(now) - (graduationYear - 3) + 1 {
	case 1:
		return "高一", true
	case 2:
		return "高二", true
	case 3:
		return "高三", true
	}
	return "", false
}

func applyColumns(rec *Record, cells []string, h *Header) {
	set := func(f Field, target *string) {
		idx, ok := h.Columns[f]
		if !ok || idx < 0 || idx >= len(cells) {
			return
		}
		*target = strings.TrimSpace(cells[idx])
	}
	set(FieldRealName, &rec.RealName)
	set(FieldGrade, &rec.Grade)
	set(FieldClassName, &rec.ClassName)
	set(FieldBuilding, &rec.Building)
	set(FieldRoomNumber, &rec.RoomNumber)
	set(FieldBedNumber, &rec.BedNumber)
	set(FieldStudentNo, &rec.StudentNo)
	set(FieldGender, &rec.Gender)
	set(FieldPhone, &rec.Phone)
}

// 特征识别用的模式。集中在这里，便于逐条解释"为什么这个 token 被判成寝室"。
var (
	// 楼栋：1号楼 / 西12号楼 / 7栋 / A栋 / 西区12栋
	reBuilding = regexp.MustCompile(`([东西南北]?\d+[号栋楼]+|[A-Za-z]\d*[号栋楼]+|[东西南北]区\d*号?[楼栋]?)`)
	// 寝室：302 / 1004 / 302室 / 4-201
	reRoomWithSuffix = regexp.MustCompile(`^([1-9]\d{1,4})(?:室|房)$`)
	reRoomDash       = regexp.MustCompile(`^(\d{1,2}-\d{2,4})(?:室|房)?$`)
	// 床位：1号床 / 1床 / 床3
	reBed = regexp.MustCompile(`^(\d{1,2})号?床$|^床(\d{1,2})$`)
	// 班级：高一(2)班 / 高三3班 / 高2401班 / 高二(12)班 / 2024级3班
	reClass = regexp.MustCompile(`(高[一二三123]|202\d级?)\s*\(?(\d{1,4})\)?\s*班?`)
	// 姓名：2~6 个汉字，允许内含间隔号（少数民族姓名）
	reName = regexp.MustCompile(`^[\p{Han}·]{2,6}$`)
	// 纯数字
	reDigits = regexp.MustCompile(`^\d+$`)
	// 带字母前缀的学号：A20240301
	reAlphaNo = regexp.MustCompile(`^[A-Za-z]\d{5,12}$`)
)

// nameStopWords 是"看着像姓名其实是标签/字段值"的黑名单。
// 旧实现是硬编码 8 个词的比较链，这里换成集合。
var nameStopWords = map[string]bool{
	"班级": true, "宿舍": true, "楼栋": true, "寝室": true, "姓名": true,
	"高一": true, "高二": true, "高三": true, "年级": true, "学号": true,
	"性别": true, "男": true, "女": true, "电话": true, "手机号": true,
	"床位": true, "床号": true, "备注": true, "编号": true, "房间": true,
	"班主任": true, "辅导员": true, "宿管": true, "组别": true, "部门": true,
}

// fillFromFeatures 用 token 分类补齐尚未取到的字段。
//
// 关键取舍：不再对整行做 strings.Replace 抹掉已知值（旧实现会抹错位置，
// 寝室号"301"可能正好命中姓名里的数字），而是逐 token 判定、一个 token 只归一个字段。
func fillFromFeatures(rec *Record, issues *[]Issue, source string, free func(Field) bool) {
	line := ToHalfWidth(source)
	tokens := strings.FieldsFunc(line, func(r rune) bool {
		return r == '\t' || r == ',' || r == ';' || r == '|' || r == '　' || unicode.IsSpace(r)
	})

	numericSlots := []string{}
	for _, tk := range tokens {
		t := strings.TrimSpace(tk)
		if t == "" {
			continue
		}
		switch {
		case free(FieldGender) && rec.Gender == "" && isGenderToken(t):
			rec.Gender = NormalizeGender(t)
		case free(FieldPhone) && rec.Phone == "" && reDigits.MatchString(compactDigits(t)) && isPhone(compactDigits(t)):
			rec.Phone = NormalizePhone(t)
		case free(FieldBedNumber) && rec.BedNumber == "" && reBed.MatchString(t):
			rec.BedNumber = NormalizeBed(t)
		case free(FieldClassName) && rec.ClassName == "" && reClass.MatchString(t):
			rec.ClassName = NormalizeClassName(t)
		case free(FieldBuilding) && rec.Building == "" && reBuilding.MatchString(t):
			rec.Building = NormalizeBuilding(t)
		case free(FieldRoomNumber) && rec.RoomNumber == "" && reRoomWithSuffix.MatchString(t):
			rec.RoomNumber = NormalizeRoom(t)
		case free(FieldRoomNumber) && rec.RoomNumber == "" && reRoomDash.MatchString(t):
			rec.RoomNumber = NormalizeRoom(t)
		case reDigits.MatchString(t):
			numericSlots = append(numericSlots, t)
		case free(FieldRealName) && rec.RealName == "" && reName.MatchString(t) && !nameStopWords[t] && !containsAny(t, "楼", "栋", "室", "班", "年级", "床", "寝"):
			rec.RealName = t
		case free(FieldStudentNo) && rec.StudentNo == "" && reAlphaNo.MatchString(t):
			rec.StudentNo = strings.ToUpper(t)
		}
	}

	// 剩下的纯数字 token：短的当寝室，长的当学号。
	// 4 位数两边都像，取不到唯一结论时按"先见为寝室"处理并如实报歧义；
	// 学号则要求至少 6 位，否则楼层、班级序号这类短数字会被误捡成学号。
	for _, n := range numericSlots {
		switch {
		case free(FieldRoomNumber) && rec.RoomNumber == "" && len(n) <= 4:
			rec.RoomNumber = n
			if len(n) == 4 {
				*issues = append(*issues, Issue{
					LineNo: rec.LineNo, Code: CodeAmbiguous, Field: FieldRoomNumber,
					Message: "数字 " + n + " 既可能是寝室号也可能是学号，已按寝室处理，请核对",
					Text:    clip(rec.SourceLine, 80),
				})
			}
		case free(FieldStudentNo) && rec.StudentNo == "" && len(n) >= 6:
			rec.StudentNo = NormalizeStudentNo(n)
		}
	}

	if rec.StudentNo != "" {
		rec.StudentNo = NormalizeStudentNo(rec.StudentNo)
	}
	if rec.Phone != "" && !isPhone(compactDigits(rec.Phone)) {
		*issues = append(*issues, Issue{
			LineNo: rec.LineNo, Code: CodeBadValue, Field: FieldPhone,
			Message: "联系电话 " + rec.Phone + " 不是 11 位手机号，已保留原值请人工核对",
			Text:    clip(rec.SourceLine, 80),
		})
	}
}

func isGenderToken(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "男", "女", "m", "f", "男性", "女性", "male", "female":
		return true
	}
	return false
}

func compactDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isPhone(digits string) bool {
	if len(digits) != 11 || !strings.HasPrefix(digits, "1") {
		return false
	}
	return digits[1] >= '3' && digits[1] <= '9'
}

// ---------------------------------------------------------------------------
// 字段规范化
// ---------------------------------------------------------------------------

// ToHalfWidth 全角转半角并压掉全角空格。
// 学校表里"高一（２）班"和"1号楼"的全角写法非常常见，不归一会直接认不出列。
func ToHalfWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u3000':
			b.WriteRune(' ')
		case r >= '\uFF01' && r <= '\uFF5E':
			b.WriteRune(r - 0xFEE0)
		case r == '\uFFE5':
			b.WriteRune('"')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeStudentNo 保住前导零。
// Excel 会把 "0012" 显示成 12，导出的 CSV 里前导零已经丢了；
// 这里能做的只有不再雪上加霜（去空格、去科学计数法尾巴），并如实说明。
func NormalizeStudentNo(s string) string {
	s = strings.TrimSpace(ToHalfWidth(s))
	s = strings.ReplaceAll(s, " ", "")
	if e := strings.IndexAny(s, "eE"); e > 0 && reDigits.MatchString(s[:e]) {
		// 1.2340301E+07 这类科学计数法已经丢精度，还原无望，原样保留交给诊断
		return s
	}
	return strings.ToUpper(s)
}

// NormalizePhone 去掉分隔符并剥掉 +86 前缀。
func NormalizePhone(s string) string {
	s = strings.TrimSpace(ToHalfWidth(s))
	digits := compactDigits(s)
	for strings.HasPrefix(digits, "86") && len(digits) > 11 {
		digits = digits[2:]
	}
	return digits
}

// NormalizeBed 统一成数字床位号。
func NormalizeBed(s string) string {
	m := reBed.FindStringSubmatch(strings.TrimSpace(ToHalfWidth(s)))
	if m == nil {
		return ""
	}
	n := m[1]
	if n == "" {
		n = m[2]
	}
	if v, err := strconv.Atoi(n); err == nil && v > 0 {
		return strconv.Itoa(v)
	}
	return ""
}

// NormalizeRoom 剥掉室/房后缀，保留 4-201 这类带楼前缀的写法。
func NormalizeRoom(s string) string {
	s = strings.TrimSpace(ToHalfWidth(s))
	if m := reRoomWithSuffix.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := reRoomDash.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return strings.TrimRight(strings.TrimSuffix(strings.TrimSuffix(s, "室"), "房"), "-")
}

// NormalizeBuilding 归一楼栋写法：西12号楼 / 12栋 / 12号楼 → 12号楼。
// 登录侧与打表侧本来就有各自的楼栋口径，导入再不归一，同一个物理楼会在库里裂成好几个。
func NormalizeBuilding(s string) string {
	s = strings.TrimSpace(ToHalfWidth(s))
	if s == "" {
		return ""
	}
	if m := reBuilding.FindString(s); m != "" {
		s = m
	}
	digits := compactDigits(s)
	prefix := ""
	for _, p := range []string{"东", "西", "南", "北"} {
		if strings.Contains(s, p) {
			prefix = p
			break
		}
	}
	if digits != "" {
		return prefix + digits + "号楼"
	}
	// A栋 / B2栋 这类字母编号
	for _, r := range s {
		if unicode.IsLetter(r) {
			return strings.ToUpper(string(r)) + "号楼"
		}
	}
	return s
}

// NormalizeClassName 统一成 "高一(2)班" 形态。
func NormalizeClassName(c string) string {
	c = ToHalfWidth(strings.TrimSpace(c))
	c = strings.ReplaceAll(c, " ", "")
	c = strings.ReplaceAll(c, "（", "(")
	c = strings.ReplaceAll(c, "）", ")")
	if m := reClass.FindStringSubmatch(c); m != nil {
		grade := normalizeGradeToken(m[1])
		num := strings.TrimLeft(m[2], "0")
		if num == "" {
			num = "0"
		}
		if grade != "" {
			return grade + "(" + num + ")班"
		}
	}
	if !strings.HasSuffix(c, "班") {
		c = c + "班"
	}
	return c
}

func normalizeGradeToken(g string) string {
	switch g {
	case "高1", "高一":
		return "高一"
	case "高2", "高二":
		return "高二"
	case "高3", "高三":
		return "高三"
	}
	return ""
}

// GradeFromClass 从班级推年级；推不出来就留空，不再默认"高一"。
func GradeFromClass(className string) string {
	c := ToHalfWidth(className)
	switch {
	case strings.Contains(c, "高一") || strings.Contains(c, "高1"):
		return "高一"
	case strings.Contains(c, "高二") || strings.Contains(c, "高2"):
		return "高二"
	case strings.Contains(c, "高三") || strings.Contains(c, "高3"):
		return "高三"
	}
	if m := reClass.FindStringSubmatch(c); m != nil {
		return normalizeGradeToken(m[1])
	}
	return ""
}

// NormalizeGender 统一成 男/女。
func NormalizeGender(s string) string {
	switch strings.ToLower(strings.TrimSpace(ToHalfWidth(s))) {
	case "男", "男性", "m", "male", "1":
		return "男"
	case "女", "女性", "f", "female", "2":
		return "女"
	}
	return ""
}

// ---------------------------------------------------------------------------
// 重复检测与工具
// ---------------------------------------------------------------------------

// detectDuplicates 只报重复、不自动删：
// 同一个人被导两次是常见事故，但哪一条才是对的，只有导入者知道。
func detectDuplicates(records []Record) []Issue {
	byNo := map[string]int{}
	byPerson := map[string]int{}
	var out []Issue

	for _, r := range records {
		if r.Blocked() {
			continue
		}
		if r.StudentNo != "" {
			if first, ok := byNo[r.StudentNo]; ok {
				out = append(out, Issue{
					LineNo: r.LineNo, Code: CodeDuplicate, Field: FieldStudentNo,
					Message: "学号 " + r.StudentNo + " 与第 " + strconv.Itoa(first) + " 行重复",
					Text:    clip(r.SourceLine, 80),
				})
			} else {
				byNo[r.StudentNo] = r.LineNo
			}
		}
		key := strings.Join([]string{r.RealName, r.ClassName, r.Building, r.RoomNumber, r.BedNumber}, "|")
		if first, ok := byPerson[key]; ok {
			out = append(out, Issue{
				LineNo: r.LineNo, Code: CodeDuplicate, Field: FieldRealName,
				Message: "与第 " + strconv.Itoa(first) + " 行是同一个人（姓名·班级·楼栋·寝室·床位完全一致）",
				Text:    clip(r.SourceLine, 80),
			})
		} else {
			byPerson[key] = r.LineNo
		}
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
