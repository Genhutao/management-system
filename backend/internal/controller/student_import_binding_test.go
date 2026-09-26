package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 覆盖导入会整表换掉 students，旧 student_id 从此指向已不存在的人。
// 悬空绑定比从未绑定更坏：寝室评优只过滤 student_id > 0 会照计一个查无此人的扣分，
// 而学生档案查询是 `student_id = ? OR (student_id = 0 AND 姓名+班级)`，悬空记录两边都不匹配。
// 这里的口径是"降级、不重绑"：每人分数每学期清零，覆盖导入正是换学期的时刻，
// 按姓名+班级把旧记录接到新名册上等于把上学期的扣分记到一个同名的人身上。

func setupImportDB(t *testing.T) uint {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Student{}, &model.DeductionRecord{}, &model.OperationLog{}); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	prev := repository.DB
	repository.DB = db
	t.Cleanup(func() {
		repository.DB = prev
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	op := model.User{
		Username: "tech_vice", RealName: "副部长甲", Role: model.RoleMember,
		Department: "技术部", Position: model.PositionVice, Status: "active",
	}
	if err := db.Create(&op).Error; err != nil {
		t.Fatalf("写入操作者失败: %v", err)
	}
	return op.ID
}

func importStudent(name, class, room string) model.Student {
	return model.Student{
		RealName: name, ClassName: class, Grade: "高一",
		Building: "1号楼", RoomNumber: room, Status: "active",
	}
}

func seedRoster(t *testing.T, name, class, room string) uint {
	t.Helper()
	s := importStudent(name, class, room)
	if err := repository.DB.Create(&s).Error; err != nil {
		t.Fatalf("写入名册学生 %s 失败: %v", name, err)
	}
	return s.ID
}

func seedBoundDeduction(t *testing.T, studentID uint, name, class, room string) uint {
	t.Helper()
	r := model.DeductionRecord{
		StudentID: studentID, Building: "1号楼", Floor: "3F", RoomNumber: room,
		StudentName: name, ClassName: class, Grade: "高一",
		Category: "内务卫生不合格", DeductPoints: 2, Reason: "验证覆盖导入的绑定收敛",
		InspectorName: "副部长甲", Status: "confirmed",
	}
	if err := repository.DB.Create(&r).Error; err != nil {
		t.Fatalf("写入打表记录失败: %v", err)
	}
	return r.ID
}

func readDeductionBinding(t *testing.T, recordID uint) uint {
	t.Helper()
	var r model.DeductionRecord
	if err := repository.DB.First(&r, recordID).Error; err != nil {
		t.Fatalf("读取打表记录 %d 失败: %v", recordID, err)
	}
	return r.StudentID
}

func runBatchImport(t *testing.T, operatorID uint, req BatchImportRequest) (int, map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("序列化导入请求失败: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/students/batch-import", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", operatorID)

	(&StudentController{}).BatchImport(c)

	var out map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestOverwriteImportReleasesDanglingBindings(t *testing.T) {
	op := setupImportDB(t)
	a := seedRoster(t, "张三", "高一(3)班", "302")
	b := seedRoster(t, "李四", "高一(4)班", "305")
	ra := seedBoundDeduction(t, a, "张三", "高一(3)班", "302")
	rb := seedBoundDeduction(t, b, "李四", "高一(4)班", "305")

	status, out := runBatchImport(t, op, BatchImportRequest{
		Students:         []model.Student{importStudent("王五", "高一(1)班", "101")},
		Overwrite:        true,
		OverwriteConfirm: "REPLACE_ALL_ROSTER",
	})
	if status != http.StatusOK {
		t.Fatalf("覆盖导入未成功: %d %v", status, out)
	}
	if got := readDeductionBinding(t, ra); got != 0 {
		t.Errorf("张三的记录仍挂在已失效主键上: student_id=%d", got)
	}
	if got := readDeductionBinding(t, rb); got != 0 {
		t.Errorf("李四的记录仍挂在已失效主键上: student_id=%d", got)
	}
	if out["bindings_released"] != float64(2) {
		t.Errorf("响应未如实报告解除条数: %v", out["bindings_released"])
	}
	if !strings.Contains(asString(out["message"]), "退回仅按姓名存底") {
		t.Errorf("响应提示未说明历史记录退回仅按姓名存底: %v", out["message"])
	}
}

// 新学期名册里出现同名同班的人，也不得把上学期的扣分接过去。
func TestOverwriteImportDoesNotRebindSameNameStudent(t *testing.T) {
	op := setupImportDB(t)
	old := seedRoster(t, "张三丰", "高一(3)班", "302")
	rec := seedBoundDeduction(t, old, "张三丰", "高一(3)班", "302")

	status, out := runBatchImport(t, op, BatchImportRequest{
		// 同名、同班、同寝室——若按姓名+班级重绑，这一条会被原样接回去
		Students:         []model.Student{importStudent("张三丰", "高一(3)班", "302")},
		Overwrite:        true,
		OverwriteConfirm: "REPLACE_ALL_ROSTER",
	})
	if status != http.StatusOK {
		t.Fatalf("覆盖导入未成功: %d %v", status, out)
	}

	var next model.Student
	if err := repository.DB.Where("real_name = ?", "张三丰").First(&next).Error; err != nil {
		t.Fatalf("新名册学生未入库: %v", err)
	}
	if got := readDeductionBinding(t, rec); got != 0 {
		t.Errorf("上学期的扣分被重绑到新学期同名学生 %d 上: student_id=%d", next.ID, got)
	}
	if out["bindings_released"] != float64(1) {
		t.Errorf("应报告解除 1 条: %v", out["bindings_released"])
	}
}

// 客户端自带的 id 一律作废：否则新生能占用旧生已失效的主键，绑定会挂到另一个人身上。
func TestImportIgnoresClientSuppliedStudentID(t *testing.T) {
	op := setupImportDB(t)
	old := seedRoster(t, "赵六", "高二(2)班", "205")
	rec := seedBoundDeduction(t, old, "赵六", "高二(2)班", "205")

	steal := importStudent("新同学", "高一(9)班", "901")
	steal.ID = old // 直接认领旧主键
	status, out := runBatchImport(t, op, BatchImportRequest{
		Students:         []model.Student{steal},
		Overwrite:        true,
		OverwriteConfirm: "REPLACE_ALL_ROSTER",
	})
	if status != http.StatusOK {
		t.Fatalf("覆盖导入未成功: %d %v", status, out)
	}

	var s model.Student
	if err := repository.DB.Where("real_name = ?", "新同学").First(&s).Error; err != nil {
		t.Fatalf("新名册学生未入库: %v", err)
	}
	if s.ID == old {
		t.Fatalf("客户端自带 id 未被作废，新生占用了旧主键 %d", old)
	}
	if got := readDeductionBinding(t, rec); got != 0 {
		t.Errorf("旧记录被挂到了占用该主键的新生身上: student_id=%d", got)
	}
}

func TestAppendImportKeepsValidBindings(t *testing.T) {
	op := setupImportDB(t)
	kept := seedRoster(t, "孙七", "高一(5)班", "505")
	rec := seedBoundDeduction(t, kept, "孙七", "高一(5)班", "505")

	status, out := runBatchImport(t, op, BatchImportRequest{
		Students: []model.Student{importStudent("周八", "高一(6)班", "606")},
	})
	if status != http.StatusOK {
		t.Fatalf("追加导入未成功: %d %v", status, out)
	}
	if got := readDeductionBinding(t, rec); got != kept {
		t.Errorf("追加导入动了仍然有效的绑定: student_id=%d，应为 %d", got, kept)
	}
	if out["bindings_released"] != float64(0) {
		t.Errorf("追加导入不该解除任何绑定: %v", out["bindings_released"])
	}
}

// 收敛只处理悬空绑定：有效绑定与本来就仅按姓名存底的记录都不该被改写。
func TestUnlinkDanglingDeductionsOnlyTouchesDangling(t *testing.T) {
	setupImportDB(t)
	alive := seedRoster(t, "吴九", "高一(7)班", "707")
	bound := seedBoundDeduction(t, alive, "吴九", "高一(7)班", "707")
	nameOnly := seedBoundDeduction(t, 0, "郑十", "高一(8)班", "808")
	dangling := seedBoundDeduction(t, 4242, "钱十一", "高一(9)班", "909")

	n, err := unlinkDanglingDeductions(repository.DB)
	if err != nil {
		t.Fatalf("收敛悬空绑定失败: %v", err)
	}
	if n != 1 {
		t.Errorf("应只影响 1 条悬空记录，实际 %d", n)
	}
	if got := readDeductionBinding(t, dangling); got != 0 {
		t.Errorf("悬空记录未被降级: student_id=%d", got)
	}
	if got := readDeductionBinding(t, bound); got != alive {
		t.Errorf("有效绑定被误改: student_id=%d", got)
	}
	if got := readDeductionBinding(t, nameOnly); got != 0 {
		t.Errorf("仅按姓名存底的记录被误改: student_id=%d", got)
	}
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}
