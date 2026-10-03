package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/plugins"
)

// PluginAdminController 文件期插件的管理接口（方案 §7.2）。
//
// 这是**宿主自己的控制台功能**，不是插件：授权按钮是交互组件，清单驱动的渲染器要到 M3
// 才有 form 类型，而"插件前端零改动"这句话不该被一个管理界面自己打破。
// 先例是 AI 配置卡与播音数据源卡——它们也都是控制台自带的能力。
//
// 四条路由全挂在既有的 /api/v1/tech 段下：那段本来就是 tech_admin 独占，
// 不需要为插件再加一条 Casbin 策略，"谁能授权插件"这个问题因此只有一个答案。
type PluginAdminController struct {
	mgr *plugins.Manager
}

func NewPluginAdminController(mgr *plugins.Manager) *PluginAdminController {
	return &PluginAdminController{mgr: mgr}
}

// List GET /api/v1/tech/plugins
//
// 只回元数据与状态：目录里的东西、指纹、谁授权的、为什么被拒、当前可信的开发者是谁。
// 这里没有任何密钥（本来也没有），也没有 manifest 之外的内容。
func (ctl *PluginAdminController) List(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"plugins_dir":  ctl.mgr.Dir(),
		"max_plugins":  plugins.MaxPlugins,
		"trusted_keys": ctl.mgr.TrustedKids(),
		"plugins":      ctl.mgr.Views(),
		"stats":        ctl.mgr.Stats(),
	})
}

// grantSelection 读界面上勾选的那几条动作 key。
//
// 空请求体是合法的（只读插件没有动作可勾，curl 手敲管理接口也不会带 body），
// 但**多数字段一律拒**：这里能收的只有 `actions` 一个键，
// 出现 `path`/`method` 之类的说明调用方在替签名清单做主，那句得当场说清楚。
func grantSelection(c *gin.Context) ([]string, error) {
	if c.Request.ContentLength == 0 {
		return nil, nil
	}
	var body struct {
		Actions []string `json:"actions"`
	}
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("请求体读不懂：%w（这里只收 {\"actions\":[\"已勾选的动作 key\"]}）", err)
	}
	return body.Actions, nil
}

// Trust POST /api/v1/tech/plugins/:id/trust
//
// 顺序是"先验口令、再动真格"：口令没过的请求不该让任何进程被拉起来。
// 授权成功之后才留痕——留痕写的是"这个入口被放开了"，做没做到必须和这句话一致。
//
// S5 之后一次授权还带着"批准了哪几条写动作"：body 里的 actions 就是界面上勾中的那几个，
// 一条都没勾 = 只让它读数。服务端不替任何人默认全批。
func (ctl *PluginAdminController) Trust(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if !requireStepUp(c, operator) {
		return
	}
	keys, err := grantSelection(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := ctl.mgr.Authorize(id, plugins.Operator{ID: operator.ID, Name: operator.RealName}, keys); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	view, _ := ctl.mgr.View(id)
	logOperationAs(c, operator, "plugin.trust", "plugin", 0, pluginTrustDetail(view))
	c.JSON(http.StatusOK, gin.H{"plugin": view})
}

// Grants POST /api/v1/tech/plugins/:id/grants
//
// 改一个已授权插件被批准的写动作范围（S5）。和授权一样要口令——
// 打开一条写通道这件事，从来不比"上线一个插件"轻。
func (ctl *PluginAdminController) Grants(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if !requireStepUp(c, operator) {
		return
	}
	keys, err := grantSelection(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	op := plugins.Operator{ID: operator.ID, Name: operator.RealName}
	if err := ctl.mgr.UpdateGrants(id, op, keys); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	view, _ := ctl.mgr.View(id)
	logOperationAs(c, operator, "plugin.grants", "plugin", 0,
		fmt.Sprintf("调整插件 %s 的写动作批准 · 现在批准 %s · exec sha256=%s · 执行人 %s(%s)",
			view.ID, grantedListForAudit(view), view.ExecHash, operator.RealName, operator.Role))
	c.JSON(http.StatusOK, gin.H{"plugin": view})
}

// Revoke POST /api/v1/tech/plugins/:id/revoke
//
// 与授权对称，也要口令（§15 决策①）：两把钥匙的口径一致，才不会出现
// "开一扇门要密码、关一扇门不用"这种能被会话劫持反着利用的缝。
func (ctl *PluginAdminController) Revoke(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if !requireStepUp(c, operator) {
		return
	}
	// 撤销之前先取一份视图：撤销会把内存里的批准清空，事后再问"撤掉的是哪几条"就问不出来了。
	// 审计要写的正是"这一次关掉了哪几扇门"，只写"撤销了某插件"的话，
	// 半年后没人知道当时那两扇没被单独撤过的门是不是被这一句顺带关掉的。
	before, _ := ctl.mgr.View(id)
	if err := ctl.mgr.Revoke(id, plugins.Operator{ID: operator.ID, Name: operator.RealName}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	view, _ := ctl.mgr.View(id)
	logOperationAs(c, operator, "plugin.revoke", "plugin", 0,
		fmt.Sprintf("撤销插件 %s · 目录 %s · 同时作废的写动作批准 %s · 执行人 %s(%s)",
			view.ID, view.Dir, revokedGrantsForAudit(before), operator.RealName, operator.Role))
	c.JSON(http.StatusOK, gin.H{"plugin": view})
}

// pluginTrustDetail 把"谁被允许读什么、这份东西是谁写的、批准了几条写动作"写进一条数据库记录里。
// 从这一刻起，"这个入口是谁开的"不再取决于谁碰过服务器文件系统。
// kid 必须在：签名准入之后，"批的是哪个作者的哪一份"才是完整的审计问题。
// 批准清单也必须在：S5 之后一次授权带着"批了哪几条"这个信息，
// 只写"授权了某某"的审计回答不了"当时到底放开了哪条写通道"。
func pluginTrustDetail(v plugins.View) string {
	roles := strings.Join(v.PolicyRoles, "、")
	if roles == "" {
		roles = "（无）"
	}
	developer := v.SignedBy
	if developer == "" {
		developer = "（无签名）" // 走到授权成功这一步正常不该出现，留着是为了不谎报
	}
	return fmt.Sprintf("授权插件 %s · 目录 %s · 开发者 kid=%s · exec sha256=%s · manifest sha256=%s · 可读角色 %s · 批准写动作 %s",
		v.ID, v.Dir, developer, v.ExecHash, v.ManifestHash, roles, grantedListForAudit(v))
}

// grantedListForAudit 已批准的那几条动作，写成"key:METHOD path"逐条列出；一条都没批就说死。
// "一条都没批"必须显式写出来，不能留空——留空在半年后读起来像"这条审计漏写了"。
func grantedListForAudit(v plugins.View) string {
	on := make([]string, 0, len(v.Actions))
	for _, a := range v.Actions {
		if a.Granted {
			on = append(on, a.Key+":"+a.Method+" "+a.Path)
		}
	}
	if len(on) == 0 {
		return "（一条都没批，只放行读数）"
	}
	return strings.Join(on, "；")
}

// revokedGrantsForAudit 撤销那一句里"被一起作废的批准"。
// 和上面那条同构，但"没东西可撤"要说成"本来一条都没批"：
// 审计里这两种说法指向完全不同的现场——前者是这个人从来没开过写通道，
// 后者才是"撤掉了一条"，写混了会把一次什么都没发生的撤销读成一次收权。
func revokedGrantsForAudit(v plugins.View) string {
	on := make([]string, 0, len(v.Actions))
	for _, a := range v.Actions {
		if a.Granted {
			on = append(on, a.Key+":"+a.Method+" "+a.Path)
		}
	}
	if len(on) == 0 {
		return "（本来一条都没批）"
	}
	return strings.Join(on, "；")
}
