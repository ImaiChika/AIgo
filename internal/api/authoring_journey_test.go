//go:build integration

package api

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthoringJourneyOwnershipCountsAndTaskFilters(t *testing.T) {
	s, store, h, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	create := func(name string) string {
		r := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": "journey-owned-fixture", "permissions": []string{domain.PermQuestionView, domain.PermQuestionConfirm, domain.PermQuestionDownload, domain.PermQuestionGenerate, domain.PermBatchRun}})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
		return loginForAuthTest(t, h, name, "journey-owned-fixture", "198.51.100.217")
	}
	owner, peer := create("journey-owner"), create("journey-peer")
	u, _ := s.authSvc.GetUserByUsername("journey-owner")
	other, _ := s.authSvc.GetUserByUsername("journey-peer")
	call := func(method, path, token string, input any, want int) []byte {
		t.Helper()
		r := serveGenerateJSON(t, h, method, path, token, input)
		if r.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, r.Code, r.Body)
		}
		return r.Body.Bytes()
	}
	q1 := seedCustomerBrowserQuestion(t, s, u.ID, u.Username, point, "journey-q1", "男，45岁。待整理，上腹痛2天。该患者最可能的诊断是")
	q2 := seedCustomerBrowserQuestion(t, s, u.ID, u.Username, point, "journey-q2", "男，45岁。已确认，上腹痛2天。该患者最可能的诊断是")
	q3 := seedCustomerBrowserQuestion(t, s, u.ID, u.Username, point, "journey-q3", "男，45岁。删除留档，上腹痛2天。该患者最可能的诊断是")
	seedCustomerBrowserQuestion(t, s, u.ID, u.Username, point, "journey-history", "男，45岁。其他历史题，上腹痛2天。该患者最可能的诊断是")
	foreign := seedCustomerBrowserQuestion(t, s, other.ID, other.Username, point, "journey-foreign", "男，45岁。他人正文不可泄露，上腹痛2天。该患者最可能的诊断是")
	call("POST", "/api/questions/confirm", owner, map[string]any{"items": []any{libraryItemForAPI(q2)}}, 200)
	call("POST", "/api/questions/library-delete", owner, map[string]any{"items": []any{libraryItemForAPI(q3)}}, 200)
	staging := q1
	staging.ID = "journey-staging"
	staging.Status = domain.StatusAutoChecked
	staging.LibraryState = domain.LibraryUnfiled
	staging.ConfirmedVersion = 0
	if e := store.SaveQuestion(t.Context(), staging); e != nil {
		t.Fatal(e)
	}
	for _, x := range []struct{ id, verdict string }{{"journey-reject", "reject"}, {"journey-fault", "check_failed"}} {
		if e := store.SaveDiscardResult(t.Context(), domain.AICheckDiscard{ID: x.id, QuestionID: x.id, OwnerID: u.ID, Verdict: x.verdict}); e != nil {
			t.Fatal(e)
		}
	}
	ids := []string{q1.ID, q2.ID, q3.ID, foreign.ID, staging.ID, "journey-reject", "journey-fault"}
	if _, e := store.CreateGenerationRun(t.Context(), domain.GenerationRun{ID: "journey-owned", OwnerID: u.ID, Status: "succeeded", RequestedCount: 7, QuestionIDs: ids, RequestJSON: json.RawMessage(`{"secret":"NEVER-SEND"}`)}); e != nil {
		t.Fatal(e)
	}
	data := call("GET", "/api/authoring-journey?task_kind=single&task_id=journey-owned", owner, nil, 200)
	var progress struct {
		Counts storage.AuthoringCounts `json:"counts"`
	}
	if e := json.Unmarshal(data, &progress); e != nil {
		t.Fatal(e)
	}
	if progress.Counts.Arrange != 1 || progress.Counts.Confirmed != 1 || progress.Counts.Deleted != 1 || progress.Counts.Checking != 1 || progress.Counts.Rejected != 1 || progress.Counts.CheckFailed != 1 {
		t.Fatalf("wrong task counts %+v", progress)
	}
	if strings.Contains(string(data), "NEVER-SEND") || strings.Contains(string(data), foreign.ID) || strings.Contains(string(data), "question_ids") {
		t.Fatal("guide returned private execution material or identifiers")
	}
	for _, token := range []string{peer, admin} {
		call("GET", "/api/authoring-journey?task_kind=single&task_id=journey-owned", token, nil, 404)
		call("GET", "/api/questions/my-new?task_kind=single&task_id=journey-owned", token, nil, 404)
	}
	call("GET", "/api/authoring-journey?task_kind=invalid&task_id=x", owner, nil, 400)
	check := func(path, token string, want int, first string) {
		t.Helper()
		var result struct {
			Questions []domain.A2Question `json:"questions"`
			Total     int                 `json:"total"`
			Task      map[string]string   `json:"task"`
		}
		json.Unmarshal(call("GET", path, token, nil, 200), &result)
		if result.Total != want || len(result.Questions) != want || want > 0 && result.Questions[0].ID != first || result.Task["id"] == "" {
			t.Fatalf("task filter failed: %+v", result)
		}
	}
	check("/api/questions/my-new?task_kind=single&task_id=journey-owned", owner, 1, q1.ID)
	check("/api/questions?scope=personal&library=active&task_kind=single&task_id=journey-owned", owner, 1, q2.ID)
	call("GET", "/api/questions?scope=global&task_kind=single&task_id=journey-owned", admin, nil, 400)
	var exported struct {
		Count int `json:"count"`
	}
	json.Unmarshal(call("POST", "/api/export/xlsx", owner, map[string]any{"export_all": true, "scope": "personal", "task_kind": "single", "task_id": "journey-owned"}, 200), &exported)
	if exported.Count != 1 {
		t.Fatal("task export included history", exported)
	}
	call("POST", "/api/export/xlsx", owner, map[string]any{"question_ids": []string{"journey-history"}, "scope": "personal", "task_kind": "single", "task_id": "journey-owned"}, 400)
	if _, e := store.CreateGenerationRun(t.Context(), domain.GenerationRun{ID: "journey-empty", OwnerID: u.ID, Status: "failed", RequestedCount: 1}); e != nil {
		t.Fatal(e)
	}
	check("/api/questions/my-new?task_kind=single&task_id=journey-empty", owner, 0, "")
	if e := store.SaveBatchJob(context.Background(), storage.BatchJobRecord{ID: "journey-batch", OwnerID: u.ID, Backend: "local-single-api", Status: "completed", TotalCount: 3, Completed: 2, Failed: 1, JobName: "本次批量", OutputJSON: `{"pipeline":true,"items":[{"question_id":"journey-q1"},{"question_id":"journey-q2"}]}`, ImportResult: `{"question_ids":["journey-q1","journey-q2"]}`}); e != nil {
		t.Fatal(e)
	}
	check("/api/questions/my-new?task_kind=batch&task_id=journey-batch", owner, 1, q1.ID)
	data = call("GET", "/api/authoring-journey?task_kind=batch&task_id=journey-batch", owner, nil, 200)
	if !strings.Contains(string(data), "本次批量") {
		t.Fatal(string(data))
	}
}
