package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type TechDBController struct{}

// ColumnMeta 单列属性定义
type ColumnMeta struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text, number, select, boolean
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

// TableMeta 数据表/名单元数据定义
type TableMeta struct {
	Key         string       `json:"key"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Icon        string       `json:"icon"`
	Count       int64        `json:"count"`
	Columns     []ColumnMeta `json:"columns"`
}

// getTableDefinitions 返回技术部支持管理的所有名单/数据表定义与 Schema
func getTableDefinitions() []TableMeta {
	return []TableMeta{
		{
			Key:         "students",
			Title:       "全校学生宿位花名册",
			Description: "高一至高三全校在宿学生档案，楼栋、宿舍、床位与班级联动关联",
			Icon:        "fa-graduation-cap",
			Columns: []ColumnMeta{
				{Key: "student_no", Label: "学号", Type: "text", Required: false},
				{Key: "real_name", Label: "真实姓名", Type: "text", Required: true},
				{Key: "grade", Label: "年级", Type: "select", Required: false, Options: []string{"高一", "高二", "高三"}},
				{Key: "class_name", Label: "班级名称", Type: "text", Required: true},
				{Key: "building", Label: "所属楼栋", Type: "text", Required: true},
				{Key: "room_number", Label: "寝室房间号", Type: "text", Required: true},
				{Key: "bed_number", Label: "床铺编号", Type: "text", Required: false},
				{Key: "gender", Label: "性别", Type: "select", Required: false, Options: []string{"男", "女"}},
				{Key: "phone", Label: "联系电话", Type: "text", Required: false},
				{Key: "status", Label: "住宿状态", Type: "select", Required: false, Options: []string{"active", "leave", "graduated"}},
			},
		},
		{
			Key:         "dorm_roster_presets",
			Title:       "宿管免密认证花名册",
			Description: "宿管手机 APK 免密直登三要素预置数据库（手机号+楼栋+姓名）",
			Icon:        "fa-id-card-clip",
			Columns: []ColumnMeta{
				{Key: "real_name", Label: "宿管姓名", Type: "text", Required: true},
				{Key: "phone", Label: "预置手机号", Type: "text", Required: true},
				{Key: "building", Label: "负责楼栋", Type: "text", Required: true},
				{Key: "floor", Label: "负责楼层", Type: "text", Required: false},
				{Key: "is_activated", Label: "已在移动端激活", Type: "boolean", Required: false},
			},
		},
		{
			Key:         "users",
			Title:       "系统用户与干事部员名单",
			Description: "学管会干部、干事部员、宿管与技术管理员账户权限库",
			Icon:        "fa-users-gear",
			Columns: []ColumnMeta{
				{Key: "username", Label: "登录账号", Type: "text", Required: true},
				{Key: "real_name", Label: "真实姓名", Type: "text", Required: true},
				{Key: "role", Label: "权限角色", Type: "select", Required: true, Options: []string{"member", "minister", "tech_admin", "dorm_manager", "viewer_export"}},
				{Key: "department", Label: "所属部门与组别", Type: "select", Required: false, Options: []string{"纪检部", "组织部 · 技术组", "组织部 · 督查组", "宣传部 · 播音组", "宣传部 · 宣传组", "综合信息档案处"}},
				{Key: "phone", Label: "联系手机", Type: "text", Required: false},
				{Key: "building", Label: "负责/常驻楼栋", Type: "text", Required: false},
				{Key: "floor", Label: "常驻楼层", Type: "text", Required: false},
				{Key: "total_score", Label: "考核积分", Type: "number", Required: false},
				{Key: "status", Label: "账号状态", Type: "select", Required: false, Options: []string{"active", "disabled"}},
			},
		},
		{
			Key:         "schedule_shifts",
			Title:       "查寝排班轮换班次大盘",
			Description: "排班轮换执行记录，单双周、时段与上岗部员分配台账",
			Icon:        "fa-calendar-days",
			Columns: []ColumnMeta{
				{Key: "date", Label: "排班日期", Type: "text", Required: true},
				{Key: "week_type", Label: "周轮换类型", Type: "select", Required: false, Options: []string{"single", "double", "normal"}},
				{Key: "shift_period", Label: "查寝时段", Type: "text", Required: true},
				{Key: "building", Label: "排班楼栋", Type: "text", Required: true},
				{Key: "floor", Label: "负责楼层", Type: "text", Required: false},
				{Key: "member_names", Label: "指派部员姓名", Type: "text", Required: true},
				{Key: "manager_name", Label: "协同宿管姓名", Type: "text", Required: false},
				{Key: "status", Label: "执行状态", Type: "select", Required: false, Options: []string{"scheduled", "in_progress", "completed", "missed"}},
			},
		},
		{
			Key:         "inspection_photos",
			Title:       "查寝隐患与违规扣分台账",
			Description: "宿管查寝隐患照片、多模态 AI 识别翻译与违纪扣分入库明细",
			Icon:        "fa-camera-retro",
			Columns: []ColumnMeta{
				{Key: "building", Label: "巡查楼栋", Type: "text", Required: true},
				{Key: "room_number", Label: "寝室号", Type: "text", Required: true},
				{Key: "manager_name", Label: "上传宿管姓名", Type: "text", Required: true},
				{Key: "photo_type", Label: "检查类别", Type: "select", Required: false, Options: []string{"violation", "sanitation", "duty_supervise"}},
				{Key: "category", Label: "违规类别", Type: "text", Required: false},
				{Key: "severity", Label: "严重等级", Type: "select", Required: false, Options: []string{"low", "medium", "high", "critical"}},
				{Key: "deduct_points", Label: "扣除积分", Type: "number", Required: false},
				{Key: "status", Label: "处理状态", Type: "select", Required: false, Options: []string{"uploaded", "ai_analyzed", "confirmed", "archived"}},
			},
		},
		{
			Key:         "leave_requests",
			Title:       "部员请假审批申请档案",
			Description: "学管会部员上工请假申报流转与部长批复明细",
			Icon:        "fa-envelope-open-text",
			Columns: []ColumnMeta{
				{Key: "member_name", Label: "请假部员姓名", Type: "text", Required: true},
				{Key: "shift_info", Label: "请假班次信息", Type: "text", Required: true},
				{Key: "reason", Label: "请假正当事由", Type: "text", Required: true},
				{Key: "substitute_name", Label: "协调替班部员", Type: "text", Required: false},
				{Key: "status", Label: "审批状态", Type: "select", Required: false, Options: []string{"pending", "approved", "rejected"}},
				{Key: "minister_name", Label: "审批部长", Type: "text", Required: false},
				{Key: "review_comment", Label: "部长审批批复", Type: "text", Required: false},
			},
		},
		{
			Key:         "recruitment_applications",
			Title:       "招新选拔报名录取名单",
			Description: "全校招新报名意向、简历、志愿部门与录取状态库",
			Icon:        "fa-user-plus",
			Columns: []ColumnMeta{
				{Key: "real_name", Label: "报名学生姓名", Type: "text", Required: true},
				{Key: "gender", Label: "性别", Type: "select", Required: false, Options: []string{"男", "女"}},
				{Key: "phone", Label: "联系电话", Type: "text", Required: true},
				{Key: "major_and_class", Label: "年级专业班级", Type: "text", Required: true},
				{Key: "building_room", Label: "所在寝室号", Type: "text", Required: true},
				{Key: "target_department", Label: "意向投递部门", Type: "select", Required: true, Options: []string{"纪检部", "组织部 · 技术组", "组织部 · 督查组", "宣传部 · 播音组", "宣传部 · 宣传组"}},
				{Key: "status", Label: "录取状态", Type: "select", Required: false, Options: []string{"submitted", "shortlisted", "interviewed", "admitted", "rejected"}},
			},
		},
		{
			Key:         "member_score_logs",
			Title:       "部员履职积分流水台账",
			Description: "部员日常查寝出勤加分、违纪扣分与部长调整流水",
			Icon:        "fa-clipboard-check",
			Columns: []ColumnMeta{
				{Key: "member_name", Label: "部员姓名", Type: "text", Required: true},
				{Key: "change_type", Label: "积分变动类型", Type: "select", Required: true, Options: []string{"bonus", "penalty", "attendance_ok", "duty_substitute", "late"}},
				{Key: "score_change", Label: "积分变动分值", Type: "number", Required: true},
				{Key: "balance_after", Label: "变动后积分余额", Type: "number", Required: false},
				{Key: "reason", Label: "变动缘由说明", Type: "text", Required: true},
				{Key: "operator_name", Label: "经办操作人", Type: "text", Required: false},
			},
		},
	}
}

// GetTablesSummary 获取数据库中各名单表的统计汇总与 Schema
func (tdb *TechDBController) GetTablesSummary(c *gin.Context) {
	tables := getTableDefinitions()
	for i := range tables {
		var cnt int64
		switch tables[i].Key {
		case "students":
			repository.DB.Model(&model.Student{}).Count(&cnt)
		case "dorm_roster_presets":
			repository.DB.Model(&model.DormRosterPreset{}).Count(&cnt)
		case "users":
			repository.DB.Model(&model.User{}).Count(&cnt)
		case "schedule_shifts":
			repository.DB.Model(&model.ScheduleShift{}).Count(&cnt)
		case "inspection_photos":
			repository.DB.Model(&model.InspectionPhoto{}).Count(&cnt)
		case "leave_requests":
			repository.DB.Model(&model.LeaveRequest{}).Count(&cnt)
		case "recruitment_applications":
			repository.DB.Model(&model.RecruitmentApplication{}).Count(&cnt)
		case "member_score_logs":
			repository.DB.Model(&model.MemberScoreLog{}).Count(&cnt)
		}
		tables[i].Count = cnt
	}

	c.JSON(http.StatusOK, gin.H{
		"total": len(tables),
		"items": tables,
	})
}

// GetTableRecords 分页与检索指定名单表数据
func (tdb *TechDBController) GetTableRecords(c *gin.Context) {
	tableKey := c.Param("table")
	q := c.Query("q")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "15"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 15
	}
	offset := (page - 1) * pageSize

	var total int64
	var records interface{}

	switch tableKey {
	case "students":
		var list []model.Student
		db := repository.DB.Model(&model.Student{})
		if q != "" {
			db = db.Where("real_name LIKE ? OR student_no LIKE ? OR building LIKE ? OR room_number LIKE ? OR class_name LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	case "dorm_roster_presets":
		var list []model.DormRosterPreset
		db := repository.DB.Model(&model.DormRosterPreset{})
		if q != "" {
			db = db.Where("real_name LIKE ? OR phone LIKE ? OR building LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	case "users":
		var list []model.User
		db := repository.DB.Model(&model.User{})
		if q != "" {
			db = db.Where("username LIKE ? OR real_name LIKE ? OR phone LIKE ? OR department LIKE ? OR role LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	case "schedule_shifts":
		var list []model.ScheduleShift
		db := repository.DB.Model(&model.ScheduleShift{})
		if q != "" {
			db = db.Where("date LIKE ? OR building LIKE ? OR member_names LIKE ? OR manager_name LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	case "inspection_photos":
		var list []model.InspectionPhoto
		db := repository.DB.Model(&model.InspectionPhoto{})
		if q != "" {
			db = db.Where("building LIKE ? OR room_number LIKE ? OR manager_name LIKE ? OR category LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	case "leave_requests":
		var list []model.LeaveRequest
		db := repository.DB.Model(&model.LeaveRequest{})
		if q != "" {
			db = db.Where("member_name LIKE ? OR reason LIKE ? OR shift_info LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	case "recruitment_applications":
		var list []model.RecruitmentApplication
		db := repository.DB.Model(&model.RecruitmentApplication{})
		if q != "" {
			db = db.Where("real_name LIKE ? OR phone LIKE ? OR target_department LIKE ? OR major_and_class LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	case "member_score_logs":
		var list []model.MemberScoreLog
		db := repository.DB.Model(&model.MemberScoreLog{})
		if q != "" {
			db = db.Where("member_name LIKE ? OR reason LIKE ? OR operator_name LIKE ?", "%"+q+"%", "%"+q+"%", "%"+q+"%")
		}
		db.Count(&total)
		db.Order("id desc").Offset(offset).Limit(pageSize).Find(&list)
		records = list

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的数据名单类型"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"table":     tableKey,
		"page":      page,
		"page_size": pageSize,
		"total":     total,
		"items":     records,
	})
}

// CreateRecord 在指定名单表中新建一条记录
func (tdb *TechDBController) CreateRecord(c *gin.Context) {
	tableKey := c.Param("table")

	switch tableKey {
	case "students":
		var s model.Student
		if err := c.ShouldBindJSON(&s); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		s.CreatedAt = time.Now()
		if s.Status == "" {
			s.Status = "active"
		}
		if err := repository.DB.Create(&s).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "学生档案已成功录入数据库！", "record": s})

	case "dorm_roster_presets":
		var r model.DormRosterPreset
		if err := c.ShouldBindJSON(&r); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		r.CreatedAt = time.Now()
		if err := repository.DB.Create(&r).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "宿管免密认证记录已成功录入！", "record": r})

	case "users":
		var u model.User
		if err := c.ShouldBindJSON(&u); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		hash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
		u.PasswordHash = string(hash)
		u.CreatedAt = time.Now()
		u.UpdatedAt = time.Now()
		if u.Status == "" {
			u.Status = "active"
		}
		if err := repository.DB.Create(&u).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建用户失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "系统用户记录已创建（默认密码 123456）！", "record": u})

	case "schedule_shifts":
		var s model.ScheduleShift
		if err := c.ShouldBindJSON(&s); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		s.CreatedAt = time.Now()
		if s.Status == "" {
			s.Status = "scheduled"
		}
		if err := repository.DB.Create(&s).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建排班班次失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "排班记录已创建！", "record": s})

	case "inspection_photos":
		var p model.InspectionPhoto
		if err := c.ShouldBindJSON(&p); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		p.CreatedAt = time.Now()
		if p.ImageURL == "" {
			p.ImageURL = "/uploads/default_inspection.jpg"
		}
		if err := repository.DB.Create(&p).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建隐患记录失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "检查台账记录已新建！", "record": p})

	case "leave_requests":
		var l model.LeaveRequest
		if err := c.ShouldBindJSON(&l); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		l.CreatedAt = time.Now()
		if l.Status == "" {
			l.Status = "pending"
		}
		if err := repository.DB.Create(&l).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建请假记录失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "部员请假申请已直接入库！", "record": l})

	case "recruitment_applications":
		var r model.RecruitmentApplication
		if err := c.ShouldBindJSON(&r); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		r.CreatedAt = time.Now()
		if r.Status == "" {
			r.Status = "submitted"
		}
		if err := repository.DB.Create(&r).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "录入招新报名失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "招新报名记录已建立！", "record": r})

	case "member_score_logs":
		var m model.MemberScoreLog
		if err := c.ShouldBindJSON(&m); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析失败: " + err.Error()})
			return
		}
		m.CreatedAt = time.Now()
		if err := repository.DB.Create(&m).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建积分流水失败: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "积分流水台账已录入！", "record": m})

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的数据名单类型"})
	}
}

// UpdateRecord 修改指定名单表中的一条记录
func (tdb *TechDBController) UpdateRecord(c *gin.Context) {
	tableKey := c.Param("table")
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的记录 ID"})
		return
	}

	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数解析错误: " + err.Error()})
		return
	}
	delete(payload, "id")
	delete(payload, "created_at")

	var result interface{}
	switch tableKey {
	case "students":
		var item model.Student
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		repository.DB.Model(&item).Updates(payload)
		result = item

	case "dorm_roster_presets":
		var item model.DormRosterPreset
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		repository.DB.Model(&item).Updates(payload)
		result = item

	case "users":
		var item model.User
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		payload["updated_at"] = time.Now()
		repository.DB.Model(&item).Updates(payload)
		result = item

	case "schedule_shifts":
		var item model.ScheduleShift
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		repository.DB.Model(&item).Updates(payload)
		result = item

	case "inspection_photos":
		var item model.InspectionPhoto
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		repository.DB.Model(&item).Updates(payload)
		result = item

	case "leave_requests":
		var item model.LeaveRequest
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		repository.DB.Model(&item).Updates(payload)
		result = item

	case "recruitment_applications":
		var item model.RecruitmentApplication
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		repository.DB.Model(&item).Updates(payload)
		result = item

	case "member_score_logs":
		var item model.MemberScoreLog
		if err := repository.DB.First(&item, id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "记录不存在"})
			return
		}
		repository.DB.Model(&item).Updates(payload)
		result = item

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的数据名单类型"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "记录已成功更新！", "record": result})
}

// DeleteRecord 删除指定名单表的一条记录
func (tdb *TechDBController) DeleteRecord(c *gin.Context) {
	tableKey := c.Param("table")
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的记录 ID"})
		return
	}

	switch tableKey {
	case "students":
		repository.DB.Delete(&model.Student{}, id)
	case "dorm_roster_presets":
		repository.DB.Delete(&model.DormRosterPreset{}, id)
	case "users":
		// 防误删保护：不允许删除 ID 为 1 的技术管理员根账户
		if id == 1 {
			c.JSON(http.StatusForbidden, gin.H{"error": "系统核心管理员账户受保护，禁止删除"})
			return
		}
		repository.DB.Delete(&model.User{}, id)
	case "schedule_shifts":
		repository.DB.Delete(&model.ScheduleShift{}, id)
	case "inspection_photos":
		repository.DB.Delete(&model.InspectionPhoto{}, id)
	case "leave_requests":
		repository.DB.Delete(&model.LeaveRequest{}, id)
	case "recruitment_applications":
		repository.DB.Delete(&model.RecruitmentApplication{}, id)
	case "member_score_logs":
		repository.DB.Delete(&model.MemberScoreLog{}, id)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的数据名单类型"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("已成功删除 %s 表中 ID 为 %d 的记录！", tableKey, id)})
}
