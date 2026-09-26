package controller

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// HonorController 每周标兵评定与公示。
//
// 文档 5.2 把标兵写成"每周评定并公示"，而原实现是请求时实时算：今天公示的榜首，
// 明天可能被一笔调分或一次核销追平，公示内容既无留存也无从复核。
// 这里把"评定"落成有留痕的一次动作——评定一次写一行快照，公示读快照而不是现算。
type HonorController struct{}

const (
	honorRankTopScore = "top_score"
	honorRankBestDuty = "best_duty"
	honorScopeSchool  = "全校"
)

// honorCandidate 一名部员在本期评定口径下的数据
type honorCandidate struct {
	user   model.User
	duty   int64
	missed int64
}

// requireHonorEditor 评定动作只有部长（限本部）与技术维护组可发起。
// 技术部副部长虽有打表权，但打的是违纪扣分，不在此列。
func requireHonorEditor(c *gin.Context) (model.User, bool) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return operator, false
	}
	if operator.Status == "disabled" {
		c.JSON(http.StatusForbidden, gin.H{"error": "该账号已被停用"})
		return operator, false
	}
	if operator.Role != model.RoleMinister && operator.Role != model.RoleTechAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "每周标兵评定仅限部长与技术维护组发起"})
		return operator, false
	}
	return operator, true
}

// honorScopeFor 解析评定范围：技术维护组可指定部门或全校，部长恒为本部。
// 部长的 department 请求参数一律忽略，避免跨部门造榜。
func honorScopeFor(operator model.User, requested string) (string, bool) {
	dept := strings.TrimSpace(requested)
	if operator.Role == model.RoleTechAdmin {
		if dept == "" || dept == "全部部门" {
			return honorScopeSchool, true
		}
		return dept, true
	}
	if strings.TrimSpace(operator.Department) == "" {
		return "", false
	}
	return operator.Department, true
}

// honorReadScope 公示读取范围：技术维护组可指定部门，其余角色固定看本部门（无部门则看全校）。
func honorReadScope(operator model.User, requested string) string {
	if operator.Role == model.RoleTechAdmin {
		dept := strings.TrimSpace(requested)
		if dept == "" || dept == "全部部门" {
			return honorScopeSchool
		}
		return dept
	}
	if strings.TrimSpace(operator.Department) == "" {
		return honorScopeSchool
	}
	return operator.Department
}

// weekRange 把 "2026-W39" 解析成 [周一 00:00, 下周一 00:00)。
// 与 model.WeekKeyOf 同一套 ISO 周口径，判定与展示不会各算各的。
func weekRange(weekKey string) (time.Time, time.Time, error) {
	var year, week int
	if _, err := fmt.Sscanf(strings.TrimSpace(weekKey), "%d-W%d", &year, &week); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("周标识应形如 2026-W39")
	}
	if year < 2000 || year > 2100 || week < 1 || week > 53 {
		return time.Time{}, time.Time{}, fmt.Errorf("周标识超出可用范围")
	}
	// 1 月 4 日必定落在 ISO 第 1 周，由它回推该周周一，再按周数平移
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.Local)
	isoDay := int(jan4.Weekday())
	if isoDay == 0 {
		isoDay = 7
	}
	start := jan4.AddDate(0, 0, -(isoDay-1)-(1-week)*7)
	end := start.AddDate(0, 0, 7)
	gotYear, gotWeek := start.ISOWeek()
	if gotYear != year || gotWeek != week {
		return time.Time{}, time.Time{}, fmt.Errorf("%s 不是有效的 ISO 周", weekKey)
	}
	return start, end, nil
}

func honorBadgeFor(score int) string {
	switch {
	case score >= 110:
		return "标兵先锋"
	case score >= 105:
		return "优秀部员"
	default:
		return "在册履职"
	}
}

// EvaluateWeekly 评定某周标兵并落快照。同一周重复评定会覆盖该周结果，不会产生第二份。
func (hc *HonorController) EvaluateWeekly(c *gin.Context) {
	operator, ok := requireHonorEditor(c)
	if !ok {
		return
	}
	scope, ok := honorScopeFor(operator, c.Query("department"))
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "您的账号未绑定部门，无法评定本部标兵，请联系技术维护组"})
		return
	}

	weekKey := strings.TrimSpace(c.Query("week"))
	if weekKey == "" {
		weekKey = model.WeekKeyOf(time.Now())
	}
	start, end, err := weekRange(weekKey)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if start.After(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "还不能评定尚未开始的周"})
		return
	}

	var members []model.User
	if err := repository.DB.Where("role = ? AND status = ?", model.RoleMember, "active").
		Order("id asc").Find(&members).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "部员名册读取失败: " + err.Error()})
		return
	}

	// 范围过滤放在内存里用 sameDepartment 做，避免 LIKE 把"技术组"串命中到别的项目
	dayStart, dayEnd := start.Format("2006-01-02"), end.Format("2006-01-02")
	pool := make([]honorCandidate, 0, len(members))
	for _, m := range members {
		if scope != honorScopeSchool && !sameDepartment(scope, m.Department) {
			continue
		}
		var duty, missed int64
		if err := repository.DB.Model(&model.ScheduleShift{}).
			Where("member_names LIKE ? AND status = ? AND date >= ? AND date < ?",
				"%"+m.RealName+"%", "completed", dayStart, dayEnd).
			Count(&duty).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "上岗班次统计失败: " + err.Error()})
			return
		}
		if err := repository.DB.Model(&model.MemberScoreLog{}).
			Where("member_id = ? AND change_type IN ? AND created_at >= ? AND created_at < ?",
				m.ID, []string{"late", "penalty", "missed"}, start, end).
			Count(&missed).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "缺工记录统计失败: " + err.Error()})
			return
		}
		pool = append(pool, honorCandidate{user: m, duty: duty, missed: missed})
	}
	if len(pool) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("【%s】范围内没有在册履职的部员，未生成快照", scope)})
		return
	}

	ranked := append([]honorCandidate(nil), pool...)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.user.TotalScore != b.user.TotalScore {
			return a.user.TotalScore > b.user.TotalScore
		}
		if a.missed != b.missed {
			return a.missed < b.missed
		}
		if a.duty != b.duty {
			return a.duty > b.duty
		}
		return a.user.ID < b.user.ID
	})
	topScore := ranked[0]
	topTied := tiedNames(ranked, func(x honorCandidate) int { return x.user.TotalScore }, topScore)

	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.duty != b.duty {
			return a.duty > b.duty
		}
		if a.missed != b.missed {
			return a.missed < b.missed
		}
		if a.user.TotalScore != b.user.TotalScore {
			return a.user.TotalScore > b.user.TotalScore
		}
		return a.user.ID < b.user.ID
	})
	bestDuty := ranked[0]
	bestTied := tiedNames(ranked, func(x honorCandidate) int { return int(x.duty) }, bestDuty)

	now := time.Now()
	rows := []model.WeeklyHonorSnapshot{
		{
			WeekKey: weekKey, RankType: honorRankTopScore, Scope: scope,
			MemberID: topScore.user.ID, MemberName: topScore.user.RealName,
			Department: topScore.user.Department, TotalScore: topScore.user.TotalScore,
			DutyCount: int(topScore.duty), MissedCount: int(topScore.missed),
			Badge: honorBadgeFor(topScore.user.TotalScore),
			Note: fmt.Sprintf("%s本周积分最高（上岗 %d 班次、缺工 %d 次）%s",
				scope, topScore.duty, topScore.missed, topTied),
			EvaluatedBy: operator.RealName, EvaluatedAt: now,
		},
		{
			WeekKey: weekKey, RankType: honorRankBestDuty, Scope: scope,
			MemberID: bestDuty.user.ID, MemberName: bestDuty.user.RealName,
			Department: bestDuty.user.Department, TotalScore: bestDuty.user.TotalScore,
			DutyCount: int(bestDuty.duty), MissedCount: int(bestDuty.missed),
			Badge: honorBadgeFor(bestDuty.user.TotalScore),
			Note: fmt.Sprintf("%s本周上岗最多（%d 班次、缺工 %d 次）%s",
				scope, bestDuty.duty, bestDuty.missed, bestTied),
			EvaluatedBy: operator.RealName, EvaluatedAt: now,
		},
	}

	err = repository.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("week_key = ? AND scope = ?", weekKey, scope).
			Delete(&model.WeeklyHonorSnapshot{}).Error; err != nil {
			return err
		}
		return tx.Create(&rows).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "标兵快照写入失败: " + err.Error()})
		return
	}

	logOperationAs(c, operator, "honor.evaluate", "weekly_honor", 0,
		fmt.Sprintf("评定 %s %s 标兵：最高积分【%s】、最优上工【%s】", weekKey, scope, topScore.user.RealName, bestDuty.user.RealName))

	c.JSON(http.StatusOK, gin.H{
		"message": fmt.Sprintf("%s %s 标兵已评定并公示快照。", weekKey, scope),
		"week":    weekKey,
		"scope":   scope,
		"items":   rows,
	})
}

// tiedNames 说明榜首是否存在并列，以及按什么规则取舍——公示口径必须可解释。
func tiedNames(pool []honorCandidate, key func(honorCandidate) int, winner honorCandidate) string {
	var names []string
	for _, p := range pool {
		if p.user.ID != winner.user.ID && key(p) == key(winner) {
			names = append(names, p.user.RealName)
		}
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) > 3 {
		names = names[:3]
	}
	return fmt.Sprintf("；与 %s 同值，按缺工更少、上岗更多、账号更早优先", strings.Join(names, "、"))
}

// loadHonorWeeks 按范围取最近若干期快照，按周倒序分组。
func loadHonorWeeks(scope string, limit int) ([]gin.H, error) {
	var rows []model.WeeklyHonorSnapshot
	if err := repository.DB.Where("scope = ?", scope).
		Order("week_key desc, rank_type asc").Limit(limit * 4).Find(&rows).Error; err != nil {
		return nil, err
	}
	grouped := make([]gin.H, 0, limit)
	seen := make(map[string]bool, limit)
	var current []model.WeeklyHonorSnapshot
	var currentWeek string
	flush := func() {
		if len(current) == 0 || seen[currentWeek] || len(grouped) >= limit {
			return
		}
		seen[currentWeek] = true
		grouped = append(grouped, gin.H{"week": currentWeek, "items": current})
	}
	for _, r := range rows {
		if r.WeekKey != currentWeek {
			flush()
			current = nil
			currentWeek = r.WeekKey
		}
		current = append(current, r)
	}
	flush()
	return grouped, nil
}

// CurrentWeekly 公示读取：只读快照。未评定的周明确回"尚未评定"，
// 绝不回落到实时计算冒充公示结果——那正是本次要修掉的问题。
func (hc *HonorController) CurrentWeekly(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	scope := honorReadScope(operator, c.Query("department"))

	weekKey := strings.TrimSpace(c.Query("week"))
	if weekKey != "" {
		if _, _, err := weekRange(weekKey); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	var rows []model.WeeklyHonorSnapshot
	var err error
	if weekKey == "" {
		var latest model.WeeklyHonorSnapshot
		if e := repository.DB.Where("scope = ?", scope).Order("week_key desc").First(&latest).Error; e == gorm.ErrRecordNotFound {
			c.JSON(http.StatusOK, gin.H{
				"evaluated": false, "scope": scope, "week": "",
				"current_week": model.WeekKeyOf(time.Now()),
				"items":        []model.WeeklyHonorSnapshot{},
				"note":         "本期范围内还没有评定过标兵，快照为空即如实为空。",
			})
			return
		} else if e != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "公示读取失败: " + e.Error()})
			return
		}
		weekKey = latest.WeekKey
	}
	if err = repository.DB.Where("scope = ? AND week_key = ?", scope, weekKey).
		Order("rank_type asc").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "公示读取失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"evaluated":    len(rows) > 0,
		"scope":        scope,
		"week":         weekKey,
		"current_week": model.WeekKeyOf(time.Now()),
		"items":        rows,
		"stale":        weekKey != model.WeekKeyOf(time.Now()) && len(rows) > 0,
	})
}

// ListHistory 公示留痕：按周倒序列出最近若干期快照，供部长与技术维护组回溯。
func (hc *HonorController) ListHistory(c *gin.Context) {
	operator, ok := operatorFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "账号信息已失效，请重新登录"})
		return
	}
	scope := honorReadScope(operator, c.Query("department"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "8"))
	if limit < 1 || limit > 30 {
		limit = 8
	}
	weeks, err := loadHonorWeeks(scope, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "公示留痕读取失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"scope": scope, "total": len(weeks), "weeks": weeks})
}
