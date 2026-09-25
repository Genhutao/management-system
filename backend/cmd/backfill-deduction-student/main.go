package main

// backfill-deduction-student 把历史打表记录按 姓名 + 班级 回填到宿位名册主键。
//
// 默认只做演练、不写库；确认输出无误后加 -apply 才真正更新。
// 姓名+班级只能唯一定位时才回填，有歧义的一律留给人工处理，避免把档案接错人。
//
//	go run ./cmd/backfill-deduction-student            # 演练
//	go run ./cmd/backfill-deduction-student -apply     # 执行

import (
	"flag"
	"fmt"
	"log"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
)

func main() {
	dbPath := flag.String("db", "xgh_system.db", "SQLite 数据库文件路径")
	apply := flag.Bool("apply", false, "真正写入数据库（默认只演练）")
	limit := flag.Int("limit", 50, "最多展示多少条无法自动判定的记录")
	flag.Parse()

	db, err := gorm.Open(sqlite.Open(*dbPath), &gorm.Config{})
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}

	var records []model.DeductionRecord
	if err := db.Where("student_id = 0").Order("id asc").Find(&records).Error; err != nil {
		log.Fatalf("读取打表记录失败: %v", err)
	}

	fmt.Printf("待回填的打表记录: %d 条\n", len(records))
	if len(records) == 0 {
		fmt.Println("没有需要回填的记录。")
		return
	}

	var linked, ambiguous, missing int
	var shown int
	updates := make(map[uint]uint)

	for _, r := range records {
		var candidates []model.Student
		q := db.Model(&model.Student{}).Where("real_name = ?", r.StudentName)
		if r.ClassName != "" {
			q = q.Where("class_name = ?", r.ClassName)
		}
		if r.RoomNumber != "" {
			q = q.Where("room_number = ?", r.RoomNumber)
		}
		q.Find(&candidates)

		switch len(candidates) {
		case 0:
			missing++
			if shown < *limit {
				fmt.Printf("  [#%d] 名册查无此人: %s / %s / %s室\n", r.ID, r.StudentName, r.ClassName, r.RoomNumber)
				shown++
			}
		case 1:
			linked++
			updates[r.ID] = candidates[0].ID
		default:
			ambiguous++
			if shown < *limit {
				fmt.Printf("  [#%d] 名册中有 %d 人同名，需人工指定: %s / %s\n", r.ID, len(candidates), r.StudentName, r.ClassName)
				shown++
			}
		}
	}

	fmt.Printf("\n可自动回填 %d 条；同名需人工 %d 条；名册查无 %d 条\n", linked, ambiguous, missing)

	if !*apply {
		fmt.Println("\n演练模式，未写入任何数据。确认无误后加 -apply 执行。")
		return
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		for recordID, studentID := range updates {
			if err := tx.Model(&model.DeductionRecord{}).Where("id = ? AND student_id = 0", recordID).
				Update("student_id", studentID).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.OperationLog{
			Action:       "deduction.backfill_student_id",
			TargetType:   "deduction_record",
			OperatorName: "系统回填工具",
			Detail:       fmt.Sprintf("自动回填 %d 条打表记录的 student_id；%d 条同名待人工处理；%d 条名册查无此人", linked, ambiguous, missing),
		}).Error
	})
	if err != nil {
		log.Fatalf("回填写入失败: %v", err)
	}
	fmt.Printf("已回填 %d 条。\n", linked)
}
