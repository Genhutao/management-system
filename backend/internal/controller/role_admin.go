package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

var assignableRoles = map[string]bool{
	model.RoleDormManager:  true,
	model.RoleMember:       true,
	model.RoleMinister:     true,
	model.RoleTechAdmin:    true,
	model.RoleViewerExport: true,
}

// ChangeUserRole 角色变更的专门入口：通用数据编辑器已被禁止指定 role。
// tech_admin（Casbin /tech/*）+ 当场重验登录口令 + 只增不改留痕。
func (tdb *TechDBController) ChangeUserRole(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if operator.Role != model.RoleTechAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "仅技术维护组可变更账号角色"})
		return
	}
	if !requireStepUp(c, operator) {
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
		return
	}

	var req struct {
		Role   string `json:"role" binding:"required"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供目标角色（role）与变更理由（reason）"})
		return
	}

	req.Role = strings.TrimSpace(req.Role)
	if !assignableRoles[req.Role] {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":    "非法角色，允许值：dorm_manager / member / minister / tech_admin / viewer_export",
			"role":     req.Role,
		})
		return
	}

	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色变更必须填写理由，以便事后追溯"})
		return
	}

	var target model.User
	if err := repository.DB.First(&target, uint(id)).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	if target.ID == operator.ID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能变更自己的角色"})
		return
	}

	previousRole := target.Role
	target.Role = req.Role
	target.UpdatedAt = time.Now()
	if err := repository.DB.Save(&target).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "角色变更保存失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "user.role_change", "user", target.ID,
		fmt.Sprintf("将【%s】角色由 %s 变更为 %s：理由：%s", target.RealName, previousRole, req.Role, reason))

	c.JSON(http.StatusOK, gin.H{
		"message":       fmt.Sprintf("【%s】角色已由 %s 变更为 %s", target.RealName, previousRole, req.Role),
		"previous_role": previousRole,
		"role":          req.Role,
		"user":          target,
	})
}

// GetOperationLogs 审计留痕的唯一查询入口（只读）。仅技术维护组可见。
func (tdb *TechDBController) GetOperationLogs(c *gin.Context) {
	if _, ok := operatorFromContext(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	if role, _ := c.Get("role"); role != model.RoleTechAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "审计留痕仅技术维护组可查"})
		return
	}

	action := strings.TrimSpace(c.Query("action"))
	operator := strings.TrimSpace(c.Query("operator"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "30"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 30
	}

	query := repository.DB.Model(&model.OperationLog{})
	if action != "" {
		query = query.Where("action = ?", action)
	}
	if operator != "" {
		query = query.Where("operator_name LIKE ?", "%"+operator+"%")
	}

	var total int64
	query.Count(&total)

	var list []model.OperationLog
	query.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list)

	c.JSON(http.StatusOK, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"items":     list,
	})
}
