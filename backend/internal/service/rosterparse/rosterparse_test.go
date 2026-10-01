package rosterparse

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// 这一组测试钉的是"解析口径"，不是代码路径：
// 每一条都对应旧实现里一个会静默出错的具体场景。

func parseText(t *testing.T, s string) *Result {
	t.Helper()
	res, err := Parse([]byte(s), Options{})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return res
}

func findIssue(res *Result, code string, lineNo int) *Issue {
	for i := range res.Issues {
		if res.Issues[i].Code == code && res.Issues[i].LineNo == lineNo {
			return &res.Issues[i]
		}
	}
	return nil
}

func mustRecord(t *testing.T, res *Result, lineNo int) Record {
	t.Helper()
	for _, r := range res.Records {
		if r.LineNo == lineNo {
			return r
		}
	}
	t.Fatalf("没有解析出第 %d 行，实际记录 %+v", lineNo, res.Records)
	return Record{}
}

// 编码：GBK / BOM / UTF-16 都要能认，认不出要如实报错
func TestDecodeHandlesChineseEncodings(t *testing.T) {
	src := "楼栋\t寝室\t姓名\t班级\n1号楼\t301\t李华\t高一(1)班\n"

	t.Run("GBK", func(t *testing.T) {
		gbk, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(src))
		if err != nil {
			t.Fatalf("构造 GBK 样本失败: %v", err)
		}
		res, err := Parse(gbk, Options{})
		if err != nil {
			t.Fatalf("GBK 名单应能解析: %v", err)
		}
		if res.Stats.Recognized != 1 {
			t.Fatalf("GBK 应识别 1 人，实际 %d", res.Stats.Recognized)
		}
		if findIssue(res, CodeEncoding, 0) == nil {
			t.Fatal("应给出 GBK 转码诊断，让导入者知道发生过什么")
		}
	})

	t.Run("UTF8BOM", func(t *testing.T) {
		res, err := Parse(append([]byte("\xEF\xBB\xBF"), []byte(src)...), Options{})
		if err != nil {
			t.Fatalf("带 BOM 应能解析: %v", err)
		}
		if res.Stats.Recognized != 1 {
			t.Fatalf("BOM 应识别 1 人，实际 %d", res.Stats.Recognized)
		}
		r := mustRecord(t, res, 2)
		if !strings.HasPrefix(r.RealName, "李华") {
			t.Fatalf("BOM 泄漏进姓名: %q", r.RealName)
		}
	})

	t.Run("二进制文件", func(t *testing.T) {
		// .xlsx 本质是 zip：PK 头 + NUL + 大量控制字符。
		// 随机高位字节会被 GBK 解成"合法乱码"，测不出防护，所以用真实形态。
		zipLike := append([]byte("PK\x03\x04\x14\x00\x06\x00\x00\x00\x00\x00"),
			[]byte("\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")...)
		_, err := Parse(zipLike, Options{})
		if err == nil {
			t.Fatal("二进制文件必须报错，不能当成名单解析")
		}
		if !strings.Contains(err.Error(), ".xlsx") || !strings.Contains(err.Error(), ".csv") {
			t.Fatalf("错误要说明支持哪些格式，实际 %q", err.Error())
		}
	})
}

// 表头：支持前置标题行、TSV、列别名；认不出姓名列就不当表头
func TestHeaderDetection(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		wantLine int
	}{
		{
			name:     "首行即表头",
			text:     "楼栋\t寝室\t姓名\t班级\n1号楼\t301\t李华\t高一(1)班\n",
			wantLine: 1,
		},
		{
			name:     "标题行在前",
			text:     "明德中学 2025 级住宿名单\n学号\t姓名\t年级\t班级\t楼栋\t房间号\t床位\n2025030101\t李华\t高一\t高一(2)班\t1号楼\t302\t3\n",
			wantLine: 2,
		},
		{
			name:     "逗号与性别电话列",
			text:     "姓名,班级,楼栋,寝室,性别,手机号\n王芳,高二(3)班,2号楼,405,女,13800000002\n",
			wantLine: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := parseText(t, tc.text)
			if res.Header == nil {
				t.Fatalf("应识别出表头，实际走的是特征识别：%+v", res.Issues)
			}
			if res.Header.LineNo != tc.wantLine {
				t.Fatalf("表头行号应为 %d，实际 %d", tc.wantLine, res.Header.LineNo)
			}
			if res.Stats.Recognized != 1 {
				t.Fatalf("应识别 1 人，实际 %d", res.Stats.Recognized)
			}
			r := mustRecord(t, res, tc.wantLine+1)
			if r.Blocked() {
				t.Fatalf("完整行不应缺字段，实际缺 %v", r.Missing)
			}
		})
	}

	t.Run("单行数据不得被当成表头", func(t *testing.T) {
		res := parseText(t, "8栋\t305\t张三\t高三(5)班\n")
		if res.Header != nil {
			t.Fatal("含数字的单元格是数据，这行被误判成表头会导致整批归零")
		}
		if res.Stats.Recognized != 1 {
			t.Fatalf("应识别 1 人，实际 %d", res.Stats.Recognized)
		}
	})
}

// 特征识别：无表头的空格分隔名单，含床位/性别/学号
func TestFeatureExtractionSample(t *testing.T) {
	text := strings.Join([]string{
		"1号楼 301 李华 高一(1)班 男 1号床",
		"2号楼 405 陈天豪 高二(3)班 男 1号床 20240301",
		"西区12栋 501 孙晓峰 高三(1)班 男 1号床 20230101",
	}, "\n")

	res := parseText(t, text)
	if res.Stats.Recognized != 3 {
		t.Fatalf("应识别 3 人，实际 %d / issues=%+v", res.Stats.Recognized, res.Issues)
	}

	r1 := mustRecord(t, res, 1)
	if r1.Building != "1号楼" || r1.RoomNumber != "301" || r1.RealName != "李华" ||
		r1.ClassName != "高一(1)班" || r1.Gender != "男" || r1.BedNumber != "1" {
		t.Fatalf("第 1 行解析结果不符: %+v", r1)
	}
	if r1.Grade != "高一" {
		t.Fatalf("年级应由班级换算得到，实际 %q", r1.Grade)
	}

	r2 := mustRecord(t, res, 2)
	if r2.StudentNo != "20240301" || r2.RoomNumber != "405" {
		t.Fatalf("长数字应归学号、短数字归寝室: %+v", r2)
	}

	r3 := mustRecord(t, res, 3)
	if r3.Building != "西12号楼" {
		t.Fatalf("西区12栋应归一为 西12号楼，实际 %q", r3.Building)
	}
}

// 不再编造默认值：缺必填就标红待补、不入库
func TestMissingRequiredBlocksInsteadOfFabricating(t *testing.T) {
	res := parseText(t, "301 李华\n")
	r := mustRecord(t, res, 1)
	if !r.Blocked() {
		t.Fatalf("缺楼栋与班级的行必须被拦下，实际记录 %+v", r)
	}
	if r.Building == "1号楼" || r.ClassName == "高一(1)班" {
		t.Fatalf("不得再编造默认值: %+v", r)
	}
	if findIssue(res, CodeMissingField, 1) == nil {
		t.Fatal("必须给出逐行缺字段诊断")
	}
	if res.Stats.Blocked != 1 || res.Stats.Recognized != 0 {
		t.Fatalf("统计应记为待补 1 / 可入库 0，实际 %+v", res.Stats)
	}
}

// 4 位数歧义：既像寝室又像学号，按寝室处理但必须报出来
func TestAmbiguousFourDigitToken(t *testing.T) {
	res := parseText(t, "1号楼 2305 李华 高一(1)班\n")
	if findIssue(res, CodeAmbiguous, 1) == nil {
		t.Fatalf("4 位数字歧义必须报 ambiguous 诊断，实际 %+v", res.Issues)
	}
}

// 手机号归一与非法值
func TestPhoneNormalization(t *testing.T) {
	if got := NormalizePhone(" +86 138-0000-0002 "); got != "13800000002" {
		t.Fatalf("手机号归一失败: %q", got)
	}
	res := parseText(t, "姓名,班级,楼栋,寝室,手机号\n王芳,高二(3)班,2号楼,405,12345\n")
	r := mustRecord(t, res, 2)
	if r.Phone != "12345" {
		t.Fatalf("原值要保留供人工核对，实际 %q", r.Phone)
	}
	if findIssue(res, CodeBadValue, 2) == nil {
		t.Fatalf("非法手机号必须报 bad_value，实际 %+v", res.Issues)
	}
}

// 引号包裹的 CSV 单元格不能把列切错
func TestQuotedCSVCells(t *testing.T) {
	res := parseText(t, "姓名,班级,楼栋,寝室\n李二,高一(1)班,\"1号楼,东区\",301\n")
	r := mustRecord(t, res, 2)
	if r.RealName != "李二" || r.RoomNumber != "301" {
		t.Fatalf("带引号逗号的单元格导致错列: %+v", r)
	}
}

// 重复检测：只报不删
func TestDuplicateDetection(t *testing.T) {
	text := "楼栋\t寝室\t姓名\t班级\n1号楼\t301\t李华\t高一(1)班\n1号楼\t301\t李华\t高一(1)班\n"
	res := parseText(t, text)
	if findIssue(res, CodeDuplicate, 3) == nil {
		t.Fatalf("同人重复必须报 duplicate，实际 %+v", res.Issues)
	}
	if res.Stats.Recognized != 2 {
		t.Fatalf("重复只提示不自动删，实际 %+v", res.Stats)
	}
}

// 全角与各种脏写法
func TestFieldNormalizers(t *testing.T) {
	if got := ToHalfWidth("高一（２）班"); got != "高一(2)班" {
		t.Fatalf("全角归一失败: %q", got)
	}
	if got := NormalizeClassName("高 一 （2） 班"); got != "高一(2)班" {
		t.Fatalf("班级归一失败: %q", got)
	}
	if got := NormalizeClassName("高三3班"); got != "高三(3)班" {
		t.Fatalf("无括号班级归一失败: %q", got)
	}
	if got := NormalizeBed("床12"); got != "12" {
		t.Fatalf("床位归一失败: %q", got)
	}
	if got := NormalizeRoom("1004室"); got != "1004" {
		t.Fatalf("寝室归一失败: %q", got)
	}
	if got := NormalizeRoom("4-201"); got != "4-201" {
		t.Fatalf("带楼前缀的寝室号应保持原样: %q", got)
	}
	if got := NormalizeBuilding("7栋"); got != "7号楼" {
		t.Fatalf("楼栋归一失败: %q", got)
	}
	if got := NormalizeGender("F"); got != "女" {
		t.Fatalf("性别归一失败: %q", got)
	}
	// Excel 把 0012 显示成 12 之后前导零已经不在数据里，这里保证不再二次破坏
	if got := NormalizeStudentNo(" 2024 0301 "); got != "20240301" {
		t.Fatalf("学号归一失败: %q", got)
	}
	if got := GradeFromClass("高二(8)班"); got != "高二" {
		t.Fatalf("年级换算失败: %q", got)
	}
	if got := GradeFromClass("2024级3班"); got != "" {
		t.Fatalf("推不出年级时必须留空而不是默认高一，实际 %q", got)
	}
}

// 少数民族姓名的间隔号不能被当成两个名字
func TestMiddotName(t *testing.T) {
	res := parseText(t, "1号楼 301 阿依·努尔 高一(1)班 女\n")
	r := mustRecord(t, res, 1)
	if r.RealName != "阿依·努尔" {
		t.Fatalf("含间隔号的姓名解析失败: %+v", r)
	}
}

// 注释行与空行不参与统计
func TestIgnoresBlankAndCommentLines(t *testing.T) {
	res := parseText(t, "# 说明：以下为高一名单\n\n1号楼 301 李华 高一(1)班\n\n")
	if res.Stats.Recognized != 1 {
		t.Fatalf("应只识别 1 人，实际 %+v", res.Stats)
	}
	if res.Stats.Skipped != 0 {
		t.Fatalf("空行与注释行不该算跳过行，实际 %+v", res.Stats)
	}
}

// 完全读不出内容的行要单独报 skipped，而不是静默消失
func TestUnparseableLineReportsSkipped(t *testing.T) {
	res := parseText(t, "…………\n1号楼 301 李华 高一(1)班\n")
	if res.Stats.Skipped != 1 {
		t.Fatalf("无有效字段的行应记为跳过，实际 %+v", res.Stats)
	}
	if findIssue(res, CodeSkipped, 1) == nil {
		t.Fatalf("必须报 skipped 诊断，实际 %+v", res.Issues)
	}
}

// 只写了名字的行不能像旧实现那样静默丢掉：它算记录，但缺字段必须逐行标出来
func TestNameOnlyLineIsBlockedNotDropped(t *testing.T) {
	res := parseText(t, "李华\n")
	r := mustRecord(t, res, 1)
	if !r.Blocked() {
		t.Fatalf("只有姓名的行必须被标为待补，实际 %+v", r)
	}
	if res.Stats.Skipped != 0 {
		t.Fatalf("这一行不该被当成无效行丢掉，实际 %+v", res.Stats)
	}
}

// 强制分隔符要覆盖自动推断
func TestForcedSeparator(t *testing.T) {
	res, err := Parse([]byte("姓名|班级|楼栋|寝室\n李华|高一(1)班|1号楼|301\n"), Options{Separator: "|"})
	if err != nil {
		t.Fatalf("强制分隔符解析失败: %v", err)
	}
	if res.Header == nil || res.Header.Separator != "|" {
		t.Fatalf("应按竖线识别表头，实际 %+v", res.Header)
	}
	if res.Stats.Recognized != 1 {
		t.Fatalf("应识别 1 人，实际 %+v", res.Stats)
	}
}

// 真实学校表：年级写的是毕业年份、班级只有序号、姓名脱敏、还带身份证号列。
func TestRealSchoolRosterFormat(t *testing.T) {
	const sample = "年级\t班级\t姓名\t身份证号\t性别\t楼栋\t楼层\t宿舍号\t床位号\t备注\n" +
		"2027\t1\t杨*健\t41***90151\t男\tN\t1\tN101\t\t"

	res, err := Parse([]byte(sample), Options{Now: time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Header == nil {
		t.Fatal("应识别出表头")
	}
	r := mustRecord(t, res, 2)

	if r.Grade != "高三" {
		t.Fatalf("2027 毕业在 2026 学年应是高三，实际 %q", r.Grade)
	}
	if r.ClassName != "高三(1)班" {
		t.Fatalf("班级序号要与年级合成完整班名，实际 %q", r.ClassName)
	}
	if r.Building != "N号楼" {
		t.Fatalf("楼栋 N 应归一为 N号楼，实际 %q", r.Building)
	}
	if r.RoomNumber != "101" {
		t.Fatalf("宿舍号 N101 应剥掉与楼栋重复的前缀，实际 %q", r.RoomNumber)
	}
	// 表里床位号是空的，"1" 是楼层列——两者都不该被拿去填别的字段
	if r.BedNumber != "" || r.Gender != "男" {
		t.Fatalf("床位应为空、性别应为男: %+v", r)
	}
	if r.Blocked() {
		t.Fatalf("该行字段齐全，不该被拦下，实际缺 %v", r.Missing)
	}

	// 身份证号：识别到、明确不入库，也不能被塞进学号字段；楼层的短数字同样不该当学号
	if r.StudentNo != "" {
		t.Fatalf("学号应为空，实际 %q", r.StudentNo)
	}
	var sensitive *Issue
	for i := range res.Issues {
		if res.Issues[i].Code == CodeSensitive {
			sensitive = &res.Issues[i]
		}
	}
	if sensitive == nil || !strings.Contains(sensitive.Message, "身份证号") {
		t.Fatalf("必须报敏感列诊断，实际 %+v", res.Issues)
	}

	// 换算与脱敏都要留痕
	if findIssue(res, CodeDerived, 2) == nil {
		t.Fatalf("毕业年份换算必须记 derived 诊断，实际 %+v", res.Issues)
	}
	if findIssue(res, CodeBadValue, 2) == nil {
		t.Fatalf("脱敏姓名必须报 bad_value，实际 %+v", res.Issues)
	}
}

// 毕业年份换算的边界：已毕业与未来入学都要报，不能猜一个年级
func TestGraduationYearBoundary(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	cases := map[string]struct {
		year int
		want string
		ok   bool
	}{
		"高三": {2027, "高三", true},
		"高二": {2028, "高二", true},
		"高一": {2029, "高一", true},
	}
	for name, tc := range cases {
		got, ok := GradeFromGraduationYear(tc.year, now)
		if !ok || got != tc.want {
			t.Fatalf("%s: %d 毕业应换算为 %s，实际 %q/%v", name, tc.year, tc.want, got, ok)
		}
	}
	// 2026 毕业的人在这个时点已经不在校
	if _, ok := GradeFromGraduationYear(2026, now); ok {
		t.Fatal("已毕业年份不得换算成在读年级")
	}
	// 春季学期（9 月前）仍算上一学年
	spring := time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local)
	if got, _ := GradeFromGraduationYear(2027, spring); got != "高二" {
		t.Fatalf("2026 年 4 月时 2027 毕业应是高二，实际 %q", got)
	}
}

// 人工指定列映射：自动识别救不回的表，由使用者直接指定列序。
func TestManualColumnMapOverridesAuto(t *testing.T) {
	const sample = "列1\t列2\t列3\t列4\nN\tG1\t杨*健\t101\n"

	// 先确认自动识别确实救不回：列名带数字不认表头，楼栋"N"与班级"G1"也没有可识别特征
	auto := parseText(t, sample)
	if auto.Header != nil {
		t.Skip("自动识别已能认出这张表，该用例失去意义")
	}
	if r := auto.Records[0]; r.RealName != "" || !r.Blocked() {
		t.Fatalf("自动识别本应拿不到姓名: %+v", r)
	}

	res, err := Parse([]byte(sample), Options{
		ColumnMap:  map[Field]int{FieldBuilding: 0, FieldClassName: 1, FieldRealName: 2, FieldRoomNumber: 3},
		HeaderLine: 1,
	})
	if err != nil {
		t.Fatalf("人工映射解析失败: %v", err)
	}
	if res.Header == nil {
		t.Fatal("人工映射应生效")
	}
	r := mustRecord(t, res, 2)
	if r.RealName != "杨*健" || r.Building != "N号楼" || r.RoomNumber != "101" {
		t.Fatalf("人工映射取值异常: %+v", r)
	}
	if r.Blocked() {
		t.Fatalf("列已指定清楚，不该再判缺字段: %v", r.Missing)
	}
	// 表头行本身不能被当成一条数据
	if res.Stats.Recognized != 1 {
		t.Fatalf("应只有 1 条数据，实际 %+v", res.Stats)
	}
}

// 没有表头的表也可以人工指定列序（HeaderLine=0），此时不跳过任何行。
func TestManualMapWithoutHeaderLine(t *testing.T) {
	res, err := Parse([]byte("N\t101\t杨健\nM\t202\t李丽\n"), Options{
		ColumnMap:  map[Field]int{FieldBuilding: 0, FieldRoomNumber: 1, FieldRealName: 2},
		HeaderLine: 0,
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Stats.DataRows != 2 {
		t.Fatalf("两行都该是数据，实际 %+v", res.Stats)
	}
	if res.Records[0].Building != "N号楼" || res.Records[1].RealName != "李丽" {
		t.Fatalf("逐行取值异常: %+v", res.Records)
	}
}

// 身份证号列如果没脱敏就是 18 位纯数字，绝不能被数字回退捡去当学号。
func TestUnmaskedIDNeverBecomesStudentNo(t *testing.T) {
	const sample = "年级\t班级\t姓名\t身份证号\t性别\t楼栋\t宿舍号\n" +
		"2027\t1\t杨健\t410123200801011234\t男\tN\tN101\n"

	res, err := Parse([]byte(sample), Options{Now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	r := mustRecord(t, res, 2)
	if r.StudentNo != "" {
		t.Fatalf("身份证号被当成学号入库了: %q", r.StudentNo)
	}
	if r.Phone != "" {
		t.Fatalf("身份证号被当成电话入库了: %q", r.Phone)
	}
	if r.RealName != "杨健" || r.RoomNumber != "101" {
		t.Fatalf("正常字段受影响: %+v", r)
	}
	var found bool
	for _, is := range res.Issues {
		if is.Code == CodeSensitive {
			found = true
		}
	}
	if !found {
		t.Fatalf("必须报敏感列诊断，实际 %+v", res.Issues)
	}
}

// 表头指纹是"记住列映射"的键：换数据不换键，换列序要换键。
func TestHeaderFingerprintStability(t *testing.T) {
	base := HeaderFingerprint([]string{"年级", "班级", "姓名", "宿舍号"})
	if base == "" {
		t.Fatal("指纹不应为空")
	}
	if got := HeaderFingerprint([]string{"年级", "班级", "姓名", "宿舍号", "", "  "}); got != base {
		t.Fatal("尾部空列不该改变指纹")
	}
	if got := HeaderFingerprint([]string{"年级", "班级", "姓名"}); got == base {
		t.Fatal("少一列必须改变指纹，否则复用时会整体错位")
	}
	if got := HeaderFingerprint([]string{"年级", "姓名", "班级", "宿舍号"}); got == base {
		t.Fatal("列序变化必须改变指纹")
	}
	if got := HeaderFingerprint([]string{"年级", "班级", "姓名（必填）", "宿舍号"}); got == base {
		t.Fatal("列名内容变化必须改变指纹")
	}
	full := HeaderFingerprint([]string{"年级", "班级", "姓名（必填）", "宿舍号"})
	half := HeaderFingerprint([]string{"年级", "班级", "姓名(必填)", "宿舍号"})
	if full != half {
		t.Fatalf("全角括号与半角应算同一列名，实际 %q vs %q", full, half)
	}
	if HeaderFingerprint(nil) != "" || HeaderFingerprint([]string{"", ""}) != "" {
		t.Fatal("空表头不应产生指纹")
	}
}

// 前端「填入示例数据」按钮给的就是这份混合样本：前 10 行空格分隔、末尾一段带表头的 TSV。
// 界面自带的示例解析不出来，是用户最先会撞上的缺陷。
func TestUISampleParsesCompletely(t *testing.T) {
	sample := "1号楼 301 李华 高一(1)班 男 1号床\n" +
		"1号楼 301 张明 高一(1)班 男 2号床\n" +
		"1号楼 302 王芳 高一(2)班 女 1号床\n" +
		"1号楼 302 赵小雨 高一(2)班 女 2号床\n" +
		"2号楼 405 陈天豪 高二(3)班 男 1号床 20240301\n" +
		"2号楼 405 周正 高二(3)班 男 2号床 20240302\n" +
		"2号楼 406 林浩然 高二(4)班 男 1号床 20240401\n" +
		"西区12栋 501 孙晓峰 高三(1)班 男 1号床 20230101\n" +
		"西区12栋 501 黄俊杰 高三(1)班 男 2号床 20230102\n" +
		"西区12栋 502 刘若涵 高三(5)班 女 1号床 20230501\n" +
		"楼栋\t寝室\t姓名\t班级\n" +
		"1号楼\t204\t钱宇航\t高一(5)班\n" +
		"2号楼\t306\t吴桐\t高二(8)班\n" +
		"西区12栋\t608\t郑文博\t高三(9)班"

	res := parseText(t, sample)
	if res.Stats.Recognized != 13 {
		t.Fatalf("示例 13 条数据应全部可入库，实际 %+v / issues=%+v", res.Stats, res.Issues)
	}
	if res.Stats.Blocked != 0 || res.Stats.Skipped != 0 {
		t.Fatalf("示例数据不应产生待补或跳过，实际 %+v", res.Stats)
	}
	if res.Stats.GradeCounts["高一"] != 5 || res.Stats.GradeCounts["高二"] != 4 || res.Stats.GradeCounts["高三"] != 4 {
		t.Fatalf("年级分布应为 5/4/4，实际 %+v", res.Stats.GradeCounts)
	}
	// 第 11 行是中途出现的表头，必须安静跳过而不是报成无效行
	if findIssue(res, CodeHeaderNote, 11) == nil {
		t.Fatalf("中途表头应记为 header 说明，实际 %+v", res.Issues)
	}

	// 同寝室同床位才是重复；示例里 1 号楼 301 的两个人床位不同，不该被误判
	for _, is := range res.Issues {
		if is.Code == CodeDuplicate {
			t.Fatalf("示例数据里不该有重复： %+v", is)
		}
	}
}

// 表尾"合计 6"不是人：按学生行处理会挂出一条"缺班级缺楼栋"的记录，看起来像名单写坏了。
func TestSummaryRowIsSkippedNotImported(t *testing.T) {
	const sample = "姓名\t班级\t楼栋\t宿舍号\t床位号\n" +
		"张三\t高一(2)班\t1号楼\t302\t5\n" +
		"合计\t\t\t\t6\n"

	res := parseText(t, sample)
	if res.Stats.Recognized != 1 || res.Stats.Blocked != 0 {
		t.Fatalf("合计行不该进统计，实际 %+v", res.Stats)
	}
	if findIssue(res, CodeSummaryRow, 3) == nil {
		t.Fatalf("要说明合计行已跳过: %+v", res.Issues)
	}
}

// 备注里出现"合计"两字的真实学生行不能被跳过。
func TestSummaryWordInRemarkDoesNotSkipStudent(t *testing.T) {
	const sample = "姓名\t班级\t楼栋\t宿舍号\t床位号\t备注\n" +
		"张三\t高一(2)班\t1号楼\t302\t5\t已合计过\n"

	res := parseText(t, sample)
	if res.Stats.Recognized != 1 {
		t.Fatalf("真实学生行应照常识别，实际 %+v / %+v", res.Stats, res.Issues)
	}
}
