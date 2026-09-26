package controller

import (
	"encoding/json"
	"strings"
	"testing"
)

// 名册文本识别的入口判定：detectHeader 决定"首行是不是表头"，判错的代价是把第一条真实数据当表头丢掉。
// 单行粘贴时后果最明显——唯一那行被当表头跳过后，识别结果为空，前端还要为 null 崩溃。

func TestDetectHeaderRejectsDataLineMistakenForHeader(t *testing.T) {
	// 这行同时含"8栋"(带"栋")与"高三(5)班"(带"班")，旧判定会数到 2 个命中而认定它是表头
	line := "8栋 502 綾川星凛 高三(5)班 女 1号床 20230501"
	if hasHeader, _, _ := detectHeader(line); hasHeader {
		t.Fatalf("数据行被误判为表头: %q", line)
	}
}

func TestDetectHeaderAcceptsRealHeaders(t *testing.T) {
	for _, line := range []string{
		"楼栋\t寝室\t姓名\t班级\t学号",
		"宿舍楼,房间号,学生姓名,班级,年级",
		"Building Room Name Class",
	} {
		hasHeader, colMap, _ := detectHeader(line)
		if !hasHeader {
			t.Fatalf("真表头未被识别: %q", line)
		}
		if colMap["name"] < 0 {
			t.Fatalf("表头 %q 没定位到姓名列: %+v", line, colMap)
		}
	}
}

func TestSingleDataLineWithoutHeaderIsRecognized(t *testing.T) {
	got, _ := smartRecognizeStudents([]string{"8栋 502 綾川星凛 高三(5)班 女 1号床 20230501"})
	if len(got) != 1 {
		t.Fatalf("单行数据识别结果 %d 条，期望 1 条", len(got))
	}
	st := got[0]
	if st.RealName != "綾川星凛" {
		t.Errorf("姓名列错: 得到 %q", st.RealName)
	}
	if st.Building != "8栋" {
		t.Errorf("楼栋列错: 得到 %q", st.Building)
	}
	if st.RoomNumber != "502" {
		t.Errorf("寝室号错: 得到 %q", st.RoomNumber)
	}
	if st.ClassName != "高三(5)班" {
		t.Errorf("班级列错: 得到 %q", st.ClassName)
	}
	if st.Grade != "高三" {
		t.Errorf("年级列错: 得到 %q", st.Grade)
	}
}

// 表头存在时必须照常按列取值，不能因为收紧了表头判定就退化成全部走正则
func TestHeaderStillDrivesColumnMapping(t *testing.T) {
	got, _ := smartRecognizeStudents([]string{
		"楼栋\t寝室\t姓名\t班级",
		"3号楼\t1204\t欧阳明日\t高一(7)班",
	})
	if len(got) != 1 {
		t.Fatalf("带表头应只识别 1 条数据，得到 %d 条", len(got))
	}
	if got[0].RealName != "欧阳明日" || got[0].RoomNumber != "1204" || got[0].Building != "3号楼" {
		t.Fatalf("按表头取列失败: %+v", got[0])
	}
}

// 识别为空时将返回 nil 切片，编码成 JSON 就是 null；前端 renderParsePreviewReport 直接
// data.preview_sample.map(...) 会抛 TypeError，把"没识别出来"这件事变成一条看不懂的崩溃。
func TestEmptyParseResultMarshalsAsArrayNotNull(t *testing.T) {
	got, _ := smartRecognizeStudents([]string{"------------"})
	if got == nil {
		t.Fatal("空结果应为非 nil 切片，否则 JSON 编码为 null")
	}
	payload := map[string]any{"preview_sample": got}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	if want := `"preview_sample":[]`; !strings.Contains(string(raw), want) {
		t.Fatalf("空结果编码应包含 %s，实际得到 %s", want, raw)
	}
}
