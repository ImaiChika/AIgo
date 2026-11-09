//go:build integration

package api

import (
	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/workflowtrace"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Real HTTP client, PostgreSQL, normal handlers/queues/checker; only the model
// provider is controlled. Every returned pass includes a warning to guard policy.
func TestModelCallEvidenceSingleMultiBatchRetryAndBaselineFirstCheck(t *testing.T) {
	for _, tc := range []struct {
		name          string
		single, batch int
	}{{"small", 2, 2}, {"scale", 20, 100}} {
		t.Run(tc.name, func(t *testing.T) { runModelCallWorkflowAcceptance(t, tc.single, tc.batch) })
	}
}
func runModelCallWorkflowAcceptance(t *testing.T, singleCount, batchCount int) {
	started := time.Now()
	s, store, h, token, p, card, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	fake := &workflowAPIClient{}
	var failNext atomic.Bool
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []llm.Message `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) == 0 {
			w.WriteHeader(400)
			return
		}
		isGeneration := strings.Contains(request.Input[len(request.Input)-1].Content, "<knowledge_material>")
		if isGeneration && failNext.CompareAndSwap(true, false) {
			w.WriteHeader(503)
			w.Write([]byte(`{"error":{"message":"controlled unavailable"}}`))
			return
		}
		raw, err := fake.Complete(r.Context(), request.Input, llm.GenerateOptions{})
		if err != nil {
			w.WriteHeader(500)
			return
		}
		if !isGeneration {
			var check map[string]any
			json.Unmarshal([]byte(raw), &check)
			check["issues"] = []domain.ReviewIssue{{Field: "expert_format", Severity: "warning", Message: "受控风格建议，模型结论仍为pass"}}
			data, _ := json.Marshal(check)
			raw = string(data)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "completed", "usage": map[string]int{"input_tokens": 11, "output_tokens": 7, "total_tokens": 18}, "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": raw}}}}})
	}))
	defer vendor.Close()
	newClient := func(purpose llm.ConfigPurpose) *llm.DynamicQwenClient {
		c := llm.NewDynamicQwenClient(nil, purpose, llm.QwenConfig{Model: "qwen3.5-flash", APIKey: "isolated-test-key", BaseURL: vendor.URL + "/v1"})
		c.SetNetworkToolsResolver(store)
		c.SetCallRecorder(store)
		return c
	}
	genClient := newClient(llm.PurposeGeneration)
	checkClient := newClient(llm.PurposeAICheck)
	checker := aicheck.NewService(checkClient, store, store, store, "fallback-must-not-be-stored")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(genClient), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	request := generationPayload("call-evidence-single", p, singleCount)
	request["presentation"] = &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, request)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	questions := waitWorkflowQuestions(t, h, token, "call-evidence-single", singleCount)
	// First-check pass+warning is visible; no automatic quality override/retry.
	for _, q := range questions {
		result, e := store.GetLatestByQuestionID(t.Context(), q.ID)
		if e != nil || result.Verdict != "pass" || result.Model != "qwen3.5-flash" || len(result.Issues) != 1 {
			t.Fatal(result, e)
		}
	}

	ids := []string{p.ID}
	perPoint := batchCount
	if batchCount > 20 {
		if batchCount%20 != 0 {
			t.Fatal("invalid scale fixture")
		}
		perPoint = 20
		for i := 1; i < batchCount/20; i++ {
			next := p
			next.ID = ""
			next.OutlineCode = fmt.Sprintf("call-evidence.%03d", i)
			created, err := s.kpSvc.CreatePoint(t.Context(), next)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, created.ID)
			card.KnowledgePoints = append(card.KnowledgePoints, *created)
		}
		response := serveGenerateJSON(t, h, "PUT", "/api/knowledge-base/"+card.ID, token, card)
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), card) != nil {
			t.Fatal(response.Code, response.Body)
		}
		response = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/"+card.ID+"/activate", token, map[string]int{"revision": card.Revision})
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body)
		}
	}
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(genClient), store, store, "qwen3.5-flash", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	ex.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer ex.Close()
	s.batchSvc = ex
	failNext.Store(true)
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": ids, "count": perPoint})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var submitted struct {
		ID string `json:"job_id"`
	}
	if e := json.Unmarshal(r.Body.Bytes(), &submitted); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(20 * time.Second)
	var job *batch.BatchJob
	for time.Now().Before(deadline) {
		job, _ = ex.GetJobStatus(t.Context(), submitted.ID)
		if job != nil && job.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job == nil || job.Completed != batchCount || job.Failed != 0 {
		t.Fatal(job)
	}
	for _, item := range job.Items {
		for time.Now().Before(deadline) {
			q, _ := store.GetQuestion(t.Context(), item.QuestionID)
			if q != nil && q.Status == domain.StatusAIReviewed {
				questions = append(questions, *q)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(questions) != singleCount+batchCount {
		t.Fatal("first check failed", len(questions))
	}
	rows, err := store.DB().QueryContext(t.Context(), `SELECT record FROM model_call_records ORDER BY started_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var records []llm.ModelCallRecord
	for rows.Next() {
		var raw []byte
		var record llm.ModelCallRecord
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	generation, checks, failed, retried, tokens := 0, 0, 0, 0, 0
	for _, record := range records {
		if record.Operation == "" || record.Attempt < 1 || record.ToolsRevision < 1 || record.Model != "qwen3.5-flash" || !record.HTTPAttempted || len(record.InputSHA256) != 64 || len(record.ResponseSHA256) != 64 || record.SearchCalls != 0 || record.ExtractorCalls != 0 {
			t.Fatal(record)
		}
		if record.Outcome == "failed" {
			failed++
			if record.HTTPStatus != 503 || record.ErrorKind != "upstream_http" || record.FailureStage != "provider" || record.UsageReported {
				t.Fatal(record)
			}
		} else if record.Outcome != "returned" || !record.UsageReported || record.TotalTokens != 18 || len(record.OutputSHA256) != 64 {
			t.Fatal(record)
		}
		if record.Attempt == 2 {
			retried++
		}
		if record.Purpose == llm.PurposeGeneration {
			generation++
			if strings.HasPrefix(record.Operation, "generate:") {
				if record.WorkflowVersion != "knowledge-v4" {
					t.Fatal(record)
				}
			} else if record.WorkflowVersion != "knowledge-v4" {
				t.Fatal(record)
			}
		} else {
			checks++
		}
		tokens += record.TotalTokens
	}
	if len(records) != singleCount+2*batchCount+2 || generation != batchCount+2 || checks != singleCount+batchCount || failed != 1 || retried != 1 || tokens != 18*(1+singleCount+2*batchCount) {
		t.Fatal(len(records), generation, checks, failed, retried, tokens)
	}

	// The new execution records must link model output, parsing, references,
	// rendering and the actual committed handoff, without changing model counts.

	// The preparation fingerprint must equal the exact accepted snapshot, including
	// the old single-run revision retained after the shared card is edited for batch.
	acceptedRun, e := store.GetGenerationRun(t.Context(), "call-evidence-single")
	if e != nil {
		t.Fatal(e)
	}
	var acceptedSpec pipeline.GenerationSpec
	if e = json.Unmarshal(acceptedRun.RequestJSON, &acceptedSpec); e != nil {
		t.Fatal(e)
	}
	singleSnapshot, e := json.Marshal(acceptedSpec.Request.Workflow)
	if e != nil {
		t.Fatal(e)
	}
	acceptedBatch, e := store.GetBatchJob(t.Context(), submitted.ID)
	if e != nil {
		t.Fatal(e)
	}
	var batchSnapshot domain.KnowledgeWorkflowSnapshot
	if e = json.Unmarshal([]byte(acceptedBatch.WorkflowSnapshotJSON), &batchSnapshot); e != nil {
		t.Fatal(e)
	}
	batchSnapshotJSON, e := json.Marshal(batchSnapshot)
	if e != nil {
		t.Fatal(e)
	}
	preparationHashes := map[string]string{"generate:call-evidence-single": fmt.Sprintf("%x", sha256.Sum256(singleSnapshot)), "batch:" + submitted.ID + ":submission": fmt.Sprintf("%x", sha256.Sum256(batchSnapshotJSON))}
	traceRows, e := store.DB().QueryContext(t.Context(), `SELECT record FROM workflow_execution_records ORDER BY started_at,id`)
	if e != nil {
		t.Fatal(e)
	}
	executions, submissions, committed, traceFailed := 0, 0, 0, 0
	calls := map[string]llm.ModelCallRecord{}
	for _, call := range records {
		calls[call.ID] = call
	}
	for traceRows.Next() {
		var raw []byte
		var execution workflowtrace.Record
		if e = traceRows.Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, &execution); e != nil {
			t.Fatal(e)
		}
		if execution.Phase == "submission" {
			submissions++
			if execution.Outcome != "accepted" || execution.CardCount < 1 || execution.PointCount < 1 || len(execution.ModelCalls) != 0 {
				t.Fatal(execution)
			}
			if len(execution.Stages) != 2 || execution.Stages[0].Name != "knowledge_prepare" || execution.Stages[0].Status != "prepared" || execution.Stages[1].Name != "queue_admission" || execution.Stages[0].Output.SHA256 != preparationHashes[execution.Operation] {
				t.Fatal(execution)
			}
			continue
		}
		executions++
		if execution.StagesTruncated || len(execution.ModelCalls) != 1 || !execution.ModelCalls[0].Recorded {
			t.Fatal(execution)
		}
		call, ok := calls[execution.ModelCalls[0].ID]
		if !ok || call.Operation != execution.Operation || call.Attempt != execution.Attempt || call.WorkflowVersion != execution.WorkflowVersion {
			t.Fatal("incorrect actual-call link", execution, call)
		}
		stages := map[string][]workflowtrace.Stage{}
		for _, stage := range execution.Stages {
			stages[stage.Name] = append(stages[stage.Name], stage)
		}
		if len(stages["generation_prepare"]) != 1 || len(stages["model_call"]) != 1 {
			t.Fatal(execution)
		}
		if execution.Outcome == "generation_failed" {
			traceFailed++
			if stages["model_call"][0].Status != "failed" || len(stages["json_parse"]) != 0 || len(stages["handoff"]) != 0 || call.HTTPStatus != 503 {
				t.Fatal(execution)
			}
		} else {
			committed++
			count := len(execution.QuestionIDs)
			if execution.Outcome != "committed" || count < 1 || len(stages["render"]) != count || len(stages["knowledge_validation"]) != count || len(stages["a2_validation"]) != count || len(stages["handoff"]) != 1 || stages["handoff"][0].Status != "committed" {
				t.Fatal(execution)
			}
			if stages["model_call"][0].Output.SHA256 != call.OutputSHA256 || stages["json_parse"][0].Input.SHA256 != call.OutputSHA256 {
				t.Fatal("raw output hash correlation broken", execution)
			}
			for i := 0; i < count; i++ {
				if stages["render"][i].Output.SHA256 != stages["a2_validation"][i].Input.SHA256 {
					t.Fatal("render/validation artifacts disconnected")
				}
			}
		}
		for _, private := range []string{"最初冻结事实", "反复上腹痛", "isolated-test-key", vendor.URL, "受控风格建议"} {
			if strings.Contains(string(raw), private) {
				t.Fatal("private contents leaked in trace", private)
			}
		}
	}
	if e = traceRows.Err(); e != nil {
		t.Fatal(e)
	}
	traceRows.Close()
	if submissions != 2 || executions != generation || traceFailed != 1 || committed != batchCount+1 {
		t.Fatal(submissions, executions, traceFailed, committed)
	}
	// The persistent logical task count is unchanged by model-call records.
	var count int
	if err = store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM ai_check_tasks`).Scan(&count); err != nil || count != singleCount+batchCount {
		t.Fatal("logical tasks repeated", count, err)
	}
	// Diagnostic records remain absent from ordinary result payloads.
	view := serveGenerateJSON(t, h, "GET", "/api/generation-runs/call-evidence-single", token, nil)
	for _, private := range []string{"config_fingerprint", "input_sha256", "total_tokens", "isolated-test-key", vendor.URL} {
		if strings.Contains(view.Body.String(), private) {
			t.Fatal("diagnostic data leaked", private)
		}
	}
	var traceAverageBytes float64
	var traceMaximumBytes int
	if err = store.DB().QueryRowContext(t.Context(), `SELECT avg(pg_column_size(record)),max(pg_column_size(record)) FROM workflow_execution_records`).Scan(&traceAverageBytes, &traceMaximumBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("workflow execution record average=%.0f bytes max=%d bytes", traceAverageBytes, traceMaximumBytes)
	var averageBytes float64
	if err = store.DB().QueryRowContext(t.Context(), `SELECT avg(pg_column_size(record)) FROM model_call_records`).Scan(&averageBytes); err != nil {
		t.Fatal(err)
	}
	t.Logf("controlled workflow elapsed=%s, average stored record=%.0f bytes", time.Since(started), averageBytes)
	t.Logf("%d controlled questions, %d generation attempts + %d first checks, %d injected HTTP fault, %d synthetic usage tokens", singleCount+batchCount, generation, checks, failed, tokens)
}
