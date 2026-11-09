//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestAuthorCancelCapabilityPreservesOpinionIsolationAndPermissions(t *testing.T) {
	s, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	h := s.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.230")
	for _, spec := range []struct{ name, role string }{{"cancel-owner", "teacher"}, {"cancel-r1", "expert"}, {"cancel-r2", "expert"}} {
		res := serveAuthJSON(t, h, http.MethodPost, "/api/users", admin, "198.51.100.230", map[string]any{"username": spec.name, "password": "c4-cancel-password", "display_name": spec.name, "role": spec.role, "roles": []string{spec.role}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", spec.name, res.Code, res.Body)
		}
	}
	owner, _ := s.authSvc.GetUserByUsername("cancel-owner")
	r1, _ := s.authSvc.GetUserByUsername("cancel-r1")
	r2, _ := s.authSvc.GetUserByUsername("cancel-r2")
	token := loginForAuthTest(t, h, "cancel-owner", "c4-cancel-password", "198.51.100.231")
	reviewerToken := loginForAuthTest(t, h, "cancel-r1", "c4-cancel-password", "198.51.100.232")
	q := domain.A2Question{ID: "cancel-capability-q", OwnerID: owner.ID, CreatedBy: owner.Username, ClinicalStem: "患者，男，50岁，持续胸痛2小时。最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}}, Answer: "A", Version: 1, Status: domain.StatusAIReviewed, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "cancel-capability-ai", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "cancel-capability-flow", Name: "可选一轮", CompletionMode: domain.ReviewCompletionOnLastRound, Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{r1.ID, r2.ID}, RequiredCount: 2}}}
	if err := s.reviewSvc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	submit := func() domain.ReviewTask {
		t.Helper()
		res := serveAuthJSON(t, h, http.MethodPost, "/api/review/submit", token, "198.51.100.231", map[string]any{"question_id": q.ID, "flow_id": flow.ID})
		var task domain.ReviewTask
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &task) != nil {
			t.Fatalf("submit: %d %s", res.Code, res.Body)
		}
		return task
	}
	capability := func(token string, want bool) reviewTaskDetail {
		t.Helper()
		res := serveAuthJSON(t, h, http.MethodGet, "/api/review/task-by-question/"+q.ID, token, "198.51.100.231", nil)
		var detail reviewTaskDetail
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &detail) != nil || detail.CanCancelSubmission != want {
			t.Fatalf("capability want %v: %d %s", want, res.Code, res.Body)
		}
		if strings.Contains(res.Body.String(), "library_before_submit") {
			t.Fatal("private snapshot exposed")
		}
		return detail
	}
	task := submit()
	capability(token, true)
	capability(admin, false)
	proxy := serveAuthJSON(t, h, http.MethodPost, "/api/review/action", admin, "198.51.100.230", map[string]any{"task_id": task.ID, "expert_id": r1.ID, "action": "approved"})
	if proxy.Code != http.StatusForbidden {
		t.Fatalf("unassigned admin impersonated direct-round reviewer: %d %s", proxy.Code, proxy.Body)
	}
	if records, err := store.ListRecordsByTaskID(t.Context(), task.ID); err != nil || len(records) != 0 {
		t.Fatal("refused proxy left a review record", records, err)
	}
	// Revocation is evaluated using the current working role; reading one's task
	// remains available, and no review:do/final permission is granted.
	if _, err := store.DB().ExecContext(t.Context(), `UPDATE roles SET permissions=array_remove(permissions,'review:submit') WHERE id='teacher'`); err != nil {
		t.Fatal(err)
	}
	capability(token, false)
	if _, err := store.DB().ExecContext(t.Context(), `UPDATE roles SET permissions=array_append(permissions,'review:submit') WHERE id='teacher'`); err != nil {
		t.Fatal(err)
	}
	capability(token, true)
	current, _ := store.GetQuestion(t.Context(), q.ID)
	payload := map[string]any{"task_id": task.ID, "expected_version": current.Version, "expected_library_revision": current.LibraryRevision, "expected_review_revision": current.ReviewRevision}
	for _, key := range []string{"expected_version", "expected_library_revision", "expected_review_revision"} {
		omitted := map[string]any{}
		for k, v := range payload {
			if k != key {
				omitted[k] = v
			}
		}
		res := serveAuthJSON(t, h, http.MethodPost, "/api/review/cancel", token, "198.51.100.231", omitted)
		if res.Code != 400 {
			t.Fatalf("missing %s accepted: %d %s", key, res.Code, res.Body)
		}
	}
	res := serveAuthJSON(t, h, http.MethodPost, "/api/review/cancel", token, "198.51.100.231", payload)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"status":"archived"`) {
		t.Fatalf("cancel: %d %s", res.Code, res.Body)
	}
	capability(token, false)
	task = submit()
	res = serveAuthJSON(t, h, http.MethodPost, "/api/review/action", reviewerToken, "198.51.100.232", map[string]any{"task_id": task.ID, "action": "approved", "opinion": "隔离评语不可泄露"})
	if res.Code != 200 {
		t.Fatalf("review: %d %s", res.Code, res.Body)
	}
	detail := capability(token, false)
	if detail.RoundResults[0].ApprovedCount != 0 || len(detail.RoundResults[0].Reviews) != 0 {
		t.Fatal("author capability leaked isolated real-time votes", detail)
	}
	current, _ = store.GetQuestion(t.Context(), q.ID)
	res = serveAuthJSON(t, h, http.MethodPost, "/api/review/cancel", token, "198.51.100.231", map[string]any{"task_id": task.ID, "expected_version": current.Version, "expected_library_revision": current.LibraryRevision, "expected_review_revision": current.ReviewRevision})
	if res.Code != 409 {
		t.Fatalf("voted task canceled: %d %s", res.Code, res.Body)
	}
}
