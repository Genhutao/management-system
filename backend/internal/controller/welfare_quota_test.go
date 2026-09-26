package controller

import (
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 透传计费的三条不变量都在这一层测：真实上游不可达时 HTTP 侧永远走不到扣费分支，
// 只有直接调用才能验证"同一份额度不会被并发花两次"和"定价按本部门取"。

func setupQuotaDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	// 内存库并发写会 BUSY，那属于测试环境噪声而不是被测逻辑；限一条连接让语句天然串行
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.MemberModelQuota{}, &model.WelfareUsageQuota{},
		&model.TechWelfareGateway{}, &model.WelfareModelPricing{}, &model.OperationLog{},
	); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	prev := repository.DB
	repository.DB = db
	t.Cleanup(func() {
		repository.DB = prev
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
}

func seedCalls(t *testing.T, userID uint, modelKey string, remain int) {
	t.Helper()
	q := model.MemberModelQuota{UserID: userID, UserName: "测试部员", ModelKey: modelKey, RemainCalls: remain}
	if err := repository.DB.Create(&q).Error; err != nil {
		t.Fatalf("写入额度失败: %v", err)
	}
}

func remainOf(t *testing.T, userID uint, modelKey string) (int, int) {
	t.Helper()
	var q model.MemberModelQuota
	if err := repository.DB.Where("user_id = ? AND model_key = ?", userID, modelKey).First(&q).Error; err != nil {
		t.Fatalf("读额度失败: %v", err)
	}
	return q.RemainCalls, q.TotalUsedCalls
}

func TestQuotaNeverGoesNegativeUnderConcurrentCharge(t *testing.T) {
	setupQuotaDB(t)
	const (
		userID  = uint(11)
		modelK  = "deepseek-chat"
		cost    = 10
		balance = 10 // 只够一次
	)
	seedCalls(t, userID, modelK, balance)

	wc := &WelfareController{}
	const attempts = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	charged, refused := 0, 0

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 全部先做 dry-run：都能读到"还有 10 次"，正是原实现双花的起点
			if _, _, err := wc.tryChargeQuota(userID, 1, modelK, cost, true); err != nil {
				mu.Lock()
				refused++
				mu.Unlock()
				return
			}
			_, _, err := wc.tryChargeQuota(userID, 1, modelK, cost, false)
			mu.Lock()
			if err == nil {
				charged++
			} else {
				refused++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	remain, used := remainOf(t, userID, modelK)
	if charged != 1 {
		t.Fatalf("余额只够一次，成功扣费必须恰好 1 次，实际 %d（拒绝 %d）", charged, refused)
	}
	if remain < 0 {
		t.Fatalf("余额不得为负，实际 %d", remain)
	}
	if remain != balance-cost || used != cost {
		t.Fatalf("扣费后应剩 %d、累计消耗 %d，实际剩 %d 用 %d", balance-cost, cost, remain, used)
	}
}

func TestChargeIsAllOrNothingWithinOnePool(t *testing.T) {
	setupQuotaDB(t)
	const userID, modelK = uint(12), "gpt-4o-mini"
	seedCalls(t, userID, modelK, 3)

	wc := &WelfareController{}
	// 余额 3、单次成本 5：既不能扣成负数，也不能"扣一半"
	if _, _, err := wc.tryChargeQuota(userID, 1, modelK, 5, false); err == nil {
		t.Fatal("余额不足时不得扣费")
	}
	if remain, used := remainOf(t, userID, modelK); remain != 3 || used != 0 {
		t.Fatalf("扣费失败后额度必须原样不动，实际剩 %d 用 %d", remain, used)
	}
	if _, _, err := wc.tryChargeQuota(userID, 1, modelK, 2, false); err != nil {
		t.Fatalf("余额足够时应可扣费: %v", err)
	}
	if remain, used := remainOf(t, userID, modelK); remain != 1 || used != 2 {
		t.Fatalf("应剩 1 用 2，实际剩 %d 用 %d", remain, used)
	}
	// 只剩 1 次时，成本 2 必须再次整体拒绝
	if _, _, err := wc.tryChargeQuota(userID, 1, modelK, 2, false); err == nil {
		t.Fatal("余额不足时不得扣费")
	}
	if remain, used := remainOf(t, userID, modelK); remain != 1 || used != 2 {
		t.Fatalf("二次失败不得改动额度，实际剩 %d 用 %d", remain, used)
	}
}

func TestLegacyGatewayPoolStillChargeable(t *testing.T) {
	setupQuotaDB(t)
	const userID, gwID = uint(13), uint(7)
	if err := repository.DB.Create(&model.WelfareUsageQuota{
		UserID: userID, GatewayID: gwID, RemainQuota: 4,
	}).Error; err != nil {
		t.Fatalf("写入旧额度池失败: %v", err)
	}

	wc := &WelfareController{}
	// 没有按模型额度行时，仍然落到该用户的网关额度池（历史数据不能作废）
	remain, pool, err := wc.tryChargeQuota(userID, gwID, "claude-3-5-sonnet", 3, false)
	if err != nil {
		t.Fatalf("旧池应可扣费: %v", err)
	}
	if pool != "gateway" || remain != 1 {
		t.Fatalf("应命中 gateway 池并剩 1，实际 pool=%s remain=%d", pool, remain)
	}
}

func TestCostPerCallUsesCallersOwnDepartment(t *testing.T) {
	setupQuotaDB(t)
	wc := &WelfareController{}
	pricings := []model.WelfareModelPricing{
		{Department: "纪检部", ModelKey: "deepseek-chat", DisplayName: "纪检部定价", CostPerCall: 1, IsEnabled: true},
		{Department: "技术组", ModelKey: "deepseek-chat", DisplayName: "技术组定价", CostPerCall: 9, IsEnabled: true},
	}
	for i := range pricings {
		if err := repository.DB.Create(&pricings[i]).Error; err != nil {
			t.Fatalf("写入定价失败: %v", err)
		}
	}

	// 修复前只按 model_key 取第一条命中：技术组部员会蹭到纪检部 1 次的低价
	if got := wc.resolveCostPerCall("deepseek-chat", model.User{Department: "技术组"}); got != 9 {
		t.Fatalf("技术组部员应按本部门定价 9 次，实际 %d", got)
	}
	if got := wc.resolveCostPerCall("deepseek-chat", model.User{Department: "纪检部"}); got != 1 {
		t.Fatalf("纪检部部员应按本部门定价 1 次，实际 %d", got)
	}

	// 全局行（department 空）优先度低于本部门行
	if err := repository.DB.Create(&model.WelfareModelPricing{
		Department: "", ModelKey: "gpt-4o-mini", DisplayName: "全局定价", CostPerCall: 4, IsEnabled: true,
	}).Error; err != nil {
		t.Fatalf("写入全局定价失败: %v", err)
	}
	if got := wc.resolveCostPerCall("gpt-4o-mini", model.User{Department: "纪检部"}); got != 4 {
		t.Fatalf("本部门无定价时应回落到全局定价 4，实际 %d", got)
	}
	if err := repository.DB.Create(&model.WelfareModelPricing{
		Department: "纪检部", ModelKey: "gpt-4o-mini", DisplayName: "纪检部专属", CostPerCall: 2, IsEnabled: true,
	}).Error; err != nil {
		t.Fatalf("写入部门定价失败: %v", err)
	}
	if got := wc.resolveCostPerCall("gpt-4o-mini", model.User{Department: "纪检部"}); got != 2 {
		t.Fatalf("本部门有定价时不得被全局行覆盖，实际 %d", got)
	}
	// 停用行不参与计价
	if err := repository.DB.Model(&model.WelfareModelPricing{}).Where("model_key = ?", "gpt-4o-mini").
		Update("is_enabled", false).Error; err != nil {
		t.Fatalf("停用定价失败: %v", err)
	}
	if got := wc.resolveCostPerCall("gpt-4o-mini", model.User{Department: "纪检部"}); got != 1 {
		t.Fatalf("全部停用后应回落到默认 1 次，实际 %d", got)
	}
	// 没有部门的人不能被任意一行的低价套利，只能走全局或默认
	if got := wc.resolveCostPerCall("deepseek-chat", model.User{}); got != 1 {
		t.Fatalf("无部门用户既无全局行，应取默认 1，实际 %d", got)
	}
}
