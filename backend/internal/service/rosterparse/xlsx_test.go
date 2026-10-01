package rosterparse

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

// xlsx 这一组钉的是"上传真表能不能进"，不是 excelize 的用法：
// 每份表都在内存里生成、再走一遍完整上传路径，改依赖或改分派逻辑时这些用例必须还成立。

var xlsxNow = time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local)

// writeSheet 按行写入一张新表，返回可直接上传的字节。
func writeSheet(t *testing.T, sheet string, rows [][]string, merges [][2]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()

	if sheet != "Sheet1" {
		idx, err := f.NewSheet(sheet)
		if err != nil {
			t.Fatalf("新建工作表失败: %v", err)
		}
		f.SetActiveSheet(idx)
		f.DeleteSheet("Sheet1")
	}
	for i, row := range rows {
		for j, cell := range row {
			axis, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatalf("单元格坐标算不出: %v", err)
			}
			if err := f.SetCellValue(sheet, axis, cell); err != nil {
				t.Fatalf("写入单元格失败: %v", err)
			}
		}
	}
	for _, m := range merges {
		if err := f.MergeCell(sheet, m[0], m[1]); err != nil {
			t.Fatalf("合并单元格失败: %v", err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("生成 xlsx 失败: %v", err)
	}
	return buf.Bytes()
}

func mustParseUpload(t *testing.T, raw []byte) *Result {
	t.Helper()
	res, err := ParseUpload(raw, Options{Now: xlsxNow})
	if err != nil {
		t.Fatalf("上传解析失败: %v", err)
	}
	return res
}

// 真表最常见的形态：封面页在前、名单在后，列里带身份证号。
func TestXLSXPicksRosterSheetNotFirstOne(t *testing.T) {
	raw := writeSheet(t, "住宿部总表", [][]string{
		{"年级", "班级", "姓名", "身份证号", "性别", "楼栋", "楼层", "宿舍号", "床位号", "备注"},
		{"2027", "1", "杨*健", "41***90151", "男", "N", "1", "N101", "", ""},
		{"2028", "3", "李雷", "41***90152", "男", "N", "2", "N205", "4", ""},
		{"2029", "2", "韩梅梅", "41***90153", "女", "A", "3", "A306", "2", ""},
	}, nil)

	res := mustParseUpload(t, raw)
	if res.Header == nil {
		t.Fatal("xlsx 应识别出表头")
	}
	if res.Stats.DataRows != 3 {
		t.Fatalf("应解析出 3 行数据，实际 %d", res.Stats.DataRows)
	}
	r := mustRecord(t, res, 2)
	if r.Grade != "高三" || r.ClassName != "高三(1)班" || r.Building != "N号楼" || r.RoomNumber != "101" {
		t.Fatalf("xlsx 行口径与文本不一致: %+v", r)
	}
	if findIssue(res, CodeSensitive, 1) == nil {
		t.Fatalf("敏感列诊断应保留: %+v", res.Issues)
	}
	if findIssue(res, CodeSheet, 0) == nil {
		t.Fatalf("应说明读取了哪张表: %+v", res.Issues)
	}
}

func TestXLSXMultiSheetUsesFattest(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	// 封面页只有两行说明，排在前面。
	for i, line := range []string{"学管会住宿名单", "本表仅作内部核对使用"} {
		axis, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetCellValue("Sheet1", axis, line); err != nil {
			t.Fatalf("写封面失败: %v", err)
		}
	}
	if _, err := f.NewSheet("名单"); err != nil {
		t.Fatalf("建名单表失败: %v", err)
	}
	rows := [][]string{
		{"姓名", "楼栋", "宿舍号", "床位号"},
		{"张三", "1号楼", "101", "1"},
		{"李四", "1号楼", "101", "2"},
		{"王五", "2号楼", "205", "3"},
	}
	for i, row := range rows {
		for j, cell := range row {
			axis, _ := excelize.CoordinatesToCellName(j+1, i+1)
			if err := f.SetCellValue("名单", axis, cell); err != nil {
				t.Fatalf("写名单失败: %v", err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("生成多表 xlsx 失败: %v", err)
	}

	res := mustParseUpload(t, buf.Bytes())
	if res.Stats.DataRows != 3 {
		t.Fatalf("应读内容最多的那张表（3 行），实际 %d 行", res.Stats.DataRows)
	}
	note := findIssue(res, CodeSheet, 0)
	if note == nil || !strings.Contains(note.Message, "名单") {
		t.Fatalf("多表时要说清读了哪张: %+v", res.Issues)
	}
}

// 合并单元格只把值存在左上角：不摊平的话第 2 行起班级全空，整批被拦下。
func TestXLSXMergedClassCellFillsDown(t *testing.T) {
	raw := writeSheet(t, "住宿部总表", [][]string{
		{"姓名", "年级", "班级", "楼栋", "宿舍号", "床位号"},
		{"杨*健", "2027", "1", "N", "N101", "1"},
		{"李雷", "", "", "N", "N102", "1"},
		{"韩梅梅", "", "", "N", "N103", "1"},
	}, [][2]string{{"B2", "B4"}, {"C2", "C4"}})

	res := mustParseUpload(t, raw)
	if res.Stats.Blocked != 0 {
		t.Fatalf("合并列摊平后不该有行被拦下，实际拦了 %d 行", res.Stats.Blocked)
	}
	for _, ln := range []int{2, 3, 4} {
		r := mustRecord(t, res, ln)
		if r.Grade != "高三" || r.ClassName != "高三(1)班" {
			t.Fatalf("第 %d 行没拿到合并区域的班级: %+v", ln, r)
		}
	}
}

// 长数字（学号 12 位、手机号 11 位）必须保持原样，不能变成科学计数法。
func TestXLSXKeepsLongDigits(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	headers := []string{"姓名", "楼栋", "宿舍号", "床位号", "学号", "联系电话"}
	data := []string{"赵六", "1号楼", "302", "5", "202601010203", "13800138000"}
	for j, h := range headers {
		axis, _ := excelize.CoordinatesToCellName(j+1, 1)
		if err := f.SetCellValue("Sheet1", axis, h); err != nil {
			t.Fatalf("写表头失败: %v", err)
		}
	}
	for j, v := range data {
		axis, _ := excelize.CoordinatesToCellName(j+1, 2)
		// 学号与电话写成数值单元格，模拟 Excel 把长数字当数字存的情形。
		var err error
		if j >= 4 {
			n, _ := strconv.ParseInt(v, 10, 64)
			err = f.SetCellValue("Sheet1", axis, n)
		} else {
			err = f.SetCellValue("Sheet1", axis, v)
		}
		if err != nil {
			t.Fatalf("写数据失败: %v", err)
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("生成 xlsx 失败: %v", err)
	}

	res := mustParseUpload(t, buf.Bytes())
	r := mustRecord(t, res, 2)
	if r.StudentNo != "202601010203" {
		t.Fatalf("学号被格式化了: %q", r.StudentNo)
	}
	if r.Phone != "13800138000" {
		t.Fatalf("手机号被格式化了: %q", r.Phone)
	}
}

// 旧版 .xls 是 OLE2 复合文档：必须明确说"另存为 .xlsx"，而不是报一句对不上号的错。
func TestParseUploadRejectsLegacyXLS(t *testing.T) {
	raw := append(append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1},
		make([]byte, 440)...), []byte("姓名\t楼栋\t宿舍号\t床位号\n张三\t1号楼\t302\t5\n")...)
	_, err := ParseUpload(raw, Options{Now: xlsxNow})
	if !errors.Is(err, ErrLegacyXLS) && (err == nil || !strings.Contains(err.Error(), ".xlsx")) {
		t.Fatalf("应给出可执行的另存为提示，实际 %v", err)
	}
}

// 伪装成 .xlsx 的 zip（改扩展名上传）同样要说清楚问题出在哪。
func TestParseUploadRejectsBogusZip(t *testing.T) {
	raw := append([]byte{'P', 'K', 0x03, 0x04}, make([]byte, 64)...)
	_, err := ParseUpload(raw, Options{Now: xlsxNow})
	if err == nil || !strings.Contains(err.Error(), ".xlsx") {
		t.Fatalf("要指出这份文件打不开成 xlsx，实际 %v", err)
	}
}

// 空表与全空工作表都不能编出记录来。
func TestParseUploadRejectsEmptyWorkbook(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("生成空 xlsx 失败: %v", err)
	}
	if _, err := ParseUpload(buf.Bytes(), Options{Now: xlsxNow}); err == nil {
		t.Fatal("全空的工作簿必须报错，不能返回一份看起来导入成功的空结果")
	}
}

// 文本路径不受分派影响：粘贴的 TSV 仍然照旧解析。
func TestParseUploadStillHandlesText(t *testing.T) {
	res := mustParseUpload(t, []byte("姓名\t班级\t楼栋\t宿舍号\t床位号\n张三\t高一(2)班\t1号楼\t302\t5\n"))
	if res.Stats.Recognized != 1 {
		t.Fatalf("文本分支应照常解析，实际 %+v", res.Stats)
	}
	if findIssue(res, CodeSheet, 0) != nil {
		t.Fatalf("文本输入不该报工作表诊断: %+v", res.Issues)
	}
}
