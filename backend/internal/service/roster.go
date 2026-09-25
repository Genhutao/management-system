package service

import (
	"fmt"
	"regexp"
	"strings"

	"gorm.io/gorm"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
)

// MaxSubjectsPerReport 单次上报允许登记的被记名学生上限，避免异常输入撑爆名单。
const MaxSubjectsPerReport = 50

// 名册匹配结果
const (
	MatchMatched   = "matched"   // 本寝名册唯一匹配，可直接打表
	MatchAmbiguous = "ambiguous" // 本寝有多名同名学生，需人工指定
	MatchUnmatched = "unmatched" // 不在本寝名册，禁止直接打表
)

var (
	rosterSeparators = regexp.MustCompile(`[\n\r、，,;；|／/]+|[ \t　]+`)
	leadingOrdinal   = regexp.MustCompile(`^\s*(?:\d+\s*[.:：、\)]|第\d+[人次])\s*`)
	trailingPunct    = regexp.MustCompile(`[\s。.：:~～\-—]+$`)
	nonNameMarker    = regexp.MustCompile(`(号楼|宿舍|寝室|班级|高一|高二|高三|\d+班)`)
	hanName          = regexp.MustCompile(`^[\p{Han}]{2,6}$`)
)

// SplitRosterNames 把"记名纸条"或纯文本申报中的姓名拆成条目。
// 手写名单转录出来常混有编号、顿号、空白与班级残留，这里统一清洗。
func SplitRosterNames(raw string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 16)

	for _, token := range rosterSeparators.Split(raw, -1) {
		token = leadingOrdinal.ReplaceAllString(token, "")
		token = trailingPunct.ReplaceAllString(strings.TrimSpace(token), "")
		if token == "" || seen[token] {
			continue
		}
		// 含楼栋/班级残留或纯数字的片段不是姓名；姓名须为 2~6 个汉字
		if nonNameMarker.MatchString(token) || !hanName.MatchString(token) {
			continue
		}
		seen[token] = true
		out = append(out, token)
		if len(out) >= MaxSubjectsPerReport {
			break
		}
	}
	return out
}

// RoomHasRoster 指定楼栋与寝室是否已有名册登记。
// 用于区分"全校名册尚未导入"（可宽松按姓名存底）与"仅本寝查无此人"（必须按冒名拒绝）。
func RoomHasRoster(building, room string) (bool, error) {
	if room == "" {
		return false, nil
	}
	var n int64
	q := repository.DB.Model(&model.Student{}).Where("room_number = ? AND status = ?", room, "active")
	if building != "" && building != "全楼" {
		q = q.Where("building LIKE ?", "%"+building+"%")
	}
	if err := q.Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// MatchSubjectInRoom 在指定楼栋与寝室的登记名册中匹配姓名，是"查寝防冒名"的服务端依据。
// 返回匹配到的 students.id（未匹配为 0）、班级、状态与给人看的原因说明。
func MatchSubjectInRoom(building, room, rawName string) (uint, string, string, string) {
	if room == "" || rawName == "" {
		return 0, "", MatchUnmatched, "缺少寝室号或被记名姓名，无法与宿位名册核对"
	}

	inRoomScope := func() *gorm.DB {
		q := repository.DB.Model(&model.Student{}).
			Where("real_name = ? AND room_number = ? AND status = ?", rawName, room, "active")
		if building != "" && building != "全楼" {
			q = q.Where("building LIKE ?", "%"+building+"%")
		}
		return q
	}

	var inRoom []model.Student
	if err := inRoomScope().Limit(3).Find(&inRoom).Error; err != nil {
		return 0, "", MatchUnmatched, "名册核对失败: " + err.Error()
	}

	switch {
	case len(inRoom) == 1:
		return inRoom[0].ID, inRoom[0].ClassName, MatchMatched, ""
	case len(inRoom) > 1:
		return 0, "", MatchAmbiguous, fmt.Sprintf("本寝登记有 %d 名同名学生，需人工指定具体班级", len(inRoom))
	}

	// 本寝查无此人：再查全校名册，把"寝室报错/冒名"与"名册没录"这两种情况区分开
	var elsewhere []model.Student
	repository.DB.Where("real_name = ? AND status = ?", rawName, "active").
		Order("building asc, room_number asc").Limit(3).Find(&elsewhere)
	if len(elsewhere) > 0 {
		places := make([]string, 0, len(elsewhere))
		for _, s := range elsewhere {
			places = append(places, fmt.Sprintf("%s %s室", s.Building, s.RoomNumber))
		}
		return 0, "", MatchUnmatched, fmt.Sprintf("该姓名在册于 %s，不在本次上报的 %s 室，疑似寝室填报有误或冒名",
			strings.Join(places, "、"), room)
	}

	return 0, "", MatchUnmatched, "全校宿位名册中查无此人，请先核对名册是否已录入"
}
