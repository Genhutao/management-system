package controller

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
)

// maxReportSubjects 单次上报最多落库的被记名学生数，超出部分保留在申报正文中但不建名单条目。
const maxReportSubjects = 60

const maxNoteTextRunes = 2000

func toString(v interface{}) string {
	s, _ := v.(string)
	return s
}

// structuredActionAdvice 从一次上报已存的结构化结果里取 AI 给的处置建议。
// 识别没成功、或历史数据里根本没有 structured_json 时返回空串 ——
// 空表示这一栏要人工填，系统不许凭空气补一条"建议"出来。
func structuredActionAdvice(report *model.InspectionPhoto) string {
	if report == nil {
		return ""
	}
	raw := strings.TrimSpace(report.StructuredJSON)
	if raw == "" {
		return ""
	}
	var structured struct {
		ActionAdvice string `json:"action_advice"`
	}
	if err := json.Unmarshal([]byte(raw), &structured); err != nil {
		return ""
	}
	return strings.TrimSpace(structured.ActionAdvice)
}

// reportActionAdvice 按上报 ID 读库取处置建议，供单条手工打表预填"如何处理"。
func reportActionAdvice(inspectionID uint) string {
	if inspectionID == 0 {
		return ""
	}
	var report model.InspectionPhoto
	if err := repository.DB.Select("structured_json").First(&report, inspectionID).Error; err != nil {
		return ""
	}
	return structuredActionAdvice(&report)
}

func splitNames(raw string) []string {
	names := service.SplitRosterNames(raw)
	if len(names) > maxReportSubjects {
		return names[:maxReportSubjects]
	}
	return names
}

// noValidNameHint 名单输入非空却一个姓名都没拆出来时的统一提示。
// 宽松口径收 汉字/字母/间隔号·/下划线 组成的姓名；数字、标点、空格不算姓名字符。
const noValidNameHint = "名单里没拆出有效姓名：姓名应为 2~12 个汉字或字母（少数民族姓名用间隔号·，如 买买提·艾力；拼音请连写，如 LiHua；可用下划线连接，如 李_华），不要带数字和其他标点"

// nameInputHasContent 输入去掉分隔符与空白后是否还有实际内容。
// 分隔符集合与 service 端 SplitRosterNames 的 rosterSeparators 保持一致，避免"全是分隔符"被当成填了名单。
func nameInputHasContent(raw string) bool {
	stripped := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t', ' ', '　', '、', '，', ',', ';', '；', '|', '/', '／':
			return -1
		}
		return r
	}, raw)
	return stripped != ""
}

// splitSubjectNames 宿管显式提交的名单：宽松口径（收·名字与拼音连写）。
func splitSubjectNames(raw string) []string {
	names := service.SplitSubjectNames(raw)
	if len(names) > maxReportSubjects {
		return names[:maxReportSubjects]
	}
	return names
}

var reportKinds = map[string]bool{"photo": true, "note": true, "text": true}

// normalizeReportKind 上报形态只有 实拍 / 记名纸条 / 纯文本 三种。
// photo_type 表达的是内容类别（卫生、违纪、上工监督），二者不可混用。
func normalizeReportKind(raw string, hasImage bool, noteText string) string {
	kind := strings.ToLower(strings.TrimSpace(raw))
	if reportKinds[kind] {
		return kind
	}
	switch {
	case !hasImage:
		return "text"
	case strings.TrimSpace(noteText) != "":
		return "note"
	default:
		return "photo"
	}
}

// maskPhone PII 脱敏：保留前 3 后 4 位，其余以 * 填充；不符合 11 位手机号规则的号码整体掩码。
func maskPhone(phone string) string {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return ""
	}
	r := []rune(phone)
	if len(r) != 11 {
		return "****"
	}
	return string(r[:3]) + "****" + string(r[7:])
}

// normalizeNoteText 统一换行并去掉首尾空白，按字符数而非字节数截断，避免把汉字切坏。
func normalizeNoteText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxNoteTextRunes {
		return s
	}
	return string([]rune(s)[:maxNoteTextRunes])
}
