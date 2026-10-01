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

// requireStepUp 高危业务操作当场重验登录口令，并拦下仍在使用初始口令的账号。
// 口令经 X-Confirm-Password 请求头传入（用请求头而非请求体，避免与处理器
// 的 JSON 绑定争抢同一个 request body），与本账号数据库中的口令 bcrypt 比对。
// 用于：写入扣分、撤销扣分、调整他人积分、变更角色。
//
// 未改过初始口令的账号必须先改密：这类账号输入出厂口令同样能过 bcrypt 比对，
// 只验口令等于把高危写权留给任何知道默认口令的人。
func requireStepUp(c *gin.Context, operator model.User) bool {
	if operator.PasswordChangedAt == nil {
		c.JSON(http.StatusForbidden, gin.H{
			"code":  "password_change_required",
			"error": "该账号仍在使用初始口令，请先到「账户安全中心」修改登录口令，再执行此类操作",
		})
		return false
	}
	return requireStepUpRaw(c, operator)
}

// requireStepUpAccountHygiene 账号自身的防护性操作（确认异地登录、解绑二次验证）
// 只重验登录口令，不受"尚未改过初始口令"限制：
// 把这些一起拦下只会让存量账号更没法自救，反而降低安全性。
func requireStepUpAccountHygiene(c *gin.Context, operator model.User) bool {
	return requireStepUpRaw(c, operator)
}

func requireStepUpRaw(c *gin.Context, operator model.User) bool {
	// 复用登录限流：同一账号+来源 IP 连续输错确认口令达阈值后暂时锁住，
	// 否则一次会话劫持就能挂在线穷举口令。
	key := "stepup|" + operator.Username + "|" + c.ClientIP()
	if allowed, retryAfter := loginAllowed(key); !allowed {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":       fmt.Sprintf("二次确认连续失败次数过多，请 %d 秒后重试", retryAfter),
			"retry_after": retryAfter,
		})
		return false
	}

	confirm := strings.TrimSpace(c.GetHeader("X-Confirm-Password"))
	if confirm == "" {
		// 没带确认头属请求格式问题，不计入失败次数
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "该操作为高危操作，请在请求头 X-Confirm-Password 中携带当前登录口令二次确认",
		})
		return false
	}
	if err := bcrypt.CompareHashAndPassword([]byte(operator.PasswordHash), []byte(confirm)); err != nil {
		loginRecordFailure(key)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "二次确认口令错误，操作未执行"})
		return false
	}
	loginRecordSuccess(key)
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
