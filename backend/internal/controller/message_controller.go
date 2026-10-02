package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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

// 长度与数量上限一律按"字符数"算而不是字节数：中文一个字占 3 个字节，
// 用 len() 卡会把一句正常的话判成超长。
const (
	messageTitleMaxRunes        = 40
	messageBodyMaxRunes         = 5000
	messageRecipientsMaxPerSend = 100
)

// sendMessageRequest 发送接口的入参。
// Kind 出现在结构体里是为了**明确拒绝它**：system_cc 是"字段就位、写入路径为空"的预留位，
// 客户端能指定它就等于任何人皆可自造一条"系统抄送"，将来查不到来源。
type sendMessageRequest struct {
	RecipientIDs []uint `json:"recipient_ids"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	Kind         string `json:"kind"`
}

// Contacts 选人列表：只返回矩阵允许发给的账号，且只带展示字段（不含手机号）。
// 查看下载岗拿到的是 200 + 空列表而不是 403：界面能写"您所在的身份暂不可发信"，
// 而一个 403 只会让人以为系统坏了。
func (m *MessageController) Contacts(c *gin.Context) {
	var from model.User
	if err := repository.DB.First(&from, c.GetUint("user_id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	// 全校账号一次性取回内存再按矩阵筛：账号数量级是"教职工几十人"，
	// 把矩阵拆成 SQL 反而会让"选人"与"发送"两处判定各写一遍——那正是漂移的起点。
	var all []model.User
	if err := repository.DB.Order("id asc").Find(&all).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取可发信对象失败"})
		return
	}
	contacts := messageContactsOf(from, all)
	c.JSON(http.StatusOK, gin.H{"can_send": len(contacts) > 0, "total": len(contacts), "items": contacts})
}

// uniqueRecipientIDs 去重并保持提交顺序，顺手丢掉 0（前端未选中时的占位值）。
func uniqueRecipientIDs(raw []uint) []uint {
	seen := make(map[uint]bool, len(raw))
	out := make([]uint, 0, len(raw))
	for _, id := range raw {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// Send 发一封站内信给若干人。三条口径：
//  1. 收件人逐个过 canMessageBetween——前端少显示几个人不构成服务端约束，
//     直接 POST 一个别人账号的 id 必须被拦；
//  2. 全成全败：一半发出去一半被拒会让界面上的"已发送"变成谎话，
//     所以任何一个收件人不合法就整封不发（同评优名单那条既有口径）；
//  3. 不要求 step-up（X-Confirm-Password）：发信不改分值、不撤销记录、不动角色，
//     与打表/改角色那一类高危写不是同类，加口令复核只会让人懒得用。
func (m *MessageController) Send(c *gin.Context) {
	var from model.User
	if err := repository.DB.First(&from, c.GetUint("user_id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式不正确，应为 {recipient_ids, title, body}"})
		return
	}
	if kind := strings.TrimSpace(req.Kind); kind != "" && kind != model.MessageKindHuman {
		c.JSON(http.StatusBadRequest, gin.H{"error": "站内信类别由服务端写入，请勿提交 kind 字段"})
		return
	}

	title, body := strings.TrimSpace(req.Title), strings.TrimSpace(req.Body)
	switch {
	case title == "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不能为空"})
		return
	case utf8.RuneCountInString(title) > messageTitleMaxRunes:
		c.JSON(http.StatusBadRequest, gin.H{"error": "标题不得超过 " + strconv.Itoa(messageTitleMaxRunes) + " 个字"})
		return
	case body == "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "正文不能为空"})
		return
	case utf8.RuneCountInString(body) > messageBodyMaxRunes:
		c.JSON(http.StatusBadRequest, gin.H{"error": "正文不得超过 " + strconv.Itoa(messageBodyMaxRunes) + " 个字"})
		return
	}

	ids := uniqueRecipientIDs(req.RecipientIDs)
	if len(ids) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请至少选择一位收件人"})
		return
	}
	if len(ids) > messageRecipientsMaxPerSend {
		c.JSON(http.StatusBadRequest, gin.H{"error": "一次最多发给 " + strconv.Itoa(messageRecipientsMaxPerSend) + " 人，群发性通知应走公告"})
		return
	}

	var found []model.User
	if err := repository.DB.Where("id IN ?", ids).Find(&found).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "校验收件人失败"})
		return
	}
	byID := make(map[uint]model.User, len(found))
	for _, u := range found {
		byID[u.ID] = u
	}

	// "这个账号不存在"与"这个账号你发不了"合并成同一句报文：
	// 分开写的话，发送接口就变成全校账号编号的存在性探测器。
	rejected := []uint{}
	recipients := make([]model.User, 0, len(ids))
	for _, id := range ids {
		target, ok := byID[id]
		if !ok || !canMessageBetween(from, target) {
			rejected = append(rejected, id)
			continue
		}
		recipients = append(recipients, target)
	}
	if len(rejected) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":                  "收件人中有不在您可发范围内的账号，本封未发出",
			"rejected_recipient_ids": rejected,
		})
		return
	}

	now := time.Now()
	rows := make([]model.Message, 0, len(recipients))
	names := make([]string, 0, len(recipients))
	for _, to := range recipients {
		rows = append(rows, model.Message{
			Kind: model.MessageKindHuman,
			// 姓名按当时快照落库：账号之后改名或停用，历史信与审计仍能还原"当时发给谁"
			SenderID: from.ID, SenderName: from.RealName,
			RecipientID: to.ID, RecipientName: to.RealName,
			Title: title, Body: body, CreatedAt: now,
		})
		names = append(names, to.RealName)
	}

	created := make([]uint, 0, len(rows))
	if err := repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.CreateInBatches(&rows, 50).Error; err != nil {
			return err
		}
		for i := range rows {
			created = append(created, rows[i].ID)
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "发信失败，请稍后重试"})
		return
	}

	// 审计只记"谁发给谁、标题是什么"，**不记正文**：正文里往往带具体学生与事件，
	// 审计表不是第二份消息存储。
	logOperationAs(c, from, "message.send", "message", created[0],
		fmt.Sprintf("收件人 %d 人：%s；标题=%s", len(recipients), strings.Join(names, "、"), title))

	c.JSON(http.StatusOK, gin.H{"sent": len(created), "ids": created, "recipients": names})
}
