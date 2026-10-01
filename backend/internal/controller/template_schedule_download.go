package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// ─────────────────────────────────────────────────────────────────────────────
// 编译式下载：把该表类型下各部门的最新生效排班，编译成与《模板》版式一致的完整
// 工作簿。文件名长日期格式："2026年9月28日~10月2日学管会排班表.xlsx"。
//
// 排班表（duty）→ 4 个 sheet（与排班表模板一致）：
//   部门排班汇总          纪检部：左栏男生排班 + 右栏女生排班并排
//   八号楼技术部及宣传播音  宣传部（广播组·播音员）+ 技术部（上午新闻/下午打表）
//   督察部男纪检 / 女纪检  督察部记录工作表（时段列双列合并版式）
// 大课间（day_break）→ 大课间工作表：左栏常规排班 + 右栏宣传部大小班
// 夜间（night）→ 夜间工作表：姓名|班级|宿舍号|工作时间、地点
// ─────────────────────────────────────────────────────────────────────────────

// longDateRangeCN "2026-09-28"~"2026-10-02" → "2026年9月28日~10月2日"（跨年则补年份）
func longDateRangeCN(start, end string) string {
	s, err1 := time.Parse("2006-01-02", start)
	e, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil {
		return start + "~" + end
	}
	if s.Year() == e.Year() {
		return fmt.Sprintf("%d年%d月%d日~%d月%d日", s.Year(), int(s.Month()), s.Day(), int(e.Month()), e.Day())
	}
	return fmt.Sprintf("%d年%d月%d日~%d年%d月%d日", s.Year(), int(s.Month()), s.Day(), e.Year(), int(e.Month()), e.Day())
}

// tplStyles 工作簿样式集（excelize 样式 ID 按 workbook 隔离，须各自创建）
type tplStyles struct {
	title   int // 表头标题：加粗居中
	section int // 楼栋/部门小节行：加粗
	header  int // 列头行：加粗居中带边框
	cell    int // 数据格：居中带边框
}

func newTplStyles(f *excelize.File) tplStyles {
	thin := []excelize.Border{
		{Type: "left", Style: 1, Color: "8A8A8A"},
		{Type: "right", Style: 1, Color: "8A8A8A"},
		{Type: "top", Style: 1, Color: "8A8A8A"},
		{Type: "bottom", Style: 1, Color: "8A8A8A"},
	}
	title, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 14},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	section, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 12},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	header, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 11},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"F2F2F2"}, Pattern: 1},
		Border:    thin,
	})
	cell, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 11},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border:    thin,
	})
	return tplStyles{title: title, section: section, header: header, cell: cell}
}

// cellName (col,row) → "B3"
func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}

// putMerged 写一个跨 width 列的合并单元格（width=1 时不合并）
func putMerged(f *excelize.File, sheet string, col, row, width int, val string, style int) {
	c0 := cellName(col, row)
	f.SetCellValue(sheet, c0, val)
	if width > 1 {
		c1 := cellName(col+width-1, row)
		f.MergeCell(sheet, c0, c1)
	}
	if style > 0 {
		f.SetCellStyle(sheet, cellName(col, row), cellName(col+width-1, row), style)
	}
}

// findDeptSnap 在部门→快照映射里按关键字找（如 "纪检"/"督察"/"宣传"/"技术"）
func findDeptSnap(snaps map[string]*TemplateSchedulePreview, keywords ...string) *TemplateSchedulePreview {
	for _, kw := range keywords {
		for dept, snap := range snaps {
			if strings.Contains(dept, kw) {
				return snap
			}
		}
	}
	return nil
}

// snapshotBuildingsOf 取快照中指定性别的楼栋（gender 为空取全部）
func snapshotBuildingsOf(snap *TemplateSchedulePreview, gender string) []TemplateGridBuilding {
	if snap == nil {
		return nil
	}
	out := make([]TemplateGridBuilding, 0)
	for _, b := range snap.Buildings {
		if gender == "" || b.Gender == gender {
			out = append(out, b)
		}
	}
	return out
}

// defaultBuildingsOf 无数据时按模板的空表骨架渲染
func defaultBuildingsOf(gender string) []TemplateGridBuilding {
	out := make([]TemplateGridBuilding, 0)
	for _, b := range dutyDefaultBuildings {
		if b.Gender == gender {
			out = append(out, TemplateGridBuilding{Name: b.Name, Gender: b.Gender, Cols: defaultColNames(SchedTypeDuty), Rows: emptyRows(b.Floors, len(dutyDefaultSlots))})
		}
	}
	return out
}

func defaultColNames(schedType string) []string {
	names := make([]string, 0)
	for _, s := range defaultSlotsOf(schedType) {
		names = append(names, s.Name)
	}
	return names
}

func emptyRows(floors []string, colCount int) []TemplateGridRow {
	rows := make([]TemplateGridRow, 0, len(floors))
	for _, f := range floors {
		rows = append(rows, TemplateGridRow{Floor: f, Cells: make([][]string, colCount)})
	}
	return rows
}

// snapColsOf 快照列头；空则回退默认
func snapColsOf(snap *TemplateSchedulePreview, schedType string) []string {
	if snap != nil && len(snap.Cols) > 0 {
		return snap.Cols
	}
	return defaultColNames(schedType)
}

// writeGridBlock 写一段"标题+楼栋(楼名行/列头行/楼层行)"的网格，返回下一空行。
// wide=true 时每列占两格并合并（督察部记录工作表版式）。
func writeGridBlock(f *excelize.File, sheet string, col0, row0 int, title string, buildings []TemplateGridBuilding, cols []string, wide bool, st tplStyles) int {
	slotW := 1
	if wide {
		slotW = 2
	}
	blockW := 1 + slotW*len(cols)

	r := row0
	putMerged(f, sheet, col0, r, blockW, title, st.title)
	r++

	for _, b := range buildings {
		// 楼名行
		putMerged(f, sheet, col0, r, blockW, b.Name, st.section)
		r++
		// 列头行
		putMerged(f, sheet, col0, r, 1, "工作时间/工作楼层", st.header)
		cc := col0 + 1
		for _, col := range cols {
			putMerged(f, sheet, cc, r, slotW, col, st.header)
			cc += slotW
		}
		r++
		// 楼层行
		for _, row := range b.Rows {
			putMerged(f, sheet, col0, r, 1, row.Floor, st.cell)
			cc := col0 + 1
			for _, names := range row.Cells {
				if wide {
					// 督察记录表：一格拆两半——左半写名字，右半留空供打勾核验
					putMerged(f, sheet, cc, r, 1, strings.Join(names, "、"), st.cell)
					putMerged(f, sheet, cc+1, r, 1, "", st.cell)
				} else {
					putMerged(f, sheet, cc, r, slotW, strings.Join(names, "、"), st.cell)
				}
				cc += slotW
			}
			r++
		}
		r++ // 楼栋之间留一空行
	}
	return r
}

// DownloadCompiledTemplate GET /minister/template-schedule/download
// Query: sched_type(duty|day_break|night) 必填；
//   department/plan_id 可选（限定某部门/某历史版本）；start/end 可选覆盖日期区间。
// 大课间/夜间工作表是排班表网格的个人视图（早=次日上午大课间、晚=当晚），
// 因此这两种下载从 duty 快照的人员视图编译；仅当完全没有 duty 计划时才回退
// 到独立的大课间/夜间计划（兼容旧数据）。
func (tc *TemplateScheduleController) DownloadCompiledTemplate(c *gin.Context) {
	schedType := c.DefaultQuery("sched_type", SchedTypeDuty)
	deptFilter := c.Query("department")
	planID := c.Query("plan_id")
	startOv, endOv := c.Query("start"), c.Query("end")

	loadSnaps := func(types ...string) (map[string]*TemplateSchedulePreview, string, string) {
		query := repository.DB.Where("rule_type = ? AND sched_type IN ?", "template_week", types)
		if planID != "" {
			query = query.Where("id = ?", planID)
		} else {
			query = query.Where("status = ?", "active")
		}
		var plans []model.SchedulePlan
		query.Order("id desc").Find(&plans)
		out := map[string]*TemplateSchedulePreview{}
		minStart, maxEnd := "", ""
		for _, p := range plans {
			if deptFilter != "" && p.Department != deptFilter {
				continue
			}
			if _, ok := out[p.Department]; !ok && strings.TrimSpace(p.GridJSON) != "" {
				var snap TemplateSchedulePreview
				if err := json.Unmarshal([]byte(p.GridJSON), &snap); err == nil {
					out[p.Department] = &snap
				}
			}
			if minStart == "" || p.StartDate < minStart {
				minStart = p.StartDate
			}
			if p.EndDate > maxEnd {
				maxEnd = p.EndDate
			}
		}
		return out, minStart, maxEnd
	}

	// duty 快照是三种表的唯一数据源；大课间/夜间为其个人视图
	snaps, minStart, maxEnd := loadSnaps(SchedTypeDuty)
	if len(snaps) == 0 && (schedType == SchedTypeDayBreak || schedType == SchedTypeNight) {
		// 兼容：完全没有 duty 计划时，回退到独立的 day_break/night 计划
		snaps, minStart, maxEnd = loadSnaps(schedType)
	}

	// 显式指定的日期区间覆盖
	if startOv != "" {
		minStart = startOv
	}
	if endOv != "" {
		maxEnd = endOv
	}

	if minStart == "" || maxEnd == "" {
		now := time.Now()
		monday := now.AddDate(0, 0, -int((now.Weekday()+6)%7))
		friday := monday.AddDate(0, 0, 4)
		minStart = monday.Format("2006-01-02")
		maxEnd = friday.Format("2006-01-02")
	}
	if minStart > maxEnd {
		minStart, maxEnd = maxEnd, minStart
	}

	short := dateRangeCN(minStart, maxEnd)
	long := longDateRangeCN(minStart, maxEnd)

	var f *excelize.File
	var filename string
	switch schedType {
	case SchedTypeDayBreak:
		filename = long + "学管会大课间.xlsx"
		f = buildDayBreakWorkbook(snaps, short)
	case SchedTypeNight:
		filename = long + "学管会夜间.xlsx"
		f = buildNightWorkbook(snaps, short)
	default:
		filename = long + "学管会排班表.xlsx"
		f = buildDutyWorkbook(snaps, short)
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "xlsx 生成失败：" + err.Error()})
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="schedule_%s.xlsx"; filename*=UTF-8''%s`,
		url.PathEscape(long), url.PathEscape(filename)))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", buf.Bytes())
}

// noteSuffixOf 取快照里的周备注（如 "本周仅周日晚周一上工"）拼成 "(…)" 后缀
func noteSuffixOf(snaps map[string]*TemplateSchedulePreview) string {
	for _, snap := range snaps {
		note := strings.TrimSpace(snap.Note)
		if note != "" {
			if strings.HasPrefix(note, "(") || strings.HasPrefix(note, "（") {
				return note
			}
			return "(" + note + ")"
		}
	}
	return ""
}

// buildDutyWorkbook 排班表：部门排班汇总 / 八号楼技术部及宣传播音 / 督察部男纪检 / 督察部女纪检
func buildDutyWorkbook(snaps map[string]*TemplateSchedulePreview, shortRange string) *excelize.File {
	f := excelize.NewFile()
	st := newTplStyles(f)
	note := noteSuffixOf(snaps)

	// ① 部门排班汇总：纪检部男/女并排
	f.SetSheetName("Sheet1", "部门排班汇总")
	jiSnap := findDeptSnap(snaps, "纪检")
	maleCols := snapColsOf(jiSnap, SchedTypeDuty)
	if jiSnap == nil {
		maleCols = defaultColNames(SchedTypeDuty)
	}
	maleB := snapshotBuildingsOf(jiSnap, "male")
	if len(maleB) == 0 {
		maleB = defaultBuildingsOf("male")
	}
	femaleB := snapshotBuildingsOf(jiSnap, "female")
	if len(femaleB) == 0 {
		femaleB = defaultBuildingsOf("female")
	}
	f.SetColWidth("部门排班汇总", "A", "A", 16)
	writeGridBlock(f, "部门排班汇总", 1, 1, shortRange+"学管会纪检部男生排班"+note, maleB, maleCols, false, st)
	// 右栏从 H 列起（与模板一致，G 为间隔）
	rightCol := 1 + 1 + len(maleCols) + 1 // 左栏宽 + 1 间隔
	f.SetColWidth("部门排班汇总", cellName(rightCol, 1)[:1], cellName(rightCol, 1)[:1], 16)
	writeGridBlock(f, "部门排班汇总", rightCol, 1, shortRange+"学管会纪检部女生排班"+note, femaleB, maleCols, false, st)

	// ②③ 督察部男/女纪检（时段列双列合并），内容直接取首页（纪检部汇总）的数据
	f.DeleteSheet("Sheet1")
	for _, side := range []struct{ sheetName, gender string }{
		{"督察部男纪检", "male"}, {"督察部女纪检", "female"},
	} {
		_, _ = f.NewSheet(side.sheetName)
		jiSnap := findDeptSnap(snaps, "纪检")
		buildings := snapshotBuildingsOf(jiSnap, side.gender)
		if len(buildings) == 0 {
			// 无数据时按模板骨架渲染空白表（含领军苑，便于手填）
			buildings = defaultBuildingsOf(side.gender)
		}
		cols := snapColsOf(jiSnap, SchedTypeDuty)
		title := shortRange + "督察部记录工作表" + note
		f.SetColWidth(side.sheetName, "A", "A", 14)
		writeGridBlock(f, side.sheetName, 1, 1, title, buildings, cols, true, st)
	}

	return f
}

// buildDayBreakWorkbook 大课间工作表：左栏常规排班 + 右栏宣传部大小班
func buildDayBreakWorkbook(snaps map[string]*TemplateSchedulePreview, shortRange string) *excelize.File {
	f := excelize.NewFile()
	st := newTplStyles(f)
	sheet := "大课间工作表"
	f.SetSheetName("Sheet1", sheet)
	note := noteSuffixOf(snaps)

	var regular []TemplatePersonRow
	for _, snap := range snaps {
		regular = append(regular, snap.Persons...)
	}
	sortPersonsByClass(regular)

	f.SetColWidth(sheet, "A", "A", 12)
	f.SetColWidth(sheet, "B", "B", 12)
	f.SetColWidth(sheet, "C", "C", 70)

	writePersonHalf(f, sheet, 1, shortRange+"学管会大课间工作表"+note, regular, st)
	return f
}

// buildNightWorkbook 夜间工作表：姓名|班级|宿舍号|工作时间、地点
func buildNightWorkbook(snaps map[string]*TemplateSchedulePreview, shortRange string) *excelize.File {
	f := excelize.NewFile()
	st := newTplStyles(f)
	sheet := "夜间工作表"
	f.SetSheetName("Sheet1", sheet)
	note := noteSuffixOf(snaps)

	var persons []TemplatePersonRow
	for _, snap := range snaps {
		// duty 快照：NightPersons = 夜间个人视图；旧快照/独立夜间计划回退 Persons
		if len(snap.NightPersons) > 0 {
			persons = append(persons, snap.NightPersons...)
		} else {
			persons = append(persons, snap.Persons...)
		}
	}
	sortPersonsByClass(persons)

	f.SetColWidth(sheet, "A", "A", 12)
	f.SetColWidth(sheet, "B", "B", 12)
	f.SetColWidth(sheet, "C", "C", 10)
	f.SetColWidth(sheet, "D", "D", 80)

	title := shortRange + "学管会夜间工作表" + note
	putMerged(f, sheet, 1, 1, 4, title, st.title)

	head := []string{"姓名", "班级", "宿舍号", "工作时间、地点"}
	for i, h := range head {
		putMerged(f, sheet, 1+i, 2, 1, h, st.header)
	}
	r := 3
	for _, p := range persons {
		putMerged(f, sheet, 1, r, 1, p.Name, st.cell)
		putMerged(f, sheet, 2, r, 1, p.ClassName, st.cell)
		putMerged(f, sheet, 3, r, 1, p.DormNumber, st.cell)
		putMerged(f, sheet, 4, r, 1, p.WorkDesc, st.cell)
		r++
	}
	return f
}

// writePersonHalf 大课间表：标题 + 姓名|班级|工作时间、地点
func writePersonHalf(f *excelize.File, sheet string, col0 int, title string, persons []TemplatePersonRow, st tplStyles) {
	putMerged(f, sheet, col0, 1, 3, title, st.title)

	head := []string{"姓名", "班级", "工作时间、地点"}
	for i, h := range head {
		putMerged(f, sheet, col0+i, 2, 1, h, st.header)
	}
	r := 3
	for _, p := range persons {
		putMerged(f, sheet, col0, r, 1, p.Name, st.cell)
		putMerged(f, sheet, col0+1, r, 1, p.ClassName, st.cell)
		putMerged(f, sheet, col0+2, r, 1, p.WorkDesc, st.cell)
		r++
	}
}

// sortPersonsByClass 按班级（年级序）+ 姓名排序
func sortPersonsByClass(persons []TemplatePersonRow) {
	sort.SliceStable(persons, func(i, j int) bool {
		gi, gj := gradeOf(persons[i].ClassName), gradeOf(persons[j].ClassName)
		if gi != gj {
			return gi < gj
		}
		return persons[i].Name < persons[j].Name
	})
}
