package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
)

// 信任表是插件能否跨重启自动拉起唯一的依据，形态比一般表怪一点：
// 主键是插件 id 字符串（不是自增数字），撤销只把 revoked_at 填上、行留着。
// 这两点都靠 AutoMigrate 的标签落地，写错了要等到"重启后拉起一个已撤销的插件"才发现，
// 所以这里直接把表建出来验一遍。
func TestPluginTrustTableMigratesWithStringPrimaryKey(t *testing.T) {
	db := newPluginTrustTestDB(t)
	if !db.Migrator().HasTable(&model.PluginTrust{}) {
		t.Fatalf("迁移后仍不存在 plugin_trusts 表")
	}

	trustedAt := time.Now().Add(-2 * time.Hour)
	row := model.PluginTrust{
		ID:            "diskusage",
		ExecHash:      "aa0123456789abcdef0123456789abcdef",
		ManifestHash:  "bb0123456789abcdef0123456789abcdef",
		TrustedBy:     1,
		TrustedByName: "技术维护组甲",
		TrustedAt:     trustedAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("写入信任记录失败: %v", err)
	}

	var got model.PluginTrust
	if err := db.First(&got, "id = ?", "diskusage").Error; err != nil {
		t.Fatalf("按插件 id 读回失败（主键没落到 id 列上？）: %v", err)
	}
	if !got.Active() {
		t.Errorf("刚授权应为有效，实际 revoked_at=%v", got.RevokedAt)
	}
	if got.TrustedByName != "技术维护组甲" {
		t.Errorf("授权人快照没落库，实际 %q", got.TrustedByName)
	}

	// 撤销：填 revoked_*，行必须还在（"谁撤的"要能查）
	now := time.Now()
	if err := db.Model(&model.PluginTrust{}).Where("id = ?", "diskusage").
		Updates(map[string]any{"revoked_at": &now, "revoked_by": 2, "revoked_by_name": "部长乙"}).Error; err != nil {
		t.Fatalf("撤销更新失败: %v", err)
	}
	if err := db.First(&got, "id = ?", "diskusage").Error; err != nil {
		t.Fatalf("撤销后行不应消失: %v", err)
	}
	if got.Active() {
		t.Error("填了 revoked_at 就该判为已撤销")
	}

	// 重新授权复用同一行：一个插件留多条信任史，"现在到底信不信"就变成要挑一条的问题。
	// map 更新里放 Go 的 nil 就是 SQL 的 NULL（不是"跳过该字段"），撤销状态确实被清掉。
	if err := db.Model(&model.PluginTrust{}).Where("id = ?", "diskusage").
		Updates(map[string]any{"revoked_at": nil, "exec_hash": "cc0123456789abcdef0123456789abcdef"}).Error; err != nil {
		t.Fatalf("重新授权更新失败: %v", err)
	}
	var count int64
	if err := db.Model(&model.PluginTrust{}).Where("id = ?", "diskusage").Count(&count).Error; err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if count != 1 {
		t.Errorf("同一插件应只有一条信任记录，实际 %d 条", count)
	}
	// 读回用**新变量**：同一个结构体复用两次时，GORM 会透过上一次留下的那个非空指针赋值，
	// 库里的 NULL 于是读成"还是刚才那个时间"——这正是本用例最容易误判成"更新没生效"的地方。
	var after model.PluginTrust
	if err := db.First(&after, "id = ?", "diskusage").Error; err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if !after.Active() {
		t.Errorf("清空 revoked_at 后应重新判为有效，实际 revoked_at=%v", after.RevokedAt)
	}
	if after.ExecHash != "cc0123456789abcdef0123456789abcdef" {
		t.Errorf("重新授权要记下新指纹，实际 %q", after.ExecHash)
	}
}

// 空指针语义单独钉一下：nil 的 *PluginTrust 不该被当成"已授权"。
// loader 里"查不到信任记录"就是这个值，判反了等于默认放行。
func TestPluginTrustNilIsNotActive(t *testing.T) {
	var missing *model.PluginTrust
	if missing.Active() {
		t.Error("查无此记录不该被读成已授权")
	}
}

func newPluginTrustTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.PluginTrust{}); err != nil {
		t.Fatalf("plugin_trusts 表迁移失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取底层连接失败: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}
