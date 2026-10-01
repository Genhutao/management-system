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
	MatchMatched      = "matched"       // 本寝名册唯一匹配，可直接打表
	MatchAmbiguous    = "ambiguous"     // 本寝有多名同名学生，需人工指定
	MatchUnmatched    = "unmatched"     // 全校名册都查无此人，禁止直接打表
	MatchRoomMismatch = "room_mismatch" // 人在册但不在本寝，疑似寝室填报有误，需核实
)

var (
	rosterSeparators = regexp.MustCompile(`[\n\r、，,;；|／/]+|[ \t　]+`)
	leadingOrdinal   = regexp.MustCompile(`^\s*(?:\d+\s*[.:：、\)]|第\d+[人次])\s*`)
	trailingPunct    = regexp.MustCompile(`[\s。.：:~～\-—]+$`)
	nonNameMarker    = regexp.MustCompile(`(号楼|宿舍|寝室|班级|高一|高二|高三|\d+班)`)
	hanName          = regexp.MustCompile(`^[\p{Han}]{2,6}$`)
	// 宽松姓名：2~12 个 汉字/字母/间隔号·/下划线 的组合，收少数民族姓名（买买提·艾力）、
	// 拼音连写（LiHua）与下划线连接的写法（李_华）。至少要有 2 个汉字/字母位，
	// 纯下划线或纯间隔号不算。真正的防冒名闸门是名册核对，这里只是存底过滤，
	// 匹配不上名册的条目照样禁止直接打表。
	looseNameChars  = regexp.MustCompile(`^[\p{Han}A-Za-z_·]{2,12}$`)
	looseNameLetter = regexp.MustCompile(`[\p{Han}A-Za-z]`)
)

// splitByRosterSeparators 名单拆分的公共骨架：按分隔符切块、去编号、去尾部标点、
// 去重限条数；"什么算一个姓名"由 accept 决定——严格口径用于从描述正文里碰运气，
// 宽松口径用于宿管显式提交的名单。
func splitByRosterSeparators(raw string, accept func(string) bool) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, 16)

	for _, token := range rosterSeparators.Split(raw, -1) {
		token = leadingOrdinal.ReplaceAllString(token, "")
		token = trailingPunct.ReplaceAllString(strings.TrimSpace(token), "")
		if token == "" || seen[token] {
			continue
		}
		// 含楼栋/班级残留或纯数字的片段不是姓名
		if nonNameMarker.MatchString(token) || !accept(token) {
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

// SplitRosterNames 严格口径：只认 2~6 个纯汉字。
// 用于从申报正文兜底拆名的场景——描述文字的碎片（"床铺不整"之类）不能变成假名单。
func SplitRosterNames(raw string) []string {
	return splitByRosterSeparators(raw, hanName.MatchString)
}

// SplitSubjectNames 宽松口径：宿管显式提交的名单。
// 收少数民族姓名（买买提·艾力）、拼音连写（LiHua）与带下划线的写法（李_华）。
// 空格仍是分隔符，"Li Hua" 会拆成 Li、Hua 两条，拼音全名请连写或用间隔号。
func SplitSubjectNames(raw string) []string {
	return splitByRosterSeparators(raw, func(token string) bool {
		if !looseNameChars.MatchString(token) {
			return false
		}
		return len(looseNameLetter.FindAllString(token, -1)) >= 2
	})
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
		return 0, "", MatchRoomMismatch, fmt.Sprintf("该姓名在册于 %s，不在本次上报的 %s 室，疑似寝室填报有误或冒名",
			strings.Join(places, "、"), room)
	}

	return 0, "", MatchUnmatched, "全校宿位名册中查无此人，请先核对名册是否已录入"
}
