package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
	"golang.org/x/crypto/bcrypt"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// TemplateScheduleController 模板排班控制器。
//
// 支持三种表类型（对齐三份 Excel 模板）：
//
//	duty      排班表          —— 部门排班汇总：楼栋分区 × 楼层行 × 列头（周日晚/周一早…），
//	                            按部门各自成表（纪检部男/女、督察部…），表头标题为
//	                            "x.xx-x.xx 学管会XX部XX排班"（x.xx 即起止日期）。
//	day_break 大课间工作表    —— 人员明细：姓名 | 班级 | 工作时间、地点，按班级排序，
//	                            列头为周一~周五，每格填当值部员。
//	night     夜间工作表      —— 人员明细：姓名 | 班级 | 宿舍号 | 工作时间、地点。
//
// 同一部门的同一表类型在同一时间窗内只有一份 active 计划；重新确认 = 归档旧版
// 并保留历史，历史可回看、可下载（最新与任意历史版本均可导出 xlsx）。
type TemplateScheduleController struct{}

// 表类型常量
const (
	SchedTypeDuty     = "duty"      // 排班表（部门排班汇总）
	SchedTypeDayBreak = "day_break" // 大课间工作表
	SchedTypeNight    = "night"     // 夜间工作表
)

// ─────────────────────────────────────────────────────────────────────────────
// 请求 / 响应结构
// ─────────────────────────────────────────────────────────────────────────────

// TemplateSlotCfg 一个列头定义
type TemplateSlotCfg struct {
	Key  string `json:"key"`  // duty: sun_mon/mon_tue/...；day_break/night: mon/tue/wed/thu/fri
	Name string `json:"name"` // 展示名，如 "周日晚/周一早"、"周一"
}

// TemplateBuildingCfg duty 表的一栋楼配置
type TemplateBuildingCfg struct {
	Name   string   `json:"name"`   // 楼栋名，如 "六号楼"
	Gender string   `json:"gender"` // male / female，决定用男/女部员池
	Floors []string `json:"floors"` // 楼层标签
}

// TemplateScheduleRequest 部长提交的生成参数
type TemplateScheduleRequest struct {
	Title       string                `json:"title" binding:"required"`
	SchedType   string                `json:"sched_type" binding:"required"` // duty / day_break / night
	Department  string                `json:"department"`                    // 归属部门；空 = 部长本部（技术组需显式填）
	StartDate   string                `json:"start_date" binding:"required"` // YYYY-MM-DD
	EndDate     string                `json:"end_date" binding:"required"`
	Note        string                `json:"note"`         // 周备注，如 "本周仅周日晚周一上工"，拼进各表标题括号
	MemberIDs   []uint                `json:"member_ids"`   // 参与轮转的部员；空 = 该部门全部在职 member
	Buildings   []TemplateBuildingCfg `json:"buildings"`    // 仅 duty 用；空 = 内置默认
	Slots       []TemplateSlotCfg     `json:"slots"`        // 列头；空 = 按类型取默认
	GenderSplit bool                  `json:"gender_split"` // duty 且按性别拆池
	PerCell     int                   `json:"per_cell"`     // duty 每格人数，默认 1
}

// TemplateScheduleShiftDTO duty 表的一条班次（落库 schedule_shifts 的口径）
type TemplateScheduleShiftDTO struct {
	Date        string   `json:"date"`
	WeekdayCN   string   `json:"weekday_cn"`
	WeekType    string   `json:"week_type"`
	ShiftPeriod string   `json:"shift_period"`
	Building    string   `json:"building"`
	Floor       string   `json:"floor"`
	MemberIDs   []uint   `json:"member_ids"`
	MemberNames []string `json:"member_names"`
	ManagerName string   `json:"manager_name"`
}

// TemplateGridBuilding duty 网格中的一栋楼
type TemplateGridBuilding struct {
	Name   string            `json:"name"`
	Gender string            `json:"gender"`
	Cols   []string          `json:"cols"` // 列头（时段名）
	Rows   []TemplateGridRow `json:"rows"` // 每层一行
}

// TemplateGridRow 一层楼一行：floor + 每列的当值姓名
type TemplateGridRow struct {
	Floor string     `json:"floor"`
	Cells [][]string `json:"cells"` // 与 cols 等长；空数组表示无人
}

// TemplatePersonRow 人员明细行（大课间/夜间工作表口径）
type TemplatePersonRow struct {
	Name       string   `json:"name"`
	ClassName  string   `json:"class_name"`
	DormNumber string   `json:"dorm_number"` // night 表用
	Cells      []string `json:"cells"`       // 与列头等长；每格为该日工作地点/时段描述
	WorkDesc   string   `json:"work_desc"`   // 汇总描述，如 "周一、三、五晚上（7号楼）"
}

// TemplateSchedulePreview 预览载荷：网格 + 人员明细 + 落库班次三份同源数据
type TemplateSchedulePreview struct {
	Title        string                     `json:"title"`
	SchedType    string                     `json:"sched_type"`
	Department   string                     `json:"department"`
	Note         string                     `json:"note,omitempty"` // 周备注，标题括号内容
	DateRangeCN  string                     `json:"date_range_cn"`  // 如 "9.28~10.2"，表头标题用
	WeekKey      string                     `json:"week_key"`
	Buildings    []TemplateGridBuilding     `json:"buildings,omitempty"`     // duty 表
	Persons      []TemplatePersonRow        `json:"persons,omitempty"`       // 大课间视图（每列"早"=次日上午大课间）
	NightPersons []TemplatePersonRow        `json:"night_persons,omitempty"` // 夜间视图（每列"晚"=当晚查寝）
	Cols         []string                   `json:"cols,omitempty"`          // 大课间/夜间列头
	Shifts       []TemplateScheduleShiftDTO `json:"shifts"`
	TotalCells   int                        `json:"total_cells"`
}

// ─────────────────────────────────────────────────────────────────────────────
// 内置默认配置（对齐三份模板文件）
// ─────────────────────────────────────────────────────────────────────────────

// 排班表列头：周日晚/周一早 … 周四晚/周五早
var dutyDefaultSlots = []TemplateSlotCfg{
	{Key: "sun_mon", Name: "周日晚/周一早"},
	{Key: "mon_tue", Name: "周一晚/周二早"},
	{Key: "tue_wed", Name: "周二晚/周三早"},
	{Key: "wed_thu", Name: "周三晚/周四早"},
	{Key: "thu_fri", Name: "周四晚/周五早"},
}

// 大课间/夜间表列头：周一 ~ 周五
var personDefaultSlots = []TemplateSlotCfg{
	{Key: "mon", Name: "周一"}, {Key: "tue", Name: "周二"}, {Key: "wed", Name: "周三"},
	{Key: "thu", Name: "周四"}, {Key: "fri", Name: "周五"},
}

// 排班表默认楼栋（模板"部门排班汇总"版式；每部门可改）
var dutyDefaultBuildings = []TemplateBuildingCfg{
	{Name: "五号楼", Gender: "female", Floors: []string{"1楼", "2楼", "3楼", "4楼", "5楼", "6楼"}},
	{Name: "六号楼", Gender: "male", Floors: []string{"6楼", "5楼", "4楼", "3楼", "2楼", "1楼"}},
	{Name: "七号楼", Gender: "female", Floors: []string{"1楼", "2楼", "3楼", "4楼", "5楼", "6楼"}},
	{Name: "八号楼", Gender: "male", Floors: []string{"5楼", "4楼", "3楼", "2楼", "1楼"}},
	{Name: "领军北苑（男）", Gender: "male", Floors: []string{"2楼", "1楼"}},
	{Name: "领军南苑（女）", Gender: "female", Floors: []string{"5楼", "4楼", "3楼", "2楼", "1楼"}},
}

// duty 表列头 → 相对"本周周一"的日期偏移；"周日晚/周一早"单独回退一天到周日。
var dutySlotOffsets = map[string]int{
	"sun_mon": 0, "mon_tue": 0, "tue_wed": 1, "wed_thu": 2, "thu_fri": 3,
}

// person 表列头（周一~周五）→ 相对本周周一的偏移
var personSlotOffsets = map[string]int{
	"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4,
}

var templateWeekdayCN = map[time.Weekday]string{
	time.Sunday: "周日", time.Monday: "周一", time.Tuesday: "周二",
	time.Wednesday: "周三", time.Thursday: "周四", time.Friday: "周五", time.Saturday: "周六",
}

// schedTypeLabel 表类型中文名（拼表头标题用）
func schedTypeLabel(t string) string {
	switch t {
	case SchedTypeDuty:
		return "排班表"
	case SchedTypeDayBreak:
		return "大课间工作表"
	case SchedTypeNight:
		return "夜间工作表"
	}
	return "排班表"
}

// dateRangeCN "2026-09-28" -> "09.28"（模板表头标题的 x.xx 口径）
func dateRangeCN(start, end string) string {
	s, err1 := time.Parse("2006-01-02", start)
	e, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil {
		return start + "~" + end
	}
	return fmt.Sprintf("%d.%d~%d.%d", int(s.Month()), s.Day(), int(e.Month()), e.Day())
}

// ─────────────────────────────────────────────────────────────────────────────
// 部员池
// ─────────────────────────────────────────────────────────────────────────────

// memberInfo 参与轮转的部员
type memberInfo struct {
	ID     uint
	Name   string
	Gender string
	Class  string
	Dorm   string
	Grade  int
}

// resolveDepartment 解析表归属部门：显式指定优先；否则部长本部；
// 技术组未指定时默认"组织部 · 技术组"。
func resolveDepartment(c *gin.Context, req *TemplateScheduleRequest) (string, model.User, error) {
	var currentUser model.User
	if err := repository.DB.First(&currentUser, c.GetUint("user_id")).Error; err != nil {
		return "", currentUser, fmt.Errorf("当前登录人身份读取失败")
	}
	dept := strings.TrimSpace(req.Department)
	if dept == "" {
		if currentUser.Role == model.RoleTechAdmin {
			dept = "组织部 · 技术组"
		} else {
			dept = strings.TrimSpace(currentUser.Department)
		}
	}
	if dept == "" {
		return "", currentUser, fmt.Errorf("无法确定归属部门：请在参数里填写部门名")
	}
	return dept, currentUser, nil
}

// buildMemberPool 解析参与轮转的部员池：指定 member_ids 按指定顺序；
// 否则取该部门的全部在职 member（LIKE 匹配复合部门名，如"组织部 · 技术组"）。
func buildMemberPool(req *TemplateScheduleRequest, dept string) ([]memberInfo, error) {
	var users []model.User
	if len(req.MemberIDs) > 0 {
		if err := repository.DB.Where("id IN ? AND role = ? AND status = ?",
			req.MemberIDs, model.RoleMember, "active").Find(&users).Error; err != nil {
			return nil, fmt.Errorf("部员池查询失败")
		}
		byID := map[uint]model.User{}
		for _, u := range users {
			byID[u.ID] = u
		}
		ordered := make([]model.User, 0, len(users))
		for _, id := range req.MemberIDs {
			if u, ok := byID[id]; ok {
				ordered = append(ordered, u)
			}
		}
		users = ordered
	} else {
		query := repository.DB.Where("role = ? AND status = ?", model.RoleMember, "active").
			Where("department LIKE ?", "%"+dept+"%")
		if err := query.Find(&users).Error; err != nil {
			return nil, fmt.Errorf("部员池查询失败")
		}
	}
	if len(users) == 0 {
		return nil, fmt.Errorf("部门【%s】下没有在职部员：请先录入部员，或指定参与部员", dept)
	}

	pool := make([]memberInfo, 0, len(users))
	for _, u := range users {
		pool = append(pool, memberInfo{
			ID:     u.ID,
			Name:   u.RealName,
			Gender: genderOf(u),
			Class:  u.ClassName,
		})
	}
	return pool, nil
}

// genderOf 从楼栋命名习惯推断性别（部员表无独立性别字段）
func genderOf(u model.User) string {
	b := u.Building
	switch {
	case strings.Contains(b, "女") || strings.Contains(b, "五号") || strings.Contains(b, "七号") || strings.Contains(b, "南苑"):
		return "female"
	default:
		return "male"
	}
}

// gradeOf 从 "高一(2)班" 提取年级序号用于排序
func gradeOf(class string) int {
	switch {
	case strings.Contains(class, "高一"), strings.Contains(class, "一"):
		return 1
	case strings.Contains(class, "高二"), strings.Contains(class, "二"):
		return 2
	case strings.Contains(class, "高三"), strings.Contains(class, "三"):
		return 3
	}
	return 9
}

// ─────────────────────────────────────────────────────────────────────────────
// 三种表的生成
// ─────────────────────────────────────────────────────────────────────────────

// generatePreview 按类型分发生成（不落库）
func generatePreview(req *TemplateScheduleRequest, dept string, pool []memberInfo) (*TemplateSchedulePreview, error) {
	start, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		return nil, fmt.Errorf("开始日期格式错误，应为 YYYY-MM-DD")
	}
	end, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil || end.Before(start) {
		return nil, fmt.Errorf("结束日期格式错误或早于开始日期")
	}

	preview := &TemplateSchedulePreview{
		Title:       req.Title,
		SchedType:   req.SchedType,
		Department:  dept,
		DateRangeCN: dateRangeCN(req.StartDate, req.EndDate),
	}
	// 周备注拼进标题括号（如 "9.28~10.2学管会纪检部排班表(本周仅周日晚周一上工)"），
	// 已含则不重复追加
	preview.Note = strings.TrimSpace(req.Note)
	if preview.Note != "" && !strings.Contains(preview.Title, preview.Note) {
		preview.Title = fmt.Sprintf("%s(%s)", preview.Title, preview.Note)
	}
	_, week1 := start.ISOWeek()
	preview.WeekKey = fmt.Sprintf("%d-W%02d", start.Year(), week1)

	switch req.SchedType {
	case SchedTypeDuty:
		generateDutyGrid(req, preview, pool)
	case SchedTypeDayBreak, SchedTypeNight:
		generatePersonSheet(req, preview, pool)
	default:
		return nil, fmt.Errorf("未知的表类型：%s（支持 duty 排班表 / day_break 大课间 / night 夜间）", req.SchedType)
	}

	if len(preview.Shifts) == 0 {
		return nil, fmt.Errorf("生成的班次为空，请检查日期范围与配置")
	}
	return preview, nil
}

// defaultSlotsOf 按表类型取默认列头
func defaultSlotsOf(schedType string) []TemplateSlotCfg {
	if schedType == SchedTypeDuty {
		return dutyDefaultSlots
	}
	return personDefaultSlots
}

// slotDateOf 计算某列的落库日期（以 start_date 对齐的周为基准）
func slotDateOf(slotKey string, index int, start time.Time) time.Time {
	monday := start.AddDate(0, 0, -int((start.Weekday()+6)%7))
	var off int
	var ok bool
	if slotKey == "" {
		// 自定义列头没有 key：周一为基准按列序推进
		off = index
	} else if off, ok = dutySlotOffsets[slotKey]; !ok {
		if off, ok = personSlotOffsets[slotKey]; !ok {
			off = index
		}
	}
	if slotKey == "sun_mon" {
		monday = monday.AddDate(0, 0, -1) // 周日晚在周一之前
	}
	return monday.AddDate(0, 0, off)
}

// generateDutyGrid 排班表：楼栋分区 × 楼层行 × 列头，格内轮转填部员
func generateDutyGrid(req *TemplateScheduleRequest, preview *TemplateSchedulePreview, pool []memberInfo) {
	buildings := req.Buildings
	if len(buildings) == 0 {
		buildings = dutyDefaultBuildings
	}
	slots := req.Slots
	if len(slots) == 0 {
		slots = dutyDefaultSlots
	}
	perCell := req.PerCell
	if perCell < 1 {
		perCell = 1
	}

	start, _ := time.Parse("2006-01-02", req.StartDate)
	mCursor, fCursor := 0, 0

	for _, b := range buildings {
		gb := TemplateGridBuilding{Name: b.Name, Gender: b.Gender, Cols: make([]string, len(slots))}
		for i, s := range slots {
			gb.Cols[i] = s.Name
		}

		for _, floor := range b.Floors {
			row := TemplateGridRow{Floor: floor, Cells: make([][]string, len(slots))}
			for si, slot := range slots {
				cellNames := make([]string, 0, perCell)
				cellIDs := make([]uint, 0, perCell)
				for k := 0; k < perCell; k++ {
					var m memberInfo
					if req.GenderSplit {
						if b.Gender == "female" {
							female := filterGender(pool, "female")
							if len(female) == 0 {
								break
							}
							m = female[fCursor%len(female)]
							fCursor++
						} else {
							male := filterGender(pool, "male")
							if len(male) == 0 {
								break
							}
							m = male[mCursor%len(male)]
							mCursor++
						}
					} else {
						m = pool[mCursor%len(pool)]
						mCursor++
					}
					cellNames = append(cellNames, m.Name)
					cellIDs = append(cellIDs, m.ID)
				}
				row.Cells[si] = cellNames

				if len(cellIDs) > 0 {
					shiftDate := slotDateOf(slot.Key, si, start)
					_, isoWeek := shiftDate.ISOWeek()
					weekType := "single"
					if isoWeek%2 == 0 {
						weekType = "double"
					}
					preview.Shifts = append(preview.Shifts, TemplateScheduleShiftDTO{
						Date:        shiftDate.Format("2006-01-02"),
						WeekdayCN:   templateWeekdayCN[shiftDate.Weekday()],
						WeekType:    weekType,
						ShiftPeriod: slot.Name,
						Building:    b.Name,
						Floor:       floor,
						MemberIDs:   cellIDs,
						MemberNames: cellNames,
						ManagerName: matchDormManager(b.Name),
					})
				}
			}
			gb.Rows = append(gb.Rows, row)
		}
		preview.Buildings = append(preview.Buildings, gb)
	}
	preview.TotalCells = countGridCells(preview.Buildings)
	preview.Cols = gb0Cols(preview.Buildings)
	preview.Persons, preview.NightPersons = derivePersonViews(preview.Shifts, pool)
}

// gb0Cols 取第一栋楼的列头作为整体列头（预览表头日期行共用）
func gb0Cols(grid []TemplateGridBuilding) []string {
	if len(grid) > 0 {
		return grid[0].Cols
	}
	return nil
}

// generatePersonSheet 大课间/夜间表：按天 × 地点（楼栋）轮转分派，
// 产出模板口径的人员明细（姓名|班级|(宿舍号)|工作时间、地点，按班级排序）。
// 例："周一、三、五晚上（7号楼）"。
func generatePersonSheet(req *TemplateScheduleRequest, preview *TemplateSchedulePreview, pool []memberInfo) {
	slots := req.Slots
	if len(slots) == 0 {
		slots = personDefaultSlots
	}
	buildings := req.Buildings
	if len(buildings) == 0 {
		buildings = personDefaultPlaces()
	}
	start, _ := time.Parse("2006-01-02", req.StartDate)

	// 人员按班级排序（模板要求），同班按姓名
	ordered := make([]memberInfo, len(pool))
	copy(ordered, pool)
	sort.Slice(ordered, func(i, j int) bool {
		gi, gj := gradeOf(ordered[i].Class), gradeOf(ordered[j].Class)
		if gi != gj {
			return gi < gj
		}
		return ordered[i].Name < ordered[j].Name
	})

	preview.Cols = make([]string, len(slots))
	for i, s := range slots {
		preview.Cols[i] = s.Name
	}

	// 确定性轮转：列（天）优先、地点次之，人员顺序循环；
	// 同一天先排第一栋楼再排第二栋，人员池顺序消耗。
	byPerson := map[string][]personAssign{}
	cursor := 0
	for si, slot := range slots {
		for _, b := range buildings {
			m := ordered[cursor%len(ordered)]
			cursor++
			byPerson[m.Name] = append(byPerson[m.Name], personAssign{col: si, building: b.Name})

			shiftDate := slotDateOf(slot.Key, si, start)
			_, isoWeek := shiftDate.ISOWeek()
			weekType := "single"
			if isoWeek%2 == 0 {
				weekType = "double"
			}
			preview.Shifts = append(preview.Shifts, TemplateScheduleShiftDTO{
				Date:        shiftDate.Format("2006-01-02"),
				WeekdayCN:   templateWeekdayCN[shiftDate.Weekday()],
				WeekType:    weekType,
				ShiftPeriod: slot.Name,
				Building:    b.Name,
				MemberIDs:   []uint{m.ID},
				MemberNames: []string{m.Name},
			})
		}
	}

	nameRow := map[string]int{}
	for i, m := range ordered {
		nameRow[m.Name] = i
	}
	preview.Persons = make([]TemplatePersonRow, len(ordered))
	for i, m := range ordered {
		preview.Persons[i] = TemplatePersonRow{
			Name:       m.Name,
			ClassName:  m.Class,
			DormNumber: m.Dorm,
			Cells:      make([]string, len(slots)),
		}
	}
	for name, list := range byPerson {
		row := &preview.Persons[nameRow[name]]
		for _, a := range list {
			row.Cells[a.col] = a.building
		}
		row.WorkDesc = compressPersonWorkDesc(list, slots, req.SchedType)
	}
	preview.TotalCells = cursor
}

// personDefaultPlaces 大课间/夜间默认地点（取自模板示例中的楼栋）
func personDefaultPlaces() []TemplateBuildingCfg {
	return []TemplateBuildingCfg{
		{Name: "八号楼"}, {Name: "领军北苑"}, {Name: "六号楼"},
	}
}

// personAssign 一次（天, 地点）分派
type personAssign struct {
	col      int
	building string
}

// compressPersonWorkDesc 按类型拼装人员工作描述：
// duty "周一、三（六号楼）"；day_break "周一、三上午大课间（八号楼）"；night "周一、三晚上（八号楼）"
func compressPersonWorkDesc(list []personAssign, slots []TemplateSlotCfg, schedType string) string {
	if len(list) == 0 {
		return ""
	}
	suffix := ""
	switch schedType {
	case SchedTypeDayBreak:
		suffix = "上午大课间"
	case SchedTypeNight:
		suffix = "晚上"
	}

	// 按楼栋分组、组内压缩连续天
	byBuilding := map[string][]int{}
	for _, a := range list {
		byBuilding[a.building] = append(byBuilding[a.building], a.col)
	}
	buildings := make([]string, 0, len(byBuilding))
	for b := range byBuilding {
		buildings = append(buildings, b)
	}
	sort.Strings(buildings)

	var parts []string
	for _, b := range buildings {
		cols := byBuilding[b]
		sort.Ints(cols)
		var dayStrs []string
		i := 0
		for i < len(cols) {
			j := i
			for j+1 < len(cols) && cols[j+1] == cols[j]+1 {
				j++
			}
			head := strings.TrimPrefix(slots[cols[i]].Name, "周")
			if j > i {
				for k := i + 1; k <= j; k++ {
					head += "、"
					head += strings.TrimPrefix(slots[cols[k]].Name, "周")
				}
			}
			dayStrs = append(dayStrs, head)
			i = j + 1
		}
		parts = append(parts, fmt.Sprintf("周%s%s（%s）", strings.Join(dayStrs, "、"), suffix, b))
	}
	return strings.Join(parts, "，")
}

// filterGender 按性别过滤部员池
func filterGender(pool []memberInfo, gender string) []memberInfo {
	out := make([]memberInfo, 0, len(pool))
	for _, p := range pool {
		if p.Gender == gender {
			out = append(out, p)
		}
	}
	return out
}

// matchDormManager 按楼栋名匹配预置花名册里的协同宿管
func matchDormManager(buildingName string) string {
	var preset model.DormRosterPreset
	if err := repository.DB.Where("building LIKE ?", "%"+buildingName+"%").First(&preset).Error; err == nil {
		return preset.RealName
	}
	return "值班宿管"
}

// countGridCells 统计 duty 网格非空格位总数
func countGridCells(grid []TemplateGridBuilding) int {
	n := 0
	for _, b := range grid {
		for _, r := range b.Rows {
			for _, c := range r.Cells {
				if len(c) > 0 {
					n++
				}
			}
		}
	}
	return n
}

// derivePersonViews 从排班表网格推导按个人的两份周表（对齐真实填写样例）：
//   - 大课间视图：网格一列的"早"=次日上午大课间（周五早=早晨宿舍锁门后）；
//   - 夜间视图：网格一列的"晚"=当晚查寝。
// 一个人占一格即同时承担当晚与次日上午。描述按楼栋分组、组内连续同标签日期压缩：
// "周三、四上午大课间、周五早晨宿舍锁门后（六号楼），周一上午大课间（七号楼）"。
func derivePersonViews(shifts []TemplateScheduleShiftDTO, pool []memberInfo) (dayBreak, night []TemplatePersonRow) {
	type dutyPoint struct {
		dayOrder int
		dayCN    string
		label    string // 上午大课间 / 早晨宿舍锁门后 / 晚上
		building string
	}
	byPerson := map[string][]dutyPoint{}
	classOf := map[string]string{}
	dormOf := map[string]string{}
	for _, p := range pool {
		classOf[p.Name] = p.Class
		dormOf[p.Name] = p.Dorm
	}

	for _, s := range shifts {
		base, err := time.Parse("2006-01-02", s.Date)
		if err != nil {
			continue
		}
		for _, name := range s.MemberNames {
			// 晚：当天晚上查寝
			byPerson[name] = append(byPerson[name], dutyPoint{
				dayOrder: weekdayOrder(s.WeekdayCN), dayCN: s.WeekdayCN,
				label: "晚上", building: s.Building,
			})
			// 早：次日上午（周五=早晨宿舍锁门后，其余=上午大课间）
			morning := base.AddDate(0, 0, 1)
			mLabel := "上午大课间"
			if morning.Weekday() == time.Friday {
				mLabel = "早晨宿舍锁门后"
			}
			mCN := templateWeekdayCN[morning.Weekday()]
			byPerson[name] = append(byPerson[name], dutyPoint{
				dayOrder: weekdayOrder(mCN), dayCN: mCN, label: mLabel, building: s.Building,
			})
		}
	}

	// 同日同楼同标签去重（一人管多层时合并）
	for name, pts := range byPerson {
		seen := map[string]bool{}
		deduped := pts[:0]
		for _, d := range pts {
			key := fmt.Sprintf("%d|%s|%s", d.dayOrder, d.label, d.building)
			if !seen[key] {
				seen[key] = true
				deduped = append(deduped, d)
			}
		}
		byPerson[name] = deduped
	}

	build := func(points []dutyPoint) string {
		// 楼栋按首次出现排序，组内按日期排序、连续同标签日期压缩
		groupOrder := make([]string, 0)
		byBuilding := map[string][]dutyPoint{}
		for _, d := range points {
			if _, ok := byBuilding[d.building]; !ok {
				groupOrder = append(groupOrder, d.building)
			}
			byBuilding[d.building] = append(byBuilding[d.building], d)
		}
		var parts []string
		for _, b := range groupOrder {
			pts := byBuilding[b]
			sort.SliceStable(pts, func(i, j int) bool { return pts[i].dayOrder < pts[j].dayOrder })
			var segs []string
			i := 0
			for i < len(pts) {
				j := i
				for j+1 < len(pts) && pts[j+1].label == pts[i].label && pts[j+1].dayOrder == pts[j].dayOrder+1 {
					j++
				}
				dayStr := pts[i].dayCN
				if j > i {
					for k := i + 1; k <= j; k++ {
						dayStr += "、" + strings.TrimPrefix(pts[k].dayCN, "周")
					}
				}
				segs = append(segs, dayStr+pts[i].label)
				i = j + 1
			}
			parts = append(parts, strings.Join(segs, "、")+"（"+b+"）")
		}
		return strings.Join(parts, "，")
	}

	names := make([]string, 0, len(byPerson))
	for name := range byPerson {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		gi, gj := gradeOf(classOf[names[i]]), gradeOf(classOf[names[j]])
		if gi != gj {
			return gi < gj
		}
		return names[i] < names[j]
	})

	for _, name := range names {
		pts := byPerson[name]
		dbPts := make([]dutyPoint, 0, len(pts))
		nPts := make([]dutyPoint, 0, len(pts))
		for _, d := range pts {
			if d.label == "晚上" {
				nPts = append(nPts, d)
			} else {
				dbPts = append(dbPts, d)
			}
		}
		row := TemplatePersonRow{Name: name, ClassName: classOf[name], DormNumber: dormOf[name]}
		if len(dbPts) > 0 {
			row.WorkDesc = build(dbPts)
			dayBreak = append(dayBreak, row)
		}
		if len(nPts) > 0 {
			nRow := row
			nRow.WorkDesc = build(nPts)
			night = append(night, nRow)
		}
	}
	return dayBreak, night
}

// weekdayOrder 周日=0 … 周六=6（周表日期顺序口径）
func weekdayOrder(dayCN string) int {
	switch dayCN {
	case "周日":
		return 0
	case "周一":
		return 1
	case "周二":
		return 2
	case "周三":
		return 3
	case "周四":
		return 4
	case "周五":
		return 5
	case "周六":
		return 6
	}
	return 9
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP 接口
// ─────────────────────────────────────────────────────────────────────────────

// PreviewTemplateSchedule POST /minister/template-schedule/preview
func (tc *TemplateScheduleController) PreviewTemplateSchedule(c *gin.Context) {
	var req TemplateScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数有误：" + err.Error()})
		return
	}

	dept, _, err := resolveDepartment(c, &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pool, err := buildMemberPool(&req, dept)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	preview, err := generatePreview(&req, dept, pool)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"preview":      preview,
		"member_count": len(pool),
	})
}

// ConfirmTemplateSchedule POST /minister/template-schedule/confirm
// 确认落库：建计划（快照网格与参数）+ 班次；同部门同类型旧 active 计划归档
// 并清理其未核销班次（已核销/旷工保留留痕）。历史计划全部可查可下载。
func (tc *TemplateScheduleController) ConfirmTemplateSchedule(c *gin.Context) {
	userID := c.GetUint("user_id")
	var payload struct {
		Request TemplateScheduleRequest `json:"request"`
		Preview TemplateSchedulePreview `json:"preview"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Preview.Title == "" || len(payload.Preview.Shifts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "预览数据缺失或格式有误，请重新生成预览"})
		return
	}

	configJSON, _ := json.Marshal(payload.Request)
	snapshot := payload.Preview
	snapshot.Shifts = nil
	snapshotJSON, _ := json.Marshal(snapshot)

	plan := model.SchedulePlan{
		Title:       payload.Preview.Title,
		RuleType:    "template_week",
		StartDate:   payload.Request.StartDate,
		EndDate:     payload.Request.EndDate,
		Description: fmt.Sprintf("%s · %s · %s：班次 %d 条", schedTypeLabel(payload.Preview.SchedType), payload.Preview.Department, payload.Preview.DateRangeCN, len(payload.Preview.Shifts)),
		CreatedBy:   userID,
		Status:      "active",
		CreatedAt:   time.Now(),
		SchedType:   payload.Preview.SchedType,
		Department:  payload.Preview.Department,
		GridJSON:    string(snapshotJSON),
		ConfigJSON:  string(configJSON),
	}
	if err := repository.DB.Create(&plan).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "排班计划创建失败：" + err.Error()})
		return
	}

	shifts := make([]model.ScheduleShift, 0, len(payload.Preview.Shifts))
	for _, s := range payload.Preview.Shifts {
		idsJSON, _ := json.Marshal(s.MemberIDs)
		shifts = append(shifts, model.ScheduleShift{
			PlanID:        plan.ID,
			Date:          s.Date,
			WeekType:      s.WeekType,
			ShiftPeriod:   s.ShiftPeriod,
			Building:      s.Building,
			Floor:         s.Floor,
			MemberIDsJSON: string(idsJSON),
			MemberNames:   strings.Join(s.MemberNames, ", "),
			ManagerName:   s.ManagerName,
			Status:        "scheduled",
			Note:          fmt.Sprintf("模板排班 %s", payload.Preview.WeekKey),
			CreatedAt:     time.Now(),
		})
	}
	if err := repository.DB.Create(&shifts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "班次写入失败：" + err.Error()})
		return
	}

	// 归档同部门同类型的旧 active 计划，并清理其未核销班次
	var oldPlans []model.SchedulePlan
	repository.DB.Where("rule_type = ? AND sched_type = ? AND department = ? AND status = ? AND id <> ?",
		"template_week", payload.Preview.SchedType, payload.Preview.Department, "active", plan.ID).Find(&oldPlans)
	archived := 0
	for _, p := range oldPlans {
		repository.DB.Where("plan_id = ? AND status = ?", p.ID, "scheduled").Delete(&model.ScheduleShift{})
		repository.DB.Model(&p).Update("status", "archived")
		archived++
	}

	// 同部门同类型且日期完全相同的旧归档版本一并删除：
	// 同一份周表反复确认时不该在历史里留下同名重复条目
	var dupePlans []model.SchedulePlan
	repository.DB.Where("rule_type = ? AND sched_type = ? AND department = ? AND start_date = ? AND end_date = ? AND status <> ? AND id <> ?",
		"template_week", payload.Preview.SchedType, payload.Preview.Department,
		payload.Request.StartDate, payload.Request.EndDate, "active", plan.ID).Find(&dupePlans)
	deduped := 0
	for _, p := range dupePlans {
		repository.DB.Where("plan_id = ?", p.ID).Delete(&model.ScheduleShift{})
		repository.DB.Delete(&p)
		deduped++
	}

	var operator model.User
	repository.DB.First(&operator, userID)
	repository.DB.Create(&model.OperationLog{
		Action:       "template_schedule_confirm",
		TargetType:   "schedule_plan",
		TargetID:     plan.ID,
		OperatorID:   operator.ID,
		OperatorName: operator.RealName,
		OperatorRole: operator.Role,
		Detail:       fmt.Sprintf("确认%s【%s】（%s · %s）：班次 %d 条，归档旧版 %d 个，清理同日期旧版 %d 个", schedTypeLabel(payload.Preview.SchedType), plan.Title, payload.Preview.Department, payload.Preview.DateRangeCN, len(shifts), archived, deduped),
		IP:           c.ClientIP(),
	})

	c.JSON(http.StatusOK, gin.H{
		"message":         fmt.Sprintf("%s【%s】已确认生效，共 %d 条班次；归档旧版 %d 个", schedTypeLabel(payload.Preview.SchedType), plan.Title, len(shifts), archived),
		"plan_id":         plan.ID,
		"generated_count": len(shifts),
		"archived_plans":  archived,
	})
}

// CurrentTemplateSchedule GET /minister/template-schedule/current?sched_type=&department=
// 返回该部门该类型当前生效的排班（快照直接回放）。
func (tc *TemplateScheduleController) CurrentTemplateSchedule(c *gin.Context) {
	schedType := c.DefaultQuery("sched_type", SchedTypeDuty)
	dept := c.Query("department")

	var plan model.SchedulePlan
	q := repository.DB.Where("rule_type = ? AND sched_type = ? AND status = ?", "template_week", schedType, "active")
	if dept != "" {
		q = q.Where("department = ?", dept)
	}
	if err := q.Order("id desc").First(&plan).Error; err != nil {
		c.JSON(http.StatusOK, gin.H{"exists": false})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"exists": true,
		"plan": gin.H{
			"id":         plan.ID,
			"title":      plan.Title,
			"sched_type": plan.SchedType,
			"department": plan.Department,
			"start_date": plan.StartDate,
			"end_date":   plan.EndDate,
			"created_at": plan.CreatedAt,
			"snapshot":   json.RawMessage(plan.GridJSON),
		},
	})
}

// ── 历史版本删除（需密码：首次删除时设置，之后每次输入验证）──────────────────

// appKeyDeletePwd AppSetting 里存删除密码 bcrypt 哈希的键
const appKeyDeletePwd = "template_schedule_delete_pwd"

// getDeletePwdHash 读取删除密码哈希；空串表示尚未设置
func getDeletePwdHash() string {
	var setting model.AppSetting
	if err := repository.DB.Where("`key` = ?", appKeyDeletePwd).First(&setting).Error; err != nil {
		return ""
	}
	return setting.Value
}

// DeletePwdStatus GET /minister/template-schedule/delete-pwd/status
func (tc *TemplateScheduleController) DeletePwdStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"set": getDeletePwdHash() != ""})
}

// SetDeletePwd POST /minister/template-schedule/delete-pwd
// 未设置时可自由设置；已设置则必须携带 old_password 原密码才能修改。
func (tc *TemplateScheduleController) SetDeletePwd(c *gin.Context) {
	var req struct {
		Password    string `json:"password"`
		OldPassword string `json:"old_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(strings.TrimSpace(req.Password)) < 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 4 位"})
		return
	}
	if existing := getDeletePwdHash(); existing != "" {
		if bcrypt.CompareHashAndPassword([]byte(existing), []byte(req.OldPassword)) != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "原密码不正确，无法修改删除密码"})
			return
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}
	var setting model.AppSetting
	if err := repository.DB.Where("`key` = ?", appKeyDeletePwd).First(&setting).Error; err == nil {
		setting.Value = string(hash)
		repository.DB.Save(&setting)
	} else {
		repository.DB.Create(&model.AppSetting{Key: appKeyDeletePwd, Value: string(hash)})
	}
	userID := c.GetUint("user_id")
	var operator model.User
	repository.DB.First(&operator, userID)
	repository.DB.Create(&model.OperationLog{
		Action: "template_schedule_delete_pwd", TargetType: "app_setting",
		OperatorID: operator.ID, OperatorName: operator.RealName, OperatorRole: operator.Role,
		Detail: "设置/修改了排班历史版本的删除密码", IP: c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"message": "删除密码已设置"})
}

// DeleteTemplatePlan POST /minister/template-schedule/plans/:id/delete
// 删除一个历史（已归档）版本及其班次；生效中的版本请用「清空」。
func (tc *TemplateScheduleController) DeleteTemplatePlan(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入删除密码"})
		return
	}
	hash := getDeletePwdHash()
	if hash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "尚未设置删除密码"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		userID := c.GetUint("user_id")
		var operator model.User
		repository.DB.First(&operator, userID)
		repository.DB.Create(&model.OperationLog{
			Action: "template_schedule_delete_fail", TargetType: "schedule_plan", TargetID: 0,
			OperatorID: operator.ID, OperatorName: operator.RealName, OperatorRole: operator.Role,
			Detail: "删除历史版本失败：密码不正确", IP: c.ClientIP(),
		})
		c.JSON(http.StatusForbidden, gin.H{"error": "删除密码不正确"})
		return
	}

	var plan model.SchedulePlan
	if err := repository.DB.Where("rule_type = ? AND id = ?", "template_week", c.Param("id")).First(&plan).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "排班版本不存在"})
		return
	}
	if plan.Status == "active" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "生效中的排班不能用删除，请使用「清空」按钮"})
		return
	}
	repository.DB.Where("plan_id = ?", plan.ID).Delete(&model.ScheduleShift{})
	repository.DB.Delete(&plan)

	userID := c.GetUint("user_id")
	var operator model.User
	repository.DB.First(&operator, userID)
	repository.DB.Create(&model.OperationLog{
		Action: "template_schedule_delete", TargetType: "schedule_plan", TargetID: plan.ID,
		OperatorID: operator.ID, OperatorName: operator.RealName, OperatorRole: operator.Role,
		Detail: fmt.Sprintf("删除历史版本【%s】（%s ~ %s）及其班次", plan.Title, plan.StartDate, plan.EndDate),
		IP:     c.ClientIP(),
	})
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已删除历史版本【%s】", plan.Title)})
}

// VersionTemplateSchedule GET /minister/template-schedule/version
// 排班内容变更版本号 = 最近一次排班相关操作（确认/清空/删除）的留痕 ID。
// 所有打开排班中枢页面的账号轮询该值，变化即重载本地视图，实现多账号同步。
func (tc *TemplateScheduleController) VersionTemplateSchedule(c *gin.Context) {
	var maxID int64
	repository.DB.Model(&model.OperationLog{}).
		Where("action IN ?", []string{"template_schedule_confirm", "template_schedule_clear", "template_schedule_delete"}).
		Select("COALESCE(MAX(id),0)").Scan(&maxID)
	c.JSON(http.StatusOK, gin.H{"version": maxID})
}

// ClearTemplateSchedule POST /minister/template-schedule/clear
// 清空当前生效的排班预览：本部门 duty 计划及其全部班次删除，留操作留痕。
// 历史版本与已下载文件不受影响；清空后可重新生成，下载则得到空白模板表格。
func (tc *TemplateScheduleController) ClearTemplateSchedule(c *gin.Context) {
	var req TemplateScheduleRequest
	_ = c.ShouldBindJSON(&req)
	dept, operator, err := resolveDepartment(c, &req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var plans []model.SchedulePlan
	repository.DB.Where("rule_type = ? AND sched_type = ? AND department = ? AND status = ?",
		"template_week", SchedTypeDuty, dept, "active").Find(&plans)

	cleared := 0
	for _, p := range plans {
		repository.DB.Where("plan_id = ?", p.ID).Delete(&model.ScheduleShift{})
		if err := repository.DB.Delete(&p).Error; err == nil {
			cleared++
		}
	}

	repository.DB.Create(&model.OperationLog{
		Action:       "template_schedule_clear",
		TargetType:   "schedule_plan",
		OperatorID:   operator.ID,
		OperatorName: operator.RealName,
		OperatorRole: operator.Role,
		Detail:       fmt.Sprintf("清空%s的生效排班预览：%d 份计划及其班次", dept, cleared),
		IP:           c.ClientIP(),
	})

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("已清空 %s 的生效排班（%d 份）", dept, cleared),
		"cleared": cleared,
	})
}

// HistoryTemplateSchedule GET /minister/template-schedule/history?sched_type=&department=&limit=
// 历史版本列表（含当前生效版，最新的在前）。
func (tc *TemplateScheduleController) HistoryTemplateSchedule(c *gin.Context) {
	schedType := c.Query("sched_type")
	dept := c.Query("department")
	limit := 20

	q := repository.DB.Where("rule_type = ?", "template_week")
	if schedType != "" {
		q = q.Where("sched_type = ?", schedType)
	}
	if dept != "" {
		q = q.Where("department = ?", dept)
	}
	var plans []model.SchedulePlan
	q.Order("id desc").Limit(limit).Find(&plans)

	// 创建人姓名映射（留痕谁生成的版本）
	creatorIDs := make([]uint, 0, len(plans))
	for _, p := range plans {
		creatorIDs = append(creatorIDs, p.CreatedBy)
	}
	creatorNames := map[uint]string{}
	var creators []model.User
	repository.DB.Where("id IN ?", creatorIDs).Find(&creators)
	for _, u := range creators {
		creatorNames[u.ID] = u.RealName
	}

	items := make([]gin.H, 0, len(plans))
	for _, p := range plans {
		var shiftCount int64
		repository.DB.Model(&model.ScheduleShift{}).Where("plan_id = ?", p.ID).Count(&shiftCount)
		items = append(items, gin.H{
			"id":          p.ID,
			"title":       p.Title,
			"sched_type":  p.SchedType,
			"department":  p.Department,
			"date_range":  dateRangeCN(p.StartDate, p.EndDate),
			"start_date":  p.StartDate,
			"end_date":    p.EndDate,
			"status":      p.Status,
			"shift_count": shiftCount,
			"created_by":  creatorNames[p.CreatedBy],
			"created_at":  p.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": len(items)})
}

// GetTemplateScheduleDetail GET /minister/template-schedule/:id —— 历史版本回看（快照回放）
func (tc *TemplateScheduleController) GetTemplateScheduleDetail(c *gin.Context) {
	var plan model.SchedulePlan
	if err := repository.DB.Where("rule_type = ? AND id = ?", "template_week", c.Param("id")).First(&plan).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "排班版本不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"plan": gin.H{
			"id":         plan.ID,
			"title":      plan.Title,
			"sched_type": plan.SchedType,
			"department": plan.Department,
			"start_date": plan.StartDate,
			"end_date":   plan.EndDate,
			"status":     plan.Status,
			"created_at": plan.CreatedAt,
			"snapshot":   json.RawMessage(plan.GridJSON),
		},
	})
}

// writeDutySheet 排班表版式：标题行 + 每栋楼（楼名行、列头行、楼层行…）
func writeDutySheet(f *excelize.File, sheet, title string, snap *TemplateSchedulePreview) {
	row := 1
	f.SetCellValue(sheet, fmt.Sprintf("A%d", row), title)
	row += 2

	for _, b := range snap.Buildings {
		f.SetCellValue(sheet, fmt.Sprintf("A%d", row), b.Name)
		row++
		f.SetCellValue(sheet, fmt.Sprintf("A%d", row), "工作时间/工作楼层")
		for i, col := range b.Cols {
			cell, _ := excelize.CoordinatesToCellName(2+i, row)
			f.SetCellValue(sheet, cell, col)
		}
		row++
		for _, r := range b.Rows {
			f.SetCellValue(sheet, fmt.Sprintf("A%d", row), r.Floor)
			for i, names := range r.Cells {
				cell, _ := excelize.CoordinatesToCellName(2+i, row)
				f.SetCellValue(sheet, cell, strings.Join(names, "、"))
			}
			row++
		}
		row++
	}
}

// writePersonSheet 大课间/夜间版式（对齐模板）：
// 标题行 + "姓名 | 班级 | 工作时间、地点"（夜间多一列宿舍号）+ 每名部员一行，
// 每格为该天负责的楼栋（如 "八号楼"），与预览矩阵一致。
func writePersonSheet(f *excelize.File, sheet, title string, snap *TemplateSchedulePreview) {
	f.SetCellValue(sheet, "A1", title)
	f.SetCellValue(sheet, "A3", "姓名")
	f.SetCellValue(sheet, "B3", "班级")
	col := 3
	if snap.SchedType == SchedTypeNight {
		f.SetCellValue(sheet, "C3", "宿舍号")
		col = 4
	}
	for i, c := range snap.Cols {
		cell, _ := excelize.CoordinatesToCellName(col+i, 3)
		f.SetCellValue(sheet, cell, c)
	}
	r := 4
	for _, p := range snap.Persons {
		f.SetCellValue(sheet, fmt.Sprintf("A%d", r), p.Name)
		f.SetCellValue(sheet, fmt.Sprintf("B%d", r), p.ClassName)
		ci := 3
		if snap.SchedType == SchedTypeNight {
			f.SetCellValue(sheet, fmt.Sprintf("C%d", r), p.DormNumber)
			ci = 4
		}
		for i, cellVal := range p.Cells {
			cell, _ := excelize.CoordinatesToCellName(ci+i, r)
			f.SetCellValue(sheet, cell, cellVal)
		}
		r++
	}
}
