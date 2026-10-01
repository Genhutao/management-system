package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service/rosterparse"
)

type StudentController struct{}

// ParsePreviewRequest 预览解析请求体
type ParsePreviewRequest struct {
	RawText   string `json:"raw_text"`  // 直接粘贴的纯文本
	Separator string `json:"separator"` // 可选强制分隔符，留空自动检测
	// ColumnMap 是界面上逐列下拉指定的结果：字段名 → 列下标，-1 表示该列不导入。
	ColumnMap map[string]int `json:"column_map"`
	// HeaderLine 是表头所在行（1 起），0 表示这份表没有表头。仅在给了 ColumnMap 时生效。
	HeaderLine int `json:"header_line"`
}

// BatchImportRequest 批量入库确认请求体
type BatchImportRequest struct {
	Students         []model.Student `json:"students" binding:"required"`
	Overwrite        bool            `json:"overwrite"`         // 是否先清空原有名册再导入
	OverwriteConfirm string          `json:"overwrite_confirm"` // 覆盖导入必须等于 REPLACE_ALL_ROSTER
}

// maxRosterBytes 单次导入名单的体积上限。整表读进内存再交给解析内核，
// 上限既防呆（误传一份大文件）也保证错误可控——超限直接拒绝，不做半份解析。
const maxRosterBytes = 8 << 20

// previewSampleSize 预览表默认展示的行数。注意入库用的是全量，界面上必须把差额说出来。
const previewSampleSize = 15

// readRosterBytes 读取上传文件的原始字节。
//
// 编码、换行、表头识别全部下沉到 rosterparse，控制器只负责"拿到完整字节"这一件事；
// 读到 0 字节不当成"没有文件"处理，否则上传 .xlsx 会退回去读 JSON 分支，
// 最终报"请提供文件或粘贴文本"，把人引到完全错误的方向上。
func readRosterBytes(file *multipart.FileHeader) ([]byte, error) {
	if file.Size <= 0 {
		return nil, fmt.Errorf("上传文件 %s 是空文件", file.Filename)
	}
	if file.Size > maxRosterBytes {
		return nil, fmt.Errorf("上传文件 %s 超过 %d MB 上限，请按年级拆分后再导入", file.Filename, maxRosterBytes>>20)
	}
	f, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("上传文件 %s 打开失败：%w", file.Filename, err)
	}
	defer f.Close()

	content, err := io.ReadAll(io.LimitReader(f, maxRosterBytes+1))
	if err != nil {
		return nil, fmt.Errorf("上传文件 %s 读取失败：%w", file.Filename, err)
	}
	if len(content) > maxRosterBytes {
		return nil, fmt.Errorf("上传文件 %s 实际体积超过 %d MB 上限", file.Filename, maxRosterBytes>>20)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, fmt.Errorf("上传文件 %s 没有读到任何内容", file.Filename)
	}
	return content, nil
}

// toFieldColumns 把前端传来的"字段名→列下标"转成内核口径。
func toFieldColumns(raw map[string]int) map[rosterparse.Field]int {
	out := map[rosterparse.Field]int{}
	for name, idx := range raw {
		if idx >= 0 {
			out[rosterparse.Field(name)] = idx
		}
	}
	return out
}

// mappingFromForm 从 multipart 表单里取逐列下拉的指定结果。
func mappingFromForm(c *gin.Context) (map[string]int, int) {
	var columnMap map[string]int
	if raw := strings.TrimSpace(c.PostForm("column_map")); raw != "" {
		_ = json.Unmarshal([]byte(raw), &columnMap)
	}
	headerLine, _ := strconv.Atoi(c.PostForm("header_line"))
	return columnMap, headerLine
}

// ParseAndPreview 解析名单并给出逐行诊断预览。
//
// 列映射有三个来源，优先级从高到低：使用者在下拉里指定 → 这张表上次记住的映射 →
// 内核自动识别。三者都拿不准时如实报缺字段，不再编默认值。
func (sc *StudentController) ParseAndPreview(c *gin.Context) {
	var (
		content    []byte
		separator  string
		columnMap  map[string]int
		headerLine int
	)

	file, fileErr := c.FormFile("file")
	if fileErr == nil {
		b, err := readRosterBytes(file)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		content = b
		separator = c.PostForm("separator")
		columnMap, headerLine = mappingFromForm(c)
	} else {
		var req ParsePreviewRequest
		if err := c.ShouldBindJSON(&req); err == nil {
			content = []byte(req.RawText)
			separator = req.Separator
			columnMap, headerLine = req.ColumnMap, req.HeaderLine
		}
	}
	if len(bytes.TrimSpace(content)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供待导入的文件或粘贴文本"})
		return
	}

	opts := rosterparse.Options{Separator: separator, Now: time.Now()}
	mappingSource := "auto"
	if len(columnMap) > 0 {
		opts.ColumnMap = toFieldColumns(columnMap)
		opts.HeaderLine = headerLine
		mappingSource = "manual"
	}

	res, err := rosterparse.ParseUpload(content, opts)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 使用者没指定列 → 套用这张表上次记住的映射。
	// 用顶层指纹查，自动识别失败的表同样能命中；同一张表换一批人重导，指纹不变。
	if len(columnMap) == 0 && res.Fingerprint != "" {
		if saved, savedHeaderLine, ok := lookupColumnMapping(res.Fingerprint); ok {
			retry := rosterparse.Options{
				Separator:  separator,
				Now:        opts.Now,
				ColumnMap:  saved,
				HeaderLine: savedHeaderLine,
			}
			if again, retryErr := rosterparse.ParseUpload(content, retry); retryErr == nil {
				res = again
				mappingSource = "saved"
			}
		}
	}

	c.JSON(http.StatusOK, previewPayload(res, mappingSource))
}

// fieldOption 是界面上逐列下拉的一个可选项。
type fieldOption struct {
	Value    string `json:"value"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

// previewPayload 组装预览响应。
//
// 保留 total_recognized / grade_stats / preview_sample / all_parsed 四个既有键，
// 现网界面不改也能继续用；但 all_parsed 现在只含**可入库**的行——
// 缺字段的行绝不能被"确认入库"写进库。
func previewPayload(res *rosterparse.Result, mappingSource string) gin.H {
	importable := make([]rosterparse.Record, 0, len(res.Records))
	for _, r := range res.Records {
		if !r.Blocked() {
			importable = append(importable, r)
		}
	}
	preview := importable
	if len(preview) > previewSampleSize {
		preview = preview[:previewSampleSize]
	}

	required := map[rosterparse.Field]bool{}
	for _, f := range rosterparse.DefaultRequired {
		required[f] = true
	}
	options := make([]fieldOption, 0, len(rosterparse.SupportedFields))
	for _, f := range rosterparse.SupportedFields {
		options = append(options, fieldOption{Value: string(f), Label: f.Label(), Required: required[f]})
	}

	return gin.H{
		"total_recognized": len(importable),
		"blocked_count":    len(res.Records) - len(importable),
		"grade_stats":      res.Stats.GradeCounts,
		"preview_sample":   preview,
		"all_parsed":       importable,
		"stats":            res.Stats,
		"issues":           res.Issues,
		"header":           res.Header,
		"field_options":    options,
		"mapping_source":   mappingSource,
		"fingerprint":      res.Fingerprint,
	}
}

// lookupColumnMapping 按表头指纹取回记住的列映射及其表头行，并累计使用次数。
func lookupColumnMapping(fingerprint string) (map[rosterparse.Field]int, int, bool) {
	var row model.RosterColumnMapping
	if err := repository.DB.Where("fingerprint = ?", fingerprint).First(&row).Error; err != nil {
		return nil, 0, false
	}
	var raw map[string]int
	if err := json.Unmarshal([]byte(row.ColumnsJSON), &raw); err != nil {
		return nil, 0, false
	}
	columns := toFieldColumns(raw)
	if len(columns) == 0 {
		return nil, 0, false
	}
	repository.DB.Model(&row).UpdateColumn("use_count", gorm.Expr("use_count + 1"))
	return columns, row.HeaderLine, true
}

// SaveColumnMapping 记住一张表的列映射（POST，部长与技术维护组均可写）。
//
// 走 POST 而不是 PUT：既有的 minister 策略是 /students/* 的 (GET)|(POST)|(DELETE)，
// 而 casbin_rule 落库后只增不改，改那一行的通配符对已有数据库根本不会生效。
func (sc *StudentController) SaveColumnMapping(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}

	var req struct {
		Fingerprint  string         `json:"fingerprint"`
		HeaderLabels []string       `json:"header_labels"`
		ColumnMap    map[string]int `json:"column_map"`
		HeaderLine   int            `json:"header_line"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式不正确"})
		return
	}
	req.Fingerprint = strings.TrimSpace(req.Fingerprint)
	if len(req.Fingerprint) != 64 || isHexOnly(req.Fingerprint) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "表头指纹不合法，请重新执行一次识别"})
		return
	}
	if len(toFieldColumns(req.ColumnMap)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "至少要指定一个字段，否则记住的映射没有意义"})
		return
	}

	columnsJSON, err := json.Marshal(req.ColumnMap)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "列映射序列化失败"})
		return
	}
	labelsJSON, err := json.Marshal(req.HeaderLabels)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "表头信息序列化失败"})
		return
	}

	var existing model.RosterColumnMapping
	created := false
	if err := repository.DB.Where("fingerprint = ?", req.Fingerprint).First(&existing).Error; err != nil {
		row := model.RosterColumnMapping{
			Fingerprint:  req.Fingerprint,
			HeaderLabels: string(labelsJSON),
			ColumnsJSON:  string(columnsJSON),
			HeaderLine:   req.HeaderLine,
		}
		if err := repository.DB.Create(&row).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "列映射保存失败: " + err.Error()})
			return
		}
		created = true
	} else if err := repository.DB.Model(&existing).Updates(map[string]interface{}{
		"header_labels": string(labelsJSON),
		"columns_json":  string(columnsJSON),
		"header_line":   req.HeaderLine,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "列映射更新失败: " + err.Error()})
		return
	}

	message := "列映射已更新，下次导入同一张表会自动套用"
	if created {
		message = "列映射已记住，下次导入同一张表会自动套用"
	}
	logOperationAs(c, operator, "student_roster.save_column_mapping", "roster_column_mapping", existing.ID,
		fmt.Sprintf("%s；表头 %d 列，指定 %d 个字段", message, len(req.HeaderLabels), len(toFieldColumns(req.ColumnMap))))

	c.JSON(http.StatusOK, gin.H{"message": message, "fingerprint": req.Fingerprint, "created": created})
}

func isHexOnly(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return true
		}
	}
	return false
}

// unlinkDanglingDeductions 把 student_id 指向"名册里已不存在的人"的打表记录降级为仅按姓名存底。
//
// 悬空绑定比从未绑定更坏：寝室评优只过滤 student_id > 0，会把一个查无此人的扣分照计进榜单；
// 而学生档案查询是 `student_id = ? OR (student_id = 0 AND 姓名+班级)`，悬空记录两边都不匹配，
// 等于从这个人的档案里凭空消失。
//
// 刻意不按 姓名+班级 把旧记录重绑到新名册：每人分数每学期清零，覆盖导入正是换学期的时刻，
// 跨学期按姓名重绑等于把上学期的扣分记到一个同名的人身上。降级为 0 才是如实状态，
// 口径与 ClearAllStudents 的显式解除一致。
func unlinkDanglingDeductions(tx *gorm.DB) (int64, error) {
	res := tx.Model(&model.DeductionRecord{}).
		Where("student_id > 0 AND student_id NOT IN (SELECT id FROM students)").
		Update("student_id", 0)
	return res.RowsAffected, res.Error
}

// BatchImport 批量确认导入数据库
func (sc *StudentController) BatchImport(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}

	var req BatchImportRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Students) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "有效学生数据为空，请重新解析"})
		return
	}

	var previousCount int64
	repository.DB.Model(&model.Student{}).Count(&previousCount)

	if req.Overwrite && strings.TrimSpace(req.OverwriteConfirm) != "REPLACE_ALL_ROSTER" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":          fmt.Sprintf("覆盖导入会先清空现有 %d 条名册且不可逆，请带 overwrite_confirm=REPLACE_ALL_ROSTER 显式确认", previousCount),
			"previous_count": previousCount,
		})
		return
	}

	now := time.Now()
	for i := range req.Students {
		// 主键一律由服务端分配：students.id 是打表绑定的外键，若放过客户端自带的 id，
		// 覆盖导入后新生就能占用旧生已经失效的主键，本该解除的历史绑定会挂到另一个人身上。
		req.Students[i].ID = 0
		req.Students[i].CreatedAt = now
		if req.Students[i].Status == "" {
			req.Students[i].Status = "active"
		}
	}

	var released int64
	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if req.Overwrite {
			if err := tx.Exec("DELETE FROM students").Error; err != nil {
				return err
			}
		}
		// 批量高性能入库 (500 条一组)
		if err := tx.CreateInBatches(req.Students, 500).Error; err != nil {
			return err
		}
		// 覆盖导入换掉了整张名册，旧 student_id 从此指向已不存在的人。
		// 必须等新行插完再收敛：先删后清会把"新生恰好复用同一个 id"误判成有效绑定。
		if req.Overwrite {
			n, err := unlinkDanglingDeductions(tx)
			if err != nil {
				return err
			}
			released = n
		}
		return nil
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量入库失败: " + err.Error()})
		return
	}

	if req.Overwrite {
		logOperationAs(c, operator, "student_roster.overwrite_import", "student", 0,
			fmt.Sprintf("覆盖导入：清空原名册 %d 条，写入 %d 条；%d 条打表记录因原绑定对象已不在名册，退回仅按姓名存底",
				previousCount, len(req.Students), released))
	} else {
		logOperationAs(c, operator, "student_roster.import", "student", 0,
			fmt.Sprintf("追加导入 %d 条，导入前名册共 %d 条", len(req.Students), previousCount))
	}

	message := fmt.Sprintf("成功导入 %d 名高一~高三学生数据！楼栋、寝室与班级信息已建立索引关联", len(req.Students))
	if req.Overwrite && released > 0 {
		message += fmt.Sprintf("；另有 %d 条历史打表记录因绑定对象已不在新名册，退回仅按姓名存底（不再计入寝室评优）", released)
	}

	c.JSON(http.StatusOK, gin.H{
		"message":           message,
		"count":             len(req.Students),
		"bindings_released": released,
	})
}

// GetStudents 多维检索学生名册
func (sc *StudentController) GetStudents(c *gin.Context) {
	grade := c.Query("grade")       // 高一, 高二, 高三
	building := c.Query("building") // 1号楼
	room := c.Query("room_number")  // 302
	className := c.Query("class_name")
	keyword := c.Query("keyword") // 姓名或学号

	query := repository.DB.Model(&model.Student{})
	if grade != "" {
		query = query.Where("grade = ?", grade)
	}
	if building != "" {
		query = query.Where("building LIKE ?", "%"+building+"%")
	}
	if room != "" {
		query = query.Where("room_number LIKE ?", "%"+room+"%")
	}
	if className != "" {
		query = query.Where("class_name LIKE ?", "%"+className+"%")
	}
	if keyword != "" {
		query = query.Where("real_name LIKE ? OR student_no LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	var total int64
	query.Count(&total)

	var list []model.Student
	query.Order("grade asc, class_name asc, building asc, room_number asc").Limit(100).Find(&list)

	// 统计高一至高三各年级人数
	type GradeCount struct {
		Grade string `json:"grade"`
		Count int64  `json:"count"`
	}
	var gradeStats []GradeCount
	repository.DB.Model(&model.Student{}).Select("grade, count(*) as count").Group("grade").Scan(&gradeStats)

	c.JSON(http.StatusOK, gin.H{
		"total":       total,
		"items":       list,
		"grade_stats": gradeStats,
	})
}

// GetRoomStudents 查寝联动接口：输入楼栋与寝室号，秒级调出该寝室入住学生名单
func (sc *StudentController) GetRoomStudents(c *gin.Context) {
	building := c.Query("building")
	room := c.Query("room_number")

	if room == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供寝室房间号"})
		return
	}

	// D-6 PII 最小化：手机号仅宿管与技术维护组可见明文（宿管需联系学生），其余角色脱敏
	role, _ := c.Get("role")
	roleStr, _ := role.(string)
	maySeePhone := roleStr == model.RoleDormManager || roleStr == model.RoleTechAdmin

	query := repository.DB.Model(&model.Student{}).Where("room_number = ?", room)
	if building != "" {
		query = query.Where("building LIKE ?", "%"+building+"%")
	}

	var list []model.Student
	query.Find(&list)

	if !maySeePhone {
		for i := range list {
			list[i].Phone = maskPhone(list[i].Phone)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"building":    building,
		"room_number": room,
		"students":    list,
		"count":       len(list),
	})
}

// ClearAllStudents 一键清空名册：要求显式确认，并保留已产生扣分记录的历史
func (sc *StudentController) ClearAllStudents(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}

	var req struct {
		Confirm      string `json:"confirm"`
		UnlinkLinked bool   `json:"unlink_linked"`
	}
	_ = c.ShouldBindJSON(&req)
	if strings.TrimSpace(req.Confirm) != "DELETE_ALL_ROSTER" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "清空全校宿位名册不可逆，请带 confirm=DELETE_ALL_ROSTER 显式确认",
		})
		return
	}

	var rosterCount, linkedCount int64
	repository.DB.Model(&model.Student{}).Count(&rosterCount)
	repository.DB.Model(&model.DeductionRecord{}).Where("student_id > 0").Count(&linkedCount)

	if linkedCount > 0 && !req.UnlinkLinked {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("有 %d 条打表记录已关联名册主键，清空后这些记录会退回仅按姓名存底的状态。"+
				"确认接受请再带 unlink_linked=true 重试；扣分记录本身不会被删除。", linkedCount),
			"roster_count":      rosterCount,
			"linked_deductions": linkedCount,
		})
		return
	}

	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if linkedCount > 0 {
			if err := tx.Model(&model.DeductionRecord{}).Where("student_id > 0").
				Update("student_id", 0).Error; err != nil {
				return err
			}
		}
		return tx.Exec("DELETE FROM students").Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "清空名册失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "student_roster.clear_all", "student", 0,
		fmt.Sprintf("清空全校宿位名册 %d 条；%d 条打表记录退回未关联状态（扣分记录保留）", rosterCount, linkedCount))

	c.JSON(http.StatusOK, gin.H{
		"message":  fmt.Sprintf("学生名册已清空 %d 条；打表记录全部保留，其中 %d 条已退回仅按姓名存底。", rosterCount, linkedCount),
		"cleared":  rosterCount,
		"unlinked": linkedCount,
	})
}

type gormSession struct{}
