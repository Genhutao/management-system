package plugins

// grants.go —— "这次批准了哪几条写动作"是数据库事实，不是界面勾选状态（计划 S5）。
//
// 三条口径先说清，它们决定了下面每个函数为什么长这样：
//  1. **批准绑文件指纹**。重新授权时旧行全部标撤、新行按这一次勾的写。
//     把上一次的批准自动续到新文件上，等于让一次点击批准一份没人看过的东西。
//  2. **method/path 从签名清单里抄，不从请求里收**。客户端能说的只有"我要批 submit_news 这个 key"；
//     批的到底是哪条接口由服务端手里那份 manifest 决定，否则批准记录本身就是可伪造的。
//  3. **撤销整插件时逐条标撤**，不删行。删了就没人能回答"这个插件当时被批准过什么"。

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/modules"
)

// grantRow 一条批准的内存形状（面板要按它把"已批准/未批准"显示出来）。
type grantRow struct {
	Key      string
	Method   string
	Path     string
	By       string
	At       time.Time
	ExecHash string
}

// loadActiveGrants 全部插件当前有效的批准，按插件 id 分组。
// 表读不出来时返回错误：授权动作宁可当场失败，也不要"批了但没落库、重启就没了"。
func loadActiveGrants() (map[string][]grantRow, error) {
	db, err := trustDB()
	if err != nil {
		return nil, err
	}
	var rows []model.PluginActionGrant
	if err := db.Where("revoked_at IS NULL").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[string][]grantRow{}
	for _, r := range rows {
		out[r.PluginID] = append(out[r.PluginID], grantRow{
			Key: r.ActionKey, Method: r.Method, Path: r.Path,
			By: r.GrantedByName, At: r.GrantedAt, ExecHash: r.ExecHash,
		})
	}
	return out, nil
}

// replaceGrants 把这个插件的批准换成 keys 这一份：旧行全部标撤，再按**签名清单**写入新行。
//
// keys 里出现清单没申请过的动作直接拒绝——批准一张没写着的申请单，比不批准更糟。
// 返回写进去的那几行，调用方把它们贴回内存 entry（面板要在同一次响应里就显示成已批准）。
func replaceGrants(p *Plugin, keys []string, op Operator) ([]grantRow, error) {
	db, err := trustDB()
	if err != nil {
		return nil, err
	}
	return replaceGrantsWith(db, p, keys, op)
}

// replaceGrantsWith 同一个实现，但由调用方给 *gorm.DB：授权那一步要把它和信任记录
// 放进同一个事务里，不能出现"信任写了、批准没写"那种半套状态。
func replaceGrantsWith(db *gorm.DB, p *Plugin, keys []string, op Operator) ([]grantRow, error) {
	declared := map[string]modules.Action{}
	for _, a := range p.Module.Actions {
		declared[a.Key] = a
	}
	want := make([]modules.Action, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue // 同一 key 勾两遍不该写出两行
		}
		a, ok := declared[k]
		if !ok {
			return nil, fmt.Errorf("插件 %s 的签名清单里没有申请过动作 %q，无法批准：批的只能是它自己写出来的那几条", p.Module.ID, k)
		}
		seen[k] = true
		want = append(want, a)
	}
	if err := db.Model(&model.PluginActionGrant{}).
		Where("plugin_id = ? AND revoked_at IS NULL", p.Module.ID).
		Updates(map[string]any{"revoked_at": time.Now(), "revoked_by": op.ID, "revoked_by_name": op.Name}).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]grantRow, 0, len(want))
	for _, a := range want {
		row := model.PluginActionGrant{
			PluginID: p.Module.ID, ActionKey: a.Key, Method: a.Method, Path: a.Path,
			ExecHash: p.ExecHash, ManifestHash: p.ManifestHash,
			GrantedBy: op.ID, GrantedByName: op.Name, GrantedAt: now,
		}
		if err := db.Create(&row).Error; err != nil {
			return nil, err
		}
		out = append(out, grantRow{Key: a.Key, Method: a.Method, Path: a.Path, By: op.Name, At: now, ExecHash: p.ExecHash})
	}
	return out, nil
}

// saveTrustAndGrants 一次授权要落的两个事实：**这个插件可以跑** + **这几条写动作被批准**。
// 同一个事务：分成两次写，就会出现"面板说批了两条、库里一条都没有"或者反过来，
// 而这两种都会让下一次重启的状态和屏幕上看到的不一样。
func saveTrustAndGrants(p *Plugin, keys []string, op Operator) (*model.PluginTrust, []grantRow, error) {
	db, err := trustDB()
	if err != nil {
		return nil, nil, err
	}
	var trustRow *model.PluginTrust
	var rows []grantRow
	if err := db.Transaction(func(tx *gorm.DB) error {
		row := model.PluginTrust{
			ID: p.Module.ID, ExecHash: p.ExecHash, ManifestHash: p.ManifestHash,
			SignedBy: p.SignedBy, TrustedBy: op.ID, TrustedByName: op.Name, TrustedAt: time.Now(),
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		g, err := replaceGrantsWith(tx, p, keys, op)
		if err != nil {
			return err
		}
		trustRow, rows = &row, g
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return trustRow, rows, nil
}

// revokeGrants 整插件撤销时把它的批准逐条标撤。
// 一行都没标上是正常的（只读插件本来就没有批准行），不当成错配报出去。
func revokeGrants(id string, op Operator) error {
	db, err := trustDB()
	if err != nil {
		return err
	}
	return db.Model(&model.PluginActionGrant{}).
		Where("plugin_id = ? AND revoked_at IS NULL", id).
		Updates(map[string]any{"revoked_at": time.Now(), "revoked_by": op.ID, "revoked_by_name": op.Name}).Error
}
