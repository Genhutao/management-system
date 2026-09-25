package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

type ExamController struct{}

type SubmitExamRequest struct {
	ApplicantName  string            `json:"applicant_name" binding:"required"`
	ApplicantPhone string            `json:"applicant_phone" binding:"required"`
	Answers        map[string]string `json:"answers" binding:"required"` // {"1": "A", "2": "A,B,D"}
}

// GetPapers 获取已发布的试卷/考核列表
func (exc *ExamController) GetPapers(c *gin.Context) {
	scope := c.Query("scope")
	var papers []model.ExamPaper
	query := repository.DB.Where("is_published = ?", true)
	if scope != "" {
		query = query.Where("scope = ? OR scope = 'all'", scope)
	}
	query.Find(&papers)

	c.JSON(http.StatusOK, gin.H{
		"total": len(papers),
		"items": papers,
	})
}

// GetPaperDetail 获取指定试卷包含的所有题目（作答时不泄露标准答案）
func (exc *ExamController) GetPaperDetail(c *gin.Context) {
	paperID := c.Param("id")

	var paper model.ExamPaper
	if err := repository.DB.Preload("Questions").First(&paper, paperID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "试卷不存在"})
		return
	}

	// 针对学生/部员端作答脱敏：去除题目的 correct_answer
	type ClientQuestion struct {
		ID           uint     `json:"id"`
		Type         string   `json:"type"`
		QuestionText string   `json:"question_text"`
		Options      []string `json:"options"`
		Score        int      `json:"score"`
		SortOrder    int      `json:"sort_order"`
	}

	var clientQuestions []ClientQuestion
	for _, q := range paper.Questions {
		var opts []string
		_ = json.Unmarshal([]byte(q.OptionsJSON), &opts)
		clientQuestions = append(clientQuestions, ClientQuestion{
			ID:           q.ID,
			Type:         q.Type,
			QuestionText: q.QuestionText,
			Options:      opts,
			Score:        q.Score,
			SortOrder:    q.SortOrder,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"id":               paper.ID,
		"title":            paper.Title,
		"description":      paper.Description,
		"duration_minutes": paper.DurationMinutes,
		"passing_score":    paper.PassingScore,
		"total_score":      paper.TotalScore,
		"questions":        clientQuestions,
	})
}

// SubmitPaper 提交试卷并自动比对判分
func (exc *ExamController) SubmitPaper(c *gin.Context) {
	paperID := c.Param("id")

	var req SubmitExamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "答题数据不完整，请提供作答人姓名、电话与答案"})
		return
	}

	var paper model.ExamPaper
	if err := repository.DB.Preload("Questions").First(&paper, paperID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "试卷不存在"})
		return
	}

	// 自动判分算法
	totalScore := 0
	type AnswerResult struct {
		QuestionID    uint   `json:"question_id"`
		UserAnswer    string `json:"user_answer"`
		CorrectAnswer string `json:"correct_answer"`
		EarnedScore   int    `json:"earned_score"`
		IsCorrect     bool   `json:"is_correct"`
	}

	var details []AnswerResult
	for _, q := range paper.Questions {
		qidStr := ""
		for k, v := range req.Answers {
			if strings.TrimSpace(k) == string(rune('0'+q.ID)) || k == strings.TrimSpace(string(rune(q.ID))) || k == string(rune(q.ID)) || fmt.Sprintf("%d", q.ID) == k {
				qidStr = v
				break
			}
		}

		userAns := strings.ToUpper(strings.TrimSpace(qidStr))
		correctAns := strings.ToUpper(strings.TrimSpace(q.CorrectAnswer))

		earned := 0
		isCorrect := false
		if userAns == correctAns && userAns != "" {
			earned = q.Score
			isCorrect = true
			totalScore += earned
		}

		details = append(details, AnswerResult{
			QuestionID:    q.ID,
			UserAnswer:    userAns,
			CorrectAnswer: correctAns,
			EarnedScore:   earned,
			IsCorrect:     isCorrect,
		})
	}

	isPassed := totalScore >= paper.PassingScore
	answersBytes, _ := json.Marshal(req.Answers)

	sub := model.ExamSubmission{
		PaperID:        paper.ID,
		PaperTitle:     paper.Title,
		ApplicantName:  req.ApplicantName,
		ApplicantPhone: req.ApplicantPhone,
		AnswersJSON:    string(answersBytes),
		Score:          totalScore,
		IsPassed:       isPassed,
		SubmittedAt:    time.Now(),
	}
	repository.DB.Create(&sub)

	c.JSON(http.StatusOK, gin.H{
		"message":        "试卷提交成功，系统已自动完成智能阅卷！",
		"score":          totalScore,
		"is_passed":      isPassed,
		"passing_score":  paper.PassingScore,
		"submission_id":  sub.ID,
		"detail_results": details,
	})
}
