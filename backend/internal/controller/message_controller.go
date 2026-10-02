package controller

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// MessageController 站内信。
//
// 行级范围只有一条口径：收件箱按 recipient_id、发件箱按 sender_id，一律取当前登录账号
// 本人的 user_id。Casbin 上的 /api/v1/messages 与 /api/v1/messages/* 是路径级策略，
// 它管不到"哪几行"，所以行级过滤必须在 SQL 里做——同宿管留痕详情那条既有教训。
type MessageController struct{}

const (
	messageDefaultPageSize = 20
	messageMaxPageSize     = 100
)

// messagePageParams 把页码与每页条数收口在合法区间内，
// 不把 ?page=-1 或 ?page_size=99999 这类输入原样透进 SQL。
func messagePageParams(c *gin.Context) (int, int) {
	page, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("page", "1")))
	if err != nil || page < 1 {
		page = 1
	}
	size, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("page_size", strconv.Itoa(messageDefaultPageSize))))
	if err != nil || size < 1 {
		size = messageDefaultPageSize
	}
	if size > messageMaxPageSize {
		size = messageMaxPageSize
	}
	return page, size
}

// truthyQuery 只认 1 与 true。不收口的话 ?unread_only=0 也会被当成"只看未读"
// （非空字符串即真），界面勾掉复选框却还在筛未读。
func truthyQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true":
		return true
	}
	return false
}

// List 收件箱（box=in，默认）与发件箱（box=sent）。
// box 传其它值一律 400：把未知取值悄悄当成收件箱，界面点"我发出的信"会看到收到的信。
func (m *MessageController) List(c *gin.Context) {
	uid := c.GetUint("user_id")
	box := strings.ToLower(strings.TrimSpace(c.DefaultQuery("box", "in")))
	if box != "in" && box != "sent" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "box 只能是 in（收件箱）或 sent（发件箱）"})
		return
	}

	column, unreadable := "recipient_id", true
	if box == "sent" {
		column, unreadable = "sender_id", false
	}
	onlyUnread := unreadable && truthyQuery(c.Query("unread_only"))

	// 每次取数都新建一条链：Count 是终结方法，复用同一个 *gorm.DB 再 Find 会带上残留状态
	scoped := func() *gorm.DB {
		q := repository.DB.Model(&model.Message{}).Where(column+" = ?", uid)
		if onlyUnread {
			q = q.Where("read_at IS NULL")
		}
		return q
	}

	var total int64
	if err := scoped().Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取站内信条数失败"})
		return
	}

	page, size := messagePageParams(c)
	var items []model.Message
	if err := scoped().Order("id desc").Limit(size).Offset((page - 1) * size).Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取站内信失败"})
		return
	}
	// 空列表必须是 []：nil 编码成 JSON 是 null，前端 .length 当场崩
	if items == nil {
		items = []model.Message{}
	}

	c.JSON(http.StatusOK, gin.H{
		"box":         box,
		"unread_only": onlyUnread,
		"total":       total,
		"page":        page,
		"page_size":   size,
		"items":       items,
	})
}

// UnreadCount 未读数，总览卡片用。只算还没被收件人清掉的。
func (m *MessageController) UnreadCount(c *gin.Context) {
	uid := c.GetUint("user_id")
	var count int64
	if err := repository.DB.Model(&model.Message{}).
		Where("recipient_id = ? AND read_at IS NULL", uid).Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "未读消息数统计失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"unread": count})
}

// MarkRead 收件人清未读。三条硬口径：
//  1. 只有收件人能清——发件人拿自己发出去那条的 id 也改不动别人的已读状态；
//  2. 重复调用幂等，不覆盖首次已读时间（否则"看过的时间"会变成"最后一次点的时间"）；
//  3. "没有这条"与"这条不是你的"合并成同一个 404、报文逐字相同，
//     否则这里就又是一个"全校谁收到过信"的存在性探测器。
func (m *MessageController) MarkRead(c *gin.Context) {
	uid := c.GetUint("user_id")
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "消息编号无效"})
		return
	}

	var msg model.Message
	if findErr := repository.DB.Where("id = ? AND recipient_id = ?", id, uid).First(&msg).Error; findErr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "该消息不存在，或不在您的收件箱中"})
		return
	}
	alreadyRead := msg.ReadAt != nil

	if !alreadyRead {
		// 条件里带上 read_at IS NULL：连点两次时后一次改不到行，首次时间因此得以保留
		if err := repository.DB.Model(&model.Message{}).
			Where("id = ? AND recipient_id = ? AND read_at IS NULL", msg.ID, uid).
			Update("read_at", time.Now()).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "标记已读失败，请稍后重试"})
			return
		}
		if err := repository.DB.First(&msg, msg.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取已读状态失败"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"id": msg.ID, "read_at": msg.ReadAt, "already_read": alreadyRead})
}
