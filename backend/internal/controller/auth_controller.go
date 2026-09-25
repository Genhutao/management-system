package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"xgh-system/internal/middleware"
	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/pkg/jwt"
)

type AuthController struct{}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type DormQuickLoginRequest struct {
	Phone    string `json:"phone" binding:"required"`
	Building string `json:"building" binding:"required"`
	Floor    string `json:"floor"`
	RealName string `json:"real_name" binding:"required"`
}

// loginKey 限流键：账号 + 来源 IP。
func loginKey(c *gin.Context, account string) string {
	return account + "|" + c.ClientIP()
}

// Login 账号密码通用登录（网站与移动端均支持）
func (a *AuthController) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误，请提供账号和密码"})
		return
	}

	username := strings.TrimSpace(req.Username)
	key := loginKey(c, username)
	if allowed, retryAfter := loginAllowed(key); !allowed {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       fmt.Sprintf("登录失败次数过多，账号已被临时锁定，请 %d 秒后重试", retryAfter),
			"retry_after": retryAfter,
		})
		return
	}

	var user model.User
	if err := repository.DB.Where("username = ?", username).First(&user).Error; err != nil {
		loginRecordFailure(key)
		logFailedLogin(c, username, "账号不存在")
		// 账号不存在与密码错误统一口径，避免可被用来枚举用户名
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}

	if user.Status == "disabled" {
		loginRecordFailure(key)
		logFailedLogin(c, username, "账号已停用")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "该账号已被停用，请联系学管会技术维护组"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		loginRecordFailure(key)
		logFailedLogin(c, username, "密码错误")
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号或密码错误"})
		return
	}

	loginRecordSuccess(key)

	token, err := jwt.GenerateToken(user.ID, user.Username, user.RealName, user.Role, user.Building, user.Floor)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "令牌签发失败: " + err.Error()})
		return
	}
	middleware.SetSessionCookie(c, token)

	logOperationAs(c, user, "auth.login", "user", user.ID, fmt.Sprintf("账号密码登录成功（角色 %s）", user.Role))

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user":  user,
	})
}

// logFailedLogin 登录失败也必须留痕。
func logFailedLogin(c *gin.Context, account, reason string) {
	_ = repository.RecordOperation(&model.OperationLog{
		Action:       "auth.login_failed",
		TargetType:   "user",
		OperatorName: account,
		Detail:       reason,
		IP:           c.ClientIP(),
		CreatedAt:    time.Now(),
	})
}

// DormQuickLogin 宿管专用三要素登录（手机端专享）
func (a *AuthController) DormQuickLogin(c *gin.Context) {
	var req DormQuickLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请完整填写手机号、负责楼栋及真实姓名"})
		return
	}

	cleanPhone := strings.TrimSpace(req.Phone)
	cleanName := strings.TrimSpace(req.RealName)
	key := loginKey(c, cleanPhone)
	if allowed, retryAfter := loginAllowed(key); !allowed {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       fmt.Sprintf("认证失败次数过多，请 %d 秒后重试", retryAfter),
			"retry_after": retryAfter,
		})
		return
	}

	var preset model.DormRosterPreset
	err := repository.DB.Where("phone = ? AND real_name = ?", cleanPhone, cleanName).First(&preset).Error
	if err != nil {
		loginRecordFailure(key)
		logFailedLogin(c, cleanName, "三要素未命中宿管预置花名册")
		c.JSON(http.StatusForbidden, gin.H{
			"error": "认证失败：手机号与姓名未在宿管预置花名册中命中，请联系学管会技术维护组录入",
		})
		return
	}

	// 楼栋按规范化后的取值精确比对：不再做开放式子串包含，
	// 否则输入 "1" 也能通过 "12号楼" 的校验。
	if normalizeBuildingToken(cleanBuilding(preset.Building)) != normalizeBuildingToken(cleanBuilding(req.Building)) {
		loginRecordFailure(key)
		logFailedLogin(c, cleanName, "楼栋不匹配")
		c.JSON(http.StatusForbidden, gin.H{
			"error":           fmt.Sprintf("楼栋信息不匹配，预置记录为：%s", preset.Building),
			"preset_building": preset.Building,
		})
		return
	}

	// 查找或自动为该宿管生成系统账号
	var user model.User
	if preset.BoundUserID > 0 {
		_ = repository.DB.First(&user, preset.BoundUserID).Error
	}

	if user.ID == 0 {
		if err := repository.DB.Where("phone = ?", cleanPhone).First(&user).Error; err == nil {
			// 命中已存在的账号：三要素登录只能拿到宿管角色，
			// 否则把教师/管理员的手机号录进花名册即可冒用其高权限账号。
			if user.Role != model.RoleDormManager {
				loginRecordFailure(key)
				logFailedLogin(c, cleanName, fmt.Sprintf("手机号已绑定 %s 角色，拒绝三要素登录", user.Role))
				c.JSON(http.StatusForbidden, gin.H{
					"error": "该手机号已绑定非宿管账号，三要素登录仅限宿管使用，请改用账号密码登录",
				})
				return
			}
		} else {
			defaultPwd, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.DefaultCost)
			user = model.User{
				Username:     "dorm_" + cleanPhone,
				PasswordHash: string(defaultPwd),
				RealName:     preset.RealName,
				Phone:        cleanPhone,
				Role:         model.RoleDormManager,
				Building:     preset.Building,
				Floor:        preset.Floor,
				Department:   "学生宿舍宿管部",
				Status:       "active",
			}
			if err := repository.DB.Create(&user).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "宿管账号创建失败: " + err.Error()})
				return
			}
		}
		preset.IsActivated = true
		preset.BoundUserID = user.ID
		repository.DB.Save(&preset)
	}

	if user.Status == "disabled" {
		loginRecordFailure(key)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "该账号已被停用，请联系学管会技术维护组"})
		return
	}

	loginRecordSuccess(key)

	token, err := jwt.GenerateToken(user.ID, user.Username, user.RealName, user.Role, user.Building, user.Floor)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成快捷登录令牌失败: " + err.Error()})
		return
	}
	middleware.SetSessionCookie(c, token)

	logOperationAs(c, user, "auth.dorm_quick_login", "user", user.ID, fmt.Sprintf("三要素登录成功（%s）", preset.Building))

	c.JSON(http.StatusOK, gin.H{
		"message": "宿管三要素核验通过，快捷登录成功！",
		"token":   token,
		"user":    user,
	})
}

// Logout 登出：清除会话 Cookie 并留痕。
func (a *AuthController) Logout(c *gin.Context) {
	if operator, ok := operatorFromContext(c); ok {
		logOperationAs(c, operator, "auth.logout", "user", operator.ID, "主动登出")
	}
	middleware.ClearSessionCookie(c)
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

// cleanBuilding 去掉空格，统一比对基础。
func cleanBuilding(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "")
}

// normalizeBuildingToken 提取楼栋的楼号数字，忽略 "号楼/号/栋" 与 "区" 前缀的写法差异。
// 边界明确：输入 "1" 归一为 "1"，而 "12号楼" 归一为 "12"，二者不再相等。
func normalizeBuildingToken(s string) string {
	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	out := digits.String()
	if out == "" {
		return strings.ToLower(s)
	}
	return out
}

// GetProfile 获取当前登录用户的个人信息
func (a *AuthController) GetProfile(c *gin.Context) {
	uid := c.GetUint("user_id")
	var user model.User
	if err := repository.DB.First(&user, uid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	c.JSON(http.StatusOK, user)
}

// UpdateSecuritySettings 用户更新自身安全设置
func (a *AuthController) UpdateSecuritySettings(c *gin.Context) {
	uid := c.GetUint("user_id")
	var user model.User
	if err := repository.DB.First(&user, uid).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	var req struct {
		Username    string `json:"username"`
		RealName    string `json:"real_name"`
		Phone       string `json:"phone"`
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.RealName = strings.TrimSpace(req.RealName)
	req.Phone = strings.TrimSpace(req.Phone)

	phoneChanged := req.Phone != "" && req.Phone != user.Phone
	passwordChanged := req.NewPassword != ""

	// 修改手机号或密码都属于敏感变更：必须先核验原密码
	if phoneChanged || passwordChanged {
		if req.OldPassword == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "修改手机号或密码时必须输入当前原密码进行核验"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "当前原密码输入错误，校验未通过"})
			return
		}
	}

	// 1. 用户名唯一性
	if req.Username != "" && req.Username != user.Username {
		var exist model.User
		if err := repository.DB.Where("username = ? AND id != ?", req.Username, uid).First(&exist).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "该登录账号已被其他成员占用，请更换"})
			return
		}
	}

	// 2. 新手机号不得已被其他账号或宿管花名册占用（三要素以手机号为主键）
	if phoneChanged {
		var dupUser model.User
		if err := repository.DB.Where("phone = ? AND id != ?", req.Phone, uid).First(&dupUser).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "该手机号已被其他账号占用"})
			return
		}
		var dupPreset model.DormRosterPreset
		if err := repository.DB.Where("phone = ?", req.Phone).First(&dupPreset).Error; err == nil && dupPreset.BoundUserID != user.ID {
			c.JSON(http.StatusConflict, gin.H{"error": "该手机号已录入宿管免密花名册，请先由技术维护组调整后再修改"})
			return
		}
	}

	// 3. 密码强度
	if passwordChanged {
		if len(req.NewPassword) < 6 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "新密码长度至少需要 6 位"})
			return
		}
		newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "新密码加密计算失败: " + err.Error()})
			return
		}
		user.PasswordHash = string(newHash)
	}

	if req.Username != "" && req.Username != user.Username {
		user.Username = req.Username
	}
	if req.RealName != "" {
		user.RealName = req.RealName
	}
	if phoneChanged {
		user.Phone = req.Phone
		// 宿管的手机号是三要素登录的主键：修改后同步预置花名册，避免登录被改挂
		if user.Role == model.RoleDormManager {
			repository.DB.Model(&model.DormRosterPreset{}).
				Where("bound_user_id = ?", user.ID).
				Update("phone", req.Phone)
		}
	}

	user.UpdatedAt = time.Now()
	if err := repository.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存安全设置失败: " + err.Error()})
		return
	}

	token, _ := jwt.GenerateToken(user.ID, user.Username, user.RealName, user.Role, user.Building, user.Floor)
	middleware.SetSessionCookie(c, token)

	logOperationAs(c, user, "auth.security_update", "user", user.ID, securityChangeSummary(req, phoneChanged, passwordChanged))

	c.JSON(http.StatusOK, gin.H{
		"message": "账号安全设置与个人信息已成功更新！",
		"token":   token,
		"user":    user,
	})
}

func securityChangeSummary(req struct {
	Username    string `json:"username"`
	RealName    string `json:"real_name"`
	Phone       string `json:"phone"`
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}, phoneChanged, passwordChanged bool) string {
	var parts []string
	if req.Username != "" {
		parts = append(parts, "修改登录账号")
	}
	if req.RealName != "" {
		parts = append(parts, "修改姓名")
	}
	if phoneChanged {
		parts = append(parts, "修改绑定手机号（已同步宿管花名册）")
	}
	if passwordChanged {
		parts = append(parts, "修改登录密码")
	}
	if len(parts) == 0 {
		return "未做任何变更"
	}
	return strings.Join(parts, "、")
}
