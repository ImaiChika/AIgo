//go:build integration

package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/postgres"
)

func TestC4ReportUnassignedReviewerIsForbiddenWithoutVote(t *testing.T) {
	s, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	h := s.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.240")
	for _, name := range []string{"report-assigned", "report-unassigned"} {
		r := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": "report-recheck-password", "permissions": []string{domain.PermReviewDo, domain.PermReviewFinal}})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
	}
	assigned, _ := s.authSvc.GetUserByUsername("report-assigned")
	q := domain.A2Question{ID: "report-not-assigned", OwnerID: "owner", ClinicalStem: "男，40岁。胸痛2小时。该患者最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}}, Answer: "A", Status: domain.StatusAIReviewed, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "report-direct", Name: "报告复现", CompletionMode: domain.ReviewCompletionOnLastRound, Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{assigned.ID}, RequiredCount: 1}}}
	if err := s.reviewSvc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	task, err := s.reviewSvc.SubmitQuestionForOwner(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	token := loginForAuthTest(t, h, "report-unassigned", "report-recheck-password", "198.51.100.241")
	r := serveGenerateJSON(t, h, "POST", "/api/review/action", token, map[string]any{"task_id": task.ID, "action": "approved"})
	if r.Code != http.StatusForbidden {
		t.Fatalf("unassigned qualification must be 403, got %d %s", r.Code, r.Body)
	}
	records, err := store.ListRecordsByTaskID(t.Context(), task.ID)
	if err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
	current, _ := store.GetTask(t.Context(), task.ID)
	if current.Status != domain.StatusReviewing {
		t.Fatal(current)
	}
}

func TestC4ReportZeroReviewRevisionIsAnExplicitReadGuard(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, v, "report-zero-revision", "男，40岁。病例。该患者最可能的诊断是")
	for _, path := range []string{"/api/questions/my-new", "/api/questions/" + q.ID} {
		t.Run(path, func(t *testing.T) {
			r := serveGenerateJSON(t, h, "GET", path, teacher, nil)
			if r.Code != 200 {
				t.Fatal(r.Code, r.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rows, ok := body["questions"].([]any); ok {
				if len(rows) != 1 {
					t.Fatal(body)
				}
				body = rows[0].(map[string]any)
			}
			value, exists := body["review_revision"]
			if !exists || value != float64(0) {
				t.Fatalf("initial guard must be explicit numeric 0: %s", r.Body)
			}
		})
	}
}

func TestC4ReportLegacyBulkAndInitialEvidenceContracts(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	if _, err := s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, append(append([]string{}, owner.DirectPermissions...), domain.PermQuestionShare), nil, nil); err != nil {
		t.Fatal(err)
	}
	q := seedPassedQuestion(t, s, h, teacher, v, "report-legacy-bulk", "男，40岁。病例。该患者最可能的诊断是")
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	r := serveGenerateJSON(t, h, "POST", "/api/question-shares/batch", teacher, map[string]any{"question_ids": []string{q.ID}})
	var result struct{ Submitted, Skipped int }
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || result.Submitted != 0 || result.Skipped != 1 {
		t.Fatal(r.Code, r.Body)
	}
	if share, _ := s.shareStore.GetQuestionShareByQuestionID(t.Context(), q.ID); share != nil {
		t.Fatal("legacy IDs bypassed zero-round guards")
	}
	current := mustLibraryQuestion(t, s, q.ID)
	current.Status = domain.StatusPublished
	current.ConfirmationSource = domain.ConfirmSourceReview
	current.LibraryRevision++
	if err := s.questionStore.SaveQuestion(storage.WithLibraryRevision(t.Context(), current.LibraryRevision-1), *current); err != nil {
		t.Fatal(err)
	}
	store := s.questionStore.(*postgres.Store)
	if _, err := store.DB().ExecContext(t.Context(), `DELETE FROM ai_review_results WHERE question_id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/share", teacher, nil)
	if r.Code != 409 {
		t.Fatalf("review source is not initial-check evidence: %d %s", r.Code, r.Body)
	}
}
