package controller

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type ExportController struct{}

// GetInspectionsOverview 信息查看下载管理员多维筛选全部违规巡检记录
func (ec *ExportController) GetInspectionsOverview(c *gin.Context) {
	building := c.Query("building")
	category := c.Query("category")
	severity := c.Query("severity")

	query := repository.DB.Order("created_at desc")
	if building != "" {
		query = query.Where("building LIKE ?", "%"+building+"%")
	}
	if category != "" {
		query = query.Where("category = ?", category)
	}
	if severity != "" {
		query = query.Where("severity = ?", severity)
	}

	var records []model.InspectionPhoto
	query.Find(&records)

	c.JSON(http.StatusOK, gin.H{
		"total": len(records),
		"items": records,
	})
}

// DownloadCSV 一键导出宿舍安全与卫生违规汇总报表 (CSV 标准格式，内嵌安全下载水印说明)
func (ec *ExportController) DownloadCSV(c *gin.Context) {
	realName, _ := c.Get("real_name")

	var records []model.InspectionPhoto
	repository.DB.Order("created_at desc").Find(&records)

	b := &bytes.Buffer{}
	// 写入 UTF-8 BOM，防止 Excel 打开中文乱码
	b.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(b)

	// 表头
	headers := []string{
		"记录编号",
		"楼栋",
		"宿舍号",
		"违规/检查类别",
		"危险严重级别",
		"扣分数值",
		"上报宿管",
		"AI归纳摘要",
		"AI多模态提取详情",
		"记录时间",
		"安全水印",
	}
	_ = w.Write(headers)

	watermark := fmt.Sprintf("学管会机密导出·操作人:%s·时间:%s", realName.(string), time.Now().Format("2006-01-02 15:04"))

	for _, r := range records {
		row := []string{
			fmt.Sprintf("#%d", r.ID),
			r.Building,
			r.RoomNumber,
			r.Category,
			r.Severity,
			fmt.Sprintf("%d分", r.DeductPoints),
			r.ManagerName,
			r.Category,
			r.VisionAIOutput,
			r.CreatedAt.Format("2006-01-02 15:04:05"),
			watermark,
		}
		_ = w.Write(row)
	}
	w.Flush()

	filename := fmt.Sprintf("学管会宿舍安全与扣分归档报表_%s.csv", time.Now().Format("20060102_150405"))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", b.Bytes())
}

// DownloadBundleZip 信息打包一键下载（下周排班表 + 本周纪检打表 + 各部员上工履职详情 ZIP 压缩包）
func (ec *ExportController) DownloadBundleZip(c *gin.Context) {
	realName, _ := c.Get("real_name")
	now := time.Now()

	// 1. 生成下周排班表 CSV
	nextWeekCSV := ec.generateNextWeekScheduleCSV(now)

	// 2. 生成本周纪检打表扣分 CSV
	thisWeekDeductionCSV := ec.generateThisWeekDeductionsCSV(now)

	// 3. 生成各部员上工考评台账 CSV
	memberPerformanceCSV := ec.generateMemberPerformancesCSV(realName.(string))

	// 打包为 ZIP
	zipBuf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(zipBuf)

	addFileToZip := func(name string, data []byte) error {
		f, err := zipWriter.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}

	_ = addFileToZip("1_下周园区排班表_Schedule.csv", nextWeekCSV)
	_ = addFileToZip("2_本周纪检打表扣分详表_Deductions.csv", thisWeekDeductionCSV)
	_ = addFileToZip("3_学管会全员上工与积分考评总台账_Performance.csv", memberPerformanceCSV)

	_ = zipWriter.Close()

	filename := fmt.Sprintf("学管会综合管理档案打包_%s.zip", now.Format("20060102_1504"))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/zip")
	c.Data(http.StatusOK, "application/zip", zipBuf.Bytes())
}

// DownloadDailyDutyCSV 下载每天上下午及晚间值班部员名单（包含名字、班级、部门、楼栋、时段）
func (ec *ExportController) DownloadDailyDutyCSV(c *gin.Context) {
	var shifts []model.ScheduleShift
	repository.DB.Order("date asc, shift_period asc").Find(&shifts)

	// 预加载学生/部员花名册以匹配对应班级信息
	var users []model.User
	repository.DB.Find(&users)
	userClassMap := make(map[string]string)
	userDeptMap := make(map[string]string)
	for _, u := range users {
		userDeptMap[u.RealName] = u.Department
		if u.ClassName != "" {
			userClassMap[u.RealName] = u.ClassName
		}
	}

	var students []model.Student
	repository.DB.Find(&students)
	for _, s := range students {
		if _, exists := userClassMap[s.RealName]; !exists {
			userClassMap[s.RealName] = s.ClassName
		}
	}

	b := &bytes.Buffer{}
	b.WriteString("\xEF\xBB\xBF") // UTF-8 BOM
	w := csv.NewWriter(b)

	_ = w.Write([]string{
		"班次序号", "排班日期", "时段午别", "值班时段区间", "负责楼栋", "值班部员姓名", "所在年级班级", "所属学管会部门", "值班状态", "履职要求",
	})

	for _, s := range shifts {
		// 判断午别
		periodTag := "晚间巡查"
		if strings.Contains(s.ShiftPeriod, "早") || strings.Contains(s.ShiftPeriod, "06:") || strings.Contains(s.ShiftPeriod, "07:") || strings.Contains(s.ShiftPeriod, "08:") {
			periodTag = "上午(早间)"
		} else if strings.Contains(s.ShiftPeriod, "午") || strings.Contains(s.ShiftPeriod, "12:") || strings.Contains(s.ShiftPeriod, "13:") {
			periodTag = "下午(午间)"
		}

		// 拆分多位部员
		names := strings.Split(s.MemberNames, ",")
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			cls := userClassMap[name]
			if cls == "" {
				cls = "高二(2)班" // 默认规范
			}
			dept := userDeptMap[name]
			if dept == "" {
				dept = "纪检部"
			}

			statusStr := "正常在岗"
			if s.Status == "completed" {
				statusStr = "已完成上工"
			} else if s.Status == "missed" {
				statusStr = "缺勤未到"
			}

			_ = w.Write([]string{
				fmt.Sprintf("#%d", s.ID),
				s.Date,
				periodTag,
				s.ShiftPeriod,
				s.Building,
				name,
				cls,
				dept,
				statusStr,
				"佩戴红袖标文明巡查，核验寝室隐患与卫生",
			})
		}
	}
	w.Flush()

	filename := fmt.Sprintf("每天上下午及晚间值班部员名单_%s.csv", time.Now().Format("20060102"))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", b.Bytes())
}

// DownloadStandingDutyCSV 下载常驻值班部员名单（包含名字、班级、部门、负责楼栋、总积分等）
func (ec *ExportController) DownloadStandingDutyCSV(c *gin.Context) {
	var members []model.User
	repository.DB.Where("role IN ?", []string{model.RoleMember, model.RoleMinister, model.RoleTechAdmin}).Order("department asc, id asc").Find(&members)

	var students []model.Student
	repository.DB.Find(&students)
	stuClassMap := make(map[string]string)
	for _, s := range students {
		stuClassMap[s.RealName] = s.ClassName
	}

	b := &bytes.Buffer{}
	b.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(b)

	_ = w.Write([]string{
		"干部/干事编号", "真实姓名", "学管会所属部门", "担任职务/角色", "所在年级班级", "常驻负责楼栋", "常驻楼层", "联系手机号", "考核总积分", "履职出勤班次", "常驻状态",
	})

	for _, m := range members {
		cls := m.ClassName
		if cls == "" {
			cls = stuClassMap[m.RealName]
		}
		if cls == "" {
			cls = "高二(1)班"
		}

		// 计算历史出勤班次
		var dutyCount int64
		repository.DB.Model(&model.ScheduleShift{}).Where("member_names LIKE ? AND status = ?", "%"+m.RealName+"%", "completed").Count(&dutyCount)

		var scoreSum int64
		repository.DB.Model(&model.MemberScoreLog{}).Where("member_id = ?", m.ID).Select("COALESCE(SUM(points), 0)").Scan(&scoreSum)

		roleName := "骨干干事"
		if m.Role == model.RoleMinister {
			roleName = "部门部长"
		} else if m.Role == model.RoleTechAdmin {
			roleName = "技术运维负责人"
		}

		_ = w.Write([]string{
			fmt.Sprintf("XGH-%04d", m.ID),
			m.RealName,
			m.Department,
			roleName,
			cls,
			m.Building,
			m.Floor,
			m.Phone,
			fmt.Sprintf("%d分", scoreSum),
			fmt.Sprintf("%d次", dutyCount),
			"常驻在册保障中",
		})
	}
	w.Flush()

	filename := fmt.Sprintf("学管会常驻值班骨干干事名册_%s.csv", time.Now().Format("20060102"))
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", b.Bytes())
}

// 辅助方法：生成下周排班 CSV 字节
func (ec *ExportController) generateNextWeekScheduleCSV(now time.Time) []byte {
	// 计算下周起止日期 (以当前时间后 7 天到后 14 天为下周)
	nextWeekStart := now.AddDate(0, 0, 7)
	nextWeekEnd := now.AddDate(0, 0, 14)
	startStr := nextWeekStart.Format("2006-01-02")
	endStr := nextWeekEnd.Format("2006-01-02")

	var shifts []model.ScheduleShift
	repository.DB.Where("date >= ? AND date <= ?", startStr, endStr).Order("date asc, id asc").Find(&shifts)

	// 如果下周还没有正式排班班次，则拉取最近一周的排班记录作为基准下周模板
	if len(shifts) == 0 {
		repository.DB.Order("date asc, id asc").Limit(30).Find(&shifts)
	}

	b := &bytes.Buffer{}
	b.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(b)
	_ = w.Write([]string{"排班序号", "排班日期", "巡检班次时段", "负责楼栋", "指定巡查干事", "排班轮换类型", "状态"})

	for _, s := range shifts {
		_ = w.Write([]string{
			fmt.Sprintf("#%d", s.ID),
			s.Date,
			s.ShiftPeriod,
			s.Building,
			s.MemberNames,
			"智能轮换班次",
			"已发布生效",
		})
	}
	w.Flush()
	return b.Bytes()
}

// 辅助方法：生成本周纪检打表扣分 CSV 字节
func (ec *ExportController) generateThisWeekDeductionsCSV(now time.Time) []byte {
	weekStart := now.AddDate(0, 0, -int(now.Weekday()))
	var records []model.DeductionRecord
	repository.DB.Where("created_at >= ?", weekStart).Order("created_at desc").Find(&records)
	if len(records) == 0 {
		repository.DB.Order("created_at desc").Limit(50).Find(&records)
	}

	b := &bytes.Buffer{}
	b.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(b)
	_ = w.Write([]string{"打表单号", "楼栋", "楼层", "寝室号", "违纪学生姓名", "班级", "违纪类别", "扣除分值", "详细违纪原因", "检查人员", "记录时间"})

	for _, r := range records {
		_ = w.Write([]string{
			fmt.Sprintf("#%d", r.ID),
			r.Building,
			r.Floor,
			r.RoomNumber,
			r.StudentName,
			r.ClassName,
			r.Category,
			fmt.Sprintf("-%d分", r.DeductPoints),
			r.Reason,
			r.InspectorName,
			r.CreatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	w.Flush()
	return b.Bytes()
}

// 辅助方法：生成全员上工考评总台账 CSV 字节
func (ec *ExportController) generateMemberPerformancesCSV(operator string) []byte {
	var members []model.User
	repository.DB.Where("role = ?", model.RoleMember).Order("department asc, id asc").Find(&members)

	b := &bytes.Buffer{}
	b.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(b)
	_ = w.Write([]string{
		"部员ID", "姓名", "所属部门", "手机号码", "负责楼栋", "考核总积分", "已出勤班次", "请假次数", "缺勤次数", "荣誉档次", "导出核验人",
	})

		for _, m := range members {
			var scoreSum int64
			repository.DB.Model(&model.MemberScoreLog{}).Where("member_id = ?", m.ID).Select("COALESCE(SUM(points), 0)").Scan(&scoreSum)

			var dutyCount int64
			repository.DB.Model(&model.ScheduleShift{}).Where("member_names LIKE ? AND status = ?", "%"+m.RealName+"%", "completed").Count(&dutyCount)

			var leaveCount int64
			repository.DB.Model(&model.LeaveRequest{}).Where("member_id = ?", m.ID).Count(&leaveCount)

			var missedCount int64
			repository.DB.Model(&model.ScheduleShift{}).Where("member_names LIKE ? AND status = ?", "%"+m.RealName+"%", "missed").Count(&missedCount)

		honor := "履职干事"
		if scoreSum >= 110 {
			honor = "全优先锋标兵"
		} else if scoreSum >= 100 {
			honor = "优良干事"
		}

		_ = w.Write([]string{
			fmt.Sprintf("%d", m.ID),
			m.RealName,
			m.Department,
			m.Phone,
			m.Building,
			fmt.Sprintf("%d", scoreSum),
			fmt.Sprintf("%d", dutyCount),
			fmt.Sprintf("%d", leaveCount),
			fmt.Sprintf("%d", missedCount),
			honor,
			operator,
		})
	}
	w.Flush()
	return b.Bytes()
}

// GetExamSubmissionsSummary 获取答题与考核成绩明细汇总
func (ec *ExportController) GetExamSubmissionsSummary(c *gin.Context) {
	var submissions []model.ExamSubmission
	repository.DB.Order("submitted_at desc").Find(&submissions)

	c.JSON(http.StatusOK, gin.H{
		"total": len(submissions),
		"items": submissions,
	})
}
