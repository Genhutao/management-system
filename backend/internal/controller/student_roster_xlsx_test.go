package controller

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

// 住宿部总表这类真表是以 .xlsx 传来的：控制器侧要能整条走通，
// 旧版 .xls 要给出「另存为」这种照着做就能解决的提示。

func xlsxBytes(t *testing.T, sheet string, rows [][]string) []byte {
	t.Helper()
	f := excelize.NewFile()
	defer f.Close()
	idx, err := f.NewSheet(sheet)
	if err != nil {
		t.Fatalf("新建工作表失败: %v", err)
	}
	f.SetActiveSheet(idx)
	f.DeleteSheet("Sheet1")
	for i, row := range rows {
		for j, cell := range row {
			axis, err := excelize.CoordinatesToCellName(j+1, i+1)
			if err != nil {
				t.Fatalf("坐标失败: %v", err)
			}
			if err := f.SetCellValue(sheet, axis, cell); err != nil {
				t.Fatalf("写单元格失败: %v", err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("生成 xlsx 失败: %v", err)
	}
	return buf.Bytes()
}

func TestParseAndPreviewAcceptsXLSX(t *testing.T) {
	setupRosterDB(t)

	raw := xlsxBytes(t, "住宿部总表", [][]string{
		{"年级", "班级", "姓名", "性别", "楼栋", "宿舍号", "床位号"},
		{"2027", "1", "杨远健", "男", "N", "N101", "1"},
		{"2028", "3", "李雷", "男", "N", "N205", "4"},
		{"2029", "2", "韩梅梅", "女", "A", "A306", "2"},
	})

	rec, c := newMultipartParseRequest(t, "202609住宿部总表.xlsx", raw, nil)
	new(StudentController).ParseAndPreview(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("真表 .xlsx 必须能进预览，实际 %d / %s", rec.Code, rec.Body.String())
	}
	payload := decodePreview(t, rec)
	if payload.TotalRecognized != 3 {
		t.Fatalf("3 行名单应全部识别，实际 %d / %+v", payload.TotalRecognized, payload.Issues)
	}
	if len(payload.AllParsed) != 3 {
		t.Fatalf("入库清单要带全部 3 行，实际 %d", len(payload.AllParsed))
	}
	if payload.AllParsed[0].Grade != "高三" || payload.AllParsed[0].ClassName != "高三(1)班" {
		t.Fatalf("毕业年份换算没走通: %+v", payload.AllParsed[0])
	}
	if payload.Fingerprint == "" {
		t.Fatal("xlsx 也要给出指纹，否则这张表的列映射记不住")
	}
	var sheetNote bool
	for _, is := range payload.Issues {
		if is.Code == "sheet" {
			sheetNote = true
		}
	}
	if !sheetNote {
		t.Fatalf("要说明读的是哪张工作表: %+v", payload.Issues)
	}
}

func TestParseAndPreviewGuidesLegacyXLS(t *testing.T) {
	setupRosterDB(t)

	raw := append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 512)...)
	rec, c := newMultipartParseRequest(t, "老名单.xls", raw, nil)
	new(StudentController).ParseAndPreview(c)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("旧版 .xls 必须 400，实际 %d", rec.Code)
	}
	payload := decodePreview(t, rec)
	if !strings.Contains(payload.Error, ".xlsx") || !strings.Contains(payload.Error, "另存为") {
		t.Fatalf("错误要给出可执行的下一步，实际 %q", payload.Error)
	}
}
