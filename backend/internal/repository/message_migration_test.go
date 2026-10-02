package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
)

// AutoMigrate 的标签写错（default、size、可空性）在业务代码里往往要等到第一次真实写入才暴露，
// 这里直接把 messages 表建出来验一遍：表在、默认值生效、read_at 可以为空。
func TestMessageTableMigratesWithDefaultKind(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Message{}); err != nil {
		t.Fatalf("messages 表迁移失败: %v", err)
	}
	if !db.Migrator().HasTable(&model.Message{}) {
		t.Fatalf("迁移后仍不存在 messages 表")
	}

	sent := time.Now().Add(-time.Hour)
	if err := db.Create(&model.Message{
		SenderID: 1, SenderName: "部员甲", RecipientID: 2, RecipientName: "部长乙",
		Title: "查寝时段调整", Body: "今晚改为 19:30", CreatedAt: sent,
	}).Error; err != nil {
		t.Fatalf("写入站内信失败: %v", err)
	}

	var got model.Message
	if err := db.First(&got).Error; err != nil {
		t.Fatalf("读回站内信失败: %v", err)
	}
	if got.Kind != model.MessageKindHuman {
		t.Errorf("kind 未落库为默认 human，实际 %q（default 标签失效）", got.Kind)
	}
	if got.ReadAt != nil {
		t.Errorf("新消息应为未读，实际 read_at=%v", *got.ReadAt)
	}

	// 收件人本人清未读才写 read_at，且只写一次不覆盖首次时间
	now := time.Now()
	if err := db.Model(&model.Message{}).Where("id = ? AND recipient_id = ? AND read_at IS NULL", got.ID, 2).
		Update("read_at", now).Error; err != nil {
		t.Fatalf("首次清未读失败: %v", err)
	}
	if err := db.First(&got, got.ID).Error; err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if got.ReadAt == nil {
		t.Fatalf("清未读后 read_at 仍为空")
	}
	first := *got.ReadAt
	// 再跑一次同样的条件语句：read_at IS NULL 已不成立，不该改动任何行
	if err := db.Model(&model.Message{}).Where("id = ? AND recipient_id = ? AND read_at IS NULL", got.ID, 2).
		Update("read_at", now.Add(48*time.Hour)).Error; err != nil {
		t.Fatalf("重复清未读失败: %v", err)
	}
	if err := db.First(&got, got.ID).Error; err != nil {
		t.Fatalf("重读失败: %v", err)
	}
	if !got.ReadAt.Equal(first) {
		t.Errorf("重复清未读覆盖了首次已读时间: %v -> %v", first, *got.ReadAt)
	}

	// 别人拿同一条 id 想清自己的未读：条件里 recipient_id 不匹配，不该有任何行被改
	if rows := db.Model(&model.Message{}).Where("id = ? AND recipient_id = ? AND read_at IS NULL", got.ID, 999).
		Update("read_at", now).RowsAffected; rows != 0 {
		t.Errorf("非收件人竟改动了 %d 行已读状态", rows)
	}
}
