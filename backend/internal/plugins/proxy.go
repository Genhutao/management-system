package plugins

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/modules"
)

// proxy.go —— /api/v1/mod/<插件id> 的处理器：把一次 HTTP GET 翻成插件的一行 data 请求。
//
// 失败语义是这套东西最容易被做坏的地方，所以先立规矩（方案 §8）：
// **取不到就明写取不到，一律回 200 + warn 值，绝不回一个看着正常的数，也绝不用 500 把面板打死。**
// 0 和空串会被读成"真的是 0"；而一个 500 在界面上只是"这块又坏了"。
// 唯一该报错的是"这个 id 根本不是插件"——那是路由本身的事，gin 的 404 已经管了。

// HandleData 本插件的数据端口。
// 要问哪些组件取自己注册表里的描述符，不吃客户端传来的 key：
// 让客户端决定问什么，等于允许它探这个插件还实现了些别的东西。
func (m *Manager) HandleData(id string) gin.HandlerFunc {
	return func(c *gin.Context) {
		keys := m.widgetKeys(id)
		if len(keys) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "插件 " + id + " 没有可渲染的组件"})
			return
		}

		proc := m.processFor(id)
		// 没进程、或已经放弃自动重启，都走同一句话：这两种情况都不该去碰那根管道，
		// 而"放弃了"必须说得出下一步（重新授权），否则运维只会一遍遍点刷新。
		if proc == nil || proc.State() == StateGaveUp {
			c.JSON(http.StatusOK, failedPayload(keys, m.absentReason(id)))
			return
		}

		values, selfErr, err := proc.Data(keys, c.GetUint("user_id"), roleOf(c))
		switch {
		case err != nil: // 传输层：没起来 / 超时 / 坏 JSON / 串号
			c.JSON(http.StatusOK, failedPayload(keys, err.Error()))
		case selfErr != "": // 插件自己说"这个数我算不出来"
			c.JSON(http.StatusOK, failedPayload(keys, selfErr))
		default:
			// 先按清单声明的形状过一遍（M2）：说好的 list 回来一段 text，
			// 前端只有"空白"和"猜着渲染"两种选法，两种都在骗人。
			// values 里缺的键刻意不补：M1 的前端遇到缺键显示"无数据"，
			// 并明写"数据端口没有返回这个组件：界面不替它编一个 0"。
			c.JSON(http.StatusOK, modules.DataPayload{Values: normalizeValues(m.widgetTypesOf(id), values)})
		}
	}
}

// roleOf 当前账号的角色名。协议里 user_id / role 对插件是可见字段，不是秘密（§14 第 4 条）。
func roleOf(c *gin.Context) string {
	if v, ok := c.Get("role"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// failedPayload 所有键一律读失败。level 用 warn：琥珀警示卡是 M1 已经验过的渲染路径，
// 前端零改动就能显示，不需要为插件再发明一种"坏了"的样子。
func failedPayload(keys []string, reason string) modules.DataPayload {
	reason = truncateReason(reason)
	if reason == "" {
		reason = "插件没有给出原因"
	}
	out := modules.DataPayload{Values: make(map[string]modules.Value, len(keys))}
	for _, k := range keys {
		out.Values[k] = modules.Value{Text: "读取失败", Hint: reason, Level: "warn"}
	}
	return out
}

// absentReason 进程不在时那句话要分得清几种情况——没授权、校验失败、没签名、签名不符、
// 在退避重启、已放弃、文件换过——它们对应的下一步动作完全不同，
// 合成一句"未运行"就等于让运维自己猜。
func (m *Manager) absentReason(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return "plugins/ 下没有这个插件"
	}
	switch {
	case e.plugin.State == StateInvalid:
		return "manifest 校验失败：" + e.plugin.Reason
	case e.plugin.State == StateUnsigned:
		return "没有可信开发者签名，加载器不收：" + e.plugin.Reason
	case e.plugin.State == StateSigBroken:
		return "签名与磁盘上的文件对不上，不运行：" + e.plugin.Reason
	case !e.trusted:
		return "插件未授权，只在管理面板里列着，不运行"
	}
	switch e.state() {
	case StateGaveUp:
		return "已连续失败到上限、放弃自动重启；重新授权可再拉起"
	case StateStale:
		return "文件与授权时指纹不符，需重新授权"
	default:
		return "插件进程正在退避重启"
	}
}
