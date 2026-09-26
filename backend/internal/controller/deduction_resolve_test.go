package controller

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// resolveStudent 是"落实到人"的唯一闸口：显式传 student_id 时以名册为准回填姓名与班级，
// 并把楼栋、寝室两个坐标都校验掉。楼栋这一道是本轮补的——寝室号跨楼栋会重复，
// 只比对寝室号时选错人也能通过强校验，把扣分写进另一栋楼。

func setupResolveDB(t *testing.T) {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Student{}); err != nil {
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
}

func seedRosterStudent(t *testing.T, name, class, building, room string) uint {
	t.Helper()
	s := model.Student{
		RealName: name, ClassName: class, Grade: "高一",
		Building: building, RoomNumber: room, Status: "active",
	}
	if err := repository.DB.Create(&s).Error; err != nil {
		t.Fatalf("写入名册学生 %s 失败: %v", name, err)
	}
	return s.ID
}

func TestResolveStudentRejectsBuildingMismatch(t *testing.T) {
	setupResolveDB(t)
	id := seedRosterStudent(t, "张三", "高一(3)班", "1号楼", "302")
	seedRosterStudent(t, "李四", "高一(4)班", "2号楼", "302")

	// 两栋楼都有 302 室：只比对寝室号的话这条会通过
	got, errMsg := resolveStudent("2号楼", "302", "张三", "", "", id)
	if errMsg == "" {
		t.Fatalf("跨楼栋错绑未被拒绝，得到 %+v", got)
	}
	if !strings.Contains(errMsg, "栋不符") {
		t.Fatalf("报错口径不是楼栋不一致: %s", errMsg)
	}
	if got != nil {
		t.Fatalf("拒绝时必须返回空档案，得到 %+v", got)
	}
}

func TestResolveStudentAcceptsLooseBuildingWording(t *testing.T) {
	setupResolveDB(t)
	id := seedRosterStudent(t, "王五", "高二(1)班", "西12号楼", "501")

	// 打表单惯写"12号楼"，名册记"西12号楼"，与查询侧 LIKE %?% 同口径，不该硬拒
	got, errMsg := resolveStudent("12号楼", "501", "", "", "", id)
	if errMsg != "" {
		t.Fatalf("宽松楼栋写法被误拒: %s", errMsg)
	}
	if got == nil || got.StudentID != id || !got.RosterChecked {
		t.Fatalf("未按名册主键绑定，得到 %+v", got)
	}
	// 姓名与班级以名册为准，不接受提交侧的自报值
	if got.RealName != "王五" || got.ClassName != "高二(1)班" {
		t.Fatalf("未以名册覆盖自报姓名班级: %+v", got)
	}
}

func TestResolveStudentRejectsRoomMismatch(t *testing.T) {
	setupResolveDB(t)
	id := seedRosterStudent(t, "赵六", "高一(2)班", "1号楼", "302")

	if _, errMsg := resolveStudent("1号楼", "303", "赵六", "", "", id); errMsg == "" {
		t.Fatal("寝室号不一致未被拒绝")
	} else if !strings.Contains(errMsg, "室不一致") {
		t.Fatalf("报错口径不是寝室不一致: %s", errMsg)
	}
}

func TestResolveStudentRejectsInactiveStudent(t *testing.T) {
	setupResolveDB(t)
	id := seedRosterStudent(t, "孙七", "高一(5)班", "1号楼", "308")
	if err := repository.DB.Model(&model.Student{}).Where("id = ?", id).Update("status", "withdrawn").Error; err != nil {
		t.Fatalf("改住宿状态失败: %v", err)
	}

	if _, errMsg := resolveStudent("1号楼", "308", "孙七", "", "", id); errMsg == "" {
		t.Fatal("非在住学生未被拒绝")
	} else if !strings.Contains(errMsg, "不能作为在寝违纪对象") {
		t.Fatalf("报错口径不是住宿状态: %s", errMsg)
	}
}

func TestResolveStudentNamePathStillRejectsSameName(t *testing.T) {
	setupResolveDB(t)
	seedRosterStudent(t, "周静", "高一(1)班", "1号楼", "310")
	seedRosterStudent(t, "周静", "高一(6)班", "1号楼", "310")

	// 没传主键时同寝同名只能拒；这正是点选选择器要解决的问题
	if _, errMsg := resolveStudent("1号楼", "310", "周静", "", "", 0); errMsg == "" {
		t.Fatal("同寝同名未被拒绝")
	} else if !strings.Contains(errMsg, "同名") {
		t.Fatalf("报错口径不是同名歧义: %s", errMsg)
	}
}

func TestResolveStudentFallsBackToNameOnly(t *testing.T) {
	setupResolveDB(t)
	seedRosterStudent(t, "在册学生", "高一(1)班", "1号楼", "320")

	// 本寝整体没有登记时才允许按姓名存底，且必须留 Note 供名册导入后回填外键
	got, errMsg := resolveStudent("1号楼", "999", "借住学员", "高一(9)班", "高一", 0)
	if errMsg != "" {
		t.Fatalf("本寝无登记时应放行: %s", errMsg)
	}
	if got.StudentID != 0 || got.RosterChecked {
		t.Fatalf("不应声称已核对名册: %+v", got)
	}
	if !strings.Contains(got.Note, "仅按姓名存底") {
		t.Fatalf("缺少可追溯的宽松说明: %+v", got)
	}
}

func TestSameBuildingMatching(t *testing.T) {
	cases := []struct {
		roster, input string
		want          bool
		desc          string
	}{
		{"1号楼", "1号楼", true, "完全一致"},
		{"西12号楼", "12号楼", true, "名册带方位前缀"},
		{"1号楼", "1号楼（男）", true, "打表侧带性别后缀"},
		{"1号楼", "2号楼", false, "不同栋"},
		{"", "1号楼", true, "名册楼栋留空不硬拒"},
		{"1号楼", "  ", true, "提交楼栋为空不硬拒"},
	}
	for _, c := range cases {
		if got := sameBuilding(c.roster, c.input); got != c.want {
			t.Errorf("%s: sameBuilding(%q, %q) = %v, 期望 %v", c.desc, c.roster, c.input, got, c.want)
		}
	}
}
