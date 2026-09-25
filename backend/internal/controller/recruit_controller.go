package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type RecruitController struct{}

type ApplicationRequest struct {
	RealName         string `json:"real_name" binding:"required"`
	MajorAndClass    string `json:"major_and_class" binding:"required"`
	TargetDepartment string `json:"target_department" binding:"required"`
	Gender           string `json:"gender"`
	Phone            string `json:"phone"`
	BuildingRoom     string `json:"building_room"`
	SelfIntroduction string `json:"self_introduction"`
	ExperienceSkills string `json:"experience_skills"`
}

// GetRecruitInfo 获取招新宣讲与部门概览（公开无鉴权）
func (rc *RecruitController) GetRecruitInfo(c *gin.Context) {
	var totalCount int64
	repository.DB.Model(&model.RecruitmentApplication{}).Count(&totalCount)

	c.JSON(http.StatusOK, gin.H{
		"title":           "2026年秋季学期学生宿舍自我管理委员会（学管会）招新纳新简章",
		"slogan":          "青春筑梦 · 宿暖人心 —— 极简数字化园区治理平台",
		"deadline":        "2026-10-15 23:59:59",
		"total_applied":   totalCount,
		"notice":          "凡具有良好品德与奉献精神的在校生均可报名，班级多媒体大屏支持填写姓名与班级一键申报！",
		"departments": []gin.H{
			{
				"name":        "组织部 · 技术组",
				"parent":      "组织部",
				"badge":       "技术驱动",
				"desc":        "负责本系统运维、AI多模态识别中枢微调、园区数据看板、软硬件技术支持与平台功能拓展。",
				"requirement": "对编程开发、网络技术、AI大模型、系统维护或软硬件折腾感兴趣。",
			},
			{
				"name":        "组织部 · 督查组",
				"parent":      "组织部",
				"badge":       "考勤督导",
				"desc":        "负责学管会全体部员考勤复核、规章制度监督、上工打表与积分台账统计。",
				"requirement": "原则性强、作风严谨正派、认真踏实负责。",
			},
			{
				"name":        "宣传部 · 播音组",
				"parent":      "宣传部",
				"badge":       "园区之声",
				"desc":        "负责学生园区晚间广播站播音、宿舍温馨安全提醒、园区特色主题音频栏目录制与播报。",
				"requirement": "普通话标准流利、热爱播音主持与朗诵、嗓音有亲和力。",
			},
			{
				"name":        "宣传部 · 宣传组",
				"parent":      "宣传部",
				"badge":       "视觉创意",
				"desc":        "负责学管会微信公众号、海报视觉设计、文案撰写、摄影剪辑及大型活动宣传推广。",
				"requirement": "具备文字功底、审美在线，熟悉 PS、剪映或摄影技能者优先。",
			},
			{
				"name":        "纪检部",
				"parent":      "纪检部",
				"badge":       "安全前哨",
				"desc":        "负责园区常规晚查寝秩序、违规大功率电器与消防安全隐患排查、佩戴红袖标巡检、协同宿管老师化解宿舍矛盾。",
				"requirement": "作风严谨、善于沟通交流、处事冷静、具备良好突发状况应对能力。",
			},
		},
		"process": []string{
			"1. 班级大屏一键填报意向表",
			"2. 参与招新笔试素养测评",
			"3. 现场/线上无领导小组面试",
			"4. 见习培训并颁发聘书",
		},
	})
}

// SubmitApplication 提交招新报名表（支持班级大屏极简「班级+姓名+意向部门」一键直报）
func (rc *RecruitController) SubmitApplication(c *gin.Context) {
	var req ApplicationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供姓名、班级与意向志愿部门"})
		return
	}

	phone := req.Phone
	buildingRoom := req.BuildingRoom
	gender := req.Gender

	// 智能联动：如果手机号或宿舍未填，尝试在导入的全校学生花名册中自动补齐
	if phone == "" || buildingRoom == "" {
		var st model.Student
		if err := repository.DB.Where("real_name = ? AND class_name LIKE ?", req.RealName, "%"+req.MajorAndClass+"%").First(&st).Error; err == nil {
			if phone == "" && st.Phone != "" {
				phone = st.Phone
			}
			if buildingRoom == "" && st.Building != "" {
				buildingRoom = fmt.Sprintf("%s %s室", st.Building, st.RoomNumber)
			}
			if gender == "" && st.Gender != "" {
				gender = st.Gender
			}
		}
	}

	if phone == "" {
		phone = "班级直报(待联络)"
	}
	if buildingRoom == "" {
		buildingRoom = "在校学生寝室"
	}

	app := model.RecruitmentApplication{
		RealName:         req.RealName,
		Gender:           gender,
		Phone:            phone,
		MajorAndClass:    req.MajorAndClass,
		BuildingRoom:     buildingRoom,
		TargetDepartment: req.TargetDepartment,
		SelfIntroduction: req.SelfIntroduction,
		ExperienceSkills: req.ExperienceSkills,
		Status:           "submitted",
		CreatedAt:        time.Now(),
	}

	if err := repository.DB.Create(&app).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "报名提交失败: " + err.Error()})
		return
	}

	admissionNo := fmt.Sprintf("XGH-2026-%04d", app.ID)

	var currentTotal int64
	repository.DB.Model(&model.RecruitmentApplication{}).Count(&currentTotal)

	c.JSON(http.StatusOK, gin.H{
		"message":           fmt.Sprintf("恭喜 %s 同学！您已成功申报【%s】，申报流水号已生成！", app.RealName, app.TargetDepartment),
		"application_id":    app.ID,
		"admission_no":      admissionNo,
		"real_name":         app.RealName,
		"major_and_class":   app.MajorAndClass,
		"target_department": app.TargetDepartment,
		"total_applied":     currentTotal,
		"created_at":        app.CreatedAt.Format("2006-01-02 15:04:05"),
	})
}

