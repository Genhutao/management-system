package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"xgh-system/internal/model"
	"xgh-system/internal/repository"
	"xgh-system/internal/service"
)

// 上传后补名单的断言重点：越权与已转打表必须挡死；只增不删；
// 补报要在复核留痕里留下补了谁，且绝不碰 status 与 ai_status。

func runAppendSubjects(t *testing.T, uid, id uint, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/dorm/inspections/subjects", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: fmt.Sprint(id)}}
	c.Set("user_id", uid)
	c.Set("role", model.RoleDormManager)
	c.Set("real_name", "宿管甲")
	(&DormController{}).AppendInspectionSubjects(c)
	return w
}

func TestAppendSubjectsAddsMatchesAndSkipsDuplicates(t *testing.T) {
	uid := setupAnalyzeDB(t)
	id := seedStreamInspection(t, uid, "uploaded")
	if err := repository.DB.Create(&model.Student{RealName: "王小明", ClassName: "高一(1)班", Building: "7号楼", RoomNumber: "701", Status: "active"}).Error; err != nil {
		t.Fatalf("写入名册失败: %v", err)
	}
	repository.DB.Create(&model.InspectionSubject{InspectionID: id, RawName: "李华", MatchStatus: "unmatched"})

	w := runAppendSubjects(t, uid, id, `{"names":"王小明、李华、赵大锤"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("补报应成功，实际 %d: %s", w.Code, w.Body.String())
	}

	var subjects []model.InspectionSubject
	repository.DB.Where("inspection_id = ?", id).Order("id asc").Find(&subjects)
	if len(subjects) != 3 {
		t.Fatalf("应有 3 条名单（原 1 + 新增 2），实际 %d", len(subjects))
	}
	if subjects[1].RawName != "王小明" || subjects[1].MatchStatus != service.MatchMatched || subjects[1].StudentID == 0 {
		t.Fatalf("王小明应与 7号楼701 名册匹配，实际 %+v", subjects[1])
	}
	if subjects[2].RawName != "赵大锤" || subjects[2].MatchStatus != service.MatchUnmatched {
		t.Fatalf("赵大锤应保持未匹配，实际 %+v", subjects[2])
	}

	rec := loadInspection(t, id)
	if rec.Status != "uploaded" || rec.AIStatus != aiStatusPending {
		t.Fatalf("补名单不得改状态，实际 status=%s ai_status=%s", rec.Status, rec.AIStatus)
	}
	if !strings.Contains(rec.ReviewNote, "补报名单") || !strings.Contains(rec.ReviewNote, "王小明、赵大锤") {
		t.Fatalf("复核留痕应记录补报内容，实际 %q", rec.ReviewNote)
	}
}

func TestAppendSubjectsForbiddenOnOthersRecordAndMissingSameAs404(t *testing.T) {
	uid := setupAnalyzeDB(t)
	id := seedStreamInspection(t, uid, "uploaded")

	w := runAppendSubjects(t, uid+999, id, `{"names":"张三"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("他人记录应 403，实际 %d: %s", w.Code, w.Body.String())
	}

	w = runAppendSubjects(t, uid, 424242, `{"names":"张三"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("不存在的记录应 404，实际 %d", w.Code)
	}
}

func TestAppendSubjectsRejectsConvertedEmptyAndAllDuplicates(t *testing.T) {
	uid := setupAnalyzeDB(t)
	id := seedStreamInspection(t, uid, "uploaded")
	repository.DB.Create(&model.InspectionSubject{InspectionID: id, RawName: "李华", MatchStatus: "unmatched"})

	converted := seedStreamInspection(t, uid, "converted")
	if w := runAppendSubjects(t, uid, converted, `{"names":"张三"}`); w.Code != http.StatusConflict {
		t.Fatalf("已转打表应 409，实际 %d: %s", w.Code, w.Body.String())
	}

	if w := runAppendSubjects(t, uid, id, `{"names":"   "}`); w.Code != http.StatusBadRequest {
		t.Fatalf("空名单应 400，实际 %d", w.Code)
	}
	if w := runAppendSubjects(t, uid, id, `{"names":"李华"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("全部重复应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	// 显式名单走宽松拆分：拼音/间隔号姓名应正常收录
	if w := runAppendSubjects(t, uid, id, `{"names":"Li Hua"}`); w.Code != http.StatusOK {
		t.Fatalf("宽松规则下 Li Hua 应拆为 Li、Hua 收录，实际 %d: %s", w.Code, w.Body.String())
	}
	// 完全拆不出有效姓名时，必须给出"转写成中文名"的明确提示，
	// 而不是误导性的"请填写名单"（使用方实测踩过：填错被拒却不知原因）。
	w := runAppendSubjects(t, uid, id, `{"names":"705!!!"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无效名单应 400，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "名单里没拆出有效姓名") {
		t.Fatalf("应提示有效姓名规则，实际 %s", w.Body.String())
	}
}

// 上传主流程同样不得静默丢名单：显式填了拆不出姓名的名单要在落库前拒绝；
// 显式名单走宽松拆分，少数民族间隔号姓名与拼音连写应正常收录。
func TestUploadExplicitNamesLooseRules(t *testing.T) {
	uid := setupAnalyzeDB(t)

	if w := runUpload(t, uid, map[string]string{
		"room_number": "701", "photo_type": "violation", "report_kind": "photo",
		"subject_names": "买买提·艾力、LiHua", "note_text": "705 寝室插座发黑", "analyze": "false",
	}, "shot.png"); w.Code != http.StatusOK {
		t.Fatalf("宽松规则下特殊姓名应收录，实际 %d: %s", w.Code, w.Body.String())
	}

	w := runUpload(t, uid, map[string]string{
		"room_number": "701", "photo_type": "violation", "report_kind": "photo",
		"subject_names": "705!!!", "analyze": "false",
	}, "shot.png")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("显式无效名单应 400，实际 %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "名单里没拆出有效姓名") {
		t.Fatalf("应提示有效姓名规则，实际 %s", w.Body.String())
	}

	// 纯文本申报整条就是名单，拆不出姓名同样拒绝
	w = runUpload(t, uid, map[string]string{
		"room_number": "701", "photo_type": "violation", "report_kind": "text",
		"note_text": "Li Hua", "analyze": "false",
	}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("纯文本申报拆不出姓名应 400，实际 %d: %s", w.Code, w.Body.String())
	}

	// 照片类上报的 note_text 只是描述文字，没有姓名也必须照常入库
	w = runUpload(t, uid, map[string]string{
		"room_number": "705", "photo_type": "violation", "report_kind": "photo",
		"note_text": "705 寝室插座发黑", "analyze": "false",
	}, "shot.png")
	if w.Code != http.StatusOK {
		t.Fatalf("描述性 note_text 不应被拒，实际 %d: %s", w.Code, w.Body.String())
	}
}
