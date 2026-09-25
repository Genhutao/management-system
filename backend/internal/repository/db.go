package repository

import (
	"encoding/json"
	"log"
	"time"

	"golang.org/x/crypto/bcrypt"
	"github.com/glebarez/sqlite" // 临时：本机无 C 编译器，跑通后还原为 gorm.io/driver/sqlite
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"xgh-system/internal/model"
)

var DB *gorm.DB

// InitDB 初始化 SQLite 并自动迁移表结构
func InitDB(dbPath string) (*gorm.DB, error) {
	if dbPath == "" {
		dbPath = "xgh_system.db"
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}

		// 自动执行表结构迁移
		err = db.AutoMigrate(
			&model.User{},
			&model.DormRosterPreset{},
			&model.SchedulePlan{},
			&model.ScheduleShift{},
			&model.MemberScoreLog{},
			&model.LeaveRequest{},
			&model.InspectionPhoto{},
			&model.ExamPaper{},
			&model.Question{},
			&model.ExamSubmission{},
			&model.RecruitmentApplication{},
			&model.AIConfig{},
				&model.Student{},
				&model.DeductionRecord{},
				&model.InspectionSubject{},
				&model.OperationLog{},
				&model.DormTaskSlotConfig{},
				&model.BroadcastNewsItem{},
				&model.BroadcastPushConfig{},
				&model.PublicityAsset{},
				&model.TechWelfareGateway{},
				&model.WelfareUsageQuota{},
				&model.WelfareModelPricing{},
				&model.MemberModelQuota{},
			)
	if err != nil {
		return nil, err
	}

	DB = db
	seedInitialData(db)
	return db, nil
}

// seedInitialData 仅保留系统必须的 5 大身份基础测试账号，删除全部虚假记录
func seedInitialData(db *gorm.DB) {
	hashPassword := func(pwd string) string {
		h, _ := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
		return string(h)
	}

	defaultPassword := hashPassword("123456")

	// 1. 仅保留五大身份的基础测试账号（密码均为 123456）
	users := []model.User{
		{
			Username:     "tech_admin",
			PasswordHash: defaultPassword,
			RealName:     "测试员-技术组",
			Phone:        "13800000001",
			Role:         model.RoleTechAdmin,
			Department:   "组织部 · 技术组",
			Status:       "active",
		},
		{
			Username:     "minister_zhang",
			PasswordHash: defaultPassword,
			RealName:     "测试员-部长",
			Phone:        "13800000002",
			Role:         model.RoleMinister,
			Department:   "纪检部",
			Status:       "active",
		},
		{
			Username:     "member_li",
			PasswordHash: defaultPassword,
			RealName:     "测试员-纪检部员",
			Phone:        "13800000003",
			Role:         model.RoleMember,
			Department:   "纪检部",
			Building:     "1号楼",
			Floor:        "1-3F",
			TotalScore:   105,
			Status:       "active",
		},
		{
			Username:     "member_ducha",
			PasswordHash: defaultPassword,
			RealName:     "测试员-督查部员",
			Phone:        "13800000007",
			Role:         model.RoleMember,
			Department:   "组织部 · 督查组",
			Building:     "2号楼",
			Floor:        "1-4F",
			TotalScore:   108,
			Status:       "active",
		},
		{
			Username:     "member_boyin",
			PasswordHash: defaultPassword,
			RealName:     "测试员-播音部员",
			Phone:        "13800000008",
			Role:         model.RoleMember,
			Department:   "宣传部 · 播音组",
			Building:     "3号楼",
			Floor:        "全楼",
			TotalScore:   112,
			Status:       "active",
		},
		{
			Username:     "export_admin",
			PasswordHash: defaultPassword,
			RealName:     "测试员-档案导出",
			Phone:        "13800000005",
			Role:         model.RoleViewerExport,
			Department:   "综合信息档案处",
			Status:       "active",
		},
		{
			Username:     "dorm_ay_liu",
			PasswordHash: defaultPassword,
			RealName:     "测试宿管",
			Phone:        "13800008888",
			Role:         model.RoleDormManager,
			Building:     "1号楼",
			Floor:        "全楼",
			Status:       "active",
		},
	}

	for _, u := range users {
		var existing model.User
		if err := db.Where("username = ?", u.Username).First(&existing).Error; err != nil {
			db.Create(&u)
		} else {
			existing.Department = u.Department
			existing.RealName = u.RealName
			existing.TotalScore = u.TotalScore
			db.Save(&existing)
		}
	}

	// 2. 宿管花名册预置名单（仅保留 1 条测试数据，供手机端三要素免密直登测试）
	var rCount int64
	db.Model(&model.DormRosterPreset{}).Count(&rCount)
	if rCount == 0 {
		// 绑定 ID 必须按用户名实测查出：硬编码序号会随种子账号增减指向错误的人，
		// 导致三要素登录直接登成别人的账号。
		var dormUser model.User
		boundID := uint(0)
		if err := db.Where("username = ?", "dorm_ay_liu").First(&dormUser).Error; err == nil {
			boundID = dormUser.ID
		}

		rosterPresets := []model.DormRosterPreset{
			{
				RealName:    "测试宿管",
				Phone:       "13800008888",
				Building:    "1号楼",
				Floor:       "全楼",
				IsActivated: true,
				BoundUserID: boundID,
			},
		}
		db.Create(&rosterPresets)
	}

	// 3. AI 调度中枢配置模板（供技术维护组在线配置外部模型与调试）
	aiConfigs := []model.AIConfig{
		{
			ConfigKey:    "vision_engine",
			DisplayName:  "多模态图像识别与信息翻译引擎",
			Provider:     "openai_compatible",
			Endpoint:     "https://api.openai.com/v1/chat/completions",
			APIKey:       "",
			ModelName:    "gpt-4o-mini",
			SystemPrompt: "你是一个专业的学生宿舍安全隐患与卫生巡查多模态识别专家。请识别并翻译宿管上传的图片中的：1. 现场场景与位置；2. 是否存在违规电器、私拉乱接电线、抽烟等违规行为；3. 宿舍卫生状态；4. 检测是否有佩戴袖标或工牌的部员。用客观专业的中文详细描述。",
			Temperature:  0.3,
			MaxTokens:    1024,
			IsEnabled:    true,
		},
		{
			ConfigKey:    "text_engine",
			DisplayName:  "文本信息规范化与结构化归纳入库引擎",
			Provider:     "openai_compatible",
			Endpoint:     "https://api.openai.com/v1/chat/completions",
			APIKey:       "",
			ModelName:    "gpt-4o-mini",
			SystemPrompt: "你负责将多模态初筛后的巡查描述提炼并结构化归类，必须只输出合法 JSON 格式：{\"category\":\"...\",\"severity\":\"low/medium/high/critical\",\"deduct_points\":0,\"summary\":\"...\",\"action_advice\":\"...\"}",
			Temperature:  0.2,
			MaxTokens:    512,
			IsEnabled:    true,
		},
	}
	db.Create(&aiConfigs)

	// 4. 标准答题框架试卷与试题结构（系统题库基础模板）
	opts1, _ := json.Marshal([]string{"A. 拍照上传系统报备并联系宿管安全处置", "B. 装作没看见", "C. 当场发生激烈争吵", "D. 私自借用"})
	opts2, _ := json.Marshal([]string{"A. 主动出示学管会工作证与红袖标", "B. 进门先敲门并文明问候", "C. 擅自翻动私人储物柜", "D. 遇突发安全隐患第一时间联系当班宿管"})
	opts3, _ := json.Marshal([]string{"A. 提前在系统发起请假申报并协调替班部员", "B. 微信群口头告知后直接不到岗", "C. 事后找借口补假", "D. 擅自无故缺席"})

	paper := model.ExamPaper{
		Title:           "2026年学管会秋季招新笔试暨纪检素养综合测评卷",
		Description:     "考察应聘同学与新晋部员对宿舍管理规范、突发事件处理及纪律条例的理解。",
		Scope:           "recruit",
		DurationMinutes: 30,
		PassingScore:    60,
		TotalScore:      100,
		IsPublished:     true,
			Questions: []model.Question{
				{
					Type:          "single",
					QuestionText:  "查寝过程中发现宿舍内疑似违规使用大功率违章电器，正确的规范处置流程是？",
					OptionsJSON:   string(opts1),
					CorrectAnswer: "A",
					Score:         30,
					SortOrder:     1,
				},
				{
					Type:          "multi",
					QuestionText:  "【多选】在开展晚查寝工作时，以下哪些属于部员规范文明巡查举止？",
					OptionsJSON:   string(opts2),
					CorrectAnswer: "A,B,D",
					Score:         35,
					SortOrder:     2,
				},
				{
					Type:          "single",
					QuestionText:  "若当晚遇到实验或突发课程冲突无法参与排班查寝，正确的请假流程是？",
					OptionsJSON:   string(opts3),
					CorrectAnswer: "A",
					Score:         35,
					SortOrder:     3,
				},
			},
		}
		db.Create(&paper)

	// 5. 宿管时段性资料提交与工作规范配置（多兼容时段模板，支持后台自由修改与拓展）
	var slotCount int64
	db.Model(&model.DormTaskSlotConfig{}).Count(&slotCount)
	if slotCount == 0 {
		defaultSlots := []model.DormTaskSlotConfig{
			{
				SlotName:          "早间离寝通风与断电检查",
				StartTime:         "06:30",
				EndTime:           "08:30",
				PeriodType:        "daily",
				RequiredMaterials: "各楼层走廊通风留痕照、宿舍空室门窗断电巡检单、早出勤部员工牌佩戴核查",
				ActionPrompt:      "检查宿舍走廊与公共照明断电，拍照上传留痕",
				TargetPhotoType:   "sanitation",
				UrgencyLevel:      "normal",
				IsEnabled:         true,
				SortOrder:         1,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			},
			{
				SlotName:          "午间违规用电排查与卫生评定",
				StartTime:         "11:40",
				EndTime:           "13:50",
				PeriodType:        "daily",
				RequiredMaterials: "高功率电煮锅/电热毯抽查照片、各宿舍内务卫生星级评定表、午检部员到岗照片",
				ActionPrompt:      "重点排查高层违规发热电器，使用多模态 AI 拍照识别存证",
				TargetPhotoType:   "violation",
				UrgencyLevel:      "high",
				IsEnabled:         true,
				SortOrder:         2,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			},
			{
				SlotName:          "晚查寝熄灯与安全隐患排查",
				StartTime:         "19:00",
				EndTime:           "22:30",
				PeriodType:        "daily",
				RequiredMaterials: "晚归/未归学生核对表、私拉乱接电线整改留痕、违规吸烟明火排查报告、当值部员红袖标佩戴监督照",
				ActionPrompt:      "协同晚查寝部员逐层巡视，重点排查消防通道堆物与大功率电器",
				TargetPhotoType:   "duty_supervise",
				UrgencyLevel:      "critical",
				IsEnabled:         true,
				SortOrder:         3,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			},
			{
				SlotName:          "深夜大门闭锁与晚归学生登记",
				StartTime:         "22:30",
				EndTime:           "06:30",
				PeriodType:        "daily",
				RequiredMaterials: "楼栋正门安全闭锁确认、门禁晚归学生姓名班级登记册、夜间安防值班巡更日志",
				ActionPrompt:      "完成门禁反锁并拍照留存，严格登记夜归学生名单",
				TargetPhotoType:   "violation",
				UrgencyLevel:      "high",
				IsEnabled:         true,
				SortOrder:         4,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			},
			{
				SlotName:          "周末全栋专项安全大排查",
				StartTime:         "09:00",
				EndTime:           "17:30",
				PeriodType:        "weekend",
				RequiredMaterials: "消防栓灭火器压力指针巡检照片、配电间防鼠板状态、周末留宿人数核对表",
				ActionPrompt:      "对全楼公共设施与消防隐患进行地毯式排查",
				TargetPhotoType:   "sanitation",
				UrgencyLevel:      "normal",
				IsEnabled:         true,
				SortOrder:         5,
				CreatedAt:         time.Now(),
				UpdatedAt:         time.Now(),
			},
		}
			db.Create(&defaultSlots)
		}

		// 6. 播音组部员表现时段推送策略与校园新闻初始种子
		var pushCfgCount int64
		db.Model(&model.BroadcastPushConfig{}).Count(&pushCfgCount)
		if pushCfgCount == 0 {
			defaultPushCfg := model.BroadcastPushConfig{
				RuleName:      "广播站每日晚间新闻打表前/后榜自动推送",
				PushTimeStart: "17:00",
				PushTimeEnd:   "23:30", // 涵盖晚自习与广播时间
				PushMode:      "overall", // 'overall' (全员前N后N) 或 'department' (各部门各N名)
				TopCount:      3,
				BottomCount:   3,
				IncludeScores: true,
				IncludeReason: true,
				IsEnabled:     true,
				CreatedAt:     time.Now(),
				UpdatedAt:     time.Now(),
			}
			db.Create(&defaultPushCfg)
		}

		var newsCount int64
		db.Model(&model.BroadcastNewsItem{}).Count(&newsCount)
		if newsCount == 0 {
			todayStr := time.Now().Format("2006-01-02")
			sampleNews := []model.BroadcastNewsItem{
				{
					Title:       "学管会纪检部完成本周宿舍大功率违规电器清查行动",
					Content:     "本周纪检部全体干事配合各楼栋宿管老师，对全校园区开展违规电热器具地毯式排查。绝大多数宿舍能够严格遵守用电规范，现场查扣违章电热壶2起，安全意识明显提升。",
					Category:    "纪律通报",
					Keywords:    "违规电器,大功率,纪检部,用电安全,清查,宿管",
					Source:      "学管会融媒体采编部",
					PublishDate: todayStr,
					CreatedBy:   "技术维护组",
					CreatedAt:   time.Now(),
				},
				{
					Title:       "校园金秋文明寝室评比揭晓：多间模范宿舍获全五星好评",
					Content:     "经过为期两周的连续巡查与量化打表，组织部督查组联合宿管会评选出高一年级与高二年级共12间文明标兵宿舍，地面整洁、通风良好、内务规范，获通报嘉奖。",
					Category:    "寝室文化",
					Keywords:    "文明寝室,五星宿舍,内务卫生,督查组,表彰,宿舍风采",
					Source:      "宣传部融媒体中心",
					PublishDate: todayStr,
					CreatedBy:   "技术维护组",
					CreatedAt:   time.Now(),
				},
				{
					Title:       "学管会自动化排班与智能替补系统正式上线试运行",
					Content:     "技术维护组自主研发的智能排班轮换与人员替补算法上线。系统可依据干事历史班次自动平衡工作负荷，并支持跨时段拍照AI智能识别，全面赋能园区数字化治理。",
					Category:    "校园时讯",
					Keywords:    "技术组,智能排班,AI多模态,数字化,创新,系统上线",
					Source:      "组织部技术组",
					PublishDate: todayStr,
					CreatedBy:   "技术维护组",
					CreatedAt:   time.Now(),
				},
				{
					Title:       "【晨间微语】自律是通往卓越的阶梯，致每一位晨读的学子",
					Content:     "亲爱的同学们，清晨的阳光已洒满园区。整理好内务，叠好被褥，精神饱满地迎接新一天的知识洗礼。学管会播音组祝大家学业有成、心情愉悦！",
					Category:    "晨间心语",
					Keywords:    "早间微语,励志,晨读,正能量,自律,问候",
					Source:      "宣传部播音组",
					PublishDate: todayStr,
					CreatedBy:   "技术维护组",
					CreatedAt:   time.Now(),
				},
			}
			db.Create(&sampleNews)
		}

		// 8. 严格清理虚假预设模型：技术部部长或各部门部长未在前端页面主动定义前，模型定价与网关均默认为空！
		// 只有当部长亲自录入并自定义后，部员才能在积分商城看到可兑换的模型
		log.Println("[Database] Clean seed initialized: zero mock models preset, awaiting minister custom configuration.")
	}
