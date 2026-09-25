package controller

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// operatorFromContext 以数据库中的最新账号信息解析当前操作者。
// 刻意不使用 JWT 载荷里的角色与姓名：令牌签发后账号可能已调岗、停用或升职，
// 留痕必须记录"当时真实的职务身份"，否则审计会被旧 token 绕过。
func operatorFromContext(c *gin.Context) (model.User, bool) {
	var operator model.User
	if err := repository.DB.First(&operator, c.GetUint("user_id")).Error; err != nil {
		return operator, false
	}
	return operator, true
}

// requireStepUp 高危操作当场重验登录口令。
// 口令经 X-Confirm-Password 请求头传入（用请求头而非请求体，避免与处理器
// 的 JSON 绑定争抢同一个 request body），与本账号数据库中的口令 bcrypt 比对。
// 用于：写入扣分、撤销扣分、调整他人积分、变更角色。
func requireStepUp(c *gin.Context, operator model.User) bool {
	confirm := strings.TrimSpace(c.GetHeader("X-Confirm-Password"))
	if confirm == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "该操作为高危操作，请在请求头 X-Confirm-Password 中携带当前登录口令二次确认",
		})
		return false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(operator.PasswordHash), []byte(confirm)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "二次确认口令错误，操作未执行"})
		return false
	}
	return true
}

// logOperationAs 记录一条高危操作留痕。写入失败不阻断业务，由 repository 侧降级打日志。
func logOperationAs(c *gin.Context, operator model.User, action, targetType string, targetID uint, detail string) {
	_ = repository.RecordOperation(&model.OperationLog{
		Action:       action,
		TargetType:   targetType,
		TargetID:     targetID,
		OperatorID:   operator.ID,
		OperatorName: operator.RealName,
		OperatorRole: operator.Role,
		Detail:       detail,
		IP:           c.ClientIP(),
		CreatedAt:    time.Now(),
	})
}
