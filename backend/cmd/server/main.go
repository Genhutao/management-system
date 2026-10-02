package main

import (
	"log"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/controller"
	"xgh-system/internal/middleware"
	"xgh-system/internal/repository"
)

func main() {
	// 1. 初始化数据库与种子数据（路径支持 DB_PATH 环境变量）
	db, err := repository.InitDB(os.Getenv("DB_PATH"))
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// 2. 初始化 GitHub 开源标准 Casbin 权限引擎
	casbinModelPath := "rbac_model.conf"
	if _, err := os.Stat(casbinModelPath); os.IsNotExist(err) {
		casbinModelPath = "backend/rbac_model.conf"
	}
	_, err = middleware.InitCasbin(db, casbinModelPath)
	if err != nil {
		log.Fatalf("Failed to initialize Casbin RBAC engine (refusing to serve without authorization): %v", err)
	}

	// 3. 创建 Gin 引擎
	r := gin.Default()
	r.Use(middleware.CORSMiddleware())
	// D-4 上传限制：限制 multipart 解析的内存占用（超出部分落临时盘），并在控制器层校验大小与类型
	r.MaxMultipartMemory = 8 << 20

	// 反向代理白名单：Gin 默认信任所有代理，此时任何人都能用一个 X-Forwarded-For
	// 自称任意 IP，而异地登录判定、会话登记与审计都拿 IP 当依据。
	// 只有明确配置了 TRUSTED_PROXIES 才采信代理头，否则一律取直连地址。
	trustedProxies := parseTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err := r.SetTrustedProxies(trustedProxies); err != nil {
		log.Fatalf("Failed to configure trusted proxies: %v", err)
	}
	if len(trustedProxies) == 0 {
		log.Printf("[Security] 未配置 TRUSTED_PROXIES：只采信直连 IP，X-Forwarded-For / X-Real-IP 一律忽略")
	} else {
		log.Printf("[Security] 已配置 %d 个可信反代地址/网段，客户端 IP 取自其转发头", len(trustedProxies))
	}

	// 静态资源与上传目录挂载
	_ = os.MkdirAll("./uploads", 0755)
	_ = os.MkdirAll("./static", 0755)
	r.Static("/uploads", "./uploads")
	r.Static("/static", "./static")
	// 首页禁缓存：保证前端改版（app.js?v=…）发布后浏览器立即拿到新版本
	r.GET("/", func(c *gin.Context) {
		c.Header("Cache-Control", "no-cache")
		c.File("./static/index.html")
	})

	// 实例化控制器
	authCtrl := &controller.AuthController{}
	dormCtrl := &controller.DormController{}
	memberCtrl := &controller.MemberController{}
	ministerCtrl := &controller.MinisterController{}
	techCtrl := &controller.TechController{}
	techDBCtrl := &controller.TechDBController{}
	exportCtrl := &controller.ExportController{}
	examCtrl := &controller.ExamController{}
	dutyCtrl := &controller.DutyController{}
	excellenceCtrl := &controller.ExcellenceController{}
	recruitCtrl := &controller.RecruitController{}
	scorePolicyCtrl := &controller.ScorePolicyController{}
	honorCtrl := &controller.HonorController{}
	templateScheduleCtrl := &controller.TemplateScheduleController{}
	studentCtrl := &controller.StudentController{}
	deductionCtrl := &controller.DeductionController{}
	publicityCtrl := &controller.PublicityController{}
	welfareCtrl := &controller.WelfareController{}
	dashboardCtrl := &controller.DashboardController{}
	accountSecurityCtrl := &controller.AccountSecurityController{}
	accountGovernanceCtrl := &controller.AccountGovernanceController{}
	messageCtrl := &controller.MessageController{}

	api := r.Group("/api/v1")
	{
		// -------------------------------------------------------------
		// 1. 公开分区 (免登录即可访问)
		// -------------------------------------------------------------
		// 招新门户
		recruit := api.Group("/recruit")
		{
			recruit.GET("/info", recruitCtrl.GetRecruitInfo)
			recruit.POST("/apply", recruitCtrl.SubmitApplication)
		}
		// 答题公开只读通道（供招新素养测验与免登录答题）
		api.GET("/public/exam/papers", examCtrl.GetPapers)
		api.GET("/public/exam/papers/:id", examCtrl.GetPaperDetail)
		api.POST("/public/exam/papers/:id/submit", examCtrl.SubmitPaper)

		// 认证接口
		auth := api.Group("/auth")
		{
			auth.POST("/login", authCtrl.Login)                     // 账号密码登录 (Web & APK 通用)
			auth.POST("/dorm-quick-login", authCtrl.DormQuickLogin) // 宿管三要素免密一键激活登录 (手机端 APK 专享)
		}

		// -------------------------------------------------------------
		// 2. 需登录鉴权分区 (受 JWT 认证 + Casbin 角色隔离多重保护)
		// -------------------------------------------------------------
		authenticated := api.Group("")
		authenticated.Use(middleware.AuthMiddleware())
		authenticated.Use(middleware.CasbinRBACMiddleware())
		{
			// 个人基础信息与安全设置
			authenticated.GET("/auth/profile", authCtrl.GetProfile)
			authenticated.PUT("/auth/security-settings", authCtrl.UpdateSecuritySettings)
			authenticated.POST("/auth/logout", authCtrl.Logout) // 服务端登出：清除会话 Cookie 并留痕

			// 登录后总览：按角色返回概览数，口径与数据范围在服务端定死
			authenticated.GET("/dashboard/summary", dashboardCtrl.Summary)

			// 账户安全中心（自助分区）：每个处理器都只用当前登录账号本人的 ID 取值，
			// 因此按角色放行整段不构成越权；改密仍走已有的 PUT /auth/security-settings。
			account := authenticated.Group("/account")
			{
				account.GET("/security", accountSecurityCtrl.Summary)                        // 评分 + 四项明细 + 会话 + 登录历史
				account.POST("/sessions/revoke", accountSecurityCtrl.RevokeOtherSessions)    // 下线其他设备（保留本机）
				account.DELETE("/sessions/:id", accountSecurityCtrl.RevokeSession)           // 下线指定会话
				account.POST("/environment/confirm", accountSecurityCtrl.ConfirmEnvironment) // 确认异地登录为本人，需二次验口令
				account.POST("/totp/setup", accountSecurityCtrl.SetupTOTP)                   // 生成待绑定密钥与 otpauth 链接
				account.POST("/totp/enable", accountSecurityCtrl.EnableTOTP)                 // 填一次正确验证码即启用
				account.POST("/totp/disable", accountSecurityCtrl.DisableTOTP)               // 解绑，需二次验口令
			}

			// 站内信：收件箱/发件箱、未读数、选人列表、发送、清未读。
			// 三条读路径一律以当前登录账号本人的 user_id 收窄，行级范围不指望 Casbin；
			// 发送的收件人还要逐个过 controller 层的收发矩阵（选人列表与它共用同一个函数）。
			messages := authenticated.Group("/messages")
			{
				messages.GET("", messageCtrl.List)                     // /api/v1/messages?box=in|sent&unread_only=1
				messages.GET("/unread-count", messageCtrl.UnreadCount) // 总览未读卡用
				messages.GET("/contacts", messageCtrl.Contacts)        // 选人列表：只回矩阵允许的对象
				messages.POST("", messageCtrl.Send)                    // 发送：收件人逐个过矩阵，全成全败
				messages.PUT("/:id/read", messageCtrl.MarkRead)        // 仅收件人本人，幂等
			}

			// a. 宿管工作台 (角色: dorm_manager, tech_admin)
			dorm := authenticated.Group("/dorm")
			{
				dorm.GET("/today-tasks", dormCtrl.GetTodayTasks)                          // 工作时间自动置顶推送监督与巡检待办
				dorm.GET("/slot-notice", dormCtrl.GetCurrentSlotNotice)                   // 根据当前时段与后台配置推送提交xx资料提醒
				dorm.POST("/upload-photo", dormCtrl.UploadPhoto)                          // 拍照上传并触发双 AI 流水线
				dorm.POST("/inspections/:id/analyze", dormCtrl.AnalyzeInspection)         // 用已上传的图片和寝室号发起识别，SSE 流式返回步骤/耗时/推理链
				dorm.POST("/inspections/:id/correct", dormCtrl.CorrectInspectionAnalysis) // AI 识别后宿管人工纠正结论，纠正结果即最终入库结论
				dorm.POST("/inspections/:id/subjects", dormCtrl.AppendInspectionSubjects) // 上传后补报名单：漏记的名字事后追加，只增不删
				dorm.GET("/inspections", dormCtrl.GetInspections)                         // 历史上传扣分图片瀑布流
				dorm.GET("/inspections/:id", dormCtrl.GetInspectionDetail)                // 单条留痕详情：记录本体 + 记名名单 + 已关联打表（点卡片查看）
			}

			// b. 学管会部员中心 (角色: member, minister, tech_admin)
			member := authenticated.Group("/member")
			{
				member.GET("/score-history", memberCtrl.GetScoreHistory)            // 个人上工表现与积分明细流水
				member.GET("/honors/weekly", honorCtrl.CurrentWeekly)               // 每周标兵公示（读快照，与部长工作台同源）
				member.GET("/my-shifts", memberCtrl.GetMyShifts)                    // 个人排班日历与班次
				member.POST("/leave", memberCtrl.CreateLeaveRequest)                // 快速请假申报
				member.GET("/leave-list", memberCtrl.GetLeaveList)                  // 请假历史
				member.GET("/substitute-recommend", memberCtrl.RecommendSubstitute) // 智能人员替补算法推荐预览
			}

			// c. 学管会部长管理中心 (角色: minister, tech_admin)
			minister := authenticated.Group("/minister")
			{
				minister.GET("/leaves", ministerCtrl.GetPendingLeaves)                              // 待审批请假列表
				minister.POST("/leaves/:id/review", ministerCtrl.ReviewLeave)                       // 审批请假 (批准/驳回，支持自动替补)
				minister.GET("/leaves/:id/substitute-preview", ministerCtrl.PreviewLeaveSubstitute) // 审批时替补人选算法预览
				minister.POST("/schedule-plans", ministerCtrl.GenerateSchedule)                     // 智能排班轮换生成引擎 (单双周/每日/自定义)
				minister.GET("/schedules", ministerCtrl.GetSchedules)                               // 查看所有班次
				minister.POST("/schedules/:id/complete", dutyCtrl.CompleteShift)                    // 值班核销：班次完成并自动 +5
				minister.POST("/schedules/sweep-missed", dutyCtrl.SweepMissedShifts)                // 旷工扫描：过期未核销置 missed 并 -5
				minister.GET("/recruit/applications", recruitCtrl.GetApplications)                  // 招新报名审核列表
				minister.POST("/recruit/applications/:id/review", recruitCtrl.ReviewApplication)    // 报名状态流转
				minister.GET("/members", ministerCtrl.GetAllMembers)                                // 部员花名册与积分榜
				minister.POST("/scores/adjust", ministerCtrl.AdjustScore)                           // 手动积分奖惩调整
				minister.POST("/members/promote", ministerCtrl.PromoteMember)                       // 部长管理部员：为旗下部员升职为副部长
				minister.GET("/week-duty-status", ministerCtrl.GetWeekDutyStatus)                   // 当前周部员值班三色状态大盘 (已值班/未值班/旷工)
				minister.GET("/score-policy", scorePolicyCtrl.GetPolicy)                            // 现行积分策略（出勤加分/单次上限/七日额度/复核线）
				minister.PUT("/score-policy", scorePolicyCtrl.SavePolicy)                           // 修改积分策略（仅技术维护组，需 step-up）
				minister.GET("/score-adjustments", scorePolicyCtrl.ListAdjustments)                 // 灵活调分复核清单（仅技术维护组）
				minister.POST("/score-logs/:id/reverse", scorePolicyCtrl.ReverseAdjustment)         // 冲正一笔灵活调分（仅技术维护组，需 step-up）

				// 每周标兵：评定落快照、公示只读快照、按周回溯留痕
				minister.POST("/honors/evaluate", honorCtrl.EvaluateWeekly)
				minister.GET("/honors/weekly", honorCtrl.CurrentWeekly)
				minister.GET("/honors/history", honorCtrl.ListHistory)

				// 部长端：AI 对话式排班与多模态图片识别排表
				minister.POST("/ai-schedule/chat", ministerCtrl.ChatAISchedule)
				minister.POST("/ai-schedule/apply", ministerCtrl.ApplyAISchedule)

				// 部长端：按《学管会排班表模板》的周表网格排班
				// 三种表类型：duty 排班表 / day_break 大课间 / night 夜间，按部门各自成表；
				// 预览→确认→重新更改=归档旧版留历史；完整模板工作簿下载（最新与历史版本）。
				minister.POST("/template-schedule/preview", templateScheduleCtrl.PreviewTemplateSchedule)
				minister.POST("/template-schedule/confirm", templateScheduleCtrl.ConfirmTemplateSchedule)
				minister.GET("/template-schedule/current", templateScheduleCtrl.CurrentTemplateSchedule)
				minister.GET("/template-schedule/history", templateScheduleCtrl.HistoryTemplateSchedule)
				minister.GET("/template-schedule/download", templateScheduleCtrl.DownloadCompiledTemplate)
				minister.POST("/template-schedule/clear", templateScheduleCtrl.ClearTemplateSchedule)
				minister.GET("/template-schedule/version", templateScheduleCtrl.VersionTemplateSchedule)
				minister.GET("/template-schedule/delete-pwd/status", templateScheduleCtrl.DeletePwdStatus)
				minister.POST("/template-schedule/delete-pwd", templateScheduleCtrl.SetDeletePwd)
				minister.POST("/template-schedule/plans/:id/delete", templateScheduleCtrl.DeleteTemplatePlan)
				minister.GET("/template-schedule/plans/:id", templateScheduleCtrl.GetTemplateScheduleDetail)
			}

			// d. 技术维护组 AI 调度与运维中枢 (角色: tech_admin)
			tech := authenticated.Group("/tech")
			{
				tech.GET("/ai-configs", techCtrl.GetAIConfigs)              // 获取多模态/文本 AI 详细配置
				tech.PUT("/ai-configs/:id", techCtrl.UpdateAIConfig)        // 修改模型参数、Prompt、API 密钥
				tech.POST("/ai-playground/test", techCtrl.TestAIPlayground) // 实时调试 Playground
				tech.GET("/roster-presets", techCtrl.GetRosterPresets)      // 宿管预置花名册
				tech.POST("/roster-presets", techCtrl.AddRosterPreset)      // 录入新宿管三要素
				tech.GET("/overview", techCtrl.GetSystemOverview)           // 系统全景监控
				tech.GET("/members", ministerCtrl.GetAllMembers)            // 全校各部门部员积分与履职总览

				// 宿管时段性资料提交规范与配置中心
				tech.GET("/task-slots", techCtrl.GetTaskSlots)
				tech.POST("/task-slots", techCtrl.CreateTaskSlot)
				tech.PUT("/task-slots/:id", techCtrl.UpdateTaskSlot)
				tech.DELETE("/task-slots/:id", techCtrl.DeleteTaskSlot)

				// 技术部专属：后台数据库与各名单全生命周期管理
				tech.GET("/db/tables", techDBCtrl.GetTablesSummary)
				tech.GET("/db/tables/:table", techDBCtrl.GetTableRecords)
				tech.POST("/db/tables/:table", techDBCtrl.CreateRecord)
				tech.PUT("/db/tables/:table/:id", techDBCtrl.UpdateRecord)
				tech.DELETE("/db/tables/:table/:id", techDBCtrl.DeleteRecord)

				// 角色变更专门入口（通用数据编辑器已禁止指定 role；需口令二次确认并留痕）
				tech.POST("/users/:id/role", techDBCtrl.ChangeUserRole)
				// 账号安全治理：只看风险不改权限；重置口令与解绑二次验证均需口令二次确认并留痕
				tech.GET("/account-governance", accountGovernanceCtrl.List)
				tech.POST("/users/:id/reset-password", accountGovernanceCtrl.ResetUserPassword)
				tech.POST("/users/:id/totp-unbind", accountGovernanceCtrl.UnbindUserTOTP)
				// 审计留痕查询（只读，仅技术维护组）
				tech.GET("/operation-logs", techDBCtrl.GetOperationLogs)
			}

			// e. 信息查看下载管理 (角色: viewer_export, minister, tech_admin)
			export := authenticated.Group("/export")
			{
				export.GET("/inspections", exportCtrl.GetInspectionsOverview)         // 违规扣分多维透视
				export.GET("/download-csv", exportCtrl.DownloadCSV)                   // 一键导出带防泄密水印 CSV/Excel
				export.GET("/bundle-zip", exportCtrl.DownloadBundleZip)               // 一键打包全量档案ZIP (下周排班+本周纪检+部员上工)
				export.GET("/daily-duty-csv", exportCtrl.DownloadDailyDutyCSV)        // 导出每天上下午值班部员名单(含班级/名字/部门)
				export.GET("/standing-duty-csv", exportCtrl.DownloadStandingDutyCSV)  // 导出常驻值班骨干干事名单(含班级/名字/部门/积分)
				export.GET("/exam-submissions", exportCtrl.GetExamSubmissionsSummary) // 答题考核成绩总表
			}

			// 答题考核通用管理接口
			exam := authenticated.Group("/exam")
			{
				exam.GET("/papers", examCtrl.GetPapers)
				exam.GET("/papers/:id", examCtrl.GetPaperDetail)
				exam.POST("/papers/:id/submit", examCtrl.SubmitPaper)
			}

			// 学生园区名册中枢 (支持多格式特征识别导入与查寝联动)
			students := authenticated.Group("/students")
			{
				students.POST("/parse-preview", studentCtrl.ParseAndPreview)
				students.POST("/column-mapping", studentCtrl.SaveColumnMapping)
				students.POST("/batch-import", studentCtrl.BatchImport)
				students.GET("", studentCtrl.GetStudents)
				students.GET("/room-members", studentCtrl.GetRoomStudents)
				students.DELETE("/clear", studentCtrl.ClearAllStudents)
			}

			// 技术部副部长查寝打表扣分与多维检索中心
			deductions := authenticated.Group("/deductions")
			{
				deductions.POST("", deductionCtrl.CreateDeduction)
				deductions.POST("/from-report", deductionCtrl.CreateDeductionsFromReport) // 从记名纸条名单批量转入打表
				deductions.GET("", deductionCtrl.GetDeductions)
				deductions.GET("/morning-dorm-reports", deductionCtrl.GetMorningDormReports)    // 宿管上报数据(楼层/宿管名/照片/名单)
				deductions.GET("/profile", deductionCtrl.GetStudentDeductionProfile)            // 按学生聚合的德育档案
				deductions.GET("/student-profiles", deductionCtrl.ListStudentDeductionProfiles) // 批量聚合，供评优看板
				deductions.GET("/export-csv", deductionCtrl.ExportDeductionsCSV)
				deductions.POST("/:id/revoke", deductionCtrl.RevokeDeduction)                  // 撤销：保留原记录并留痕
				deductions.GET("/room-excellence", excellenceCtrl.GetRoomExcellence)           // 文明标兵寝室评选数据
				deductions.GET("/room-excellence-csv", excellenceCtrl.ExportRoomExcellenceCSV) // 评优 CSV 导出
			}

			// 宣传部 (播音组打新闻与关键词检索 + 宣传组多标签随机海报图库)
			publicity := authenticated.Group("/publicity")
			{
				// 播音组专属：关键词自动筛选新闻、播报单管理
				publicity.GET("/broadcast/news", publicityCtrl.GetBroadcastNews)
				publicity.POST("/broadcast/news", publicityCtrl.CreateBroadcastNews)
				publicity.PUT("/broadcast/news/:id/toggle", publicityCtrl.ToggleBroadcastStatus)

				// 播音打新闻专用：在指定时段自动推送部员表现（全员/分部门红黑榜）
				publicity.GET("/broadcast/member-push", publicityCtrl.GetBroadcastMemberRankPush)
				publicity.PUT("/broadcast/push-config", publicityCtrl.UpdateBroadcastPushConfig)

				// 播音组 AI 讲稿：外部数据源配置与生成。feed-config 的写在 controller 里
				// 再判一次技术组 —— Casbin 对 /publicity/* 连 PUT 一起开给了部员。
				publicity.GET("/broadcast/feed-config", publicityCtrl.GetBroadcastFeedConfig)
				publicity.PUT("/broadcast/feed-config", publicityCtrl.UpdateBroadcastFeedConfig)
				publicity.POST("/broadcast/ai-script", publicityCtrl.GenerateBroadcastScript)

				// 宣传组专属：多分类随机插画素材（上游栗次元 JSON 接口，后端代理 + 本地缓存）
				publicity.GET("/gallery/random-images", publicityCtrl.GetRandomPublicityImages)
				publicity.GET("/images", publicityCtrl.GetRandomPublicityImages)
				publicity.GET("/images/categories", publicityCtrl.ListPublicityImageCategories)
				publicity.GET("/images/file/:name", publicityCtrl.ServePublicityImageFile)
				publicity.GET("/broadcast-news", publicityCtrl.GetBroadcastNews)
				publicity.GET("/broadcast-rank-push", publicityCtrl.GetBroadcastMemberRankPush)
			}

			// 学管会干事积分商城 · 部员福利与奖品兑换中心 (谁的部员谁定义；奖品CRUD、兑换、核销交付)
			welfare := authenticated.Group("/welfare")
			{
				// 1. 真实商品上架与兑换流转
				welfare.GET("/items", welfareCtrl.GetRewardItems)                   // 获取本部门奖品目录与本人总积分
				welfare.POST("/items", welfareCtrl.SaveRewardItem)                  // 部长新增/修改奖品
				welfare.DELETE("/items/:id", welfareCtrl.DeleteRewardItem)          // 部长删除奖品
				welfare.POST("/upload-image", welfareCtrl.UploadRewardImage)        // 部长上传奖品展示图片
				welfare.POST("/exchange", welfareCtrl.ExchangeRewardItem)           // 部员积分兑换奖品 (行级锁防超卖)
				welfare.GET("/orders", welfareCtrl.GetRewardOrders)                 // 查阅未交付与历史兑换订单
				welfare.POST("/orders/:id/deliver", welfareCtrl.DeliverRewardOrder) // 部长核销并确认交付奖品

				// 2. 技术组专用网关通道 (保留作为辅助福利)
				welfare.GET("/gateways", welfareCtrl.GetGateways)
				welfare.POST("/gateways", welfareCtrl.SaveGateway)
				welfare.POST("/gateways/:id/probe-models", welfareCtrl.ProbeModels)
				welfare.POST("/gateways/:id/exchange", welfareCtrl.ExchangeQuota)
				welfare.POST("/chat-relay", welfareCtrl.RelayChat)

				// 3. 兼容保留历史模型接口
				welfare.GET("/pricings", welfareCtrl.GetModelPricings)
				welfare.POST("/pricings", welfareCtrl.SaveModelPricing)
				welfare.DELETE("/pricings/:id", welfareCtrl.DeleteModelPricing)
				welfare.POST("/exchange-model", welfareCtrl.ExchangeModelCalls)
			}
		}
	}

	// 端口监听
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf(" 学管会综合管理系统后端服务已就绪，正在监听: :%s ...", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Server start failed: %v", err)
	}
}

// parseTrustedProxies 解析逗号分隔的 TRUSTED_PROXIES（IP 或 CIDR 网段）。
// 返回 nil 表示不信任任何代理，此时 Gin 只采信直连地址。
func parseTrustedProxies(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}
