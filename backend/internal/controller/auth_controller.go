package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

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

// Login 账号密码通用登录（网站与移动端均支持）
func (a *AuthController) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误，请提供账号和密码"})
		return
	}

	var user model.User
	if err := repository.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号不存在或已被禁用"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "密码错误，请重新输入"})
		return
	}

	token, err := jwt.GenerateToken(user.ID, user.Username, user.RealName, user.Role, user.Building, user.Floor)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "令牌签发失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": token,
		"user":  user,
	})
}

// DormQuickLogin 宿管专用快速认证登录（手机端专享：手机号 + 负责楼栋楼层 + 姓名 三要素匹配预置花名册）
func (a *AuthController) DormQuickLogin(c *gin.Context) {
	var req DormQuickLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请完整填写手机号、负责楼栋及真实姓名"})
		return
	}

	cleanPhone := strings.TrimSpace(req.Phone)
	cleanName := strings.TrimSpace(req.RealName)
	cleanBuilding := strings.TrimSpace(req.Building)

	// 查询预置花名册
	var preset model.DormRosterPreset
	err := repository.DB.Where("phone = ? AND real_name = ?", cleanPhone, cleanName).First(&preset).Error
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "认证失败：未在宿管预置花名册中找到匹配的手机号与姓名，请联系学管会技术维护组录入",
		})
		return
	}

	// 楼栋粗略校验
	if !strings.Contains(preset.Building, cleanBuilding) && !strings.Contains(cleanBuilding, preset.Building) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": fmt.Sprintf("楼栋信息不匹配，预置记录为：%s", preset.Building),
		})
		return
	}

	// 查找或自动为该宿管生成系统 User 账号
	var user model.User
	if preset.BoundUserID > 0 {
		_ = repository.DB.First(&user, preset.BoundUserID).Error
	}

	if user.ID == 0 {
		// 查询手机号是否已有关联
		if err := repository.DB.Where("phone = ?", cleanPhone).First(&user).Error; err != nil {
			// 首次激活，自动创建 User
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
			repository.DB.Create(&user)
		}
		// 标记预置花名册已激活
		preset.IsActivated = true
		preset.BoundUserID = user.ID
		repository.DB.Save(&preset)
	}

	token, err := jwt.GenerateToken(user.ID, user.Username, user.RealName, user.Role, user.Building, user.Floor)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成快捷登录令牌失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "宿管三要素核验通过，快捷登录成功！",
		"token":   token,
		"user":    user,
	})
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

// UpdateSecuritySettings 用户更新自身安全设置 (修改登录账号、密码、真实姓名及手机号)
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

	// 1. 如果修改账号用户名，校验唯一性
	if req.Username != "" && req.Username != user.Username {
		var exist model.User
		if err := repository.DB.Where("username = ? AND id != ?", req.Username, uid).First(&exist).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "该登录账号已被其他成员占用，请更换"})
			return
		}
		user.Username = req.Username
	}

	// 2. 如果提供新姓名
	if req.RealName != "" {
		user.RealName = req.RealName
	}

	// 3. 如果提供新手机号
	if req.Phone != "" {
		user.Phone = req.Phone
	}

	// 4. 如果修改密码，需先核对原旧密码
	if req.NewPassword != "" {
		if req.OldPassword == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "修改密码时必须输入当前原密码进行核验"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.OldPassword)); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "当前原密码输入错误，校验未通过"})
			return
		}
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

	user.UpdatedAt = time.Now()
	if err := repository.DB.Save(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存安全设置失败: " + err.Error()})
		return
	}

	// 重新生成最新 Token (包含最新账号与姓名)
	token, _ := jwt.GenerateToken(user.ID, user.Username, user.RealName, user.Role, user.Building, user.Floor)

	c.JSON(http.StatusOK, gin.H{
		"message": "账号安全设置与个人信息已成功更新！",
		"token":   token,
		"user":    user,
	})
}
