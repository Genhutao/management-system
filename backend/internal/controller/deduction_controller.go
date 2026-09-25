package controller

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
	"xgh-system/pkg/ai"
)

type DeductionController struct{}

// requireDeductionAuthority 打表权限的唯一服务端判定入口。
//
// 文档规定"全校宿舍违纪扣分严格集中在技术部副部长与技术维护人员"，而这是
// 部门 + 职务的属性组合，Casbin 的 RBAC 主体只有 role 表达不了，故集中在此判定。
// 身份取自数据库最新值而非 JWT 快照，保证升职与降职立即生效。
func requireDeductionAuthority(c *gin.Context) (model.User, bool) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return operator, false
	}

	if operator.Status == "disabled" {
		c.JSON(http.StatusForbidden, gin.H{"error": "该账号已被停用，无法操作打表"})
		return operator, false
	}

	if operator.Role == model.RoleTechAdmin {
		return operator, true
	}

	isDeputy := (operator.Role == model.RoleMember || operator.Role == model.RoleMinister) &&
		strings.Contains(operator.Department, "技术") && operator.Position == "副部长"
	if isDeputy {
		return operator, true
	}

	c.JSON(http.StatusForbidden, gin.H{
		"error":      "打表权限不足：全校宿舍违纪扣分仅限技术部副部长与技术维护组操作",
		"department": operator.Department,
		"position":   operator.Position,
	})
	return operator, false
}

// CreateDeductionRequest 手工录入一条打表扣分
type CreateDeductionRequest struct {
	StudentID          uint   `json:"student_id"`
	Building           string `json:"building" binding:"required"`
	Floor              string `json:"floor" binding:"required"`
	RoomNumber         string `json:"room_number" binding:"required"`
	StudentName        string `json:"student_name" binding:"required"`
	ClassName          string `json:"class_name" binding:"required"`
	Grade              string `json:"grade"`
	Category           string `json:"category" binding:"required"`
	DeductPoints       int    `json:"deduct_points" binding:"required"`
	Reason             string `json:"reason" binding:"required"`
	SourceInspectionID uint   `json:"source_inspection_id"`
	SourceSubjectID    uint   `json:"source_subject_id"`
}

// CreateFromReportRequest 从一次上报的记名名单批量转入打表
type CreateFromReportRequest struct {
	InspectionID uint   `json:"inspection_id" binding:"required"`
	SubjectIDs   []uint `json:"subject_ids" binding:"required"`
	Floor        string `json:"floor"`
	Category     string `json:"category" binding:"required"`
	DeductPoints int    `json:"deduct_points" binding:"required"`
	Reason       string `json:"reason" binding:"required"`
}

type errDuplicateDeduction struct{ existingID uint }

func (e *errDuplicateDeduction) Error() string { return "该上报条目已转入打表" }

// resolvedStudent 与宿位名册核对后确定下来的被扣分学生
type resolvedStudent struct {
	StudentID     uint
	RealName      string
	ClassName     string
	Grade         string
	RosterChecked bool
	Note          string
}

// resolveStudent 把提交的学生定位到宿位名册主键，并就地完成"查寝防冒名"校验。
//
// 仅在全校名册尚未导入时放宽为按姓名存底（否则打表流程会被直接锁死），
// 放宽结果会写入留痕，便于名册导入后回填外键。
func resolveStudent(building, room, name, className, grade string, studentID uint) (*resolvedStudent, string) {
	empty, err := rosterIsEmpty()
	if err != nil {
		return nil, "名册读取失败: " + err.Error()
	}
	if empty {
		return &resolvedStudent{
			RealName: name, ClassName: className, Grade: grade,
			Note: "全校宿位名册尚未导入，本次仅按姓名存底",
		}, ""
	}

	// 本寝整体没有登记时同样宽松：否则名册没导入的寝室一律无法扣分
	registered, err := service.RoomHasRoster(building, room)
	if err != nil {
		return nil, "名册读取失败: " + err.Error()
	}
	if !registered {
		return &resolvedStudent{
			RealName: name, ClassName: className, Grade: grade,
			Note: fmt.Sprintf("%s %s 室在宿位名册中无任何登记，本次仅按姓名存底", building, room),
		}, ""
	}

	if studentID > 0 {
		var s model.Student
		if err := repository.DB.First(&s, studentID).Error; err != nil {
			return nil, "所选学生不存在于宿位名册"
		}
		if s.Status != "active" {
			return nil, fmt.Sprintf("防冒名校验未通过：【%s】当前住宿状态为 %s，不能作为在寝违纪对象", s.RealName, s.Status)
		}
		if s.RoomNumber != room {
			return nil, fmt.Sprintf("防冒名校验未通过：名册显示【%s】登记于 %s %s 室，与本次打表的 %s %s 室不一致",
				s.RealName, s.Building, s.RoomNumber, building, room)
		}
		return &resolvedStudent{
			StudentID: s.ID, RealName: s.RealName, ClassName: s.ClassName, Grade: s.Grade,
			RosterChecked: true,
		}, ""
	}

	matchedID, matchedClass, status, note := service.MatchSubjectInRoom(building, room, name)
	switch status {
	case service.MatchMatched:
		return &resolvedStudent{
			StudentID: matchedID, RealName: name, ClassName: firstNonEmpty(matchedClass, className), Grade: grade,
			RosterChecked: true,
		}, ""
	default:
		return nil, "防冒名校验未通过：" + note
	}
}

func rosterIsEmpty() (bool, error) {
	var n int64
	if err := repository.DB.Model(&model.Student{}).Count(&n).Error; err != nil {
		return false, err
	}
	return n == 0, nil
}

// CreateDeduction 技术部副部长打表：录入一条违规扣分记录
func (dc *DeductionController) CreateDeduction(c *gin.Context) {
	operator, ok := requireDeductionAuthority(c)
	if !ok {
		return
	}
	// 写入扣分属高危操作：必须当场重验登录口令
	if !requireStepUp(c, operator) {
		return
	}

	var req CreateDeductionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请完整填写楼栋、楼层、寝室、学生姓名、班级、扣分项与违规原因"})
		return
	}

	studentID := req.StudentID
	var subject model.InspectionSubject
	if req.SourceSubjectID > 0 {
		if err := repository.DB.First(&subject, req.SourceSubjectID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "来源名单条目不存在"})
			return
		}
		if subject.InspectionID != req.SourceInspectionID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "名单条目与所属上报记录不匹配"})
			return
		}
		if studentID == 0 {
			studentID = subject.StudentID
		}
	}

	name := req.StudentName
	if studentID == 0 && subject.ID > 0 {
		name = subject.RawName
	}

	resolved, errMsg := resolveStudent(req.Building, req.RoomNumber, name, req.ClassName, req.Grade, studentID)
	if errMsg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errMsg})
		return
	}

	record := model.DeductionRecord{
		StudentID:          resolved.StudentID,
		Building:           req.Building,
		Floor:              req.Floor,
		RoomNumber:         req.RoomNumber,
		StudentName:        firstNonEmpty(resolved.RealName, name),
		ClassName:          firstNonEmpty(resolved.ClassName, req.ClassName),
		Grade:              firstNonEmpty(resolved.Grade, req.Grade),
		Category:           req.Category,
		DeductPoints:       req.DeductPoints,
		Reason:             req.Reason,
		InspectorName:      operator.RealName,
		InspectorID:        operator.ID,
		SourceInspectionID: req.SourceInspectionID,
		SourceSubjectID:    req.SourceSubjectID,
		Status:             "confirmed",
		CreatedAt:          time.Now(),
	}

	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if req.SourceSubjectID > 0 {
			var dup model.DeductionRecord
			if err := tx.Where("source_subject_id = ? AND status <> ?", req.SourceSubjectID, "revoked").
				First(&dup).Error; err == nil {
				return &errDuplicateDeduction{existingID: dup.ID}
			}
		} else if req.SourceInspectionID > 0 {
			var dup model.DeductionRecord
			if err := tx.Where("source_inspection_id = ? AND student_name = ? AND status <> ?",
				req.SourceInspectionID, record.StudentName, "revoked").First(&dup).Error; err == nil {
				return &errDuplicateDeduction{existingID: dup.ID}
			}
		}

		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if req.SourceSubjectID > 0 {
			if err := tx.Model(&model.InspectionSubject{}).Where("id = ?", req.SourceSubjectID).
				Update("converted_deduction_id", record.ID).Error; err != nil {
				return err
			}
		}
		return markReportConverted(tx, req.SourceInspectionID)
	})

	var dupErr *errDuplicateDeduction
	if errors.As(err, &dupErr) {
		c.JSON(http.StatusConflict, gin.H{
			"error":              fmt.Sprintf("该上报条目此前已转入打表（记录 #%d），不予重复扣分", dupErr.existingID),
			"existing_deduction": dupErr.existingID,
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "打表扣分保存失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "deduction.create", "deduction_record", record.ID,
		fmt.Sprintf("为【%s %s室 %s（%s）】扣 %d 分：%s%s", record.Building, record.RoomNumber, record.StudentName,
			record.ClassName, record.DeductPoints, record.Reason, auditSuffix(resolved)))

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("已成功为【%s %s寝室】学生【%s (%s)】录入违纪扣分 (-%d 分)！",
			record.Building, record.RoomNumber, record.StudentName, record.ClassName, record.DeductPoints),
		"record":        record,
		"roster_linked": record.StudentID > 0,
	})
}

// CreateDeductionsFromReport 从一次宿管上报的记名名单批量转入打表。
// 名单中任何未通过名册核对的姓名都会整单拒绝，避免生成半途的部分记录。
func (dc *DeductionController) CreateDeductionsFromReport(c *gin.Context) {
	operator, ok := requireDeductionAuthority(c)
	if !ok {
		return
	}
	// 批量写入扣分属高危操作：必须当场重验登录口令
	if !requireStepUp(c, operator) {
		return
	}

	var req CreateFromReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供上报 ID、勾选的名单条目、扣分项与事由"})
		return
	}

	var report model.InspectionPhoto
	if err := repository.DB.First(&report, req.InspectionID).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "上报记录不存在"})
		return
	}

	var subjects []model.InspectionSubject
	repository.DB.Where("id IN ? AND inspection_id = ?", req.SubjectIDs, req.InspectionID).Find(&subjects)
	if len(subjects) != len(req.SubjectIDs) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "勾选的名单条目与该上报不匹配，请刷新后重试"})
		return
	}
	if len(subjects) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请至少勾选一名被记名学生"})
		return
	}

	floor := firstNonEmpty(req.Floor, floorFromRoom(report.RoomNumber))

	records := make([]model.DeductionRecord, 0, len(subjects))
	for i := range subjects {
		s := subjects[i]
		if s.ConvertedDeductionID > 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error":              fmt.Sprintf("【%s】已转入打表（记录 #%d），不予重复扣分", s.RawName, s.ConvertedDeductionID),
				"existing_deduction": s.ConvertedDeductionID,
			})
			return
		}
		resolved, errMsg := resolveStudent(report.Building, report.RoomNumber, s.RawName, s.ClassName, "", s.StudentID)
		if errMsg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("名单中的【%s】无法打表：%s", s.RawName, errMsg), "subject": s.RawName})
			return
		}
		records = append(records, model.DeductionRecord{
			StudentID:          resolved.StudentID,
			Building:           report.Building,
			Floor:              floor,
			RoomNumber:         report.RoomNumber,
			StudentName:        firstNonEmpty(resolved.RealName, s.RawName),
			ClassName:          firstNonEmpty(resolved.ClassName, s.ClassName),
			Grade:              resolved.Grade,
			Category:           req.Category,
			DeductPoints:       req.DeductPoints,
			Reason:             req.Reason,
			InspectorName:      operator.RealName,
			InspectorID:        operator.ID,
			SourceInspectionID: report.ID,
			SourceSubjectID:    s.ID,
			Status:             "confirmed",
			CreatedAt:          time.Now(),
		})
	}

	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		for i := range records {
			var live model.DeductionRecord
			if err := tx.Where("source_subject_id = ? AND status <> ?", records[i].SourceSubjectID, "revoked").
				First(&live).Error; err == nil {
				return &errDuplicateDeduction{existingID: live.ID}
			}
			if err := tx.Create(&records[i]).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.InspectionSubject{}).Where("id = ?", records[i].SourceSubjectID).
				Update("converted_deduction_id", records[i].ID).Error; err != nil {
				return err
			}
		}
		return markReportConverted(tx, report.ID)
	})
	if err != nil {
		var dupErr *errDuplicateDeduction
		if errors.As(err, &dupErr) {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("名单中有条目此前已转入打表（记录 #%d），本次未提交", dupErr.existingID)})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量打表失败: " + err.Error()})
		return
	}

	names := make([]string, 0, len(records))
	for _, r := range records {
		names = append(names, r.StudentName)
	}
	logOperationAs(c, operator, "deduction.create_batch", "inspection_photo", report.ID,
		fmt.Sprintf("由上报 #%d 批量转入 %d 条扣分（%s），每人 -%d 分：%s",
			report.ID, len(records), strings.Join(names, "、"), req.DeductPoints, req.Reason))

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("已为该上报的 %d 名学生完成打表扣分。", len(records)),
		"count":   len(records),
		"records": records,
	})
}

func markReportConverted(tx *gorm.DB, inspectionID uint) error {
	if inspectionID == 0 {
		return nil
	}
	return tx.Model(&model.InspectionPhoto{}).Where("id = ?", inspectionID).
		Update("status", "converted").Error
}

// refreshReportConverted 撤销后若该上报已无有效扣分记录，把状态退回待核准以便重新打表
func refreshReportConverted(tx *gorm.DB, inspectionID uint) error {
	if inspectionID == 0 {
		return nil
	}
	var remaining int64
	if err := tx.Model(&model.DeductionRecord{}).
		Where("source_inspection_id = ? AND status <> ?", inspectionID, "revoked").
		Count(&remaining).Error; err != nil {
		return err
	}
	if remaining > 0 {
		return nil
	}
	return tx.Model(&model.InspectionPhoto{}).Where("id = ?", inspectionID).
		Update("status", "ai_analyzed").Error
}

// floorFromRoom 寝室号首位即楼层，如 302 -> 3F
func floorFromRoom(roomNumber string) string {
	digits := strings.TrimRight(strings.TrimSpace(roomNumber), "室房")
	if len(digits) >= 3 {
		return string(digits[0]) + "F"
	}
	return "全楼"
}

func auditSuffix(r *resolvedStudent) string {
	if r == nil {
		return ""
	}
	if r.RosterChecked {
		return fmt.Sprintf("（名册核对通过，student_id=%d）", r.StudentID)
	}
	if r.Note != "" {
		return "（" + r.Note + "）"
	}
	return "（未关联名册主键）"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// deductionFilters 把查询条件构造收在一处，避免列表、合计、导出三处各自漂移
func deductionFilters(c *gin.Context) func() *gorm.DB {
	floor := c.Query("floor")
	room := c.Query("room")
	name := c.Query("name")
	className := c.Query("class")
	building := c.Query("building")
	category := c.Query("category")
	q := c.Query("q")
	status := strings.TrimSpace(c.Query("status"))

	return func() *gorm.DB {
		query := repository.DB.Model(&model.DeductionRecord{})
		if floor != "" {
			query = query.Where("floor LIKE ?", "%"+floor+"%")
		}
		if room != "" {
			query = query.Where("room_number LIKE ?", "%"+room+"%")
		}
		if name != "" {
			query = query.Where("student_name LIKE ?", "%"+name+"%")
		}
		if className != "" {
			query = query.Where("class_name LIKE ?", "%"+className+"%")
		}
		if building != "" {
			query = query.Where("building LIKE ?", "%"+building+"%")
		}
		if category != "" {
			query = query.Where("category = ?", category)
		}
		if status == "confirmed" || status == "revoked" {
			query = query.Where("status = ?", status)
		}
		if q != "" {
			like := "%" + q + "%"
			query = query.Where("student_name LIKE ? OR class_name LIKE ? OR room_number LIKE ? OR floor LIKE ? OR building LIKE ? OR reason LIKE ?",
				like, like, like, like, like, like)
		}
		return query
	}
}

// GetDeductions 多维实时筛选扣分打表记录（支持楼层-寝室-名字-班级任意组合筛选）
func (dc *DeductionController) GetDeductions(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	scope := deductionFilters(c)

	var total int64
	scope().Count(&total)

	// 合计只计未撤销记录：撤销项仍可在列表与导出中看到，但不再参与评优算分
	var totalDeduct int64
	scope().Where("status <> ?", "revoked").Select("COALESCE(SUM(deduct_points), 0)").Scan(&totalDeduct)

	var list []model.DeductionRecord
	scope().Order("id desc").Offset(offset).Limit(pageSize).Find(&list)

	unlinked := 0
	for _, r := range list {
		if r.StudentID == 0 {
			unlinked++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"total":            total,
		"page":             page,
		"page_size":        pageSize,
		"total_deduct_sum": totalDeduct,
		"unlinked_in_page": unlinked,
		"status_filter":    c.Query("status"),
		"items":            list,
	})
}

// StudentDeductionProfile 一名学生的德育扣分档案
type StudentDeductionProfile struct {
	StudentID     uint      `json:"student_id"`
	StudentName   string    `json:"student_name"`
	ClassName     string    `json:"class_name"`
	Grade         string    `json:"grade"`
	Building      string    `json:"building"`
	RoomNumber    string    `json:"room_number"`
	BedNumber     string    `json:"bed_number"`
	TotalDeduct   int       `json:"total_deduct"`
	RecordCount   int64     `json:"record_count"`
	RevokedCount  int64     `json:"revoked_count"`
	FirstAt       time.Time `json:"first_at"`
	LastAt        time.Time `json:"last_at"`
	CategoryStats []struct {
		Category string `json:"category"`
		Count    int64  `json:"count"`
		Points   int    `json:"points"`
	} `json:"category_stats"`
	Records []model.DeductionRecord `json:"records"`
	Note    string                  `json:"note"`
}

// GetStudentDeductionProfile 按学生聚合的德育档案。
// 支持 student_id 精确查询，或用 姓名 + 班级（可选寝室）定位；未回填外键的历史记录仍可查到。
func (dc *DeductionController) GetStudentDeductionProfile(c *gin.Context) {
	if _, ok := requireDeductionAuthority(c); !ok {
		return
	}

	studentID, _ := strconv.ParseUint(c.Query("student_id"), 10, 64)
	name := strings.TrimSpace(c.Query("name"))
	className := strings.TrimSpace(c.Query("class"))
	room := strings.TrimSpace(c.Query("room"))

	var student model.Student
	switch {
	case studentID > 0:
		if err := repository.DB.First(&student, uint(studentID)).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "宿位名册中不存在该学生"})
			return
		}
	case name != "":
		q := repository.DB.Model(&model.Student{}).Where("real_name = ?", name)
		if className != "" {
			q = q.Where("class_name LIKE ?", "%"+className+"%")
		}
		if room != "" {
			q = q.Where("room_number = ?", room)
		}
		var found []model.Student
		q.Limit(2).Find(&found)
		if len(found) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "宿位名册中未找到该学生，请核对姓名与班级"})
			return
		}
		if len(found) > 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "同名学生有多条名册记录，请补充班级或寝室号后再查"})
			return
		}
		student = found[0]
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供 student_id 或学生姓名"})
		return
	}

	profile := StudentDeductionProfile{
		StudentID:   student.ID,
		StudentName: student.RealName,
		ClassName:   student.ClassName,
		Grade:       student.Grade,
		Building:    student.Building,
		RoomNumber:  student.RoomNumber,
		BedNumber:   student.BedNumber,
		Records:     []model.DeductionRecord{},
	}

	var live []model.DeductionRecord
	repository.DB.Where("(student_id = ? OR (student_id = 0 AND student_name = ? AND class_name = ?)) AND status <> ?",
		student.ID, student.RealName, student.ClassName, "revoked").
		Order("id desc").Find(&live)

	earliestSet := false
	for _, r := range live {
		profile.TotalDeduct += r.DeductPoints
		if !earliestSet || r.CreatedAt.Before(profile.FirstAt) {
			profile.FirstAt = r.CreatedAt
			earliestSet = true
		}
		if r.CreatedAt.After(profile.LastAt) {
			profile.LastAt = r.CreatedAt
		}
	}
	profile.RecordCount = int64(len(live))
	repository.DB.Model(&model.DeductionRecord{}).
		Where("student_id = ? AND status = ?", student.ID, "revoked").Count(&profile.RevokedCount)

	type catStat struct {
		Category string `json:"category"`
		Count    int64  `json:"count"`
		Points   int    `json:"points"`
	}
	var stats []catStat
	repository.DB.Model(&model.DeductionRecord{}).
		Select("category, COUNT(*) as count, COALESCE(SUM(deduct_points),0) as points").
		Where("student_id = ? AND status <> ?", student.ID, "revoked").
		Group("category").Order("points desc").Scan(&stats)
	for _, s := range stats {
		profile.CategoryStats = append(profile.CategoryStats, s)
	}
	profile.Records = live

	if profile.RevokedCount > 0 {
		profile.Note = fmt.Sprintf("另有 %d 条记录已被撤销，仍保留原始数据留痕，可在打表台账中按 status=revoked 查看。", profile.RevokedCount)
	}

	c.JSON(http.StatusOK, profile)
}

// StudentProfileSummary 按班级/楼栋等维度聚合的学生德育扣分概览
type StudentProfileSummary struct {
	StudentID   uint   `json:"student_id"`
	StudentName string `json:"student_name"`
	ClassName   string `json:"class_name"`
	Grade       string `json:"grade"`
	Building    string `json:"building"`
	RoomNumber  string `json:"room_number"`
	TotalDeduct int    `json:"total_deduct"`
	RecordCount int64  `json:"record_count"`
}

// ListStudentDeductionProfiles 批量聚合学生扣分，供评优与看板使用。
// 出于对未成年人档案的最小必要原则，只有技术维护组能不带条件地看全校，
// 其余角色（含副部长）必须自带班级/年级/楼栋/寝室等收敛条件。
func (dc *DeductionController) ListStudentDeductionProfiles(c *gin.Context) {
	if _, ok := requireDeductionAuthority(c); !ok {
		return
	}

	operator, _ := operatorFromContext(c)
	class := strings.TrimSpace(c.Query("class"))
	grade := strings.TrimSpace(c.Query("grade"))
	building := strings.TrimSpace(c.Query("building"))
	room := strings.TrimSpace(c.Query("room"))

	isGlobalScope := operator.Role == model.RoleTechAdmin
	if !isGlobalScope && class == "" && grade == "" && building == "" && room == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "按最小必要原则，查询学生德育档案必须限定班级、年级或楼栋范围"})
		return
	}

	studentQuery := repository.DB.Model(&model.Student{}).Where("status = ?", "active")
	if class != "" {
		studentQuery = studentQuery.Where("class_name LIKE ?", "%"+class+"%")
	}
	if grade != "" {
		studentQuery = studentQuery.Where("grade = ?", grade)
	}
	if building != "" {
		studentQuery = studentQuery.Where("building LIKE ?", "%"+building+"%")
	}
	if room != "" {
		studentQuery = studentQuery.Where("room_number = ?", room)
	}

	var students []model.Student
	studentQuery.Order("grade asc, class_name asc, building asc, room_number asc").Limit(500).Find(&students)

	ids := make([]uint, 0, len(students))
	for _, s := range students {
		ids = append(ids, s.ID)
	}

	type agg struct {
		StudentID uint
		Points    int
		Count     int64
	}
	var aggs []agg
	if len(ids) > 0 {
		repository.DB.Model(&model.DeductionRecord{}).
			Select("student_id, COALESCE(SUM(deduct_points),0) as points, COUNT(*) as count").
			Where("student_id IN ? AND status <> ?", ids, "revoked").
			Group("student_id").Scan(&aggs)
	}
	byID := make(map[uint]agg, len(aggs))
	for _, a := range aggs {
		byID[a.StudentID] = a
	}

	list := make([]StudentProfileSummary, 0, len(students))
	withDeduction := 0
	for _, s := range students {
		a := byID[s.ID]
		item := StudentProfileSummary{
			StudentID: s.ID, StudentName: s.RealName, ClassName: s.ClassName, Grade: s.Grade,
			Building: s.Building, RoomNumber: s.RoomNumber,
			TotalDeduct: a.Points, RecordCount: a.Count,
		}
		if item.TotalDeduct > 0 {
			withDeduction++
		}
		list = append(list, item)
	}

	c.JSON(http.StatusOK, gin.H{
		"total_students":   len(list),
		"with_deduction":   withDeduction,
		"clean_students":   len(list) - withDeduction,
		"revoked_excluded": true,
		"scope":            gin.H{"class": class, "grade": grade, "building": building, "room": room},
		"items":            list,
	})
}

// RevokeDeduction 撤销一条打表记录：保留原始数据并记录撤销人、时间与理由
func (dc *DeductionController) RevokeDeduction(c *gin.Context) {
	operator, ok := requireDeductionAuthority(c)
	if !ok {
		return
	}
	// 撤销扣分属高危操作：必须当场重验登录口令
	if !requireStepUp(c, operator) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的记录 ID"})
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&req)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "撤销打表记录必须填写理由，以便事后追溯"})
		return
	}

	var record model.DeductionRecord
	if err := repository.DB.First(&record, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
		return
	}
	if record.Status == "revoked" {
		c.JSON(http.StatusConflict, gin.H{
			"error": "该记录已是撤销状态", "revoked_by": record.RevokedByName, "revoked_at": record.RevokedAt,
		})
		return
	}

	now := time.Now()
	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.DeductionRecord{}).Where("id = ?", record.ID).Updates(map[string]interface{}{
			"status":          "revoked",
			"revoked_by":      operator.ID,
			"revoked_by_name": operator.RealName,
			"revoke_reason":   reason,
			"revoked_at":      now,
		}).Error; err != nil {
			return err
		}
		if record.SourceSubjectID > 0 {
			if err := tx.Model(&model.InspectionSubject{}).Where("id = ?", record.SourceSubjectID).
				Update("converted_deduction_id", 0).Error; err != nil {
				return err
			}
		}
		return refreshReportConverted(tx, record.SourceInspectionID)
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "撤销失败: " + err.Error()})
		return
	}

	record.Status = "revoked"
	record.RevokedBy = operator.ID
	record.RevokedByName = operator.RealName
	record.RevokeReason = reason
	record.RevokedAt = &now

	logOperationAs(c, operator, "deduction.revoke", "deduction_record", record.ID,
		fmt.Sprintf("撤销【%s %s室 %s】的 -%d 分记录（%s）：理由：%s",
			record.Building, record.RoomNumber, record.StudentName, record.DeductPoints, record.Category, reason))

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("打表记录 #%d 已撤销，原始记录保留留痕。", record.ID),
		"record":  record,
	})
}

// ExportDeductionsCSV 一键导出符合筛选条件的打表标准 CSV 文件
func (dc *DeductionController) ExportDeductionsCSV(c *gin.Context) {
	var list []model.DeductionRecord
	deductionFilters(c)().Order("id desc").Find(&list)

	filename := fmt.Sprintf("学管会打表扣分明细_%s.csv", time.Now().Format("20060102_150405"))
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))

	// 写入 UTF-8 BOM，防止 Windows Excel 打开乱码
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(c.Writer)
	defer writer.Flush()

	_ = writer.Write([]string{
		"打表流水号", "所属楼栋", "楼层", "寝室房间号", "学生班级", "违纪学生姓名",
		"违纪行为类别", "扣除分值", "违纪具体事由详情", "打表记录人", "记录时间",
		"存底状态", "撤销人", "撤销时间", "撤销理由", "名册关联",
	})

	for _, item := range list {
		revokedAt := ""
		if item.RevokedAt != nil {
			revokedAt = item.RevokedAt.Format("2006-01-02 15:04:05")
		}
		link := "未关联名册"
		if item.StudentID > 0 {
			link = fmt.Sprintf("student_id=%d", item.StudentID)
		}
		_ = writer.Write([]string{
			fmt.Sprintf("#%d", item.ID), item.Building, item.Floor, item.RoomNumber,
			item.ClassName, item.StudentName, item.Category, fmt.Sprintf("-%d", item.DeductPoints),
			item.Reason, item.InspectorName, item.CreatedAt.Format("2006-01-02 15:04:05"),
			item.Status, item.RevokedByName, revokedAt, item.RevokeReason, link,
		})
	}
}

// MorningSubjectDTO 上报名单中的单个被记名学生
type MorningSubjectDTO struct {
	ID                   uint   `json:"id"`
	RawName              string `json:"raw_name"`
	StudentID            uint   `json:"student_id"`
	ClassName            string `json:"class_name"`
	MatchStatus          string `json:"match_status"`
	MatchNote            string `json:"match_note"`
	ConvertedDeductionID uint   `json:"converted_deduction_id"`
}

// MorningDormReportDTO 宿管上报数据概览（供技术部副部长核对与快速打表使用）
type MorningDormReportDTO struct {
	ID            uint                `json:"id"`
	Building      string              `json:"building"`
	Floor         string              `json:"floor"`
	RoomNumber    string              `json:"room_number"`
	ManagerName   string              `json:"manager_name"`
	PhotoType     string              `json:"photo_type"`
	ReportKind    string              `json:"report_kind"`
	NoteText      string              `json:"note_text"`
	ImageURL      string              `json:"image_url"`
	AIStatus      string              `json:"ai_status"`
	SubmittedText string              `json:"submitted_text"`
	Category      string              `json:"category"`
	Severity      string              `json:"severity"`
	DeductPoints  int                 `json:"deduct_points"`
	CreatedAt     time.Time           `json:"created_at"`
	IsReported    bool                `json:"is_reported"`
	IsDeducted    bool                `json:"is_deducted"`
	SubjectTotal  int                 `json:"subject_total"`
	Subjects      []MorningSubjectDTO `json:"subjects"`
}

// GetMorningDormReports 宿管上报数据列表。
// 是否已打表严格按上报记录自身的关联判定，不再用"同寝室当天有任意记录"来猜测。
func (dc *DeductionController) GetMorningDormReports(c *gin.Context) {
	if _, ok := requireDeductionAuthority(c); !ok {
		return
	}

	scope := c.Query("scope")
	if scope != "all" {
		scope = "today"
	}

	photoQuery := repository.DB.Model(&model.InspectionPhoto{})
	if scope == "today" {
		now := time.Now()
		todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		photoQuery = photoQuery.Where("created_at >= ?", todayStart)
	}

	var photos []model.InspectionPhoto
	photoQuery.Order("id desc").Limit(300).Find(&photos)

	var presets []model.DormRosterPreset
	repository.DB.Find(&presets)
	presetMap := make(map[string]model.DormRosterPreset)
	for _, p := range presets {
		presetMap[p.RealName] = p
	}

	inspectionIDs := make([]uint, 0, len(photos))
	for _, p := range photos {
		inspectionIDs = append(inspectionIDs, p.ID)
	}

	subjectsByInspection := make(map[uint][]model.InspectionSubject)
	converted := make(map[uint]bool)
	if len(inspectionIDs) > 0 {
		var subjects []model.InspectionSubject
		repository.DB.Where("inspection_id IN ?", inspectionIDs).Order("id asc").Find(&subjects)
		for _, s := range subjects {
			subjectsByInspection[s.InspectionID] = append(subjectsByInspection[s.InspectionID], s)
		}

		var linked []model.DeductionRecord
		repository.DB.Where("source_inspection_id IN ? AND status <> ?", inspectionIDs, "revoked").Find(&linked)
		for _, r := range linked {
			converted[r.SourceInspectionID] = true
		}
	}

	dtoList := make([]MorningDormReportDTO, 0, len(photos))
	for _, p := range photos {
		fl := "全楼"
		if pr, ok := presetMap[p.ManagerName]; ok && pr.Floor != "" {
			fl = pr.Floor
		} else {
			fl = floorFromRoom(p.RoomNumber)
		}

		// 未经真实模型识别的记录不得携带任何"AI 结论"，否则副部长核对的就是伪造证据
		aiVerified := p.AIStatus == ai.StatusReal
		desc := p.NoteText
		category, severity := p.Category, p.Severity
		deductPoints := p.DeductPoints
		if !aiVerified {
			category, severity, deductPoints = "", "", 0
			if desc == "" {
				desc = "系统未完成 AI 识别（引擎未配置或调用失败），请人工查看原图后自行录入扣分项。"
			}
		} else if p.VisionAIOutput != "" {
			desc = p.VisionAIOutput
		}

		aiStatus := p.AIStatus
		if aiStatus == "" {
			aiStatus = ai.StatusUnknown
		}

		subs := subjectsByInspection[p.ID]
		subjectDTOs := make([]MorningSubjectDTO, 0, len(subs))
		for _, s := range subs {
			subjectDTOs = append(subjectDTOs, MorningSubjectDTO{
				ID:                   s.ID,
				RawName:              s.RawName,
				StudentID:            s.StudentID,
				ClassName:            s.ClassName,
				MatchStatus:          s.MatchStatus,
				MatchNote:            s.MatchNote,
				ConvertedDeductionID: s.ConvertedDeductionID,
			})
		}

		dtoList = append(dtoList, MorningDormReportDTO{
			ID:            p.ID,
			Building:      p.Building,
			Floor:         fl,
			RoomNumber:    p.RoomNumber,
			ManagerName:   p.ManagerName,
			PhotoType:     p.PhotoType,
			ReportKind:    reportKindOr(p.ReportKind, p.ImageURL),
			NoteText:      p.NoteText,
			ImageURL:      p.ImageURL,
			AIStatus:      aiStatus,
			SubmittedText: desc,
			Category:      category,
			Severity:      severity,
			DeductPoints:  deductPoints,
			CreatedAt:     p.CreatedAt,
			IsReported:    true,
			IsDeducted:    p.Status == "converted" || converted[p.ID],
			SubjectTotal:  len(subs),
			Subjects:      subjectDTOs,
		})
	}

	title := "今日宿管数据上报台账"
	if scope == "all" {
		title = "全部宿管数据上报台账"
	}
	c.JSON(http.StatusOK, gin.H{
		"total":        len(dtoList),
		"items":        dtoList,
		"today":        time.Now().Format("2006-01-02"),
		"scope":        scope,
		"period_title": title,
	})
}

func reportKindOr(kind, imageURL string) string {
	if kind != "" {
		return kind
	}
	if strings.TrimSpace(imageURL) == "" {
		return "text"
	}
	return "photo"
}
