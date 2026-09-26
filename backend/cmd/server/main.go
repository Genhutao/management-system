package main

import (
	"log"
	"os"

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

	// 静态资源与上传目录挂载
	_ = os.MkdirAll("./uploads", 0755)
	_ = os.MkdirAll("./static", 0755)
	r.Static("/uploads", "./uploads")
	r.Static("/static", "./static")
	r.StaticFile("/", "./static/index.html")

	// 实例化控制器
	authCtrl := &controller.AuthController{}
	dormCtrl := &controller.DormController{}
	memberCtrl := &controller.MemberController{}
	ministerCtrl := &controller.MinisterController{}
		techCtrl := &controller.TechController{}
		techDBCtrl := &controller.TechDBController{}
		exportCtrl := &controller.ExportController{}
	examCtrl := &controller.ExamController{}
		recruitCtrl := &controller.RecruitController{}
		studentCtrl := &controller.StudentController{}
		deductionCtrl := &controller.DeductionController{}
		publicityCtrl := &controller.PublicityController{}
		welfareCtrl := &controller.WelfareController{}

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
			auth.POST("/login", authCtrl.Login)                         // 账号密码登录 (Web & APK 通用)
			auth.POST("/dorm-quick-login", authCtrl.DormQuickLogin)     // 宿管三要素免密一键激活登录 (手机端 APK 专享)
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
				authenticated.POST("/auth/logout", authCtrl.Logout)     // 服务端登出：清除会话 Cookie 并留痕

				// a. 宿管工作台 (角色: dorm_manager, tech_admin)
				dorm := authenticated.Group("/dorm")
				{
					dorm.GET("/today-tasks", dormCtrl.GetTodayTasks)         // 工作时间自动置顶推送监督与巡检待办
					dorm.GET("/slot-notice", dormCtrl.GetCurrentSlotNotice)  // 根据当前时段与后台配置推送提交xx资料提醒
					dorm.POST("/upload-photo", dormCtrl.UploadPhoto)         // 拍照上传并触发双 AI 流水线
					dorm.GET("/inspections", dormCtrl.GetInspections)        // 历史上传扣分图片瀑布流
				}

				// b. 学管会部员中心 (角色: member, minister, tech_admin)
				member := authenticated.Group("/member")
				{
					member.GET("/score-history", memberCtrl.GetScoreHistory) // 个人上工表现与积分明细流水
					member.GET("/my-shifts", memberCtrl.GetMyShifts)         // 个人排班日历与班次
					member.POST("/leave", memberCtrl.CreateLeaveRequest)     // 快速请假申报
					member.GET("/leave-list", memberCtrl.GetLeaveList)       // 请假历史
					member.GET("/substitute-recommend", memberCtrl.RecommendSubstitute) // 智能人员替补算法推荐预览
				}

					// c. 学管会部长管理中心 (角色: minister, tech_admin)
					minister := authenticated.Group("/minister")
					{
						minister.GET("/leaves", ministerCtrl.GetPendingLeaves)             // 待审批请假列表
						minister.POST("/leaves/:id/review", ministerCtrl.ReviewLeave)      // 审批请假 (批准/驳回，支持自动替补)
						minister.GET("/leaves/:id/substitute-preview", ministerCtrl.PreviewLeaveSubstitute) // 审批时替补人选算法预览
						minister.POST("/schedule-plans", ministerCtrl.GenerateSchedule)    // 智能排班轮换生成引擎 (单双周/每日/自定义)
						minister.GET("/schedules", ministerCtrl.GetSchedules)              // 查看所有班次
						minister.GET("/members", ministerCtrl.GetAllMembers)               // 部员花名册与积分榜
							minister.POST("/scores/adjust", ministerCtrl.AdjustScore)          // 手动积分奖惩调整
							minister.POST("/members/promote", ministerCtrl.PromoteMember)      // 部长管理部员：为旗下部员升职为副部长
							minister.GET("/week-duty-status", ministerCtrl.GetWeekDutyStatus)  // 当前周部员值班三色状态大盘 (已值班/未值班/旷工)
						
						// 部长端：AI 对话式排班与多模态图片识别排表
						minister.POST("/ai-schedule/chat", ministerCtrl.ChatAISchedule)
						minister.POST("/ai-schedule/apply", ministerCtrl.ApplyAISchedule)
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
						// 审计留痕查询（只读，仅技术维护组）
						tech.GET("/operation-logs", techDBCtrl.GetOperationLogs)
					}

				// e. 信息查看下载管理 (角色: viewer_export, minister, tech_admin)
				export := authenticated.Group("/export")
				{
					export.GET("/inspections", exportCtrl.GetInspectionsOverview)       // 违规扣分多维透视
					export.GET("/download-csv", exportCtrl.DownloadCSV)                 // 一键导出带防泄密水印 CSV/Excel
					export.GET("/bundle-zip", exportCtrl.DownloadBundleZip)             // 一键打包全量档案ZIP (下周排班+本周纪检+部员上工)
					export.GET("/daily-duty-csv", exportCtrl.DownloadDailyDutyCSV)       // 导出每天上下午值班部员名单(含班级/名字/部门)
					export.GET("/standing-duty-csv", exportCtrl.DownloadStandingDutyCSV)// 导出常驻值班骨干干事名单(含班级/名字/部门/积分)
					export.GET("/exam-submissions", exportCtrl.GetExamSubmissionsSummary)// 答题考核成绩总表
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
								deductions.GET("/morning-dorm-reports", deductionCtrl.GetMorningDormReports) // 宿管上报数据(楼层/宿管名/照片/名单)
								deductions.GET("/profile", deductionCtrl.GetStudentDeductionProfile)         // 按学生聚合的德育档案
								deductions.GET("/student-profiles", deductionCtrl.ListStudentDeductionProfiles) // 批量聚合，供评优看板
								deductions.GET("/export-csv", deductionCtrl.ExportDeductionsCSV)
								deductions.POST("/:id/revoke", deductionCtrl.RevokeDeduction)                // 撤销：保留原记录并留痕
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

								// 宣传组专属：多标签随机 API 图库 (二次元, 摄影, 水墨, 写真, 科技)
								publicity.GET("/gallery/random-images", publicityCtrl.GetRandomPublicityImages)
								publicity.GET("/images", publicityCtrl.GetRandomPublicityImages)
								publicity.GET("/broadcast-news", publicityCtrl.GetBroadcastNews)
								publicity.GET("/broadcast-rank-push", publicityCtrl.GetBroadcastMemberRankPush)
							}

									// 学管会干事积分商城 · 部员福利与奖品兑换中心 (谁的部员谁定义；奖品CRUD、兑换、核销交付)
									welfare := authenticated.Group("/welfare")
									{
										// 1. 真实商品上架与兑换流转
										welfare.GET("/items", welfareCtrl.GetRewardItems)                    // 获取本部门奖品目录与本人总积分
										welfare.POST("/items", welfareCtrl.SaveRewardItem)                   // 部长新增/修改奖品
										welfare.DELETE("/items/:id", welfareCtrl.DeleteRewardItem)           // 部长删除奖品
										welfare.POST("/upload-image", welfareCtrl.UploadRewardImage)         // 部长上传奖品展示图片
										welfare.POST("/exchange", welfareCtrl.ExchangeRewardItem)            // 部员积分兑换奖品 (行级锁防超卖)
										welfare.GET("/orders", welfareCtrl.GetRewardOrders)                  // 查阅未交付与历史兑换订单
										welfare.POST("/orders/:id/deliver", welfareCtrl.DeliverRewardOrder)  // 部长核销并确认交付奖品

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
