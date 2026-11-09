//go:build integration

package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/workflowtrace"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWorkflowDiagnosticsBoundedPrivateAndLiveAuthorization(t *testing.T) {
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	runID := "diagnostics-owned-run"
	response := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(runID, p, 1))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	owner, e := s.authSvc.GetUserByUsername("admin")
	if e != nil {
		t.Fatal(e)
	}
	stamp := time.Date(2026, 10, 23, 8, 0, 0, 1, time.UTC)
	for i := 0; i < 27; i++ {
		r := workflowtrace.Record{ID: fmt.Sprintf("diag-row-%03d", i), Operation: "generate:" + runID, Phase: "generation", Attempt: i + 1, WorkflowVersion: "knowledge-v3", StartedAt: stamp.Add(time.Duration(i) * time.Nanosecond), Outcome: "committed", QuestionIDs: []string{"PRIVATE-QUESTION"}, Stages: []workflowtrace.Stage{{Name: "json_parse", Status: "parsed", Input: workflowtrace.Fingerprint{SHA256: "PRIVATE-HASH", Bytes: 999}}}}
		if i == 0 {
			call := llm.ModelCallRecord{ID: "diag-call", Operation: r.Operation, Attempt: r.Attempt, Purpose: llm.PurposeGeneration, Stage: "model_call", StartedAt: stamp, Model: "PRIVATE-MODEL", ConfigID: "PRIVATE-CONFIG", TotalTokens: 177, HTTPAttempted: true, Outcome: "returned", ToolEventsObserved: true}
			if e = store.SaveModelCall(t.Context(), call); e != nil {
				t.Fatal(e)
			}
			r.ModelCalls = []workflowtrace.ModelReference{{ID: call.ID, Recorded: true}}
		}
		if e = store.SaveWorkflowExecution(t.Context(), r); e != nil {
			t.Fatal(e)
		}
	}
	base := "/api/generation-runs/" + runID + "/diagnostics?limit=5"
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	for {
		path := base
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		r := serveGenerateJSON(t, h, "GET", path, token, nil)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		for _, private := range []string{"PRIVATE-HASH", "PRIVATE-QUESTION", "PRIVATE-MODEL", "PRIVATE-CONFIG", "total_tokens", "input_sha256", "knowledge_basis", "RequestJSON"} {
			if strings.Contains(r.Body.String(), private) {
				t.Fatal("diagnostic privacy regression", private)
			}
		}
		var data struct {
			Entries []diagnosticEntry `json:"entries"`
			Next    string            `json:"next_cursor"`
		}
		if e = json.Unmarshal(r.Body.Bytes(), &data); e != nil {
			t.Fatal(e)
		}
		if len(data.Entries) > 5 {
			t.Fatal("unbounded page")
		}
		for _, entry := range data.Entries {
			if seen[entry.ID] {
				t.Fatal("duplicate cursor row", entry.ID)
			}
			seen[entry.ID] = true
		}
		pages++
		cursor = data.Next
		if cursor == "" {
			break
		}
		if pages > 10 {
			t.Fatal("cursor did not advance")
		}
	}
	if len(seen) != 28 {
		t.Fatal("lost rows at PostgreSQL timestamp precision", len(seen))
	}
	for _, query := range []string{"?limit=21", "?limit=0", "?cursor=bad-json"} {
		r := serveGenerateJSON(t, h, "GET", "/api/generation-runs/"+runID+"/diagnostics"+query, token, nil)
		if r.Code != 400 {
			t.Fatal(query, r.Code)
		}
	}
	userResponse := serveGenerateJSON(t, h, "POST", "/api/users", token, map[string]any{"username": "diag-other", "password": "diagnostics-password", "permissions": []string{domain.PermQuestionGenerate, domain.PermBatchRun, domain.PermQuestionViewGlobal, domain.PermKBView}})
	var other auth.User
	if userResponse.Code != 201 || json.Unmarshal(userResponse.Body.Bytes(), &other) != nil {
		t.Fatal(userResponse.Code, userResponse.Body)
	}
	otherToken := loginForAuthTest(t, h, other.Username, "diagnostics-password", "198.51.100.211")
	if r := serveGenerateJSON(t, h, "GET", base, otherToken, nil); r.Code != 404 {
		t.Fatal("global viewing leaked another author's diagnostics", r.Code)
	}
	for _, id := range []string{"diag-job", "diag-job:other"} {
		if e = store.SaveBatchJob(t.Context(), storage.BatchJobRecord{ID: id, OwnerID: owner.ID, Status: "completed", TotalCount: 2, PointsJSON: "[]", OutputJSON: "{}"}); e != nil {
			t.Fatal(e)
		}
	}
	for i, operation := range []string{"batch:diag-job:0", "batch:diag-job:1", "batch:diag-job:submission", "batch:diag-job:other:0"} {
		if e = store.SaveWorkflowExecution(t.Context(), workflowtrace.Record{ID: fmt.Sprintf("batchdiag-%d", i), Operation: operation, Phase: "generation", Attempt: 1, StartedAt: stamp, Outcome: "committed", Stages: []workflowtrace.Stage{}}); e != nil {
			t.Fatal(e)
		}
	}
	r := serveGenerateJSON(t, h, "GET", "/api/batch/status/diag-job/diagnostics", token, nil)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var batchData struct {
		Entries []diagnosticEntry `json:"entries"`
	}
	if e = json.Unmarshal(r.Body.Bytes(), &batchData); e != nil || len(batchData.Entries) != 3 {
		t.Fatal("batch namespace crossed or rows missing", r.Body, e)
	}
	if r = serveGenerateJSON(t, h, "GET", "/api/batch/status/diag-job/diagnostics", otherToken, nil); r.Code != 404 {
		t.Fatal("foreign batch diagnostics readable", r.Code)
	}
	if _, e = store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'kb:view') WHERE id IN ('super_admin','admin')`); e != nil {
		t.Fatal(e)
	}
	if r = serveGenerateJSON(t, h, "GET", base, token, nil); r.Code != 403 {
		t.Fatal("old token retained KB diagnostic grant", r.Code)
	}
	if r = serveGenerateJSON(t, h, "GET", "/api/batch/status/diag-job/diagnostics", token, nil); r.Code != 200 {
		t.Fatal("direct diagnostic incorrectly depends on KB grant", r.Code)
	}
	if g, c := client.counts(); g != 0 || c != 0 {
		t.Fatal("reading diagnostics invoked model", g, c)
	}
}
func TestWorkflowDiagnosticMissingEvidenceIsNotGenerationFailure(t *testing.T) {
	s, store, h, token, _, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	owner, _ := s.authSvc.GetUserByUsername("admin")
	if _, e := store.CreateGenerationRun(t.Context(), domain.GenerationRun{ID: "diagnostic-empty-run", OwnerID: owner.ID, Status: domain.GenerationRunPending, RequestedCount: 1, RequestJSON: json.RawMessage(`{"request":{}}`), MaxAttempts: 3}); e != nil {
		t.Fatal(e)
	}
	r := serveGenerateJSON(t, h, "GET", "/api/generation-runs/diagnostic-empty-run/diagnostics", token, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"availability":"not_recorded"`) {
		t.Fatal(r.Code, r.Body)
	}
	run, e := store.GetGenerationRun(t.Context(), "diagnostic-empty-run")
	if e != nil || run.Status != domain.GenerationRunPending {
		t.Fatal("read changed task state", run, e)
	}
}
