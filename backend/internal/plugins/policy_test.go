package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"xgh-system/internal/middleware"
	"xgh-system/internal/model"
	"xgh-system/internal/modules"
)

// 和 controller 那边同一口径：清单可见性是从 Casbin 推出来的，就得拿真 Casbin 来验，
// 用我自己写的桩判"这个角色能不能读"，测的只是我对桩的想象。
func setupPolicyEnforcer(t *testing.T) (*gorm.DB, policyEnforcer) {
	t.Helper()
	conf := ""
	for _, p := range []string{"../../rbac_model.conf", "../rbac_model.conf", "rbac_model.conf"} {
		if abs, err := filepath.Abs(p); err == nil {
			if _, err := os.Stat(abs); err == nil {
				conf = abs
				break
			}
		}
	}
	if conf == "" {
		t.Skip("未找到 rbac_model.conf，跳过策略注入断言")
	}
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "_plugpolicy"
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	e, err := middleware.InitCasbin(db, conf)
	if err != nil {
		t.Fatalf("初始化 Casbin 引擎失败: %v", err)
	}
	prev := middleware.Enforcer
	t.Cleanup(func() {
		middleware.Enforcer = prev
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db, e
}

func policySnapshot(t *testing.T, e policyEnforcer) []string {
	t.Helper()
	list, err := e.(interface {
		GetPolicy() ([][]string, error)
	}).GetPolicy()
	if err != nil {
		t.Fatalf("读策略表失败: %v", err)
	}
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, strings.Join(p, "|"))
	}
	sort.Strings(out)
	return out
}

func countCasbinRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Table("casbin_rule").Count(&n).Error; err != nil {
		t.Fatalf("数 casbin_rule 失败: %v", err)
	}
	return n
}

const testPlugID = "diskusage"

// 授权前后的可见性必须真的翻面：这是信任门"没授权就连清单都不出现"的机制本身。
func TestGrantAndRevokeFlipReadAccessPerRole(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	obj := DataEndpoint(testPlugID)

	roles := []string{model.RoleDormManager, model.RoleMember, model.RoleMinister, model.RoleViewerExport}
	for _, r := range roles {
		if ok, _ := e.Enforce("role:"+r, obj, "GET"); ok {
			t.Fatalf("授权前 %s 就能读插件端点，那信任门等于没有", r)
		}
	}

	if err := grantPolicy(e, modOfID(testPlugID), roles); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	for _, r := range roles {
		if ok, _ := e.Enforce("role:"+r, obj, "GET"); !ok {
			t.Errorf("授权后 %s 仍读不到 %s", r, obj)
		}
		// §6：只放开 GET。给了写权限的话，一个"看数据"的插件就能改业务数据
		if ok, _ := e.Enforce("role:"+r, obj, "POST"); ok {
			t.Errorf("%s 通过插件策略拿到了 %s 的 POST 权限", r, obj)
		}
		// 只放开它自己那一个端点：别的接口一概不沾
		if ok, _ := e.Enforce("role:"+r, "/api/v1/deductions", "GET"); ok && r == model.RoleDormManager {
			t.Errorf("策略注入顺带放开了别的端点：宿管现在能读 %s", "/api/v1/deductions")
		}
	}

	if err := revokePolicy(e, modOfID(testPlugID), roles); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	for _, r := range roles {
		if ok, _ := e.Enforce("role:"+r, obj, "GET"); ok {
			t.Errorf("撤销后 %s 仍能读 %s —— 面板说下线了，门其实还开着", r, obj)
		}
	}
	// 撤销不重复报错：幂等才能既给界面用、又给启动回滚用
	if err := revokePolicy(e, modOfID(testPlugID), roles); err != nil {
		t.Errorf("重复撤销不该报错: %v", err)
	}
}

// tech_admin 不需要注入也能读 /api/v1/mod/*（seed 里那条显式策略）。
// 这条断言记下的是设计的承重处：对技术维护组来说，"未授权就看不见"靠的是
// 模块压根没进注册表，而不是 Casbin 拦它 —— 所以 Unregister 必须和撤策略同批发生。
func TestTechAdminSeesEndpointViaSeedPolicy(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	ok, err := e.Enforce("role:"+model.RoleTechAdmin, DataEndpoint(testPlugID), "GET")
	if err != nil {
		t.Fatalf("判定失败: %v", err)
	}
	if !ok {
		t.Error("tech_admin 读不到 /api/v1/mod/*：seed 里那条显式策略不见了？")
	}
}

// 一轮授权 + 撤销之后，引擎里的策略必须与开始时逐条相同（零残留），
// casbin_rule 表更是一个字节都不许多（方案 §6 的硬承诺）。
func TestPolicyInjectionLeavesNoResidue(t *testing.T) {
	db, e := setupPolicyEnforcer(t)
	before := policySnapshot(t, e)
	rowsBefore := countCasbinRows(t, db)

	roles := []string{model.RoleMember, model.RoleMinister}
	if err := grantPolicy(e, modOfID(testPlugID), roles); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if len(policySnapshot(t, e)) != len(before)+len(roles) {
		t.Errorf("注入后引擎里该多出 %d 条，实际 %d -> %d",
			len(roles), len(before), len(policySnapshot(t, e)))
	}
	// 注入的这一刻，库里也不该多：autoSave 在 InitCasbin 里就被关掉了
	if got := countCasbinRows(t, db); got != rowsBefore {
		t.Errorf("插件策略漏进了 casbin_rule（%d -> %d 行）：临时权限写进了正式权限表", rowsBefore, got)
	}

	if err := revokePolicy(e, modOfID(testPlugID), roles); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	after := policySnapshot(t, e)
	if strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("撤销后策略与初始状态不一致：\n初始 %v\n现在 %v", before, after)
	}
	if got := countCasbinRows(t, db); got != rowsBefore {
		t.Errorf("casbin_rule 行数变了: %d -> %d", rowsBefore, got)
	}
}

// 可见性推导（modules.ManifestForRole）要随注入翻面：
// 这验的是"策略注入"和"清单推导"真的接在同一份 enforcer 上，没有各判各的。
func TestManifestVisibilityFollowsInjection(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	mod := modules.Module{
		ID: testPlugID, Title: "磁盘用量", Tab: testPlugID,
		MinManifestVersion: modules.ManifestVersion,
		Widgets: []modules.Widget{{
			Key: "data_disk", Type: modules.WidgetStat, Label: "数据盘",
			DataEndpoint: DataEndpoint(testPlugID),
		}},
	}
	if err := modules.TryRegister(mod); err != nil {
		t.Fatalf("登记测试模块失败: %v", err)
	}
	t.Cleanup(func() { modules.Unregister(testPlugID) })

	canRead := roleCanRead(e)
	if got := modules.ManifestForRole(model.RoleMember, canRead); hasModule(got, testPlugID) {
		t.Error("没给部员注入策略，清单里却出现了插件模块")
	}
	roles := []string{model.RoleMember}
	if err := grantPolicy(e, modOfID(testPlugID), roles); err != nil {
		t.Fatalf("授权失败: %v", err)
	}
	if got := modules.ManifestForRole(model.RoleMember, canRead); !hasModule(got, testPlugID) {
		t.Error("注入策略后部员清单里仍没有该模块：可见性推导没吃这份 enforcer")
	}
	if err := revokePolicy(e, modOfID(testPlugID), roles); err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if got := modules.ManifestForRole(model.RoleMember, canRead); hasModule(got, testPlugID) {
		t.Error("撤销后部员清单里还有它")
	}
}

func hasModule(list []modules.Module, id string) bool {
	for _, m := range list {
		if m.ID == id {
			return true
		}
	}
	return false
}

// roleCanRead 对 nil 引擎必须一律不可见（同"鉴权引擎未就绪就拒绝请求"的失败关闭口径）。
func TestRoleCanReadFailsClosed(t *testing.T) {
	if roleCanRead(nil)("member", "GET", "/api/v1/anything") {
		t.Error("判定器为空时不该放行")
	}
}

// 中途失败要回滚：注入到第二个角色就炸，第一个角色的策略也不能留下。
type flakyEnforcer struct {
	added   []string
	failOn  int
	base    policyEnforcer
	addErrs int
}

func (f *flakyEnforcer) Enforce(rvals ...any) (bool, error) {
	return f.base.Enforce(rvals...)
}
func (f *flakyEnforcer) AddPolicy(params ...any) (bool, error) {
	if len(f.added) >= f.failOn {
		f.addErrs++
		return false, fmt.Errorf("模拟引擎故障")
	}
	f.added = append(f.added, fmt.Sprint(params...))
	return f.base.AddPolicy(params...)
}
func (f *flakyEnforcer) RemovePolicy(params ...any) (bool, error) {
	if len(f.added) > 0 {
		f.added = f.added[:len(f.added)-1]
	}
	return f.base.RemovePolicy(params...)
}

func TestGrantPolicyRollsBackOnPartialFailure(t *testing.T) {
	_, e := setupPolicyEnforcer(t)
	flaky := &flakyEnforcer{base: e, failOn: 2}
	roles := []string{model.RoleDormManager, model.RoleMember, model.RoleMinister}
	if err := grantPolicy(flaky, modOfID(testPlugID), roles); err == nil {
		t.Fatal("引擎报错时 grantPolicy 不该报成功")
	}
	// 已经加进引擎的那两条必须被撤掉
	for _, r := range roles {
		if ok, _ := e.Enforce("role:"+r, DataEndpoint(testPlugID), "GET"); ok {
			t.Errorf("注入半途失败后 %s 仍可读该端点：残留了半套策略", r)
		}
	}
}

// modOfID 给策略注入测试拼一个最小描述符。
// 传 Module 而不是 id 是有原因的（S3）：注入哪些端点由"这个模块声明了什么"决定，
// 描述符是唯一的那份事实；早期签名收 id 时，动作端点就得在调用方各拼一遍，
// 两处拼法迟早和注册表里的对不上。
func modOfID(id string) modules.Module {
	return modules.Module{ID: id}
}
