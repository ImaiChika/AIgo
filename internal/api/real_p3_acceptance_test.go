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
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type realP3Capture struct {
	client                            *llm.DynamicQwenClient
	mu                                sync.Mutex
	knowledgeInputs, knowledgeOutputs int
}

func (c *realP3Capture) Info() llm.ClientInfo                { return c.client.Info() }
func (c *realP3Capture) Ready(ctx context.Context) bool      { return c.client.Ready(ctx) }
func (c *realP3Capture) WaitReady(ctx context.Context) error { return c.client.WaitReady(ctx) }
func (c *realP3Capture) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	knowledge := strings.Contains(m[len(m)-1].Content, "<knowledge_material>")
	if knowledge {
		c.mu.Lock()
		c.knowledgeInputs++
		c.mu.Unlock()
	}
	raw, err := c.client.Complete(ctx, m, o)
	if knowledge && err == nil && strings.Contains(raw, "fact_refs") && strings.Contains(raw, "case_plan") {
		c.mu.Lock()
		c.knowledgeOutputs++
		c.mu.Unlock()
	}
	return raw, err
}

// Explicit opt-in only. Uses the ordinary API/PG/queues, restores no historical
// questions and accepts legitimate quality discards without relaxing the gate.
func TestRealP3WorkflowAcceptance(t *testing.T) {
	if os.Getenv("AIGO_RUN_REAL_P3_ACCEPTANCE") != "1" {
		t.Skip("explicit real-model opt-in required")
	}
	key := os.Getenv("AIGO_REAL_QWEN_KEY")
	if key == "" {
		t.Fatal("real-model key unavailable")
	}
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	point := p
	point.ID = ""
	point.OutlineCode = "110.4.1.5.1.5"
	point.Topic = "社区起病成人肺炎的诊断"
	point.Subject = "呼吸系统"
	created, err := s.kpSvc.CreatePoint(t.Context(), point)
	if err != nil {
		t.Fatal(err)
	}
	point = *created
	raw, err := os.ReadFile("../../tools/fixtures/knowledge_import_demo.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cards []domain.KnowledgeCard `json:"cards"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	owner, err := s.authSvc.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range fixture.Cards {
		input.KnowledgePoints = []domain.KnowledgePoint{point}
		saved, e := s.knowledgeBaseSvc.Save(t.Context(), owner.ID, owner.Role, "", input)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.knowledgeBaseSvc.Transition(t.Context(), owner.ID, owner.Role, saved.ID, "activate", saved.Revision); e != nil {
			t.Fatal(e)
		}
	}
	cfg := llm.QwenConfig{APIKey: key, BaseURL: os.Getenv("AIGO_REAL_QWEN_BASE"), Model: "qwen3.5-flash", HTTPTimeout: 150 * time.Second}
	recovery := llm.NewEndpointRecovery()
	makeClient := func(purpose llm.ConfigPurpose) *llm.DynamicQwenClient {
		c := llm.NewDynamicQwenClient(nil, purpose, cfg)
		c.SetCallRecorder(store)
		c.SetNetworkToolsResolver(store)
		c.SetEndpointRecovery(recovery)
		return c
	}
	capture := &realP3Capture{client: makeClient(llm.PurposeGeneration)}
	checker := aicheck.NewService(makeClient(llm.PurposeAICheck), store, store, store, "qwen3.5-flash")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(capture), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	allIDs := []string{}
	wait := func(runID, jobID string) (int, int, int, time.Duration) {
		t.Helper()
		started := time.Now()
		deadline := started.Add(8 * time.Minute)
		ids := []string{}
		for time.Now().Before(deadline) {
			if runID != "" {
				run, e := store.GetGenerationRun(t.Context(), runID)
				if e != nil {
					t.Fatal("read run failed")
				}
				if run.Status == domain.GenerationRunFailed {
					t.Fatal("real generation exhausted its original retry budget")
				}
				ids = run.QuestionIDs
			}
			if jobID != "" {
				job, e := store.GetBatchJob(t.Context(), jobID)
				if e != nil {
					t.Fatal("read batch failed")
				}
				var output struct {
					Items []batch.ImportItem `json:"items"`
				}
				if json.Unmarshal([]byte(job.OutputJSON), &output) == nil {
					ids = nil
					for _, item := range output.Items {
						if item.QuestionID != "" {
							ids = append(ids, item.QuestionID)
						}
					}
				}
				if job.Failed > 0 && job.Status == "completed" {
					t.Fatal("real batch generation failed")
				}
			}
			if len(ids) == 1 {
				_, counts, e := checker.ProgressByQuestionIDs(t.Context(), ids)
				if e == nil && counts["passed"]+counts["discarded"]+counts["exhausted"] == 1 {
					allIDs = append(allIDs, ids...)
					return counts["passed"], counts["discarded"], counts["exhausted"], time.Since(started)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("real operation did not terminate")
		return 0, 0, 0, 0
	}
	for _, mode := range []string{"direct", "knowledge"} {
		id := "real-p3-" + mode
		request := generationPayload(id, point, 1)
		request["workflow_mode"] = mode
		if mode == "knowledge" {
			request["presentation"] = &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
		}
		started := time.Now()
		r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, request)
		if r.Code != 200 {
			t.Fatal("real submission failed", r.Code)
		}
		passed, discarded, exhausted, _ := wait(id, "")
		t.Logf("REAL_P3 mode=%s elapsed_ms=%d passed=%d discarded=%d exhausted=%d", mode, time.Since(started).Milliseconds(), passed, discarded, exhausted)
		view := serveGenerateJSON(t, h, "GET", "/api/generation-runs/"+id+"/diagnostics", token, nil)
		if view.Code != 200 {
			t.Fatal("diagnostics failed")
		}
	}
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(capture), store, store, "qwen3.5-flash", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	started := time.Now()
	r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": point.VersionID, "knowledge_point_ids": []string{point.ID}, "count": 1, "presentation": &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}})
	if r.Code != 200 {
		t.Fatal("real batch submission failed", r.Code)
	}
	var submitted struct {
		ID string `json:"job_id"`
	}
	if json.Unmarshal(r.Body.Bytes(), &submitted) != nil {
		t.Fatal("invalid submit response")
	}
	passed, discarded, exhausted, _ := wait("", submitted.ID)
	t.Logf("REAL_P3 mode=batch-knowledge elapsed_ms=%d passed=%d discarded=%d exhausted=%d", time.Since(started).Milliseconds(), passed, discarded, exhausted)
	var calls, generation, checks, input, output, total, search, fetch int
	e := store.DB().QueryRow(`SELECT count(*),count(*) FILTER(WHERE purpose='generation'),count(*) FILTER(WHERE purpose='ai_check'),coalesce(sum((record->>'input_tokens')::integer),0),coalesce(sum((record->>'output_tokens')::integer),0),coalesce(sum((record->>'total_tokens')::integer),0),coalesce(sum((record->>'search_calls')::integer),0),coalesce(sum((record->>'extractor_calls')::integer),0) FROM model_call_records`).Scan(&calls, &generation, &checks, &input, &output, &total, &search, &fetch)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("REAL_P3 aggregate http=%d generation=%d checks=%d input=%d output=%d total=%d search=%d fetch=%d", calls, generation, checks, input, output, total, search, fetch)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.knowledgeInputs < 2 || capture.knowledgeOutputs < 2 {
		t.Fatal("knowledge protocol was not actually traversed")
	}
	for _, id := range allIDs {
		q, e := store.GetQuestion(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		if q != nil && q.WorkflowVersion != "" {
			frozen, e := store.GetQuestionGenerationContext(t.Context(), id)
			if e != nil || frozen == nil || len(frozen.Bundle.Cards) != 3 || len(frozen.Basis.FactRefs) == 0 {
				t.Fatal("frozen reference provenance missing")
			}
		}
	}
}
