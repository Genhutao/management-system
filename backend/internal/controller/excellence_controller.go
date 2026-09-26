package controller

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type ExcellenceController struct{}

// requireExcellenceReader 评优榜单是只读报表，读者是部长/档案导出岗/技术维护组。
// 不复用 requireDeductionAuthority：那道闸门只放技术部副部长与技术维护组，会把部长挡在外面。
func requireExcellenceReader(c *gin.Context) (model.User, bool) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return operator, false
	}
	if operator.Status == "disabled" {
		c.JSON(http.StatusForbidden, gin.H{"error": "该账号已被停用"})
		return operator, false
	}
	switch operator.Role {
	case model.RoleMinister, model.RoleTechAdmin, model.RoleViewerExport:
		return operator, true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "评优榜单仅限部长、档案导出岗与技术维护组查看"})
	return operator, false
}

// RoomExcellenceRow 单个寝室的纪律汇总（评优数据源）
type RoomExcellenceRow struct {
	Building         string `json:"building"`
	RoomNumber       string `json:"room_number"`
	TotalDeduct      int    `json:"total_deduct"`
	RecordCount      int64  `json:"record_count"`
	InvolvedStudents int64  `json:"involved_students"`
}

// GetRoomExcellence 文明标兵寝室评选数据：按寝室聚合未撤销扣分，分最低的排最前。
// 文档 5.2/25 "期末评选文明标兵寝室" 的数据落地。
func (ec *ExcellenceController) GetRoomExcellence(c *gin.Context) {
	if _, ok := requireExcellenceReader(c); !ok {
		return
	}

	grade := c.Query("grade")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if limit < 1 || limit > 50 {
		limit = 10
	}

	// 只有绑定了名册主键的扣分才能可靠归到寝室
	q := repository.DB.Model(&model.DeductionRecord{}).
		Select("building, room_number, COALESCE(SUM(deduct_points),0) as total_deduct, COUNT(*) as record_count, COUNT(DISTINCT student_id) as involved_students").
		Where("status <> ? AND student_id > 0", "revoked").
		Group("building, room_number")
	if grade != "" {
		q = q.Where("grade = ?", grade)
	}

	var rows []RoomExcellenceRow
	if err := q.Order("total_deduct asc, record_count asc, building asc, room_number asc").
		Limit(limit).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "聚合失败: " + err.Error()})
		return
	}

	// 参与对比的全校概览
	var overall struct {
		Records int64
		Points  int64
	}
	repository.DB.Model(&model.DeductionRecord{}).
		Select("COUNT(*) as records, COALESCE(SUM(deduct_points),0) as points").
		Where("status <> ? AND student_id > 0", "revoked").Scan(&overall)

	var studentCount int64
	repository.DB.Model(&model.Student{}).Where("status = ?", "active").Count(&studentCount)

	// 无任何已关联数据时如实说明，不编造榜单
	note := ""
	if overall.Records == 0 {
		note = "当前没有任何已关联名册主键的扣分记录，无法形成客观排名；等打表数据积累后再评。"
	}

	c.JSON(http.StatusOK, gin.H{
		"total_linked_records": overall.Records,
		"total_linked_points":  overall.Points,
		"active_students":      studentCount,
		"limit":                limit,
		"grade":                grade,
		"note":                 note,
		"items":                rows,
	})
}

// ExportRoomExcellenceCSV 导出文明标兵寝室评选表（评优留痕用）
func (ec *ExcellenceController) ExportRoomExcellenceCSV(c *gin.Context) {
	if _, ok := requireExcellenceReader(c); !ok {
		return
	}

	grade := c.Query("grade")
	q := repository.DB.Model(&model.DeductionRecord{}).
		Select("building, room_number, COALESCE(SUM(deduct_points),0) as total_deduct, COUNT(*) as record_count, COUNT(DISTINCT student_id) as involved_students").
		Where("status <> ? AND student_id > 0", "revoked").
		Group("building, room_number")
	if grade != "" {
		q = q.Where("grade = ?", grade)
	}

	var rows []RoomExcellenceRow
	q.Order("total_deduct asc, record_count asc, building asc, room_number asc").Limit(200).Find(&rows)

	filename := fmt.Sprintf("文明标兵寝室评选_%s.csv", time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	_ = writer.Write([]string{"名次", "楼栋", "寝室号", "累计扣分", "违纪记录数", "涉及学生数"})
	for i, r := range rows {
		_ = writer.Write([]string{
			fmt.Sprintf("%d", i+1), r.Building, r.RoomNumber,
			fmt.Sprintf("%d", r.TotalDeduct), fmt.Sprintf("%d", r.RecordCount), fmt.Sprintf("%d", r.InvolvedStudents),
		})
	}
}
