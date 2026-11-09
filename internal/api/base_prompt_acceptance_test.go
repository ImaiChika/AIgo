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
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type baseAcceptanceModel struct{}

func (*baseAcceptanceModel) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	if strings.HasSuffix(llm.CallOperation(ctx), ":first_check") {
		return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[{"field":"stem","severity":"info","message":"受控表达建议，不改变可用性"}]}`, nil
	}
	return (&fakeAPILLM{}).Complete(ctx, m, o)
}

type baseAPICapture struct {
	gen, check       llm.Client
	mu               sync.Mutex
	started          int
	referenceStems   []string
	generationInputs int
	checkInputs      int
}

func (c *baseAPICapture) Info() llm.ClientInfo {
	if p, ok := c.gen.(llm.InfoProvider); ok {
		return p.Info()
	}
	return llm.ClientInfo{Model: "base-controlled"}
}
func (c *baseAPICapture) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	checking := strings.HasSuffix(llm.CallOperation(ctx), ":first_check")
	for _, message := range m {
		if strings.Contains(message.Content, "<knowledge_material>") || strings.Contains(message.Content, "<knowledge_reference>") {
			return "", fmt.Errorf("base-only acceptance received a knowledge package")
		}
		if !checking {
			for _, stem := range c.referenceStems {
				if len(stem) > 30 && strings.Contains(message.Content, stem) {
					return "", fmt.Errorf("evaluation example leaked into generation input")
				}
			}
		}
	}
	c.mu.Lock()
	if c.started >= 32 {
		c.mu.Unlock()
		return "", fmt.Errorf("explicit experiment request cap reached")
	}
	c.started++
	if checking {
		c.checkInputs++
	} else {
		c.generationInputs++
	}
	c.mu.Unlock()
	client := c.gen
	if checking {
		client = c.check
	}
	return client.Complete(ctx, m, o)
}

// No KB service, cards, imports, private rules, reference snippets or historical
// questions participate in generation. The real flag alone permits billing.
func TestBasePromptDirectAcceptance(t *testing.T) {
	path := os.Getenv("AIGO_BASE_FIXTURE")
	if path == "" {
		t.Skip("explicit base-only fixture required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Points []domain.KnowledgePoint `json:"points"`
		Cases  []struct {
			Question domain.A2Question `json:"question"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || len(fixture.Points) != 6 {
		t.Fatal("invalid base fixture", err)
	}
	s, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	s.quotaStore = store
	// Revoke KB even for the fixture administrator: any accidental use must fail.
	if _, err = store.DB().Exec(`UPDATE roles SET permissions=array_remove(array_remove(array_remove(permissions,'kb:view'),'kb:use'),'kb:manage') WHERE id IN ('super_admin','admin')`); err != nil {
		t.Fatal(err)
	}
	version, err := s.kpSvc.CreateVersion(t.Context(), "本轮基座临床验收大纲", 2026, "")
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
	real := os.Getenv("AIGO_RUN_BASE_API_ACCEPTANCE") == "1"
	var gen, check llm.Client = &baseAcceptanceModel{}, &baseAcceptanceModel{}
	if real {
		key := os.Getenv("AIGO_REAL_QWEN_KEY")
		if key == "" {
			t.Fatal("explicit real key required")
		}
		cfg := llm.QwenConfig{APIKey: key, BaseURL: os.Getenv("AIGO_REAL_QWEN_BASE"), Model: "qwen3.5-flash", HTTPTimeout: 150 * time.Second}
		recovery := llm.NewEndpointRecovery()
		makeClient := func(p llm.ConfigPurpose) *llm.DynamicQwenClient {
			c := llm.NewDynamicQwenClient(nil, p, cfg)
			c.SetCallRecorder(store)
			c.SetNetworkToolsResolver(store)
			c.SetEndpointRecovery(recovery)
			return c
		}
		gen, check = makeClient(llm.PurposeGeneration), makeClient(llm.PurposeAICheck)
	}
	capture := &baseAPICapture{gen: gen, check: check}
	for _, sample := range fixture.Cases {
		capture.referenceStems = append(capture.referenceStems, sample.Question.ClinicalStem)
	}
	checker := aicheck.NewService(capture, store, store, store, "base-acceptance")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(capture), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(capture), store, store, "base-acceptance", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	h := s.Handler()
	token := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.72")
	type taskResult struct {
		Name      string `json:"name"`
		MS        int64  `json:"ms"`
		Passed    int    `json:"passed"`
		Discarded int    `json:"discarded"`
		Exhausted int    `json:"exhausted"`
		Failed    bool   `json:"generation_failed"`
	}
	tasks := []taskResult{}
	questions := []domain.A2Question{}
	results := []domain.AIReviewResult{}
	discardReports := []domain.AICheckDiscard{}
	wait := func(name, runID, jobID string, count int) {
		t.Helper()
		started := time.Now()
		deadline := started.Add(9 * time.Minute)
		for time.Now().Before(deadline) {
			ids := []string{}
			if runID != "" {
				run, e := store.GetGenerationRun(t.Context(), runID)
				if e != nil {
					t.Fatal(e)
				}
				if run.Status == domain.GenerationRunFailed {
					tasks = append(tasks, taskResult{Name: name, MS: time.Since(started).Milliseconds(), Failed: true})
					t.Errorf("base generation failed: %s", run.Error)
					return
				}
				ids = run.QuestionIDs
			}
			if jobID != "" {
				job, e := ex.GetJobStatus(t.Context(), jobID)
				if e != nil {
					t.Fatal(e)
				}
				if job.Status == "completed" && job.Failed > 0 {
					t.Fatal("base batch generation failed")
				}
				for _, item := range job.Items {
					if item.QuestionID != "" {
						ids = append(ids, item.QuestionID)
					}
				}
			}
			if len(ids) == count {
				_, progress, e := checker.ProgressByQuestionIDs(t.Context(), ids)
				if e == nil && progress["passed"]+progress["discarded"]+progress["exhausted"] == count {
					task := taskResult{Name: name, MS: time.Since(started).Milliseconds(), Passed: progress["passed"], Discarded: progress["discarded"], Exhausted: progress["exhausted"]}
					tasks = append(tasks, task)
					t.Logf("BASE_TASK name=%s ms=%d passed=%d discarded=%d exhausted=%d", name, task.MS, task.Passed, task.Discarded, task.Exhausted)
					for _, id := range ids {
						q, e := store.GetQuestion(t.Context(), id)
						if e != nil {
							t.Fatal(e)
						}
						if q == nil {
							discards, e := store.ListDiscardResultsByQuestionIDs(t.Context(), []string{id})
							if e != nil {
								t.Fatal(e)
							}
							if discard, ok := discards[id]; ok {
								q = discard.Question
								discardReports = append(discardReports, discard)
							}
						}
						if q != nil {
							if q.WorkflowVersion != "" || q.GenerationContext != nil || q.FormatVersion != domain.A2FormatFocused {
								t.Fatal("base output lost policy or acquired knowledge")
							}
							questions = append(questions, *q)
						}
						review, e := store.GetLatestByQuestionID(t.Context(), id)
						if e != nil {
							t.Fatal(e)
						}
						if review != nil {
							results = append(results, *review)
						}
					}
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("base task did not terminate", name)
	}
	submit := func(id string, index, count int) {
		t.Helper()
		p := points[index]
		r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, map[string]any{"run_id": id, "knowledge_point_id": p.ID, "version_id": p.VersionID, "count": count, "workflow_mode": "direct"})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		wait(id, id, "", count)
	}
	if os.Getenv("AIGO_BASE_SUBSET") == "retest" {
		submit("base-principle-retest", 1, 2)
		submit("base-diagnosis-retest", 0, 2)
	} else {
		batchPoints := []string{points[3].ID, points[4].ID, points[5].ID}
		if os.Getenv("AIGO_BASE_SUBSET") == "final" {
			submit("focused-single", 2, 1)
			submit("focused-principle-two", 1, 2)
			submit("focused-diagnosis-two", 0, 2)
			batchPoints = []string{points[5].ID}
		} else {
			submit("base-diagnosis", 0, 1)
			submit("base-principle", 1, 2)
			submit("base-hypertension", 2, 2)
		}
		r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"knowledge_point_ids": batchPoints, "version_id": version.ID, "count": 1, "workflow_mode": "direct"})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		var j struct {
			ID string `json:"job_id"`
		}
		json.Unmarshal(r.Body.Bytes(), &j)
		wait("base-batch", "", j.ID, len(batchPoints))
	}
	var cards, contexts, callCount, input, output, total, search, fetch int
	if err = store.DB().QueryRow(`SELECT (SELECT count(*) FROM knowledge_cards),(SELECT count(*) FROM question_generation_context)`).Scan(&cards, &contexts); err != nil || cards != 0 || contexts != 0 {
		t.Fatal("base-only test used knowledge", cards, contexts, err)
	}
	if err = store.DB().QueryRow(`SELECT count(*),coalesce(sum((record->>'input_tokens')::integer),0),coalesce(sum((record->>'output_tokens')::integer),0),coalesce(sum((record->>'total_tokens')::integer),0),coalesce(sum((record->>'search_calls')::integer),0),coalesce(sum((record->>'extractor_calls')::integer),0) FROM model_call_records`).Scan(&callCount, &input, &output, &total, &search, &fetch); err != nil {
		t.Fatal(err)
	}
	t.Logf("BASE_TOTAL real=%t http=%d input=%d output=%d total=%d search=%d fetch=%d cards=%d contexts=%d", real, callCount, input, output, total, search, fetch, cards, contexts)
	if path := os.Getenv("AIGO_BASE_API_REPORT"); path != "" {
		raw, _ := json.Marshal(map[string]any{"real": real, "tasks": tasks, "questions": questions, "reviews": results, "discards": discardReports, "http": callCount, "input_tokens": input, "output_tokens": output, "total_tokens": total, "search": search, "fetch": fetch, "knowledge_cards": cards, "knowledge_contexts": contexts})
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
