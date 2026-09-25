package controller

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type StudentController struct{}

// ParsePreviewRequest 预览解析请求体
type ParsePreviewRequest struct {
	RawText   string `json:"raw_text"`   // 直接粘贴的纯文本
	Separator string `json:"separator"`  // 可选强制分隔符，留空自动检测
}

// BatchImportRequest 批量入库确认请求体
type BatchImportRequest struct {
	Students        []model.Student `json:"students" binding:"required"`
	Overwrite       bool            `json:"overwrite"`        // 是否先清空原有名册再导入
	OverwriteConfirm string         `json:"overwrite_confirm"` // 覆盖导入必须等于 REPLACE_ALL_ROSTER
}

// 自动特征识别提取出的学生中间对象
type ExtractedStudent struct {
	RealName   string `json:"real_name"`
	Grade      string `json:"grade"`       // 高一, 高二, 高三
	ClassName  string `json:"class_name"`  // 如 "高一(2)班"
	Building   string `json:"building"`    // 如 "1号楼"
	RoomNumber string `json:"room_number"` // 如 "302"
	BedNumber  string `json:"bed_number"`
	StudentNo  string `json:"student_no"`
	SourceLine string `json:"source_line"` // 原行文本
}

// ParseAndPreview 智能特征识别与解析预览
func (sc *StudentController) ParseAndPreview(c *gin.Context) {
	var rawLines []string

	// 1. 尝试从上传文件中读取
	file, err := c.FormFile("file")
	if err == nil {
		f, fErr := file.Open()
		if fErr == nil {
			defer f.Close()
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line != "" {
					rawLines = append(rawLines, line)
				}
			}
		}
	}

	// 2. 若无文件，从 JSON raw_text 中读取
	if len(rawLines) == 0 {
		var req ParsePreviewRequest
		if err := c.ShouldBindJSON(&req); err == nil && req.RawText != "" {
			scanner := bufio.NewScanner(strings.NewReader(req.RawText))
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line != "" {
					rawLines = append(rawLines, line)
				}
			}
		}
	}

	if len(rawLines) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供待导入的文件或粘贴文本"})
		return
	}

	// 执行智能特征识别引擎
	extracted, stats := smartRecognizeStudents(rawLines)

	c.JSON(http.StatusOK, gin.H{
		"total_recognized": len(extracted),
		"grade_stats":      stats,
		"preview_sample":   extracted[:min(15, len(extracted))],
		"all_parsed":       extracted,
	})
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
			"error": fmt.Sprintf("覆盖导入会先清空现有 %d 条名册且不可逆，请带 overwrite_confirm=REPLACE_ALL_ROSTER 显式确认", previousCount),
			"previous_count": previousCount,
		})
		return
	}

	now := time.Now()
	for i := range req.Students {
		req.Students[i].CreatedAt = now
		if req.Students[i].Status == "" {
			req.Students[i].Status = "active"
		}
	}

	err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if req.Overwrite {
			if err := tx.Exec("DELETE FROM students").Error; err != nil {
				return err
			}
		}
		// 批量高性能入库 (500 条一组)
		return tx.CreateInBatches(req.Students, 500).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "批量入库失败: " + err.Error()})
		return
	}

	if req.Overwrite {
		logOperationAs(c, operator, "student_roster.overwrite_import", "student", 0,
			fmt.Sprintf("覆盖导入：清空原名册 %d 条，写入 %d 条", previousCount, len(req.Students)))
	} else {
		logOperationAs(c, operator, "student_roster.import", "student", 0,
			fmt.Sprintf("追加导入 %d 条，导入前名册共 %d 条", len(req.Students), previousCount))
	}

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("成功导入 %d 名高一~高三学生数据！楼栋、寝室与班级信息已建立索引关联", len(req.Students)),
		"count":   len(req.Students),
	})
}

// GetStudents 多维检索学生名册
func (sc *StudentController) GetStudents(c *gin.Context) {
	grade := c.Query("grade")         // 高一, 高二, 高三
	building := c.Query("building")   // 1号楼
	room := c.Query("room_number")    // 302
	className := c.Query("class_name")
	keyword := c.Query("keyword")     // 姓名或学号

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
		Confirm     string `json:"confirm"`
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
			"roster_count": rosterCount,
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
		"message": fmt.Sprintf("学生名册已清空 %d 条；打表记录全部保留，其中 %d 条已退回仅按姓名存底。", rosterCount, linkedCount),
		"cleared": rosterCount,
		"unlinked": linkedCount,
	})
}

// =============================================================================
// 核心特征识别引擎算法 (支持 CSV/制表符/多空格/无序自由文本)
// =============================================================================
func smartRecognizeStudents(lines []string) ([]ExtractedStudent, map[string]int) {
	var results []ExtractedStudent
	stats := map[string]int{
		"高一": 0,
		"高二": 0,
		"高三": 0,
		"其他": 0,
	}

	if len(lines) == 0 {
		return results, stats
	}

	// 1. 判断首行是否为表头
	firstLine := lines[0]
	hasHeader, colMap, sep := detectHeader(firstLine)

	startIndex := 0
	if hasHeader {
		startIndex = 1
	}

	// 楼栋正则: 1号楼, 西12号楼, 7栋, A栋
	reBuilding := regexp.MustCompile(`([东西南北]?\d+[号栋楼]+|[A-Za-z]\d*[号栋楼]+|[东西南北]区\d*号?[楼栋]?)`)
	// 房间号正则: 302, 1004, 302室, 4-201
	reRoom := regexp.MustCompile(`\b([1-9]\d{2,3}(?:室|房)?|\d+-\d{2,3})\b`)
	// 年级班级正则: 高一(2)班, 高三3班, 高2401班, 高二(12)班
	reClass := regexp.MustCompile(`(高[一二三123]|202[3-7]级?)\s*\(?(\d{1,2})\)?\s*班?`)
	// 年级单独提取: 高一, 高二, 高三
	reGrade := regexp.MustCompile(`(高一|高二|高三|高1|高2|高3)`)
	// 姓名候选正则 (2~4 个连续中文字符)
	reChineseName := regexp.MustCompile(`[\p{Han}]{2,4}`)

	for i := startIndex; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		var st ExtractedStudent
		st.SourceLine = line

		// A. 如果识别出表头且行内包含对应分隔符
		if hasHeader && sep != "" && strings.Contains(line, sep) {
			cols := splitLine(line, sep)
			if colMap["building"] >= 0 && colMap["building"] < len(cols) {
				st.Building = strings.TrimSpace(cols[colMap["building"]])
			}
			if colMap["room"] >= 0 && colMap["room"] < len(cols) {
				st.RoomNumber = strings.TrimSpace(cols[colMap["room"]])
			}
			if colMap["name"] >= 0 && colMap["name"] < len(cols) {
				st.RealName = strings.TrimSpace(cols[colMap["name"]])
			}
			if colMap["class"] >= 0 && colMap["class"] < len(cols) {
				st.ClassName = strings.TrimSpace(cols[colMap["class"]])
			}
			if colMap["grade"] >= 0 && colMap["grade"] < len(cols) {
				st.Grade = strings.TrimSpace(cols[colMap["grade"]])
			}
			if colMap["no"] >= 0 && colMap["no"] < len(cols) {
				st.StudentNo = strings.TrimSpace(cols[colMap["no"]])
			}
		}

		// B. 正则模式特征兜底提取 (填补缺失字段)
		if st.Building == "" {
			if m := reBuilding.FindString(line); m != "" {
				st.Building = m
			}
		}
		if st.RoomNumber == "" {
			if m := reRoom.FindString(line); m != "" {
				st.RoomNumber = strings.TrimSuffix(strings.TrimSuffix(m, "室"), "房")
			}
		}
		if st.ClassName == "" {
			if m := reClass.FindString(line); m != "" {
				st.ClassName = normalizeClassName(m)
			}
		}
		if st.Grade == "" {
			if st.ClassName != "" {
				st.Grade = extractGradeFromClass(st.ClassName)
			} else if m := reGrade.FindString(line); m != "" {
				st.Grade = normalizeGrade(m)
			}
		}

		// 姓名提取
		if st.RealName == "" {
			// 在去除楼栋、寝室、班级后的残余字符串中提取 2~4 字姓名
			cleaned := line
			if st.Building != "" {
				cleaned = strings.Replace(cleaned, st.Building, " ", 1)
			}
			if st.RoomNumber != "" {
				cleaned = strings.Replace(cleaned, st.RoomNumber, " ", 1)
			}
			if st.ClassName != "" {
				cleaned = strings.Replace(cleaned, st.ClassName, " ", 1)
			}
			names := reChineseName.FindAllString(cleaned, -1)
			for _, n := range names {
				if n != "班级" && n != "宿舍" && n != "楼栋" && n != "寝室" && n != "姓名" && n != "高一" && n != "高二" && n != "高三" {
					st.RealName = n
					break
				}
			}
		}

		// 规范化与默认兜底
		if st.Building == "" {
			st.Building = "1号楼"
		}
		if st.Grade == "" {
			st.Grade = "高一"
		}
		if st.ClassName == "" {
			st.ClassName = st.Grade + "(1)班"
		}

		// 只要提取出了姓名或寝室号即认定为有效记录
		if st.RealName != "" || st.RoomNumber != "" {
			if st.RealName == "" {
				st.RealName = "学生"
			}
			results = append(results, st)
			if _, ok := stats[st.Grade]; ok {
				stats[st.Grade]++
			} else {
				stats["其他"]++
			}
		}
	}

	return results, stats
}

// 自动检测表头与列索引
func detectHeader(line string) (bool, map[string]int, string) {
	separators := []string{"\t", ",", ";", "|", "  "}
	var detectedSep string
	var cols []string

	for _, sep := range separators {
		if strings.Contains(line, sep) {
			detectedSep = sep
			cols = splitLine(line, sep)
			break
		}
	}

	if len(cols) == 0 {
		// 单空格尝试
		cols = strings.Fields(line)
		if len(cols) >= 3 {
			detectedSep = " "
		}
	}

	colMap := map[string]int{
		"building": -1,
		"room":     -1,
		"name":     -1,
		"class":    -1,
		"grade":    -1,
		"no":       -1,
	}

	hitCount := 0
	for i, c := range cols {
		c = strings.ToLower(strings.TrimSpace(c))
		if strings.Contains(c, "楼") || strings.Contains(c, "栋") || c == "building" {
			colMap["building"] = i
			hitCount++
		} else if strings.Contains(c, "寝") || strings.Contains(c, "房") || strings.Contains(c, "室") || c == "room" || c == "dorm" {
			colMap["room"] = i
			hitCount++
		} else if strings.Contains(c, "名") || strings.Contains(c, "姓") || c == "name" || c == "student" {
			colMap["name"] = i
			hitCount++
		} else if strings.Contains(c, "班") || c == "class" {
			colMap["class"] = i
			hitCount++
		} else if strings.Contains(c, "年级") || c == "grade" {
			colMap["grade"] = i
			hitCount++
		} else if strings.Contains(c, "学号") || c == "no" || c == "id" {
			colMap["no"] = i
		}
	}

	return hitCount >= 2, colMap, detectedSep
}

func splitLine(line, sep string) []string {
	if sep == "," {
		r := csv.NewReader(strings.NewReader(line))
		rec, err := r.Read()
		if err == nil {
			return rec
		}
	}
	if sep == "  " {
		return strings.Fields(line)
	}
	return strings.Split(line, sep)
}

func extractGradeFromClass(className string) string {
	if strings.Contains(className, "高一") || strings.Contains(className, "高1") {
		return "高一"
	}
	if strings.Contains(className, "高二") || strings.Contains(className, "高2") {
		return "高二"
	}
	if strings.Contains(className, "高三") || strings.Contains(className, "高3") {
		return "高三"
	}
	return "高一"
}

func normalizeGrade(g string) string {
	switch g {
	case "高1", "高一":
		return "高一"
	case "高2", "高二":
		return "高二"
	case "高3", "高三":
		return "高三"
	default:
		return "高一"
	}
}

func normalizeClassName(c string) string {
	c = strings.ReplaceAll(c, " ", "")
	c = strings.ReplaceAll(c, "（", "(")
	c = strings.ReplaceAll(c, "）", ")")
	if !strings.HasSuffix(c, "班") {
		c = c + "班"
	}
	return c
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type gormSession struct{}
