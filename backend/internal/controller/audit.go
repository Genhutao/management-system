package controller

import (
	"time"

	"github.com/gin-gonic/gin"

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
