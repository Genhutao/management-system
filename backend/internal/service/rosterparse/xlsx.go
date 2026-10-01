// xlsx 工作表读取：把 .xlsx 交给 excelize 真正解析成单元格矩阵，再走与文本完全相同的解析内核。
//
// 这里刻意不做"把单元格拼回字符串"那条路：拼回去再按分隔符切一次，
// 单元格内含逗号或制表符时列会整体错位——而学校表的班级列恰恰写着"2027届1班,住宿部"这种值。
package rosterparse

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

const (
	// maxWorkbookRows 限制单个工作表的行数。xlsx 是压缩过的，
	// 几 MB 的文件里藏几十万空行完全可能，不设上限就等于把内存交给上传者也交了。
	maxWorkbookRows = 200000
	// maxMergeSpan 限制合并区域展开的跨度。整列合并（A:A）在真实表里很常见，
	// 不按已用范围收敛就会展开成上百万个单元格。
	maxMergeSpan = 512
)

var (
	zipMagic = []byte{'P', 'K', 0x03, 0x04}
	// oleMagic 是 OLE2 复合文档的头部，旧版 .xls（Excel 97-2003）与 .doc 都是它。
	oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
)

// ErrLegacyXLS 单独成变量，控制器和测试都能稳定断言这一类拒绝。
var ErrLegacyXLS = errors.New("这是旧版 .xls（Excel 97-2003）格式，内核只认 .xlsx：请在 Excel 或 WPS 里另存为「Excel 工作表(.xlsx)」后重新上传")

// ParseUpload 按字节头部自动分派：.xlsx 走工作表读取，其余仍按文本解析。
//
// 分派只看内容不看扩展名——把 .xls 改名成 .xlsx 上传是这里最常见的实际事故形态，
// 按扩展名放行就会在后面一步报出完全对不上号的错。
func ParseUpload(raw []byte, opts Options) (*Result, error) {
	switch {
	case hasPrefix(raw, oleMagic):
		return nil, ErrLegacyXLS
	case hasPrefix(raw, zipMagic):
		return parseWorkbook(raw, opts)
	default:
		return Parse(raw, opts)
	}
}

func hasPrefix(raw []byte, magic []byte) bool {
	return len(raw) >= len(magic) && bytes.Equal(raw[:len(magic)], magic)
}

// parseWorkbook 读取工作簿里"最像名单"的那一张表。
//
// 选非空行最多的表而不是第一张：学校发的总表常带一张封面/说明页排在最前面，
// 认第一张等于必然解析失败。选了哪张、为什么选，都如实写进诊断，
// 免得使用者以为传上来的都在库里。
func parseWorkbook(raw []byte, opts Options) (*Result, error) {
	// RawCellValue：要单元格里存的原始值，不要被数字格式改写过的显示值。
	// 学号 12 位以上时格式化路径会给出科学计数法，手机号也一样，
	// 而这里要的恰好是那串数字本身。
	f, err := excelize.OpenReader(bytes.NewReader(raw), excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, fmt.Errorf("无法作为 .xlsx 打开这份文件（可能是损坏的表格，或其实不是 Excel 工作表）：%w", err)
	}
	defer f.Close()

	names := f.GetSheetList()
	if len(names) == 0 {
		return nil, errors.New("这份 .xlsx 里没有任何工作表")
	}

	var (
		bestName  string
		bestCells [][]string
		bestScore int
		readErrs  []string
	)
	for _, name := range names {
		cells, err := sheetCells(f, name)
		if err != nil {
			readErrs = append(readErrs, name+"："+err.Error())
			continue
		}
		score := 0
		for _, row := range cells {
			if !isBlankRow(row) {
				score++
			}
		}
		if score > bestScore {
			bestName, bestCells, bestScore = name, cells, score
		}
	}

	if bestScore == 0 {
		if len(readErrs) > 0 {
			return nil, fmt.Errorf("这份工作表读取失败：%s", strings.Join(readErrs, "；"))
		}
		return nil, errors.New("这份 .xlsx 的所有工作表都是空的，没有可导入的内容")
	}

	res, err := ParseCells(bestCells, opts)
	if res != nil {
		note := Issue{
			Code:    CodeSheet,
			Message: fmt.Sprintf("已读取工作表「%s」（有效行 %d 行，共 %d 张表）", bestName, bestScore, len(names)),
		}
		if len(names) > 1 {
			note.Message = fmt.Sprintf("这份工作簿有 %d 张表，已读取内容最多的「%s」（有效行 %d 行）；名单不在该表时请把名单单独存一份再导入", len(names), bestName, bestScore)
		}
		res.Issues = append([]Issue{note}, res.Issues...)
	}
	return res, err
}

// sheetCells 取一张表的单元格矩阵，并把合并区域摊平。
//
// 合并单元格只把值存在左上角：住宿部总表里"2027届1班"跨 8 行合并时，
// 不摊平的话第 2 行起班级全空，整批行都会被判成"缺班级"。
func sheetCells(f *excelize.File, name string) ([][]string, error) {
	cells, err := f.GetRows(name, excelize.Options{RawCellValue: true})
	if err != nil {
		return nil, err
	}
	if len(cells) > maxWorkbookRows {
		return nil, fmt.Errorf("工作表「%s」有 %d 行，超过单次导入上限 %d 行", name, len(cells), maxWorkbookRows)
	}
	return applyMerges(f, name, cells), nil
}

// applyMerges 用左上角值填满合并区域，越界与超宽的区域按已用范围收敛。
func applyMerges(f *excelize.File, name string, cells [][]string) [][]string {
	merges, err := f.GetMergeCells(name)
	if err != nil || len(merges) == 0 {
		return cells
	}
	for _, m := range merges {
		value := strings.TrimSpace(m.GetCellValue())
		if value == "" {
			continue
		}
		c1, r1, err := excelize.CellNameToCoordinates(m.GetStartAxis())
		if err != nil || r1 < 1 || c1 < 1 {
			continue
		}
		c2, r2, err := excelize.CellNameToCoordinates(m.GetEndAxis())
		if err != nil || c2 < c1 || r2 < r1 {
			continue
		}
		// 合并区域常写成整列/整行；只保留落在已用范围内的部分。
		if r2 > len(cells) {
			r2 = len(cells)
		}
		if c2 > c1+maxMergeSpan {
			c2 = c1 + maxMergeSpan
		}
		for r := r1; r <= r2; r++ {
			row := cells[r-1]
			for c := c1; c <= c2; c++ {
				for len(row) < c {
					row = append(row, "")
				}
				if strings.TrimSpace(row[c-1]) == "" {
					row[c-1] = value
				}
			}
			cells[r-1] = row
		}
	}
	return cells
}
