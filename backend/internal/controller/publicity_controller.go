package controller

import (
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type PublicityController struct{}

// -----------------------------------------------------------------------------
// 1. 播音部员新闻打表中心：关键词智能检索与推荐
// -----------------------------------------------------------------------------

// GetBroadcastNews 播音部员获取校园新闻列表，支持关键词自动筛选与类别检索
func (pc *PublicityController) GetBroadcastNews(c *gin.Context) {
	keywords := strings.TrimSpace(c.Query("keywords"))
	category := strings.TrimSpace(c.Query("category"))
	date := strings.TrimSpace(c.Query("date"))

	query := repository.DB.Model(&model.BroadcastNewsItem{}).Order("id desc")

	if category != "" {
		query = query.Where("category = ?", category)
	}
	if date != "" {
		query = query.Where("publish_date = ?", date)
	}

	// 关键词多字段联合模糊检索 (标题、正文、关键词标签)
	if keywords != "" {
		terms := strings.Split(keywords, ",")
		if len(terms) == 1 {
			terms = strings.Fields(keywords)
		}
		for _, t := range terms {
			t = strings.TrimSpace(t)
			if t != "" {
				termPattern := "%" + t + "%"
				query = query.Where("title LIKE ? OR content LIKE ? OR keywords LIKE ?", termPattern, termPattern, termPattern)
			}
		}
	}

	var list []model.BroadcastNewsItem
	query.Limit(50).Find(&list)

	c.JSON(http.StatusOK, gin.H{
		"total": len(list),
		"items": list,
	})
}

// CreateBroadcastNews 录入或生成新闻快讯稿件
func (pc *PublicityController) CreateBroadcastNews(c *gin.Context) {
	var item model.BroadcastNewsItem
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数格式错误: " + err.Error()})
		return
	}

	realName, _ := c.Get("real_name")
	item.CreatedBy = realName.(string)
	if item.PublishDate == "" {
		item.PublishDate = time.Now().Format("2006-01-02")
	}
	item.CreatedAt = time.Now()

	if err := repository.DB.Create(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "广播新闻稿件录入成功！", "item": item})
}

// ToggleBroadcastStatus 切换新闻是否已入播音单状态
func (pc *PublicityController) ToggleBroadcastStatus(c *gin.Context) {
	id := c.Param("id")
	var item model.BroadcastNewsItem
	if err := repository.DB.First(&item, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "新闻条目不存在"})
		return
	}

	item.IsBroadcast = !item.IsBroadcast
	repository.DB.Save(&item)
	c.JSON(http.StatusOK, gin.H{"message": "播音单状态已更新", "item": item})
}

// -----------------------------------------------------------------------------
// 2. 播音部员红黑榜：指定时段自动推送当前最优和最差部员 (支持全员前/后N名或各部门前/后N名)
// -----------------------------------------------------------------------------

type MemberRankItem struct {
	ID          uint   `json:"id"`
	RealName    string `json:"real_name"`
	Department  string `json:"department"`
	TotalScore  int    `json:"total_score"`
	DutyCount   int    `json:"duty_count"`
	MissedCount int    `json:"missed_count"`
	RankType    string `json:"rank_type"` // "top" (最优红榜), "bottom" (最差/加油榜)
	Reason      string `json:"reason"`
	Badge       string `json:"badge"`
}

// GetBroadcastMemberRankPush 播音打新闻专用：自动核验当前时段，动态推送最优/最差名单供播报
func (pc *PublicityController) GetBroadcastMemberRankPush(c *gin.Context) {
	now := time.Now()
	nowMinute := now.Hour()*60 + now.Minute()

	// 读取当前激活的推送策略
	var pushCfg model.BroadcastPushConfig
	err := repository.DB.Where("is_enabled = ?", true).First(&pushCfg).Error
	if err != nil {
		// 默认兜底策略
		pushCfg = model.BroadcastPushConfig{
			RuleName:      "默认广播时段推送",
			PushTimeStart: "17:00",
			PushTimeEnd:   "23:59",
			PushMode:      "overall",
			TopCount:      3,
			BottomCount:   3,
			IncludeScores: true,
			IncludeReason: true,
			IsEnabled:     true,
		}
	}

	parseHM := func(hm string) int {
		var h, m int
		fmt.Sscanf(hm, "%d:%d", &h, &m)
		return h*60 + m
	}

	startMin := parseHM(pushCfg.PushTimeStart)
	endMin := parseHM(pushCfg.PushTimeEnd)
	isPushTime := false
	if startMin <= endMin {
		isPushTime = (nowMinute >= startMin && nowMinute <= endMin)
	} else {
		isPushTime = (nowMinute >= startMin || nowMinute <= endMin)
	}

	// 汇总所有活跃部员并计算积分、出勤与缺卡
	var members []model.User
	repository.DB.Where("role = ? AND status = ?", model.RoleMember, "active").Find(&members)

	type MemberStat struct {
		User        model.User
		TotalScore  int
		DutyCount   int
		MissedCount int
	}

	var stats []MemberStat
	for _, m := range members {
		// 积分余额以 users.total_score 为准：出勤结算、代班、灵活调分每一条流水都同步过它。
		// 这里不再从流水表二次聚合——旧写法引用了不存在的 points 列，Scan 吞掉错误后
		// 所有人积分都是 0，红黑榜因此把每位部员都判进黑榜。
		var dutyCount int64
		repository.DB.Model(&model.ScheduleShift{}).Where("member_names LIKE ? AND status = ?", "%"+m.RealName+"%", "completed").Count(&dutyCount)

		var missedCount int64
		repository.DB.Model(&model.ScheduleShift{}).Where("member_names LIKE ? AND status = ?", "%"+m.RealName+"%", "missed").Count(&missedCount)

		stats = append(stats, MemberStat{
			User:        m,
			TotalScore:  m.TotalScore,
			DutyCount:   int(dutyCount),
			MissedCount: int(missedCount),
		})
	}

	// 依据策略组织排行榜：'overall' (全员前N后N) 或 'department' (各部门前N后N)
	var topRank []MemberRankItem
	var bottomRank []MemberRankItem
	deptRanks := make(map[string]gin.H)

	if pushCfg.PushMode == "department" {
		// 分部门计算
		deptMap := make(map[string][]MemberStat)
		for _, s := range stats {
			dept := s.User.Department
			if dept == "" {
				dept = "未分配"
			}
			deptMap[dept] = append(deptMap[dept], s)
		}

		for deptName, list := range deptMap {
			// 排序按积分降序
			sorted := make([]MemberStat, len(list))
			copy(sorted, list)
			for i := 0; i < len(sorted)-1; i++ {
				for j := i + 1; j < len(sorted); j++ {
					if sorted[i].TotalScore < sorted[j].TotalScore {
						sorted[i], sorted[j] = sorted[j], sorted[i]
					}
				}
			}

			var dTop []MemberRankItem
			topN := pushCfg.TopCount
			if topN > len(sorted) {
				topN = len(sorted)
			}
			for i := 0; i < topN; i++ {
				dTop = append(dTop, MemberRankItem{
					ID:          sorted[i].User.ID,
					RealName:    sorted[i].User.RealName,
					Department:  deptName,
					TotalScore:  sorted[i].TotalScore,
					DutyCount:   sorted[i].DutyCount,
					MissedCount: sorted[i].MissedCount,
					RankType:    "top",
					Reason:      fmt.Sprintf("累计履职 %d 班次，巡查扎实无违纪", sorted[i].DutyCount),
					Badge:       "部门标兵",
				})
			}

			var dBottom []MemberRankItem
			bottomN := pushCfg.BottomCount
			if bottomN > len(sorted) {
				bottomN = len(sorted)
			}
			for i := len(sorted) - 1; i >= len(sorted)-bottomN && i >= 0; i-- {
				if sorted[i].TotalScore < 100 || sorted[i].MissedCount > 0 {
					reason := "出勤积分需迎头赶上"
					if sorted[i].MissedCount > 0 {
						reason = fmt.Sprintf("有 %d 次缺勤/迟到记录，需及时补班", sorted[i].MissedCount)
					}
					dBottom = append(dBottom, MemberRankItem{
						ID:          sorted[i].User.ID,
						RealName:    sorted[i].User.RealName,
						Department:  deptName,
						TotalScore:  sorted[i].TotalScore,
						DutyCount:   sorted[i].DutyCount,
						MissedCount: sorted[i].MissedCount,
						RankType:    "bottom",
						Reason:      reason,
						Badge:       "督促加油",
					})
				}
			}

			deptRanks[deptName] = gin.H{
				"top_members":    dTop,
				"bottom_members": dBottom,
			}
		}
	} else {
		// 全员统一前N后N
		sorted := make([]MemberStat, len(stats))
		copy(sorted, stats)
		for i := 0; i < len(sorted)-1; i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[i].TotalScore < sorted[j].TotalScore {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}

		topN := pushCfg.TopCount
		if topN > len(sorted) {
			topN = len(sorted)
		}
		for i := 0; i < topN; i++ {
			topRank = append(topRank, MemberRankItem{
				ID:          sorted[i].User.ID,
				RealName:    sorted[i].User.RealName,
				Department:  sorted[i].User.Department,
				TotalScore:  sorted[i].TotalScore,
				DutyCount:   sorted[i].DutyCount,
				MissedCount: sorted[i].MissedCount,
				RankType:    "top",
				Reason:      fmt.Sprintf("全校巡检考评卓越，已出勤 %d 班次", sorted[i].DutyCount),
				Badge:       "全校标兵",
			})
		}

		bottomN := pushCfg.BottomCount
		if bottomN > len(sorted) {
			bottomN = len(sorted)
		}
		for i := len(sorted) - 1; i >= len(sorted)-bottomN && i >= 0; i-- {
			reason := "履职积分处于后列"
			if sorted[i].MissedCount > 0 {
				reason = fmt.Sprintf("存在 %d 次缺勤未到岗记录", sorted[i].MissedCount)
			}
			bottomRank = append(bottomRank, MemberRankItem{
				ID:          sorted[i].User.ID,
				RealName:    sorted[i].User.RealName,
				Department:  sorted[i].User.Department,
				TotalScore:  sorted[i].TotalScore,
				DutyCount:   sorted[i].DutyCount,
				MissedCount: sorted[i].MissedCount,
				RankType:    "bottom",
				Reason:      reason,
				Badge:       "重点督促",
			})
		}
	}

	// 自动生成一份现成通读的「广播通报稿件模板」供播音部员一键带走播报
	broadcastScript := generateBroadcastScript(topRank, bottomRank, deptRanks, pushCfg.PushMode)

	c.JSON(http.StatusOK, gin.H{
		"is_in_push_time":  isPushTime,
		"push_time_window": fmt.Sprintf("%s ~ %s", pushCfg.PushTimeStart, pushCfg.PushTimeEnd),
		"push_mode":        pushCfg.PushMode,
		"rule_name":        pushCfg.RuleName,
		"top_members":      topRank,
		"bottom_members":   bottomRank,
		"department_ranks": deptRanks,
		"broadcast_script": broadcastScript,
		"server_time":      now.Format("15:04:05"),
	})
}

// UpdateBroadcastPushConfig 修改推送策略
func (pc *PublicityController) UpdateBroadcastPushConfig(c *gin.Context) {
	var req model.BroadcastPushConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	var existing model.BroadcastPushConfig
	if err := repository.DB.First(&existing).Error; err != nil {
		req.CreatedAt = time.Now()
		req.UpdatedAt = time.Now()
		repository.DB.Create(&req)
		existing = req
	} else {
		existing.RuleName = req.RuleName
		existing.PushTimeStart = req.PushTimeStart
		existing.PushTimeEnd = req.PushTimeEnd
		existing.PushMode = req.PushMode
		existing.TopCount = req.TopCount
		existing.BottomCount = req.BottomCount
		existing.IncludeScores = req.IncludeScores
		existing.IncludeReason = req.IncludeReason
		existing.IsEnabled = req.IsEnabled
		existing.UpdatedAt = time.Now()
		repository.DB.Save(&existing)
	}

	c.JSON(http.StatusOK, gin.H{"message": "播音部员推送策略已更新！", "config": existing})
}

// -----------------------------------------------------------------------------
// 3. 宣传部随机 API 图库：支持二次元、摄影、水墨、写真、科技等多标签可选
// -----------------------------------------------------------------------------

// GetRandomPublicityImages 依据标签获取高质量随机图
func (pc *PublicityController) GetRandomPublicityImages(c *gin.Context) {
	tag := strings.TrimSpace(c.Query("tag"))
	if tag == "" {
		tag = "anime"
	}
	count := 6

	type ImageAssetResult struct {
		ID        string `json:"id"`
		Tag       string `json:"tag"`
		TagName   string `json:"tag_name"`
		Title     string `json:"title"`
		URL       string `json:"url"`
		ThumbURL  string `json:"thumb_url"`
		SourceAPI string `json:"source_api"`
		Aspect    string `json:"aspect"`
	}

	// 预设各类丰富精选高质量图源池，并支持动态随机参数
	var results []ImageAssetResult
	rand.Seed(time.Now().UnixNano())

	tagMap := map[string]string{
		"anime":       "二次元动漫 (Anime)",
		"photography": "自然风光与纪实摄影 (Photography)",
		"ink":         "国风水墨与东方古典 (Ink Wash)",
		"portrait":    "青春写真与人像艺术 (Portrait)",
		"tech":        "数字科技与极简未来 (Cyber & Tech)",
	}

	tagName := tagMap[tag]
	if tagName == "" {
		tagName = "精选壁纸"
	}

	// 针对不同标签的高清图库资源池
	animePool := []string{
		"https://images.unsplash.com/photo-1578632767115-351597cf2477?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1607604276583-eef5d076aa5f?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1563089145-599997674d42?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1534447677768-be436bb09401?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1618005182384-a83a8bd57fbe?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1569701813229-33284b643e3c?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1579783902614-a3fb3927b675?w=1200&auto=format&fit=crop",
	}

	photoPool := []string{
		"https://images.unsplash.com/photo-1506744038136-46273834b3fb?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1470071459604-3b5ec3a7fe05?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1441974231531-c6227db76b6e?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1472214103451-9374bd1c798e?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1511497584788-87676104235f?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1426604966848-d7adac402bff?w=1200&auto=format&fit=crop",
	}

	inkPool := []string{
		"https://images.unsplash.com/photo-1544717305-2782549b5136?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1518709268805-4e9042af9f23?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1509198397868-475647b2a1e5?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1579783900882-c0d3dad7b119?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1513542789411-b6a5d4f31634?w=1200&auto=format&fit=crop",
	}

	portraitPool := []string{
		"https://images.unsplash.com/photo-1534528741775-53994a69daeb?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1517841905240-472988babdf9?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1539571696357-5a69c17a67c6?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1524504388940-b1c1722653e1?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1494790108377-be9c29b29330?w=1200&auto=format&fit=crop",
	}

	techPool := []string{
		"https://images.unsplash.com/photo-1518770660439-4636190af475?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1451187580459-43490279c0fa?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1526374965328-7f61d4dc18c5?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1550751827-4bd374c3f58b?w=1200&auto=format&fit=crop",
		"https://images.unsplash.com/photo-1504384308090-c894fdcc538d?w=1200&auto=format&fit=crop",
	}

	selectedPool := animePool
	if tag == "photography" {
		selectedPool = photoPool
	} else if tag == "ink" {
		selectedPool = inkPool
	} else if tag == "portrait" {
		selectedPool = portraitPool
	} else if tag == "tech" {
		selectedPool = techPool
	}

	// 乱序抽取生成指定数量
	shuffled := make([]string, len(selectedPool))
	copy(shuffled, selectedPool)
	rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })

	for i := 0; i < count && i < len(shuffled); i++ {
		imgURL := shuffled[i]
		// 加入随机缓存破除参数保证每次刷新均有差异感
		variedURL := fmt.Sprintf("%s&sig=%d", imgURL, rand.Intn(9999))
		results = append(results, ImageAssetResult{
			ID:        fmt.Sprintf("img_%s_%d", tag, i+1),
			Tag:       tag,
			TagName:   tagName,
			Title:     fmt.Sprintf("%s 优质宣发海报素材 #%d", tagName, i+1),
			URL:       variedURL,
			ThumbURL:  strings.Replace(variedURL, "w=1200", "w=400", 1),
			SourceAPI: "Unsplash High-Res CDN + Tagged Pipeline",
			Aspect:    "16:9",
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"tag":        tag,
		"tag_name":   tagName,
		"total":      len(results),
		"items":      results,
		"timestamp":  time.Now().Unix(),
	})
}

// 辅助：生成现成播音通报文稿
func generateBroadcastScript(top []MemberRankItem, bottom []MemberRankItem, deptRanks map[string]gin.H, mode string) string {
	var sb strings.Builder
	sb.WriteString("【校园广播站 · 学管会每日工作动态通报】\n")
	sb.WriteString("亲爱的老师、同学们，大家晚上好！现在由学管会播音组为您播送今日园区履职风采榜：\n\n")

	if mode == "department" {
		sb.WriteString("各部门今日优秀履职标兵表彰：\n")
		for dName, dMap := range deptRanks {
			topList, _ := dMap["top_members"].([]MemberRankItem)
			if len(topList) > 0 {
				var names []string
				for _, m := range topList {
					names = append(names, fmt.Sprintf("%s（%d分）", m.RealName, m.TotalScore))
				}
				sb.WriteString(fmt.Sprintf("· 【%s】：%s 表彰嘉奖。\n", dName, strings.Join(names, "、")))
			}
		}
	} else {
		sb.WriteString("今日全校最佳履职红榜标兵：\n")
		for idx, m := range top {
			sb.WriteString(fmt.Sprintf("%d. %s同学（%s，总积分 %d分，出勤%d次），踏实巡查，受到全楼师生一致好评！\n", idx+1, m.RealName, m.Department, m.TotalScore, m.DutyCount))
		}

		if len(bottom) > 0 {
			sb.WriteString("\n同时温馨提醒以下同学加快步调、及时补齐查寝班次：\n")
			for idx, m := range bottom {
				sb.WriteString(fmt.Sprintf("%d. %s同学（%s，%s），请在接下来工作中积极补卡，共创文明寝室！\n", idx+1, m.RealName, m.Department, m.Reason))
			}
		}
	}

	sb.WriteString("\n感谢大家对学管会工作的大力支持，今天的播音到此结束，祝大家自习顺利，晚安！")
	return sb.String()
}
