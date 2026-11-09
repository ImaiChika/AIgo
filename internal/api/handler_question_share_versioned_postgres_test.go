//go:build integration

package api

import (
	"encoding/json"
	"testing"

	"aigo/internal/domain"
)

func TestVersionedSharingAPIRejectsStaleSelectionsAndFreezesBulkItems(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	if _, err := s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, append(append([]string{}, owner.DirectPermissions...), domain.PermQuestionShare), nil, nil); err != nil {
		t.Fatal(err)
	}
	q := seedPassedQuestion(t, s, h, teacher, v, "share-api-guard", "患者，男，40岁。病例。最可能的诊断是")
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	q = *mustLibraryQuestion(t, s, q.ID)
	guard := func(q domain.A2Question) map[string]any {
		return map[string]any{"expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision}
	}
	single := "/api/questions/" + q.ID + "/share"
	for _, tc := range []struct {
		name string
		body any
		want int
	}{
		{"missing", map[string]any{}, 400},
		{"negative", map[string]any{"expected_version": 1, "expected_library_revision": q.LibraryRevision, "expected_review_revision": -1}, 400},
		{"stale_content", map[string]any{"expected_version": q.Version + 1, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision}, 409},
		{"stale_library", map[string]any{"expected_version": q.Version, "expected_library_revision": q.LibraryRevision + 1, "expected_review_revision": q.ReviewRevision}, 409},
		{"stale_review", map[string]any{"expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision + 1}, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serveGenerateJSON(t, h, "POST", single, teacher, tc.body)
			if r.Code != tc.want {
				t.Fatalf("%d want%d %s", r.Code, tc.want, r.Body)
			}
		})
	}
	preview := serveGenerateJSON(t, h, "POST", "/api/question-shares/preview", teacher, map[string]any{})
	var selection struct {
		Items []domain.QuestionShareExpectation `json:"items"`
		Total int                               `json:"total"`
	}
	if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &selection) != nil || selection.Total != 1 || len(selection.Items) != 1 || selection.Items[0].ExpectedVersion != q.Version || selection.Items[0].ExpectedLibraryRevision == nil || selection.Items[0].ExpectedReviewRevision == nil {
		t.Fatal(preview.Code, preview.Body)
	}
	for _, tc := range []struct {
		name string
		body any
		want int
	}{
		{"missing_bulk_guards", map[string]any{"items": []map[string]any{{"question_id": q.ID, "expected_version": 1}}}, 400},
		{"duplicate", map[string]any{"items": append(selection.Items, selection.Items...)}, 400},
		{"mixed", map[string]any{"items": selection.Items, "question_ids": []string{q.ID}}, 400},
		{"legacy_zero_round", map[string]any{"question_ids": []string{q.ID}}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := serveGenerateJSON(t, h, "POST", "/api/question-shares/batch", teacher, tc.body)
			if r.Code != tc.want {
				t.Fatalf("%d want%d %s", r.Code, tc.want, r.Body)
			}
			request, _ := s.shareStore.GetQuestionShareByQuestionID(t.Context(), q.ID)
			if request != nil {
				t.Fatal("invalid request persisted")
			}
		})
	}
	changed := guard(q)
	changed["clinical_stem"] = "患者，男，40岁。老师修改后的病例。最可能的诊断是"
	if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, teacher, changed); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/question-shares/batch", teacher, map[string]any{"items": selection.Items}); r.Code != 409 {
		t.Fatal("preview silently used latest version", r.Code, r.Body)
	}
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	q = *mustLibraryQuestion(t, s, q.ID)
	fresh := serveGenerateJSON(t, h, "POST", single, teacher, guard(q))
	if fresh.Code != 201 {
		t.Fatal(fresh.Code, fresh.Body)
	}
	var shared questionShareView
	if err := json.Unmarshal(fresh.Body.Bytes(), &shared); err != nil {
		t.Fatal(err)
	}
	if shared.Request.QuestionVersion != 2 || shared.Request.LibraryRevision != q.LibraryRevision || shared.Request.Revision != 1 {
		t.Fatal(shared)
	}
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.212")
	reviewURL := "/api/question-shares/" + shared.Request.ID + "/review"
	if r := serveGenerateJSON(t, h, "POST", reviewURL, admin, map[string]any{"status": "approved", "expected_share_revision": 2}); r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", reviewURL, admin, map[string]any{"status": "approved", "expected_share_revision": 1}); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", reviewURL, admin, map[string]any{"status": "rejected", "expected_share_revision": 1}); r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
}
