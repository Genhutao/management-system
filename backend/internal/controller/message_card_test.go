package controller

import (
	"strings"
	"testing"
	"time"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// 未读消息卡是站内信在工作台里的唯一入口，断言三件事：
// 每个角色都有、数字与 /messages/unread-count 同一口径（只算本人的未读）、
// 0 封时也照样出现（藏起来会让人以为功能没上线，"没有未读"本身就是信息）。
func TestSummaryCarriesUnreadMessageCardForEveryRole(t *testing.T) {
	setupDashboardDB(t)

	roles := []model.User{
		{Username: "r_dorm", RealName: "宿管甲", Role: model.RoleDormManager, Building: "7号楼", Floor: "全楼", Status: "active"},
		{Username: "r_member", RealName: "部员乙", Role: model.RoleMember, Department: "纪检部", Status: "active"},
		{Username: "r_minister", RealName: "部长丙", Role: model.RoleMinister, Department: "纪检部", Status: "active"},
		{Username: "r_tech", RealName: "技术丁", Role: model.RoleTechAdmin, Department: "组织部 · 技术组", Status: "active"},
		{Username: "r_viewer", RealName: "查看戊", Role: model.RoleViewerExport, Status: "active"},
	}
	ids := make([]uint, 0, len(roles))
	for i := range roles {
		ids = append(ids, seedDashboardUser(t, roles[i]).ID)
	}

	// 给第 1 个账号 2 封未读 + 1 封已读，给最后一个账号 1 封已读；其余人零封
	now := time.Now()
	read := &now
	repository.DB.Create(&model.Message{SenderID: 99, RecipientID: ids[0], Title: "未读一", ReadAt: nil})
	repository.DB.Create(&model.Message{SenderID: 99, RecipientID: ids[0], Title: "未读二", ReadAt: nil})
	repository.DB.Create(&model.Message{SenderID: 99, RecipientID: ids[0], Title: "看过了", ReadAt: read})
	repository.DB.Create(&model.Message{SenderID: 99, RecipientID: ids[4], Title: "发给查看岗且已读", ReadAt: read})
	// 别人之间的信不得算进任何人的未读
	repository.DB.Create(&model.Message{SenderID: 99, RecipientID: 8123, Title: "与我无关"})

	cases := []struct {
		uid  uint
		name string
		want int
	}{
		{ids[0], "宿管", 2},
		{ids[1], "部员", 0},
		{ids[2], "部长", 0},
		{ids[3], "技术维护组", 0},
		{ids[4], "查看下载岗", 0},
	}
	for _, tc := range cases {
		code, body := runSummary(t, tc.uid)
		if code != 200 {
			t.Fatalf("%s 总览应 200，实际 %d: %v", tc.name, code, body)
		}
		card, ok := cardKeys(t, body)["unread"]
		if !ok {
			t.Fatalf("%s 的总览缺未读消息卡（0 封也要显示）", tc.name)
		}
		if card.Value != tc.want {
			t.Errorf("%s 未读数应为 %d，实际 %d: %+v", tc.name, tc.want, card.Value, card)
		}
		if card.Tab != "messages" {
			t.Errorf("%s 未读卡应指向 messages 面板，实际 tab=%q", tc.name, card.Tab)
		}
		if card.Unit != "封" {
			t.Errorf("%s 未读卡的单位应为封，实际 %q", tc.name, card.Unit)
		}
	}
}

// 提醒条只在真有未读时出现，且指向同一个面板；数字为 0 时不得凭空冒出一条提醒。
func TestSummaryUnreadNoticeTracksCount(t *testing.T) {
	setupDashboardDB(t)
	withMail := seedDashboardUser(t, model.User{
		Username: "n_member", RealName: "部员甲", Role: model.RoleMember, Department: "纪检部", Status: "active",
	})
	quiet := seedDashboardUser(t, model.User{
		Username: "n_minister", RealName: "部长乙", Role: model.RoleMinister, Department: "纪检部", Status: "active",
	})
	repository.DB.Create(&model.Message{SenderID: quiet.ID, RecipientID: withMail.ID, Title: "查寝时段调整"})

	notices := func(uid uint) []interface{} {
		_, body := runSummary(t, uid)
		list, _ := body["notices"].([]interface{})
		return list
	}

	hits := 0
	for _, raw := range notices(withMail.ID) {
		row, _ := raw.(map[string]interface{})
		text, _ := row["text"].(string)
		if len(text) > 0 && containsMailHint(text) {
			hits++
			if row["tab"] != "messages" {
				t.Errorf("未读提醒应指向 messages 面板，实际 %v", row["tab"])
			}
		}
	}
	if hits != 1 {
		t.Fatalf("有一封未读时应恰好一条提醒，实际 %d 条", hits)
	}

	for _, raw := range notices(quiet.ID) {
		row, _ := raw.(map[string]interface{})
		if text, _ := row["text"].(string); containsMailHint(text) {
			t.Fatalf("零未读却给出了未读提醒: %q", text)
		}
	}
}

// 中文文案只匹配"站内信/未读"两个词，避免把整句写死导致改文案就红。
func containsMailHint(text string) bool {
	return strings.Contains(text, "站内信") || strings.Contains(text, "未读")
}
