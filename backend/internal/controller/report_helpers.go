package controller

import (
	"strings"
	"unicode/utf8"

	"xgh-system/internal/service"
)

// maxReportSubjects 单次上报最多落库的被记名学生数，超出部分保留在申报正文中但不建名单条目。
const maxReportSubjects = 60

const maxNoteTextRunes = 2000

func toString(v interface{}) string {
	s, _ := v.(string)
	return s
}

// subjectSubmittedNamesOr 宿管显式填写的名单优先；留空时退回从申报正文里拆分。
func subjectSubmittedNamesOr(submitted, noteText string) string {
	if strings.TrimSpace(submitted) != "" {
		return submitted
	}
	return noteText
}

func splitNames(raw string) []string {
	names := service.SplitRosterNames(raw)
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
