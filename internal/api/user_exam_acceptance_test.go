//go:build integration

package api

import (
	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in user materials are data, not expert confirmation or automatic seeds.
func importUserExamFixture(t *testing.T, s *Server, h http.Handler, token string) []domain.KnowledgePoint {
	t.Helper()
	path := os.Getenv("AIGO_USER_EXAM_FIXTURE")
	if path == "" {
		t.Skip("explicit isolated user-exam fixture required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Points   []domain.KnowledgePoint `json:"points"`
		Document json.RawMessage         `json:"import_document"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Points) != 8 {
		t.Fatal("expected eight explicitly selected points")
	}
	version, err := s.kpSvc.CreateVersion(t.Context(), "本轮用户真题临时大纲-非正式资料", 2024, "")
	if err != nil {
		t.Fatal(err)
	}
	points := []domain.KnowledgePoint{}
	for _, point := range fixture.Points {
		point.VersionID = version.ID
		p, e := s.kpSvc.CreatePoint(t.Context(), point)
		if e != nil {
			t.Fatal(e)
		}
		points = append(points, *p)
	}
	if err = s.kpSvc.PublishVersion(t.Context(), version.ID); err != nil {
		t.Fatal(err)
	}
	req := knowledgebase.ImportRequest{VersionID: version.ID, Document: string(fixture.Document)}
	r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/preview", token, req)
	var preview knowledgebase.ImportPreview
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &preview) != nil || !preview.Valid || len(preview.Rows) != 24 {
		t.Fatalf("24-card preview failed status=%d", r.Code)
	}
	req.Token = preview.Token
	r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", token, req)
	var saved knowledgebase.ImportResult
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &saved) != nil || saved.Saved != 24 {
		t.Fatalf("24-card import failed status=%d", r.Code)
	}
	for _, card := range saved.Cards {
		if card.Status != "draft" {
			t.Fatal("import auto-activated data")
		}
		r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/"+card.ID+"/activate", token, map[string]int{"revision": card.Revision})
		if r.Code != 200 {
			t.Fatal("fixture activation failed", r.Code)
		}
	}
	return points
}

type examCapture struct {
	inner           llm.Client
	checkInner      llm.Client
	mu              sync.Mutex
	started         int
	knowledgeInputs int
	cases           []map[string]any
}

func (c *examCapture) Info() llm.ClientInfo {
	if p, ok := c.inner.(llm.InfoProvider); ok {
		return p.Info()
	}
	return llm.ClientInfo{Model: "exam-controlled"}
}

func (c *examCapture) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	c.mu.Lock()
	if c.started >= 20 {
		c.mu.Unlock()
		return "", fmt.Errorf("explicit acceptance request cap reached")
	}
	c.started++
	ordinal := c.started
	c.mu.Unlock()
	operation := llm.CallOperation(ctx)
	checking := strings.HasSuffix(operation, ":first_check")
	if !checking && strings.Contains(m[len(m)-1].Content, "<knowledge_material>") {
		c.mu.Lock()
		c.knowledgeInputs++
		c.mu.Unlock()
	}
	inner := c.inner
	if checking && c.checkInner != nil {
		inner = c.checkInner
	}
	raw, err := inner.Complete(ctx, m, o)
	if err == nil && !checking {
		items, e := generator.ParseQuestionsFromRaw(raw)
		if e == nil {
			c.mu.Lock()
			for _, item := range items {
				c.cases = append(c.cases, map[string]any{"operation": operation, "call_ordinal": ordinal, "item": item})
			}
			c.mu.Unlock()
		}
	}
	return raw, err
}

func TestUserExamWorkflowAcceptance(t *testing.T) {
	if os.Getenv("AIGO_USER_EXAM_FIXTURE") == "" {
		t.Skip("explicit isolated user-exam fixture required")
	}
	s, store, h, token, _, _, controlled, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	points := importUserExamFixture(t, s, h, token)
	onlyKnowledge := os.Getenv("AIGO_EXAM_TASKS") == "knowledge"
	real := os.Getenv("AIGO_RUN_REAL_EXAM_ACCEPTANCE") == "1"
	var generationClient, checkClient llm.Client = controlled, controlled
	if real {
		key := os.Getenv("AIGO_REAL_QWEN_KEY")
		if key == "" {
			t.Fatal("explicit real key required")
		}
		cfg := llm.QwenConfig{APIKey: key, BaseURL: os.Getenv("AIGO_REAL_QWEN_BASE"), Model: "qwen3.5-flash", HTTPTimeout: 150 * time.Second}
		recovery := llm.NewEndpointRecovery()
		makeClient := func(p llm.ConfigPurpose) *llm.DynamicQwenClient {
			c := llm.NewDynamicQwenClient(nil, p, cfg)
			c.SetNetworkToolsResolver(store)
			c.SetCallRecorder(store)
			c.SetEndpointRecovery(recovery)
			return c
		}
		generationClient, checkClient = makeClient(llm.PurposeGeneration), makeClient(llm.PurposeAICheck)
	}
	capture := &examCapture{inner: generationClient, checkInner: checkClient}
	checker := aicheck.NewService(capture, store, store, store, "exam-acceptance")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(capture), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	// One baseline task is converted before workers start, modeling an existing
	// original direct task; no production endpoint mutates accepted snapshots.
	var r *httptest.ResponseRecorder
	if !onlyKnowledge {
		r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, map[string]any{"run_id": "exam-baseline", "knowledge_point_id": points[0].ID, "version_id": points[0].VersionID, "count": 1})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		if _, err := store.DB().Exec(`UPDATE generation_runs SET request_json=request_json #- '{request,prompt_snapshot}' WHERE id='exam-baseline'`); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(capture), store, store, "exam-acceptance", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	type taskResult struct {
		Mode             string `json:"mode"`
		Milliseconds     int64  `json:"milliseconds"`
		Passed           int    `json:"passed"`
		Discarded        int    `json:"discarded"`
		Exhausted        int    `json:"exhausted"`
		GenerationFailed bool   `json:"generation_failed"`
	}
	results := []taskResult{}
	wait := func(mode, runID, jobID string, count int) {
		t.Helper()
		started := time.Now()
		deadline := started.Add(8 * time.Minute)
		for time.Now().Before(deadline) {
			ids := []string{}
			if runID != "" {
				run, e := store.GetGenerationRun(t.Context(), runID)
				if e != nil {
					t.Fatal(e)
				}
				if run.Status == domain.GenerationRunFailed {
					results = append(results, taskResult{Mode: mode, Milliseconds: time.Since(started).Milliseconds(), GenerationFailed: true})
					t.Errorf("%s exhausted original generation retry", mode)
					return
				}
				ids = run.QuestionIDs
			}
			if jobID != "" {
				j, e := ex.GetJobStatus(t.Context(), jobID)
				if e != nil {
					t.Fatal(e)
				}
				if j.Status == "completed" && j.Failed > 0 {
					t.Fatal("batch generation failed")
				}
				for _, item := range j.Items {
					if item.QuestionID != "" {
						ids = append(ids, item.QuestionID)
					}
				}
			}
			if len(ids) == count {
				_, counts, e := checker.ProgressByQuestionIDs(t.Context(), ids)
				if e == nil && counts["passed"]+counts["discarded"]+counts["exhausted"] == count {
					result := taskResult{Mode: mode, Milliseconds: time.Since(started).Milliseconds(), Passed: counts["passed"], Discarded: counts["discarded"], Exhausted: counts["exhausted"]}
					results = append(results, result)
					t.Logf("EXAM_TASK mode=%s ms=%d passed=%d discarded=%d exhausted=%d", mode, result.Milliseconds, result.Passed, result.Discarded, result.Exhausted)
					for _, id := range ids {
						q, _ := store.GetQuestion(t.Context(), id)
						if q != nil && q.WorkflowVersion != "" {
							frozen, e := store.GetQuestionGenerationContext(t.Context(), id)
							if e != nil || frozen == nil || len(frozen.Bundle.Cards) != 3 || len(frozen.Basis.FactRefs) == 0 || knowledgebase.ValidateContext(frozen) != nil {
								t.Fatal("actual knowledge provenance missing")
							}
						}
					}
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("acceptance task did not terminate", mode)
	}
	if !onlyKnowledge {
		wait("baseline_direct_1", "exam-baseline", "", 1)
	}
	for _, mode := range []string{"direct", "knowledge"} {
		if onlyKnowledge && mode != "knowledge" {
			continue
		}
		id := "exam-" + mode
		r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, map[string]any{"run_id": id, "knowledge_point_id": points[0].ID, "version_id": points[0].VersionID, "count": 2, "workflow_mode": mode})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		wait(mode+"_2", id, "", 2)
	}
	if !onlyKnowledge {
		r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"knowledge_point_ids": []string{points[1].ID}, "version_id": points[1].VersionID, "count": 1, "workflow_mode": "knowledge"})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		var job struct {
			ID string `json:"job_id"`
		}
		json.Unmarshal(r.Body.Bytes(), &job)
		wait("batch_knowledge_1", "", job.ID, 1)
	}
	var calls, input, output, total, search, fetch int
	if err := store.DB().QueryRow(`SELECT count(*),coalesce(sum((record->>'input_tokens')::integer),0),coalesce(sum((record->>'output_tokens')::integer),0),coalesce(sum((record->>'total_tokens')::integer),0),coalesce(sum((record->>'search_calls')::integer),0),coalesce(sum((record->>'extractor_calls')::integer),0) FROM model_call_records`).Scan(&calls, &input, &output, &total, &search, &fetch); err != nil {
		t.Fatal(err)
	}
	t.Logf("EXAM_TOTAL real=%t http=%d input=%d output=%d total=%d search=%d fetch=%d", real, calls, input, output, total, search, fetch)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	wantKnowledge := 2
	if onlyKnowledge {
		wantKnowledge = 1
	}
	if capture.knowledgeInputs < wantKnowledge {
		t.Fatal("model did not receive real imported knowledge")
	}
	if path := os.Getenv("AIGO_EXAM_PRIVATE_REPORT"); path != "" {
		raw, _ := json.Marshal(map[string]any{"real": real, "tasks": results, "http": calls, "input_tokens": input, "output_tokens": output, "total_tokens": total, "search": search, "fetch": fetch, "cases": capture.cases})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
