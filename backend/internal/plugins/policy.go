package plugins

import (
	"fmt"

	"xgh-system/internal/modules"
)

// policy.go —— 插件的权限只在内存里活（方案 §6）。
//
// 授权时按 manifest.policy 给每个角色注入策略，撤销时原样移除。一条都不写进 casbin_rule：
// 那张表只归 middleware.seedCasbinRules 管，插件下线要删得干净——库里留一条，
// "这个入口当初是谁开的"就再也问不出来了。
//
// 注入的对象不止一个（S3 起）：
//   - ("role:<角色>", "/api/v1/mod/<id>", "GET")        —— 读它的数据端点，P1 起就有；
//   - ("role:<角色>", "/api/v1/mod/<id>/action", "POST") —— 只有声明了动作的插件才有这条。
//
// 第二条不能省。少了它会出现一种最难查的界面：清单里那个按钮看得见（可见性是按
// 目标接口的权限筛的），点下去却被动作端点自己的 Casbin 判定拦掉——那句 403 说的还是
// "您所在的身份角色无权操作此资源"，人根本不知道被拦在第二道门上。
// 反过来它也**不等于放行了写**：代执行用的是操作者自己的会话，真正的写权限
// 由被指的那个业务接口的 Casbin 判定说了算（计划 §2 那句"恰好等于点按钮那人自己能改的"）。
//
// middleware.InitCasbin 在播种完成后已经把 autoSave 关掉，所以这里加策略天然不落库。
// 本文件不调 SavePolicy，也不把 autoSave 打开：谁哪天加上那一句，就等于把临时策略
// 永久写进正式权限表，而这一步在界面上没有任何痕迹。

// policyEnforcer 只声明这里真正用到的四个方法。
// 类型写接口而不是 *casbin.SyncedEnforcer：测试照样能塞真引擎（照 controller 那边
// "清单可见性必须拿真 Casbin 验"的做法），生产传 middleware.Enforcer，两边同一份实现，
// 不会出现"测的是我自己写的桩"。
type policyEnforcer interface {
	Enforce(rvals ...any) (bool, error)
	AddPolicy(params ...any) (bool, error)
	RemovePolicy(params ...any) (bool, error)
}

// policyPair 一条要注入/要移除的策略。
type policyPair struct {
	obj  string
	act  string
	only string // 人话说明，报错时用得上
}

// policyObjects 该插件授权后应存在的策略集合。
// 规则只有这一处：注入与移除都从这里取，两半才不会一个加了一个没删。
func policyObjects(mod modules.Module) []policyPair {
	out := []policyPair{{obj: DataEndpoint(mod.ID), act: "GET", only: "读数据端点"}}
	if len(mod.Actions) > 0 {
		// 方法只有 POST、对象只有它自己那一个动作端点：白名单之外没有第二种写法可走（§6）
		out = append(out, policyPair{obj: ActionEndpoint(mod.ID), act: "POST", only: "发起动作"})
	}
	return out
}

// grantPolicy 放开 roles 对这些角色读该插件的数据端点、发起它的动作。
// 中途失败就把已经加上的撤干净再报错：引擎里剩半套策略，
// 面板上写着"未授权"而实际读得到（或看得见按钮却打不通），是最难查的那种不一致。
func grantPolicy(e policyEnforcer, mod modules.Module, roles []string) error {
	pairs := policyObjects(mod)
	added := make([]string, 0, len(roles)*len(pairs))
	addedPairs := make([]policyPair, 0, len(roles)*len(pairs))
	for _, r := range roles {
		for _, p := range pairs {
			if _, err := e.AddPolicy("role:"+r, p.obj, p.act); err != nil {
				for i, done := range added {
					_, _ = e.RemovePolicy("role:"+done, addedPairs[i].obj, addedPairs[i].act)
				}
				return fmt.Errorf("为角色 %s 放开 %s %s 失败: %w", r, p.act, p.obj, err)
			}
			added = append(added, r)
			addedPairs = append(addedPairs, p)
		}
	}
	return nil
}

// revokePolicy 撤掉授权时注入的那些策略。
// 策略本来就不在（重复撤销、或授权前就崩了、或这插件本来没声明动作）不算错：
// RemovePolicy 返回 false, nil，这里只把引擎真报错的情况回出去。
func revokePolicy(e policyEnforcer, mod modules.Module, roles []string) error {
	for _, r := range roles {
		for _, p := range policyObjects(mod) {
			if _, err := e.RemovePolicy("role:"+r, p.obj, p.act); err != nil {
				// "没删干净"必须让调用方看得见：面板报未授权而实际门还开着，是最难查的不一致
				return fmt.Errorf("移除角色 %s 对 %s %s 的策略失败: %w", r, p.act, p.obj, err)
			}
		}
	}
	return nil
}

// setActionPolicy 只调"能不能发起这个插件的动作"那一条策略，读数据端点那条一字不动。
//
// 存在的理由就是 S5 的批准范围会变：一个已授权插件可以"原本批了两条动作、现在一条都不批"，
// 或反过来。这时要动的只有那一条 POST 策略；直接拿 revokePolicy(grantPolicy) 整对重来，
// 会顺手把 GET 那条也撤掉——读数的好功能跟着动作批准一起没了，是最容易被误伤的地方。
// on=false 时即使策略本来就不在也不算错（RemovePolicy 返回 false, nil）。
func setActionPolicy(e policyEnforcer, id string, roles []string, on bool) error {
	if e == nil {
		return fmt.Errorf("权限引擎不可用")
	}
	for _, r := range roles {
		var err error
		if on {
			_, err = e.AddPolicy("role:"+r, ActionEndpoint(id), "POST")
		} else {
			_, err = e.RemovePolicy("role:"+r, ActionEndpoint(id), "POST")
		}
		if err != nil {
			return fmt.Errorf("为角色 %s %s %s POST 失败: %w", r, map[bool]string{true: "放开", false: "收回"}[on], ActionEndpoint(id), err)
		}
	}
	return nil
}

// roleCanRead 给 modules.ManifestForRole 用的判定闭包：清单可见性是问 Casbin 问出来的。
func roleCanRead(e policyEnforcer) func(role, method, path string) bool {
	return func(role, method, path string) bool {
		if e == nil {
			return false // 判定器缺失就一律不可见：宁可不显示，也不默认放行
		}
		ok, err := e.Enforce("role:"+role, path, method)
		return err == nil && ok
	}
}
