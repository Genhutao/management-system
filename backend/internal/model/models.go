package model

import (
	"time"
)

// 系统预置角色
const (
	RoleDormManager  = "dorm_manager"  // 宿管
	RoleMember       = "member"        // 学管会部员
	RoleMinister     = "minister"      // 学管会部长
	RoleTechAdmin    = "tech_admin"    // 学管会技术维护组
	RoleViewerExport = "viewer_export" // 信息查看下载管理
)

// User 系统用户表
type User struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash string    `gorm:"size:255" json:"-"`
	RealName     string    `gorm:"size:64;not null" json:"real_name"`
	Phone        string    `gorm:"size:32;index" json:"phone"`
	Role         string    `gorm:"size:32;not null;index" json:"role"` // 见角色常量
	Building     string    `gorm:"size:64" json:"building"`            // 负责/所属楼栋，如 "西区12号楼"
	Floor        string    `gorm:"size:32" json:"floor"`               // 负责/所属楼层，如 "3F"
		ClassName    string    `gorm:"size:64" json:"class_name"`          // 所在年级班级，如 "高二(2)班"
		Department   string    `gorm:"size:64" json:"department"`          // 部门，如 "纪检部", "组织部 · 技术组"
		Position     string    `gorm:"size:32;default:'部员'" json:"position"` // 职位：如 "部员", "副部长", "部长"
		TotalScore   int       `gorm:"default:100" json:"total_score"`     // 部员基础积分，默认100
	Status       string    `gorm:"size:16;default:'active'" json:"status"` // active, disabled
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DormRosterPreset 宿管花名册预置表（供宿管手机端“手机号+楼栋楼层+姓名”三要素快速认证激活）
type DormRosterPreset struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	RealName    string    `gorm:"size:64;not null;index" json:"real_name"`
	Phone       string    `gorm:"size:32;not null;index" json:"phone"`
	Building    string    `gorm:"size:64;not null" json:"building"` // 如 "7号楼"
	Floor       string    `gorm:"size:32;not null" json:"floor"`    // 如 "全楼" 或 "1-3F"
	IsActivated bool      `gorm:"default:false" json:"is_activated"`
	BoundUserID uint      `json:"bound_user_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// SchedulePlan 排班计划主表
type SchedulePlan struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Title       string    `gorm:"size:128;not null" json:"title"`
	RuleType    string    `gorm:"size:32;not null" json:"rule_type"` // daily (每日轮换), weekly_single_double (单双周轮换), weekday (周内轮换), custom (自定义)
	StartDate   string    `gorm:"size:32;not null" json:"start_date"` // YYYY-MM-DD
	EndDate     string    `gorm:"size:32;not null" json:"end_date"`
	Description string    `gorm:"type:text" json:"description"`
	CreatedBy   uint      `json:"created_by"`
	Status      string    `gorm:"size:16;default:'active'" json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// ScheduleShift 排班班次明细表
type ScheduleShift struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	PlanID        uint      `gorm:"index" json:"plan_id"`
	Date          string    `gorm:"size:32;index;not null" json:"date"` // YYYY-MM-DD
	WeekType      string    `gorm:"size:16" json:"week_type"`           // single (单周), double (双周), normal
	ShiftPeriod   string    `gorm:"size:64;not null" json:"shift_period"` // 例如 "12:00-13:00 午检", "19:00-21:00 晚查寝"
	Building      string    `gorm:"size:64;not null" json:"building"`
	Floor         string    `gorm:"size:32" json:"floor"`
	MemberIDsJSON string    `gorm:"type:text" json:"member_ids_json"` // JSON 数组存储指派部员 ID
	MemberNames   string    `gorm:"size:255" json:"member_names"`     // 如 "张三, 李四"
	DormManagerID uint      `json:"dorm_manager_id"`                  // 协同宿管
	ManagerName   string    `gorm:"size:64" json:"manager_name"`
	Status        string    `gorm:"size:32;default:'scheduled'" json:"status"` // scheduled, in_progress, completed, missed
	SupervisorPic string    `gorm:"size:512" json:"supervisor_pic"`   // 宿管工作时间监督拍照存证
	Note          string    `gorm:"type:text" json:"note"`
	CreatedAt     time.Time `json:"created_at"`
}

// MemberScoreLog 部员表现与积分流水表
type MemberScoreLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	MemberID     uint      `gorm:"index;not null" json:"member_id"`
	MemberName   string    `gorm:"size:64;not null" json:"member_name"`
	ShiftID      uint      `json:"shift_id"`
	ChangeType   string    `gorm:"size:32;not null" json:"change_type"` // attendance_ok (+5), duty_substitute (+3), late (-2), leave (-0), penalty (-5), outstanding (+10)
	ScoreChange  int       `json:"score_change"`                        // +5, -2 等
	BalanceAfter int       `json:"balance_after"`                       // 变动后总积分
	Reason       string    `gorm:"size:255;not null" json:"reason"`
	OperatorName string    `gorm:"size:64" json:"operator_name"`
	CreatedAt    time.Time `json:"created_at"`
}

// LeaveRequest 部员请假申报
type LeaveRequest struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	MemberID         uint       `gorm:"index;not null" json:"member_id"`
	MemberName       string     `gorm:"size:64;not null" json:"member_name"`
	ShiftID          uint       `json:"shift_id"`
	ShiftInfo        string     `gorm:"size:255" json:"shift_info"` // 如 "2026-09-20 晚查寝 12号楼"
	Reason           string     `gorm:"type:text;not null" json:"reason"`
	SubstituteID     uint       `json:"substitute_id"`              // 替班部员
	SubstituteName   string     `gorm:"size:64" json:"substitute_name"`
	AutoSubstitute   bool       `gorm:"default:false" json:"auto_substitute"` // 可选功能：是否启用系统监测自动替补
	SubstituteReason string     `gorm:"size:255" json:"substitute_reason"`    // 算法匹配推荐依据
	Status           string     `gorm:"size:32;default:'pending'" json:"status"` // pending (待审批), approved (已批准), rejected (已驳回)
	MinisterID       uint       `json:"minister_id"`
	MinisterName     string     `gorm:"size:64" json:"minister_name"`
	ReviewComment    string     `gorm:"type:text" json:"review_comment"`
	ReviewedAt       *time.Time `json:"reviewed_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// InspectionPhoto 宿管拍照上传与 AI 处理归纳记录
type InspectionPhoto struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	DormManagerID  uint       `gorm:"index;not null" json:"dorm_manager_id"`
	ManagerName    string     `gorm:"size:64;not null" json:"manager_name"`
	Building       string     `gorm:"size:64;not null" json:"building"`
	RoomNumber     string     `gorm:"size:32" json:"room_number"`
	ImageURL       string     `gorm:"size:512;not null" json:"image_url"`
	PhotoType      string     `gorm:"size:32;default:'sanitation'" json:"photo_type"` // sanitation (卫生), violation (违规电器/违纪), duty_supervise (部员上工监督)
	ReportKind     string     `gorm:"size:16;index;default:'photo'" json:"report_kind"` // photo(现场实拍) / note(记名纸条) / text(纯文本申报)
	NoteText       string     `gorm:"type:text" json:"note_text"`                       // 宿管抄录的纸条名单原文或纯文本申报说明
	AIStatus       string     `gorm:"size:16;index" json:"ai_status"`                 // real / disabled / failed；空值为迁移前的历史数据，按不可信处理

	// 多模态 AI 识别提取阶段
	VisionAIOutput string     `gorm:"type:text" json:"vision_ai_output"` // 多模态翻译提取到的文本/描述/隐患
	
	// 文本 AI 结构化清洗与归纳入库阶段
	StructuredJSON string     `gorm:"type:text" json:"structured_json"`  // 规范化 JSON: { "category": "违规电器", "risk_level": "高", "deduct_points": 5, "summary": "..." }
	Category       string     `gorm:"size:64" json:"category"`
	DeductPoints   int        `gorm:"default:0" json:"deduct_points"`
	Severity       string     `gorm:"size:32;default:'low'" json:"severity"` // low, medium, high, critical
	
	Status         string     `gorm:"size:32;index;default:'uploaded'" json:"status"` // uploaded, ai_analyzed, converted, archived
	ReviewNote     string     `gorm:"type:text" json:"review_note"`
	CreatedAt      time.Time  `json:"created_at"`
	ProcessedAt    *time.Time `json:"processed_at"`
}

// ExamPaper 答题框架 - 试卷/问卷
type ExamPaper struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Title            string     `gorm:"size:128;not null" json:"title"`
	Description      string     `gorm:"type:text" json:"description"`
	Scope            string     `gorm:"size:32;default:'recruit'" json:"scope"` // recruit (招新笔试), member_training (部员培训考核), dorm_check (宿舍检查自测)
	DurationMinutes  int        `gorm:"default:30" json:"duration_minutes"`
	PassingScore     int        `gorm:"default:60" json:"passing_score"`
	TotalScore       int        `gorm:"default:100" json:"total_score"`
	IsPublished      bool       `gorm:"default:true" json:"is_published"`
	CreatedAt        time.Time  `json:"created_at"`
	Questions        []Question `gorm:"foreignKey:PaperID" json:"questions,omitempty"`
}

// Question 题目
type Question struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	PaperID       uint      `gorm:"index;not null" json:"paper_id"`
	Type          string    `gorm:"size:32;not null" json:"type"` // single (单选), multi (多选), essay (问答)
	QuestionText  string    `gorm:"type:text;not null" json:"question_text"`
	OptionsJSON   string    `gorm:"type:text" json:"options_json"` // JSON Array: ["A. ...", "B. ..."]
	CorrectAnswer string    `gorm:"size:255" json:"correct_answer"` // 如 "A" 或 "A,B"
	Score         int       `gorm:"default:10" json:"score"`
	SortOrder     int       `gorm:"default:0" json:"sort_order"`
	CreatedAt     time.Time `json:"created_at"`
}

// ExamSubmission 答卷记录
type ExamSubmission struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	PaperID        uint      `gorm:"index;not null" json:"paper_id"`
	PaperTitle     string    `gorm:"size:128" json:"paper_title"`
	ApplicantName  string    `gorm:"size:64;not null" json:"applicant_name"`
	ApplicantPhone string    `gorm:"size:32;not null" json:"applicant_phone"`
	AnswersJSON    string    `gorm:"type:text" json:"answers_json"` // JSON Object: { "1": "A", "2": "B" }
	Score          int       `gorm:"default:0" json:"score"`
	IsPassed       bool      `gorm:"default:false" json:"is_passed"`
	SubmittedAt    time.Time `json:"submitted_at"`
}

// RecruitmentApplication 招新报名信息
type RecruitmentApplication struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	RealName          string    `gorm:"size:64;not null" json:"real_name"`
	Gender            string    `gorm:"size:16" json:"gender"`
	Phone             string    `gorm:"size:32;not null" json:"phone"`
	MajorAndClass     string    `gorm:"size:128;not null" json:"major_and_class"`
	BuildingRoom      string    `gorm:"size:64;not null" json:"building_room"` // 如 "西12-402"
	TargetDepartment  string    `gorm:"size:64;not null" json:"target_department"` // 意向部门
	SelfIntroduction  string    `gorm:"type:text" json:"self_introduction"`
	ExperienceSkills  string    `gorm:"type:text" json:"experience_skills"`
	Status            string    `gorm:"size:32;default:'submitted'" json:"status"` // submitted, shortlisted, interviewed, admitted, rejected
	InterviewFeedback string    `gorm:"type:text" json:"interview_feedback"`
	CreatedAt         time.Time `json:"created_at"`
}

// AIConfig 技术维护组配置与调试中心表
type AIConfig struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	ConfigKey      string    `gorm:"size:64;uniqueIndex;not null" json:"config_key"` // vision_engine, text_engine
	DisplayName    string    `gorm:"size:128;not null" json:"display_name"`
	Provider       string    `gorm:"size:64;default:'mock_openai'" json:"provider"` // openai_compatible, qwen, ollama, mock
	Endpoint       string    `gorm:"size:255" json:"endpoint"`
	APIKey         string    `gorm:"size:255" json:"api_key"`
	ModelName      string    `gorm:"size:128" json:"model_name"`
	SystemPrompt   string    `gorm:"type:text" json:"system_prompt"`
	Temperature    float64   `gorm:"default:0.7" json:"temperature"`
	MaxTokens      int       `gorm:"default:2048" json:"max_tokens"`
	IsEnabled      bool      `gorm:"default:true" json:"is_enabled"`
	LastTestedAt   *time.Time `json:"last_tested_at"`
	LastTestResult string    `gorm:"type:text" json:"last_test_result"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Student 高一~高三学生园区名册 (楼-寝-名字-班级)
type Student struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	StudentNo  string    `gorm:"size:32;index" json:"student_no"`   // 学号 (可选)
	RealName   string    `gorm:"size:64;not null;index" json:"real_name"` // 姓名
	Grade      string    `gorm:"size:32;index" json:"grade"`       // 年级：高一, 高二, 高三
	ClassName  string    `gorm:"size:64;index" json:"class_name"`  // 班级：如 高一(1)班, 高二(3)班
	Building   string    `gorm:"size:64;index" json:"building"`    // 楼栋：如 1号楼, 西12号楼
	RoomNumber string    `gorm:"size:32;index" json:"room_number"` // 寝室号：如 302, 501
	BedNumber  string    `gorm:"size:16" json:"bed_number"`        // 床位号 (可选)
	Gender     string    `gorm:"size:16" json:"gender"`            // 性别 (可选)
	Phone      string    `gorm:"size:32" json:"phone"`             // 联系方式 (可选)
	Status     string    `gorm:"size:16;default:'active'" json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

// DeductionRecord 技术部副部长查寝打表与扣分存底记录表
type DeductionRecord struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	StudentID     uint      `gorm:"index" json:"student_id"`       // 关联 students.id；0 表示历史数据尚未回填
	Building      string    `gorm:"size:64;index;not null" json:"building"`       // 楼栋
	Floor         string    `gorm:"size:32;index;not null" json:"floor"`          // 楼层，如 1F, 3楼
	RoomNumber    string    `gorm:"size:32;index;not null" json:"room_number"`    // 寝室号，如 302
	StudentName   string    `gorm:"size:64;index;not null" json:"student_name"`   // 违纪学生姓名
	ClassName     string    `gorm:"size:64;index;not null" json:"class_name"`     // 班级，如 高一(3)班
	Grade         string    `gorm:"size:32;index" json:"grade"`                   // 年级
	Category      string    `gorm:"size:64;not null;index" json:"category"`       // 违规类别
	DeductPoints  int       `gorm:"not null" json:"deduct_points"`                // 扣除分值
	Reason        string    `gorm:"type:text;not null" json:"reason"`             // 违纪事由说明
	InspectorName string    `gorm:"size:64;not null" json:"inspector_name"`       // 打表记录人
	InspectorID   uint      `gorm:"index" json:"inspector_id"`                    // 记录人 ID
	SourceInspectionID uint      `gorm:"index" json:"source_inspection_id"`       // 来源宿管上报，0 表示手工录入
	SourceSubjectID    uint      `gorm:"index" json:"source_subject_id"`          // 来源上报的名单条目
	Status        string    `gorm:"size:32;index;default:'confirmed'" json:"status"` // 状态: confirmed, revoked
	RevokedBy     uint      `gorm:"index" json:"revoked_by"`
	RevokedByName string    `gorm:"size:64" json:"revoked_by_name"`
	RevokeReason  string    `gorm:"size:255" json:"revoke_reason"`
	RevokedAt     *time.Time `json:"revoked_at"`
	CreatedAt     time.Time `gorm:"index" json:"created_at"`                      // 打表时间
}

// InspectionSubject 一次上报所涉及的被记名学生（承接"记名纸条"与纯文本申报的名单）
type InspectionSubject struct {
	ID                   uint      `gorm:"primaryKey" json:"id"`
	InspectionID         uint      `gorm:"index;not null" json:"inspection_id"`
	RawName              string    `gorm:"size:64;not null" json:"raw_name"` // 纸条抄录或识别出的原始姓名
	StudentID            uint      `gorm:"index" json:"student_id"`          // 匹配到的名册记录，0 表示未匹配
	ClassName            string    `gorm:"size:64" json:"class_name"`
	MatchStatus          string    `gorm:"size:16;index;default:'unmatched'" json:"match_status"` // matched / ambiguous / unmatched
	MatchNote            string    `gorm:"type:text" json:"match_note"`
	ConvertedDeductionID uint      `gorm:"index" json:"converted_deduction_id"` // 已转入的打表记录，用于阻止重复扣分
	CreatedAt            time.Time `json:"created_at"`
}

// OperationLog 高危操作的只增不改留痕（撤销扣分、清空或批量导入名册等）
type OperationLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Action       string    `gorm:"size:64;index;not null" json:"action"`
	TargetType   string    `gorm:"size:64;index" json:"target_type"`
	TargetID     uint      `gorm:"index" json:"target_id"`
	OperatorID   uint      `gorm:"index" json:"operator_id"`
	OperatorName string    `gorm:"size:64" json:"operator_name"`
	OperatorRole string    `gorm:"size:32" json:"operator_role"`
	Detail       string    `gorm:"type:text" json:"detail"`
	RequestID    string    `gorm:"size:64" json:"request_id"`
	IP           string    `gorm:"size:64" json:"ip"`
	CreatedAt    time.Time `gorm:"index" json:"created_at"`
}

// DormTaskSlotConfig 宿管时段性资料提交与工作规范配置表（支持后台多规则动态拓展）
type DormTaskSlotConfig struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	SlotName          string    `gorm:"size:64;not null" json:"slot_name"`                    // 时段名称，如 "午间用电排查与卫生评定"
	StartTime         string    `gorm:"size:16;not null" json:"start_time"`                   // 起始时间 HH:MM
	EndTime           string    `gorm:"size:16;not null" json:"end_time"`                     // 截止时间 HH:MM (支持跨午夜)
	PeriodType        string    `gorm:"size:32;default:'daily'" json:"period_type"`           // daily (每日), weekday (周内工作日), weekend (周末), single_week (单周), double_week (双周)
	RequiredMaterials string    `gorm:"type:text;not null" json:"required_materials"`          // 需提交的具体资料规范说明
	ActionPrompt      string    `gorm:"size:255" json:"action_prompt"`                        // 操作指引与核验要求
	TargetPhotoType   string    `gorm:"size:32;default:'violation'" json:"target_photo_type"` // 建议关联拍照类型: violation, sanitation, duty_supervise
	UrgencyLevel      string    `gorm:"size:16;default:'normal'" json:"urgency_level"`        // 紧迫度: normal, high, critical
	IsEnabled         bool      `gorm:"default:true" json:"is_enabled"`                       // 是否启用
	SortOrder         int       `gorm:"default:0" json:"sort_order"`                          // 优先级排序
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// BroadcastNewsItem 播音部员新闻稿件与校园快讯库
type BroadcastNewsItem struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Title       string    `gorm:"size:255;not null;index" json:"title"`
	Content     string    `gorm:"type:text;not null" json:"content"`
	Category    string    `gorm:"size:64;index;default:'校园时讯'" json:"category"` // 校园时讯、纪律通报、寝室文化、红榜表彰、晨间心语
	Keywords    string    `gorm:"size:255;index" json:"keywords"`               // 逗号隔开的关键词标签
	Source      string    `gorm:"size:128;default:'学管会融媒体采编'" json:"source"`
	PublishDate string    `gorm:"size:32;index" json:"publish_date"`            // YYYY-MM-DD
	IsBroadcast bool      `gorm:"default:false" json:"is_broadcast"`            // 是否已被播音录入播音单
	CreatedBy   string    `gorm:"size:64" json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// BroadcastPushConfig 播音组部员表现自动推送策略配置（支持指定时段、全员前/后N名、各部门前/后N名）
type BroadcastPushConfig struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	RuleName       string    `gorm:"size:128;not null" json:"rule_name"`
	PushTimeStart  string    `gorm:"size:16;not null;default:'17:30'" json:"push_time_start"` // 开始推送时段 HH:MM
	PushTimeEnd    string    `gorm:"size:16;not null;default:'18:45'" json:"push_time_end"`   // 结束推送时段 HH:MM
	PushMode       string    `gorm:"size:32;default:'overall'" json:"push_mode"`             // 'overall' (全员总榜前N后N), 'department' (各部门前N后N)
	TopCount       int       `gorm:"default:3" json:"top_count"`                             // 最优前几个 (红榜)
	BottomCount    int       `gorm:"default:3" json:"bottom_count"`                          // 最差后几个 (黑榜/需加油榜)
	IncludeScores  bool      `gorm:"default:true" json:"include_scores"`                     // 是否包含具体分数
	IncludeReason  bool      `gorm:"default:true" json:"include_reason"`                     // 是否包含奖惩/缺勤原因
	IsEnabled      bool      `gorm:"default:true" json:"is_enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// PublicityAsset 宣传部图库资源与海报素材记录
type PublicityAsset struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Title       string    `gorm:"size:128" json:"title"`
	Tag         string    `gorm:"size:64;index" json:"tag"`             // anime(二次元), photography(摄影风景), ink(国风水墨), portrait(写真人像), tech(未来科技)
	ImageURL    string    `gorm:"size:512;not null" json:"image_url"`
	SourceAPI   string    `gorm:"size:128" json:"source_api"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Downloaded  int       `gorm:"default:0" json:"downloaded"`
	CreatedBy   string    `gorm:"size:64" json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// TechWelfareGateway 技术部部长与技术组福利 API 中转透传网关 (谁设置、谁用、谁管理)
type TechWelfareGateway struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	OwnerID           uint      `gorm:"index;not null" json:"owner_id"`                // 拥有者用户 ID (物理权限隔离)
	OwnerName         string    `gorm:"size:64;not null" json:"owner_name"`
	GatewayName       string    `gorm:"size:128;not null" json:"gateway_name"`         // 中转站名称
	BaseURL           string    `gorm:"size:255;not null" json:"base_url"`             // 上游 API 端点
	APIKey            string    `gorm:"size:255;not null" json:"api_key,omitempty"`    // 服务端透传密钥 (脱敏)
	RecognizedModels  string    `gorm:"type:text" json:"recognized_models"`            // 自动向上游探测识别到的模型列表 JSON
	DefaultModel      string    `gorm:"size:64;default:'gpt-4o-mini'" json:"default_model"`
	PointCostPerCall  int       `gorm:"default:3" json:"point_cost_per_call"`          // 积分兑换消耗倍率
	IsActive          bool      `gorm:"default:true" json:"is_active"`
	TotalRelayedCalls int       `gorm:"default:0" json:"total_relayed_calls"`          // 累计透传调用次数
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// WelfareUsageQuota 部员积分兑换 AI 额度流水与可用额度
type WelfareUsageQuota struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	UserID        uint      `gorm:"index;not null" json:"user_id"`
	UserName      string    `gorm:"size:64;not null" json:"user_name"`
	GatewayID     uint      `gorm:"index;not null" json:"gateway_id"`
	GatewayName   string    `gorm:"size:128" json:"gateway_name"`
	RemainQuota   int       `gorm:"default:0" json:"remain_quota"`                 // 剩余可用调用额度
	TotalExchange int       `gorm:"default:0" json:"total_exchange"`               // 累计兑换消耗积分
	TotalUsed     int       `gorm:"default:0" json:"total_used"`                   // 累计已消费次数
	UpdatedAt     time.Time `json:"updated_at"`
}

// WelfareModelPricing 部长自定义各模型的价格与积分兑换规则 (按部门归属，谁的部员谁定义)
type WelfareModelPricing struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Department   string    `gorm:"size:64;index;default:''" json:"department"`       // 所属部门：如 "纪检部", "组织部 · 技术组"
	ModelKey     string    `gorm:"size:64;not null" json:"model_key"`               // 模型标识: gpt-4o-mini, gpt-4o, deepseek-chat, claude-3-5-sonnet, o1-mini
	DisplayName  string    `gorm:"size:128;not null" json:"display_name"`           // 展示名称: 如 "DeepSeek Chat (极速推理)"
	Provider     string    `gorm:"size:64;default:'OpenAI/DeepSeek'" json:"provider"`// 服务商类别
	PointsCost   int       `gorm:"not null;default:10" json:"points_cost"`          // 兑换所需积分
	CallsGranted int       `gorm:"not null;default:20" json:"calls_granted"`        // 每次兑换获取的调用次数
	CostPerCall  int       `gorm:"default:1" json:"cost_per_call"`                  // 单次提问消耗次数 (默认1次)
	Description  string    `gorm:"size:255" json:"description"`                     // 模型特色说明与性能说明
	IconTag      string    `gorm:"size:32;default:'bolt'" json:"icon_tag"`          // 图标标签
	SortOrder    int       `gorm:"default:0" json:"sort_order"`
	IsEnabled    bool      `gorm:"default:true" json:"is_enabled"`
	CreatedBy    string    `gorm:"size:64" json:"created_by"`                        // 创建/设定的部长姓名
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// MemberModelQuota 部员在各个模型上的剩余调用次数与消耗记录
type MemberModelQuota struct {
	ID                  uint       `gorm:"primaryKey" json:"id"`
	UserID              uint       `gorm:"index;not null" json:"user_id"`
	UserName            string     `gorm:"size:64;not null" json:"user_name"`
	ModelKey            string     `gorm:"size:64;index;not null" json:"model_key"`
	DisplayName         string     `gorm:"size:128" json:"display_name"`
	RemainCalls         int        `gorm:"default:0" json:"remain_calls"`           // 剩余可用调用次数
	TotalExchangedCalls int        `gorm:"default:0" json:"total_exchanged_calls"`  // 累计兑换获取次数
	TotalUsedCalls      int        `gorm:"default:0" json:"total_used_calls"`       // 累计已消费次数
	LastUsedAt          *time.Time `json:"last_used_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}


