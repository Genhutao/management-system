package repository

import (
	"testing"
	"time"

	"xgh-system/internal/model"
)

func TestInitDBMigratesSecurityCenterSchema(t *testing.T) {
	t.Setenv("CRYPTO_SECRET", "unit-test-crypto-secret")
	db, err := InitDB("file:sec_center_migrate?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("InitDB 失败: %v", err)
	}

	for _, field := range []string{
		"TokenVersion", "PasswordChangedAt", "PasswordStrength",
		"TotpSecretEnc", "TotpLastStep", "LastLoginIP", "LastLoginAt",
	} {
		if !db.Migrator().HasColumn(&model.User{}, field) {
			t.Fatalf("users 表缺少安全字段 %s", field)
		}
	}
	if !db.Migrator().HasTable(&model.UserSession{}) {
		t.Fatal("缺少 user_sessions 表")
	}

	var seeded int64
	db.Model(&model.User{}).Count(&seeded)
	if seeded == 0 {
		t.Fatal("种子账号未落库")
	}

	now := time.Now()
	sess := model.UserSession{
		UserID:     1,
		Jti:        "test-jti-0001",
		LoginIP:    "10.0.0.9",
		UserAgent:  "unit-test",
		LoginAt:    now,
		LastSeenAt: now,
	}
	if err := db.Create(&sess).Error; err != nil {
		t.Fatalf("写入 UserSession 失败: %v", err)
	}
	var got model.UserSession
	if err := db.Where("jti = ?", "test-jti-0001").First(&got).Error; err != nil {
		t.Fatalf("按 jti 查询会话失败: %v", err)
	}
	if got.LoginIP != "10.0.0.9" {
		t.Fatalf("会话 IP 不符: %q", got.LoginIP)
	}

	// 服务每次启动都会执行 AutoMigrate，必须幂等
	if err := db.AutoMigrate(&model.User{}, &model.UserSession{}); err != nil {
		t.Fatalf("重复 AutoMigrate 失败: %v", err)
	}
}
