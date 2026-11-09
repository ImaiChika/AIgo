//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestAuthorRevisionAllowsIndependentWordingThroughConfirmationAndRestore(t *testing.T) {
	s, h, token, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	owner, err := s.authSvc.GetUserByUsername("lib-teacher")
	if err != nil {
		t.Fatal(err)
	}
	q := domain.A2Question{ID: "free-author", OwnerID: owner.ID, CreatedBy: owner.Username, Version: 1, Status: domain.StatusAIReviewed,
		FormatVersion: domain.A2FormatFocused, ClinicalStem: "患者，男，45岁。反复上腹痛，餐后加重。该患者最可能的诊断是",
		Options: []domain.Option{{Label: "A", Text: "胃溃疡"}, {Label: "B", Text: "十二指肠溃疡"}, {Label: "C", Text: "胃癌"}, {Label: "D", Text: "慢性胃炎"}, {Label: "E", Text: "功能性消化不良"}},
		Answer:  "A", Explanation: "正确答案为A。病例符合胃溃疡。B项、C项、D项、E项均不符合本例特点。故选A。", Difficulty: "0.65", CognitiveLevel: "应用", ExamPoints: "诊断与鉴别诊断", OutlineCode: "lib.001", Profession: "消化", System: "消化系统",
		KnowledgePoints: []domain.KnowledgePoint{{ID: "lib.001", VersionID: version, Topic: "题库归属考点", Category: "临床综合", Subject: "消化"}}}
	if err := q.ValidateGeneratedA2(); err != nil {
		t.Fatal(err)
	}
	ctx := storage.WithQuestionChange(t.Context(), storage.QuestionChange{OwnerID: owner.ID, Actor: owner.Username})
	if err := s.questionStore.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := s.questionStore.(storage.AIReviewStore).SaveReviewResult(ctx, domain.AIReviewResult{ID: "free-author-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	q = *mustLibraryQuestion(t, s, q.ID)
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		r := serveGenerateJSON(t, h, method, path, token, body)
		if r.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, r.Code, r.Body)
		}
		return r.Body.Bytes()
	}
	guard := func(q *domain.A2Question) map[string]any {
		return map[string]any{"expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision}
	}
	stem, explanation := "因反复上腹痛就诊的一名中年患者。主诉：进食后不适。结合检查，应该选择哪个诊断？", "教师说明：结合病史与检查支持A；其余选项应结合临床逐一鉴别。教学提示：保留自主表述。"
	payload := guard(&q)
	payload["clinical_stem"] = stem
	payload["explanation"] = explanation
	call(http.MethodPut, "/api/questions/"+q.ID, payload, 200)
	current := mustLibraryQuestion(t, s, q.ID)
	if current.Version != 2 || current.ClinicalStem != stem || current.Explanation != explanation {
		t.Fatal("teacher wording rewritten or version missing")
	}
	if current.ValidateGeneratedA2() == nil {
		t.Fatal("AI generation validation was weakened")
	}
	call("POST", "/api/questions/confirm", libraryItemsPayload(t, s, q.ID), 200)
	for _, format := range []string{"xlsx", "docx"} {
		call("POST", "/api/export/"+format, map[string]any{"scope": "personal", "question_ids": []string{q.ID}}, 200)
	}
	current = mustLibraryQuestion(t, s, q.ID)
	payload = guard(current)
	payload["explanation"] = "另一种教师自定说明。"
	call("PUT", "/api/questions/"+q.ID, payload, 200)
	current = mustLibraryQuestion(t, s, q.ID)
	payload = guard(current)
	payload["version"] = 2
	call("POST", "/api/questions/"+q.ID+"/restore", payload, 200)
	current = mustLibraryQuestion(t, s, q.ID)
	if current.Explanation != explanation || current.LibraryState != domain.LibraryUnfiled {
		t.Fatal("restoring teacher version lost wording or confirmation guard")
	}
	call("POST", "/api/questions/confirm", libraryItemsPayload(t, s, q.ID), 200)
	// Invalid structure still cannot overwrite a valid teacher revision.
	for _, bad := range []map[string]any{{"clinical_stem": " "}, {"answer": "Z"}, {"options": []domain.Option{{Label: "A", Text: "甲"}, {Label: "A", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}}}} {
		current = mustLibraryQuestion(t, s, q.ID)
		payload = guard(current)
		for k, v := range bad {
			payload[k] = v
		}
		call("PUT", "/api/questions/"+q.ID, payload, 400)
	}
	var versions struct {
		Versions []domain.QuestionVersion `json:"versions"`
	}
	json.Unmarshal(call("GET", "/api/questions/"+q.ID+"/versions", nil, 200), &versions)
	first, err := s.questionStore.GetQuestionVersion(context.Background(), q.ID, 1)
	if err != nil || first == nil || first.Snapshot.ClinicalStem != q.ClinicalStem {
		t.Fatal("immutable original AI version changed", err)
	}
	if !strings.Contains(string(call("GET", "/api/questions/"+q.ID, nil, 200)), "教师说明") {
		t.Fatal("saved wording missing")
	}
}
