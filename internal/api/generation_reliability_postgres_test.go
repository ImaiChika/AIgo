//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
)

type reliabilityModel struct {
	mu                  sync.Mutex
	fault               string
	always              bool
	discardSecond       bool
	byOperation         map[string]int
	gen, checks, hinted int
}

type restartReliabilityModel struct {
	first   atomic.Bool
	started chan struct{}
	base    reliabilityModel
}

func (c *restartReliabilityModel) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	if !strings.Contains(m[len(m)-1].Content, "请检查以下 A2 型试题的质量") && c.first.CompareAndSwap(false, true) {
		close(c.started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	return c.base.Complete(ctx, m, o)
}

func TestGenerationMultiRestartKeepsFrozenInputsAndOneFirstCheck(t *testing.T) {
	s, store, h, token, p, card, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &restartReliabilityModel{started: make(chan struct{})}
	checker := aicheck.NewService(client, store, store, store, "controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	s.pipe.GenerationRetryBackoff = time.Millisecond
	id := "restart-multi-contract"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, p, 2))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	s.pipe.StartGenerationWorkers(ctxA, 2)
	select {
	case <-client.started:
	case <-time.After(4 * time.Second):
		t.Fatal("call not started")
	}
	cancelA()
	deadline := time.Now().Add(4 * time.Second)
	var run *domain.GenerationRun
	for time.Now().Before(deadline) {
		run, _ = store.GetGenerationRun(t.Context(), id)
		if run.Status == domain.GenerationRunPending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if run == nil || run.Status != domain.GenerationRunPending || len(run.QuestionIDs) != 0 {
		t.Fatal("interrupted run not recoverable", run)
	}
	before := string(run.RequestJSON)
	card.Content.Facts[0].Text = "重启期间改写的新事实"
	r = serveGenerateJSON(t, h, "PUT", "/api/knowledge-base/"+card.ID, token, card)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	checker.StartWorkers(ctxB, 2)
	s.pipe.StartGenerationWorkers(ctxB, 2)
	questions := waitWorkflowQuestions(t, h, token, id, 2)
	after, _ := store.GetGenerationRun(t.Context(), id)
	if after.Attempts != 2 || string(after.RequestJSON) != before {
		t.Fatal("restart changed accepted request", after)
	}
	for _, q := range questions {
		view := serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID+"/generation-context", token, nil)
		if view.Code != 200 || !strings.Contains(view.Body.String(), "最初冻结事实") || strings.Contains(view.Body.String(), "重启期间改写的新事实") {
			t.Fatal(view.Code, view.Body)
		}
	}
	if g, c, h := client.base.counts(); g != 1 || c != 2 || h != 1 {
		t.Fatal("restart rechecked or skipped recovery hint", g, c, h)
	}
}

type concurrentReliabilityModel struct {
	base        reliabilityModel
	mu          sync.Mutex
	active, max map[string]int
}

func (c *concurrentReliabilityModel) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	pool := "single"
	operation := llm.CallOperation(ctx)
	if strings.HasPrefix(operation, "batch:") {
		pool = "batch"
	}
	if strings.Contains(m[len(m)-1].Content, "请检查以下 A2 型试题的质量") {
		pool = "check"
	}
	c.mu.Lock()
	if c.active == nil {
		c.active = map[string]int{}
		c.max = map[string]int{}
	}
	c.active[pool]++
	if c.active[pool] > c.max[pool] {
		c.max[pool] = c.active[pool]
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.active[pool]--; c.mu.Unlock() }()
	select {
	case <-time.After(15 * time.Millisecond):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return c.base.Complete(ctx, m, o)
}

func TestGenerationMixedOwnersAndThreePoolsKeepResultsIsolated(t *testing.T) {
	s, store, h, admin, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &concurrentReliabilityModel{}
	checker := aicheck.NewService(client, store, store, store, "controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	created := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "reliability-owner", "password": "isolated-owner-password", "role": "teacher", "permissions": []string{"kb:view", "kb:use", "batch:run"}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	other := loginForAuthTest(t, h, "reliability-owner", "isolated-owner-password", "198.51.100.216")
	for _, tc := range []struct {
		id, token string
		count     int
	}{{"mixed-root-5", admin, 5}, {"mixed-root-20", admin, 20}, {"mixed-other-2", other, 2}} {
		r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", tc.token, generationPayload(tc.id, p, tc.count))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", other, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": []string{p.ID}, "count": 10})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var jobResponse struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(r.Body.Bytes(), &jobResponse)
	for _, tc := range []struct {
		id, token string
		count     int
	}{{"mixed-root-5", admin, 5}, {"mixed-root-20", admin, 20}, {"mixed-other-2", other, 2}} {
		questions := waitWorkflowQuestions(t, h, tc.token, tc.id, tc.count)
		if tc.token == admin {
			denied := serveGenerateJSON(t, h, "GET", "/api/generation-runs/"+tc.id, other, nil)
			if denied.Code != 403 && denied.Code != 404 {
				t.Fatal("cross-owner run visible", denied.Code)
			}
			denied = serveGenerateJSON(t, h, "GET", "/api/questions/"+questions[0].ID+"/generation-context", other, nil)
			if denied.Code != 403 && denied.Code != 404 {
				t.Fatal("cross-owner context visible", denied.Code)
			}
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, c, _ := client.base.counts()
		if c == 37 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if g, c, _ := client.base.counts(); g != 13 || c != 37 {
		t.Fatal(g, c)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	for _, pool := range []string{"single", "batch", "check"} {
		if client.max[pool] < 1 || client.max[pool] > 2 {
			t.Fatal("pool cap violated", pool, client.max)
		}
	}
}

func (c *reliabilityModel) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	prompt := m[len(m)-1].Content
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(prompt, "请检查以下 A2 型试题的质量") {
		c.checks++
		if c.discardSecond && c.checks == 2 {
			return `{"verdict":"issues_found","scores":{"scientific":85,"logic":80,"a2_fit":90,"answer":75},"issues":[{"field":"options","severity":"warning","message":"受控质量淘汰，不作医学结论"}]}`, nil
		}
		return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`, nil
	}
	if c.byOperation == nil {
		c.byOperation = map[string]int{}
	}
	operation := llm.CallOperation(ctx)
	c.byOperation[operation]++
	c.gen++
	hinted := strings.Contains(prompt, "【原任务自动重试：输出格式提醒】")
	if hinted {
		c.hinted++
	}
	raw, err := (&workflowAPIClient{}).Complete(ctx, m, o)
	if err != nil {
		return "", err
	}
	var items []map[string]any
	if err = json.Unmarshal([]byte(raw), &items); err != nil {
		return "", err
	}
	if !strings.Contains(prompt, "<knowledge_material>") {
		count := 1
		match := regexp.MustCompile(`数量：(\d+)道`).FindStringSubmatch(prompt)
		if len(match) > 1 {
			count, _ = strconv.Atoi(match[1])
		}
		first := items[0]
		items = nil
		for i := 0; i < count; i++ {
			copy := map[string]any{}
			for k, v := range first {
				copy[k] = v
			}
			items = append(items, copy)
		}
	}
	// The controlled provider recovers only when the original queue passes the
	// format reminder. No hidden model repair request is permitted.
	if c.fault != "" && (c.always || c.byOperation[operation] == 1 || !hinted) {
		switch c.fault {
		case "syntax":
			return `[{"clinical_stem":"synthetic broken response"]`, nil
		case "missing":
			items = items[:len(items)-1]
		case "extra":
			items = append(items, items[0])
		case "invalid_member":
			items[len(items)-1]["answer"] = "Z"
		case "duplicate":
			raw, _ := json.Marshal(items)
			return strings.Replace(string(raw), `"answer":"A"`, `"answer":"A","answer":"B"`, 1), nil
		case "http":
			return "", &llm.HTTPStatusError{Status: 503, Message: "controlled outage"}
		case "timeout":
			return "", context.DeadlineExceeded
		}
	}
	encoded, err := json.Marshal(items)
	return string(encoded), err
}

func (c *reliabilityModel) counts() (int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen, c.checks, c.hinted
}

func TestGenerationTaskResponseContractRecoveryAndExhaustion(t *testing.T) {
	for _, mode := range []string{"direct", "knowledge"} {
		for _, fault := range []string{"syntax", "missing", "extra", "invalid_member", "duplicate", "http", "timeout"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
				defer cleanup()
				client := &reliabilityModel{fault: fault}
				checker := aicheck.NewService(client, store, store, store, "controlled")
				s.aiCheckSvc = checker
				s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
				s.pipe.SetDraftChecker(checker)
				s.pipe.GenerationRetryBackoff = time.Millisecond
				s.pipe.GenerationPollInterval = time.Millisecond
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				checker.StartWorkers(ctx, 2)
				s.pipe.StartGenerationWorkers(ctx, 2)
				id := "contract-recover-" + mode + "-" + fault
				payload := generationPayload(id, p, 2)
				payload["workflow_mode"] = mode
				r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, payload)
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body)
				}
				before, err := store.GetGenerationRun(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				questions := waitWorkflowQuestions(t, h, token, id, 2)
				after, err := store.GetGenerationRun(t.Context(), id)
				if err != nil || after.Attempts != 2 || string(before.RequestJSON) != string(after.RequestJSON) {
					t.Fatal(after, err)
				}
				if g, c, h := client.counts(); g != 2 || c != 2 || h != 1 {
					t.Fatal("extra calls or lost reminder", g, c, h)
				}
				var nq, nt int
				if err = store.DB().QueryRow(`SELECT (SELECT count(*) FROM questions),(SELECT count(*) FROM ai_check_tasks)`).Scan(&nq, &nt); err != nil || nq != 2 || nt != 2 {
					t.Fatal("partial/duplicate persistence", nq, nt, err)
				}
				if questions[0].ID == questions[1].ID {
					t.Fatal("duplicate question identities")
				}
			})
		}
	}
	for _, mode := range []string{"direct", "knowledge"} {
		t.Run(mode+"/exhausted", func(t *testing.T) {
			s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
			defer cleanup()
			client := &reliabilityModel{fault: "syntax", always: true}
			checker := aicheck.NewService(client, store, store, store, "controlled")
			s.aiCheckSvc = checker
			s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
			s.pipe.SetDraftChecker(checker)
			s.pipe.GenerationPollInterval = time.Millisecond
			s.pipe.GenerationRetryBackoff = time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checker.StartWorkers(ctx, 2)
			s.pipe.StartGenerationWorkers(ctx, 2)
			id := "contract-exhaust-" + mode
			payload := generationPayload(id, p, 2)
			payload["workflow_mode"] = mode
			r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, payload)
			if r.Code != 200 {
				t.Fatal(r.Code, r.Body)
			}
			deadline := time.Now().Add(8 * time.Second)
			var run *domain.GenerationRun
			for time.Now().Before(deadline) {
				run, _ = store.GetGenerationRun(t.Context(), id)
				if run.Status == domain.GenerationRunFailed {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if run == nil || run.Status != domain.GenerationRunFailed || run.Attempts != 3 || len(run.QuestionIDs) != 0 {
				t.Fatal(run)
			}
			if g, c, h := client.counts(); g != 3 || c != 0 || h != 2 {
				t.Fatal(g, c, h)
			}
			var nq, nt int
			if err := store.DB().QueryRow(`SELECT (SELECT count(*) FROM questions),(SELECT count(*) FROM ai_check_tasks)`).Scan(&nq, &nt); err != nil || nq != 0 || nt != 0 {
				t.Fatal("bad result survived", nq, nt, err)
			}
		})
	}
}

func TestGenerationSingleMultiAndBatchShareStrictContract(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &reliabilityModel{fault: "missing"}
	checker := aicheck.NewService(client, store, store, store, "controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	s.pipe.GenerationRetryBackoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	for _, count := range []int{1, 5, 20} {
		id := fmt.Sprintf("strict-count-%d", count)
		r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, p, count))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		if got := waitWorkflowQuestions(t, h, token, id, count); len(got) != count {
			t.Fatal(len(got))
		}
	}
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	ex.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer ex.Close()
	s.batchSvc = ex
	r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": []string{p.ID}, "count": 10})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var submitted struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(r.Body.Bytes(), &submitted)
	deadline := time.Now().Add(12 * time.Second)
	var job *batch.BatchJob
	for time.Now().Before(deadline) {
		job, _ = ex.GetJobStatus(t.Context(), submitted.JobID)
		if job != nil && job.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job == nil || job.Status != "completed" || job.Completed != 10 || job.Failed != 0 {
		t.Fatal(job)
	}
	for _, item := range job.Items {
		if item.Attempts != 2 {
			t.Fatal("batch skipped contract retry", item)
		}
	}
	for time.Now().Before(deadline) {
		_, checks, _ := client.counts()
		if checks == 36 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if g, c, h := client.counts(); g != 26 || c != 36 || h != 13 {
		t.Fatal("wrong model/check counts", g, c, h)
	}
}

func TestGenerationCardinalityDoesNotRegenerateQualityDiscard(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &reliabilityModel{discardSecond: true}
	checker := aicheck.NewService(client, store, store, store, "controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	id := "mixed-quality-contract"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, p, 2))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	deadline := time.Now().Add(8 * time.Second)
	var run *domain.GenerationRun
	for time.Now().Before(deadline) {
		run, _ = store.GetGenerationRun(t.Context(), id)
		if len(run.QuestionIDs) == 2 {
			progress := serveGenerateJSON(t, h, "POST", "/api/ai-check/progress", token, map[string]any{"question_ids": run.QuestionIDs})
			var v struct {
				Counts map[string]int `json:"counts"`
			}
			json.Unmarshal(progress.Body.Bytes(), &v)
			if v.Counts["passed"] == 1 && v.Counts["discarded"] == 1 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	view := serveGenerateJSON(t, h, "GET", "/api/generation-runs/"+id, token, nil)
	var result struct {
		Questions []domain.A2Question `json:"questions"`
	}
	json.Unmarshal(view.Body.Bytes(), &result)
	if run == nil || run.Status != domain.GenerationRunSucceeded || run.Attempts != 1 || len(run.QuestionIDs) != 2 || len(result.Questions) != 1 {
		t.Fatal("quality result confused with generation contract failure", run, view.Body)
	}
	if g, c, _ := client.counts(); g != 1 || c != 2 {
		t.Fatal("quality discard caused regeneration/recheck", g, c)
	}
}
