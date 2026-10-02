package controller

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// DashboardController 登录后总览。口径只有一条硬约束：
// 每张卡片都必须落在该角色**本来就有权限读到的接口**范围内，
// 总览不是新的授权通道 —— 宿管看不到违纪统计（其 Casbin 无 /deductions），
// 查看下载岗看不到部员积分（其 Casbin 无 /member/*）。
type DashboardController struct{}

// DashboardCard 一张概览卡。Value 与 Text 二选一：计数用 Value，负责范围这类用 Text。
type DashboardCard struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value int    `json:"value"`
	Unit  string `json:"unit,omitempty"`
	Text  string `json:"text,omitempty"`
	Hint  string `json:"hint,omitempty"`
	Tab   string `json:"tab,omitempty"` // 点卡片跳哪个 panel
	Sub   string `json:"sub,omitempty"` // 目标页内的子分页
}

// DashboardNotice 概览下方的一句话提醒。level 只有 info/warn，前端据此配色。
type DashboardNotice struct {
	Level string `json:"level"`
	Text  string `json:"text"`
	Tab   string `json:"tab,omitempty"`
	Sub   string `json:"sub,omitempty"`
}

// Summary 返回当前登录用户的总览。身份取库里的最新行而不是 JWT 声明，
// 因为任免、部门、楼栋改了之后旧 Cookie 仍带着改前的声明，会显示错范围。
func (dc *DashboardController) Summary(c *gin.Context) {
	var user model.User
	if err := repository.DB.First(&user, c.GetUint("user_id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	// 刻意不用 nil 起步：空列表编码成 JSON 是 null，前端 forEach 直接崩。
	cards := []DashboardCard{}
	notices := []DashboardNotice{}

	switch user.Role {
	case model.RoleDormManager:
		cards, notices = dormSummary(user)
	case model.RoleMember:
		cards, notices = memberSummary(user)
	case model.RoleMinister:
		cards, notices = ministerSummary(user)
	case model.RoleTechAdmin:
		cards, notices = techSummary(user)
	case model.RoleViewerExport:
		cards, notices = viewerSummary(user)
	default:
		c.JSON(http.StatusForbidden, gin.H{"error": "当前身份没有对应的总览页"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"role":        user.Role,
		"name":        user.RealName,
		"department":  user.Department,
		"position":    user.Position,
		"scope":       scopeLabel(user),
		"week":        model.WeekKeyOf(time.Now()),
		"server_time": time.Now().Format("2006-01-02 15:04:05"),
		"cards":       cards,
		"notices":     notices,
	})
}

// scopeLabel 说明"这些数字算的是哪一片"，避免部长把本部人数当成全校人数。
func scopeLabel(u model.User) string {
	switch u.Role {
	case model.RoleDormManager:
		if u.Floor != "" && u.Building != "" {
			return u.Building + " " + u.Floor
		}
		if u.Building != "" {
			return u.Building
		}
		return "未分配楼栋"
	case model.RoleMinister:
		if u.Department != "" {
			return u.Department
		}
		return "未分配部门"
	case model.RoleMember:
		if u.Department != "" {
			return u.Department
		}
		return "未分配部门"
	default:
		return "全校"
	}
}

// weekBounds 复用评优那套 ISO 周口径，避免总览与标兵榜算出两个"本周"。
func weekBounds() (time.Time, time.Time) {
	start, end, err := weekRange(model.WeekKeyOf(time.Now()))
	if err != nil {
		now := time.Now()
		monday := now.AddDate(0, 0, -int(now.Weekday())+7)
		if now.Weekday() != time.Sunday {
			monday = now.AddDate(0, 0, -int(now.Weekday())+1)
		}
		start = monday
		end = monday.AddDate(0, 0, 7)
	}
	return start, end
}

func countWhere(modelPtr interface{}, query string, args ...interface{}) int {
	var n int64
	repository.DB.Model(modelPtr).Where(query, args...).Count(&n)
	return int(n)
}

func sumDeductPoints(query string, args ...interface{}) int {
	var total int
	repository.DB.Model(&model.DeductionRecord{}).
		Where("status <> ?", "revoked").
		Where(query, args...).
		Select("COALESCE(SUM(deduct_points), 0)").Scan(&total)
	return total
}

// dormSummary 宿管：只看自己上报过的记录，不含任何全校违纪数据。
func dormSummary(u model.User) ([]DashboardCard, []DashboardNotice) {
	cards := []DashboardCard{}
	notices := []DashboardNotice{}
	weekStart, weekEnd := weekBounds()

	cards = append(cards, DashboardCard{Key: "scope", Label: "负责范围", Text: scopeLabel(u), Hint: "登录账号上配置的楼栋与楼层"})
	cards = append(cards, DashboardCard{
		Key: "today", Label: "今天已上报", Value: countWhere(&model.InspectionPhoto{},
			"dorm_manager_id = ? AND created_at >= ? AND created_at < ?", u.ID, dayStart(), dayStart().AddDate(0, 0, 1)),
		Unit: "条", Tab: "dorm",
	})
	cards = append(cards, DashboardCard{
		Key: "week", Label: "本周已上报", Value: countWhere(&model.InspectionPhoto{},
			"dorm_manager_id = ? AND created_at >= ? AND created_at < ?", u.ID, weekStart, weekEnd),
		Unit: "条", Tab: "dorm",
	})
	cards = append(cards, DashboardCard{
		Key: "pending", Label: "还没变成扣分的上报", Value: countWhere(&model.InspectionPhoto{},
			"dorm_manager_id = ? AND status IN ?", u.ID, []string{"uploaded", "ai_analyzed"}),
		// 不给 Tab：宿管的 Casbin 没有 /deductions，跳过去只会被拦；
		// 这里刻意只做统计展示，转扣分由组织部副部长在打表区完成。
		Unit: "条", Hint: "组织部副部长会转成扣分，这里只统计不处理",
	})

	if today := cards[1].Value; today == 0 {
		notices = append(notices, DashboardNotice{Level: "info", Text: "今天还没有提交查寝记录。", Tab: "dorm"})
	}
	return cards, notices
}

// memberSummary 部员：个人积分与请假进度；有打表权的人才给打表数。
func memberSummary(u model.User) ([]DashboardCard, []DashboardNotice) {
	cards := []DashboardCard{}
	notices := []DashboardNotice{}

	cards = append(cards, DashboardCard{Key: "me", Label: "我是", Text: u.RealName, Hint: scopeLabel(u) + " · " + u.Position})
	cards = append(cards, DashboardCard{Key: "score", Label: "当前积分", Value: u.TotalScore, Unit: "分", Tab: "member"})
	cards = append(cards, DashboardCard{
		Key: "duty", Label: "累计上工", Unit: "次",
		Value: countWhere(&model.MemberScoreLog{}, "member_id = ? AND change_type = ? AND score_change > 0", u.ID, "attendance_ok"),
		Hint:  "按值班核销的加分流水统计", Tab: "member",
	})
	cards = append(cards, DashboardCard{
		Key: "missed", Label: "累计缺勤", Unit: "次",
		Value: countWhere(&model.MemberScoreLog{}, "member_id = ? AND change_type = ?", u.ID, "penalty"),
		Tab:   "member",
	})
	cards = append(cards, DashboardCard{
		Key: "leave", Label: "我的请假待审批", Unit: "条",
		Value: countWhere(&model.LeaveRequest{}, "member_id = ? AND status = ?", u.ID, "pending"),
		Tab:   "leave",
	})
	cards = append(cards, DashboardCard{
		Key: "reward", Label: "我的奖品待交付", Unit: "件",
		Value: countWhere(&model.RewardOrder{}, "member_id = ? AND status = ?", u.ID, "pending"),
		Tab:   "welfare",
	})

	if model.HasDeductionAuthority(u) {
		weekStart, weekEnd := weekBounds()
		cards = append(cards, DashboardCard{
			Key: "deduct", Label: "本周我打表", Unit: "条",
			Value: countWhere(&model.DeductionRecord{}, "inspector_id = ? AND created_at >= ? AND created_at < ? AND status <> ?",
				u.ID, weekStart, weekEnd, "revoked"),
			Hint: "你已被任命为副部长，有打表权限", Tab: "deductions",
		})
		notices = append(notices, DashboardNotice{Level: "info", Text: "打表入口在「违纪扣分台账与快速打表」页。", Tab: "deductions"})
	}
	return cards, notices
}

// ministerSummary 部长：待办数 + 本周全局口径。
// 待审批请假沿用 GET /minister/leaves 的现有口径 —— 它本来就不按部门过滤，
// 卡片与点进去的列表必须一致，所以这里同样给全校数，并在 hint 里说明。
func ministerSummary(u model.User) ([]DashboardCard, []DashboardNotice) {
	cards := []DashboardCard{}
	notices := []DashboardNotice{}
	weekStart, weekEnd := weekBounds()
	pendingLeaves := countWhere(&model.LeaveRequest{}, "status = ?", "pending")

	cards = append(cards, DashboardCard{
		Key: "leave", Label: "待审批请假", Unit: "条",
		Value: pendingLeaves,
		Hint:  "全校范围，与请假审批列表一致",
		Tab:   "minister", Sub: "overview",
	})
	cards = append(cards, DashboardCard{
		Key: "recruit", Label: "招新报名待处理", Unit: "人",
		Value: countWhere(&model.RecruitmentApplication{}, "status = ?", "submitted"),
		Tab:   "minister", Sub: "recruit",
	})

	var members []model.User
	repository.DB.Where("role = ? AND status = ?", model.RoleMember, "active").Find(&members)
	deptCount := 0
	for _, m := range members {
		if u.Department != "" && sameDepartment(m.Department, u.Department) {
			deptCount++
		}
	}
	cards = append(cards, DashboardCard{
		Key: "members", Label: "本部门部员", Unit: "人", Value: deptCount,
		Hint: "全校 " + strconv.Itoa(len(members)) + " 人 · 部门名允许复合写法，按互相包含判定", Tab: "minister", Sub: "members-mgr",
	})

	cards = append(cards, DashboardCard{
		Key: "shifts", Label: "本周排班班次", Unit: "班",
		Value: countWhere(&model.ScheduleShift{}, "date >= ? AND date < ?", weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02")),
		Tab:   "minister", Sub: "overview",
	})
	cards = append(cards, DashboardCard{
		Key: "missed", Label: "本周已判定旷工", Unit: "人次",
		Value: countWhere(&model.MemberScoreLog{}, "change_type = ? AND created_at >= ? AND created_at < ?", "penalty", weekStart, weekEnd),
		Hint:  "由值班核销扫描写入", Tab: "minister", Sub: "overview",
	})
	rewardHint := "核销入口在积分商城"
	rewardQuery := "status = ?"
	rewardArgs := []interface{}{"pending"}
	if u.Department != "" {
		rewardQuery += " AND department LIKE ?"
		rewardArgs = append(rewardArgs, "%"+u.Department+"%")
		rewardHint = "只算 " + u.Department + "，核销入口在积分商城"
	} else {
		rewardHint = "本账号未配部门，这里统计全校待交付；核销入口在积分商城"
	}
	cards = append(cards, DashboardCard{
		Key: "reward", Label: "奖品待交付", Unit: "件",
		Value: countWhere(&model.RewardOrder{}, rewardQuery, rewardArgs...),
		Hint:  rewardHint, Tab: "welfare",
	})
	cards = append(cards, DashboardCard{
		Key: "deduct_week", Label: "本周违纪扣分", Unit: "条",
		Value: countWhere(&model.DeductionRecord{}, "created_at >= ? AND created_at < ? AND status <> ?", weekStart, weekEnd, "revoked"),
		Tab:   "deductions",
	})
	cards = append(cards, DashboardCard{
		Key: "deduct_sum", Label: "本周累计扣分", Unit: "分",
		Value: sumDeductPoints("created_at >= ? AND created_at < ?", weekStart, weekEnd),
		Tab:   "deductions",
	})

	if pendingLeaves > 0 {
		notices = append(notices, DashboardNotice{Level: "warn", Text: "有 " + strconv.Itoa(pendingLeaves) + " 条请假等着审批。", Tab: "minister", Sub: "overview"})
	}
	return cards, notices
}

// techSummary 技术维护组：系统层面的量与配置状态。
func techSummary(u model.User) ([]DashboardCard, []DashboardNotice) {
	cards := []DashboardCard{}
	notices := []DashboardNotice{}

	cards = append(cards, DashboardCard{Key: "users", Label: "系统账号", Unit: "个", Value: countWhere(&model.User{}, "1 = 1"), Tab: "tech", Sub: "db"})
	cards = append(cards, DashboardCard{Key: "students", Label: "学生名册", Unit: "人", Value: countWhere(&model.Student{}, "status = ?", "active"), Tab: "students"})
	cards = append(cards, DashboardCard{Key: "photos", Label: "宿管上报", Unit: "条", Value: countWhere(&model.InspectionPhoto{}, "1 = 1"), Tab: "deductions"})
	cards = append(cards, DashboardCard{Key: "deducts", Label: "打表记录", Unit: "条", Value: countWhere(&model.DeductionRecord{}, "status <> ?", "revoked"), Tab: "deductions"})
	cards = append(cards, DashboardCard{Key: "shifts", Label: "排班班次", Unit: "条", Value: countWhere(&model.ScheduleShift{}, "1 = 1"), Tab: "minister"})
	cards = append(cards, DashboardCard{Key: "papers", Label: "素养测评卷", Unit: "份", Value: countWhere(&model.ExamPaper{}, "1 = 1"), Tab: "exam"})

	var aiTotal int64
	repository.DB.Model(&model.AIConfig{}).Count(&aiTotal)
	aiReady := countWhere(&model.AIConfig{}, "is_enabled = ? AND endpoint <> ? AND api_key <> ?", true, "", "")
	cards = append(cards, DashboardCard{
		Key: "ai", Label: "AI 引擎已配置", Unit: "个", Value: aiReady,
		Hint: "共 " + strconv.Itoa(int(aiTotal)) + " 个引擎位，两个都配好后上报才带出扣分建议", Tab: "tech", Sub: "ai",
	})

	if aiReady < 2 {
		notices = append(notices, DashboardNotice{Level: "warn", Text: "AI 引擎还没配全，宿管上报会显示为不可用而不是伪造结论。", Tab: "tech", Sub: "ai"})
	}
	if countWhere(&model.Student{}, "1 = 1") == 0 {
		notices = append(notices, DashboardNotice{Level: "info", Text: "学生名册是空的，导入后打表才能选到具体的人。", Tab: "students"})
	}
	return cards, notices
}

// viewerSummary 信息查看下载管理：只给可读的档案量，并指到导出页。
func viewerSummary(u model.User) ([]DashboardCard, []DashboardNotice) {
	cards := []DashboardCard{}
	weekStart, weekEnd := weekBounds()

	cards = append(cards, DashboardCard{Key: "deducts", Label: "打表记录", Unit: "条", Value: countWhere(&model.DeductionRecord{}, "status <> ?", "revoked"), Tab: "deductions"})
	cards = append(cards, DashboardCard{Key: "deduct_sum", Label: "累计扣分", Unit: "分", Value: sumDeductPoints("1 = 1"), Tab: "deductions"})
	cards = append(cards, DashboardCard{
		Key: "deduct_week", Label: "本周扣分", Unit: "条",
		Value: countWhere(&model.DeductionRecord{}, "created_at >= ? AND created_at < ? AND status <> ?", weekStart, weekEnd, "revoked"),
		Tab:   "deductions",
	})
	cards = append(cards, DashboardCard{Key: "reports", Label: "宿管上报", Unit: "条", Value: countWhere(&model.InspectionPhoto{}, "1 = 1"), Tab: "export"})
	cards = append(cards, DashboardCard{Key: "students", Label: "学生名册", Unit: "人", Value: countWhere(&model.Student{}, "status = ?", "active"), Tab: "students"})

	return cards, []DashboardNotice{{Level: "info", Text: "这个岗位只读，导出与打水印下载在「档案导出」页。", Tab: "export"}}
}

func dayStart() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}
