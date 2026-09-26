package model

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"xgh-system/pkg/secretbox"
)

func openHookTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("CRYPTO_SECRET", "hook-test-secret")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "hook.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&AIConfig{}, &TechWelfareGateway{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("underlying db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// rawAPIKey 绕开 AfterFind（Table 查询不触发模型钩子），读回真正落库的字节。
func rawAPIKey(t *testing.T, db *gorm.DB, table string, id uint) string {
	t.Helper()
	var row struct{ APIKey string }
	// 用 Table() 就没有主键语义，条件必须显式写，否则 GORM 会把 id 绑到查询列上。
	if err := db.Table(table).Select("api_key").Where("id = ?", id).Scan(&row).Error; err != nil {
		t.Fatalf("read raw %s.api_key: %v", table, err)
	}
	return row.APIKey
}

// 落库密文、读回明文：AI 调用链取到的必须仍是可用密钥。
func TestAIConfigHooksSealOnWriteAndOpenOnRead(t *testing.T) {
	db := openHookTestDB(t)
	const plain = "sk-hook-vision-000111"

	cfg := AIConfig{ConfigKey: "vision_engine", DisplayName: "视觉", Endpoint: "https://example.invalid/v1/chat/completions", APIKey: plain, ModelName: "gpt-4o-mini"}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	stored := rawAPIKey(t, db, "ai_configs", cfg.ID)
	if !secretbox.IsSealed(stored) {
		t.Fatalf("落库值不是密文: %q", stored)
	}
	if strings.Contains(stored, plain) {
		t.Fatalf("明文密钥出现在库中: %q", stored)
	}

	var reloaded AIConfig
	if err := db.First(&reloaded, cfg.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.APIKey != plain {
		t.Fatalf("读回密钥不等于原文: got %q", reloaded.APIKey)
	}
}

// 反复保存不得层层加壳：AfterFind 已解密，BeforeSave 再封装一次即可。
func TestAPIKeyNotDoubleSealedAcrossSaves(t *testing.T) {
	db := openHookTestDB(t)
	const plain = "sk-hook-text-aabbcc"

	cfg := AIConfig{ConfigKey: "text_engine", DisplayName: "文本", APIKey: plain, Endpoint: "https://example.invalid/v1/chat/completions"}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < 3; i++ {
		var loaded AIConfig
		if err := db.First(&loaded, cfg.ID).Error; err != nil {
			t.Fatalf("load round %d: %v", i, err)
		}
		loaded.ModelName = "deepseek-chat"
		if err := db.Save(&loaded).Error; err != nil {
			t.Fatalf("save round %d: %v", i, err)
		}
		stored := rawAPIKey(t, db, "ai_configs", cfg.ID)
		if n := strings.Count(stored, "enc:v1:"); n != 1 {
			t.Fatalf("第 %d 轮保存后封装标记出现 %d 次: %q", i, n, stored)
		}
	}

	var reloaded AIConfig
	if err := db.First(&reloaded, cfg.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.APIKey != plain {
		t.Fatalf("多次保存后密钥被破坏: got %q", reloaded.APIKey)
	}
}

// 福利网关同样走封装：中转透传时内存里是明文，库里只有密文。
func TestWelfareGatewayHooksSealOnWriteAndOpenOnRead(t *testing.T) {
	db := openHookTestDB(t)
	const plain = "sk-hook-gateway-999"

	gw := TechWelfareGateway{OwnerID: 1, OwnerName: "技术维护组", GatewayName: "验证网关", BaseURL: "https://example.invalid/v1", APIKey: plain}
	if err := db.Create(&gw).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	stored := rawAPIKey(t, db, "tech_welfare_gateways", gw.ID)
	if !secretbox.IsSealed(stored) || strings.Contains(stored, plain) {
		t.Fatalf("网关密钥未按预期入库: %q", stored)
	}

	var reloaded TechWelfareGateway
	if err := db.First(&reloaded, gw.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.APIKey != plain {
		t.Fatalf("网关读回密钥不等于原文: got %q", reloaded.APIKey)
	}
	if secretbox.Mask(reloaded.APIKey) != "sk-h****-999" {
		t.Fatalf("网关脱敏形态不符: %q", secretbox.Mask(reloaded.APIKey))
	}
}

// 空密钥不该被封装成"空密文"，否则会污染 has_key 判定。
func TestEmptyAPIKeyStaysEmpty(t *testing.T) {
	db := openHookTestDB(t)
	cfg := AIConfig{ConfigKey: "placeholder_engine", DisplayName: "未配置", APIKey: ""}
	if err := db.Create(&cfg).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if stored := rawAPIKey(t, db, "ai_configs", cfg.ID); stored != "" {
		t.Fatalf("空密钥被写成 %q", stored)
	}
}
