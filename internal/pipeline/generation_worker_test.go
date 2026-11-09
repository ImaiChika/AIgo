package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

type liveGenerationClient struct {
	started, allow chan struct{}
	active, calls  atomic.Int32
}

func (c *liveGenerationClient) Complete(ctx context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.active.Add(1)
	c.calls.Add(1)
	defer c.active.Add(-1)
	c.started <- struct{}{}
	select {
	case <-c.allow:
		return (&fakeLLM{}).Complete(ctx, nil, llm.GenerateOptions{})
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestGenerationDispatcherLiveLimitsDoNotLeaseWaitingWork(t *testing.T) {
	client := &liveGenerationClient{started: make(chan struct{}, 8), allow: make(chan struct{}, 8)}
	p := New(generator.NewService(client), nil, nil, nil, testutil.NewMemoryStore())
	p.GenerationPollInterval = time.Millisecond
	p.GenerationLimiter = storage.NewConcurrencyLimiter(2)
	for i := 0; i < 5; i++ {
		submitTestRun(t, p, fmt.Sprintf("live-generation-%d", i))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1) // Injected persisted value must take priority over startup fallback.
	for i := 0; i < 2; i++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("initial slots not used")
		}
	}
	_ = p.GenerationLimiter.SetLimit(1)
	client.allow <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for client.active.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("completed call did not drain")
		}
		time.Sleep(time.Millisecond)
	}
	if client.calls.Load() != 2 {
		t.Fatal("shrink leased or started waiting work too early")
	}
	_ = p.GenerationLimiter.SetLimit(3)
	for i := 0; i < 2; i++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("raising limit did not start waiting work")
		}
	}
	for i := 0; i < 4; i++ {
		client.allow <- struct{}{}
	}
	for i := 0; i < 5; i++ {
		run := waitForRun(t, p, fmt.Sprintf("live-generation-%d", i), func(r *domain.GenerationRun) bool { return r.Status == domain.GenerationRunSucceeded })
		if run.Attempts != 1 || len(run.QuestionIDs) != 1 {
			t.Fatal("live change duplicated/retried a run")
		}
	}
	if client.calls.Load() != 5 {
		t.Fatal("not exactly one generation per run")
	}
}

// flakyLLM 前 failTimes 次调用返回错误，之后返回与 fakeLLM 相同的固定题目。
type flakyLLM struct {
	failTimes atomic.Int64
}

func (f *flakyLLM) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	if f.failTimes.Add(-1) >= 0 {
		return "", errors.New("模拟 LLM 调用失败")
	}
	var base fakeLLM
	return base.Complete(context.Background(), nil, llm.GenerateOptions{})
}

// alwaysFailLLM 始终返回错误。
type alwaysFailLLM struct{}

func (f *alwaysFailLLM) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	return "", errors.New("模拟 LLM 持续失败")
}

// countingLLM 只计数；被调用即说明不该发生的模型请求发生了。
type countingLLM struct{ calls atomic.Int64 }

func (f *countingLLM) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	f.calls.Add(1)
	return "", errors.New("不应调用模型")
}

// recordingBanks/auditing 记录题库归纳与审计调用。
type recordingBanks struct{ assigned []string }

func (r *recordingBanks) AssignBank(_ context.Context, q *domain.A2Question) error {
	r.assigned = append(r.assigned, q.ID)
	return nil
}

type recordingAudit struct{ created []string }

func (r *recordingAudit) LogCreate(_ context.Context, questionID, _ string) error {
	r.created = append(r.created, questionID)
	return nil
}

type quotaTransitionChecker struct {
	runs     storage.GenerationRunStore
	runID    string
	observed chan error
}

type immediateOutcomeChecker struct {
	store   storage.QuestionStore
	discard bool
	err     error
}

func (c *immediateOutcomeChecker) CheckAsync(ids ...string) {
	for _, id := range ids {
		if c.discard {
			c.err = c.store.DeleteQuestion(context.Background(), id)
		} else {
			q, err := c.store.GetQuestion(context.Background(), id)
			if err != nil || q == nil {
				c.err = fmt.Errorf("draft missing: %v", err)
				return
			}
			q.Status = domain.StatusAIReviewed
			c.err = c.store.SaveQuestion(context.Background(), *q)
		}
	}
}

func TestGenerationWorkerPreservesImmediateCheckOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		discard bool
	}{
		{name: "pass"}, {name: "discard", discard: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newWorkerPipeline(&fakeLLM{}, &recordingBanks{}, &recordingAudit{})
			checker := &immediateOutcomeChecker{store: p.store, discard: tc.discard}
			p.SetDraftChecker(checker)
			submitTestRun(t, p, "immediate-"+tc.name)
			run, raw, err := p.runs.ClaimNextGenerationRun(context.Background(), time.Minute)
			if err != nil || run == nil {
				t.Fatalf("claim: %v", err)
			}
			var spec GenerationSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				t.Fatal(err)
			}
			p.executeGenerationRun(context.Background(), run, &spec)
			finished, _ := p.runs.GetGenerationRun(context.Background(), run.ID)
			if checker.err != nil || finished.Status != domain.GenerationRunSucceeded {
				t.Fatalf("execute: %v %+v", checker.err, finished)
			}
			q, err := p.store.GetQuestion(context.Background(), finished.QuestionIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			if tc.discard && q != nil {
				t.Fatal("generation resurrected a discarded question")
			}
			if !tc.discard && (q == nil || q.Status != domain.StatusAIReviewed) {
				t.Fatalf("generation overwrote first-check status: %+v", q)
			}
		})
	}
}

func (c *quotaTransitionChecker) CheckAsync(ids ...string) {
	run, err := c.runs.GetGenerationRun(context.Background(), c.runID)
	if err == nil && (run == nil || run.Status != domain.GenerationRunRunning || len(run.QuestionIDs) != len(ids)) {
		err = fmt.Errorf("check queued before running question IDs were recorded: run=%+v ids=%v", run, ids)
	}
	if err == nil {
		for i := range ids {
			if run.QuestionIDs[i] != ids[i] {
				err = fmt.Errorf("recorded IDs mismatch: %v vs %v", run.QuestionIDs, ids)
				break
			}
		}
	}
	c.observed <- err
}

func TestGenerationWorkerRecordsIDsBeforeFirstCheck(t *testing.T) {
	p := newWorkerPipeline(&fakeLLM{}, &recordingBanks{}, &recordingAudit{})
	checker := &quotaTransitionChecker{runs: p.runs, runID: "quota-transition", observed: make(chan error, 1)}
	p.SetDraftChecker(checker)
	submitTestRun(t, p, checker.runID)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1)
	select {
	case err := <-checker.observed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not queue first check")
	}
}

func submitTestRun(t *testing.T, p *Pipeline, runID string) {
	t.Helper()
	spec, err := json.Marshal(GenerationSpec{
		Request: domain.GenerationRequest{
			Subject: "消化",
			KnowledgePoints: []domain.KnowledgePoint{{
				ID: "110.4.3.1.1", Subject: "消化", Topic: "消化性溃疡", OutlineCode: "110.4.3.1.1",
			}},
			Count: 1,
		},
		Actor: "tester", OwnerID: "owner-1", BankID: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := p.runs.CreateGenerationRun(context.Background(), domain.GenerationRun{
		ID: runID, OwnerID: "owner-1", Status: domain.GenerationRunPending,
		RequestedCount: 1, MaxAttempts: 3, RequestJSON: spec,
	})
	if err != nil || !created {
		t.Fatalf("提交运行失败: created=%v err=%v", created, err)
	}
}

func waitForRun(t *testing.T, p *Pipeline, runID string, until func(*domain.GenerationRun) bool) *domain.GenerationRun {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, err := p.runs.GetGenerationRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run != nil && until(run) {
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("等待运行终态超时")
	return nil
}

func newWorkerPipeline(client llm.Client, banks *recordingBanks, audit *recordingAudit) *Pipeline {
	p := New(generator.NewService(client), nil, nil, nil, testutil.NewMemoryStore())
	p.SetBankAssigner(banks)
	p.SetGenerationAuditor(audit)
	// 测试用小间隔：轮询 10ms、首次退避 20ms
	p.GenerationPollInterval = 10 * time.Millisecond
	p.GenerationRetryBackoff = 20 * time.Millisecond
	return p
}

// 完整执行链：pending → worker 抢占执行 → succeeded，题目以提交人归属落库，
// 题库归纳与审计与原同步路径一致。
func TestGenerationWorkerExecutesPendingRun(t *testing.T) {
	banks := &recordingBanks{}
	audit := &recordingAudit{}
	p := newWorkerPipeline(&fakeLLM{}, banks, audit)
	submitTestRun(t, p, "gen-worker-ok")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1)

	run := waitForRun(t, p, "gen-worker-ok", func(r *domain.GenerationRun) bool {
		return r.Status == domain.GenerationRunSucceeded || r.Status == domain.GenerationRunFailed
	})
	if run.Status != domain.GenerationRunSucceeded {
		t.Fatalf("运行应成功，实际 %s（%s）", run.Status, run.Error)
	}
	if len(run.QuestionIDs) != 1 {
		t.Fatalf("应生成 1 道题，实际 %v", run.QuestionIDs)
	}
	q, err := p.store.GetQuestion(context.Background(), run.QuestionIDs[0])
	if err != nil || q == nil {
		t.Fatalf("题目应已落库: %v", err)
	}
	if q.OwnerID != "owner-1" || q.Status != domain.StatusAIDraft {
		t.Fatalf("题目归属/状态不符: owner=%q status=%s", q.OwnerID, q.Status)
	}
	if len(banks.assigned) != 1 || banks.assigned[0] != q.ID {
		t.Fatalf("应自动归纳题库 1 次: %v", banks.assigned)
	}
	if len(audit.created) != 1 || audit.created[0] != q.ID {
		t.Fatalf("应写审计 1 次: %v", audit.created)
	}
}

// 生成阶段瞬时失败：按退避重试直至成功，attempts 递增且最终成功。
func TestGenerationWorkerRetriesTransientFailure(t *testing.T) {
	flaky := &flakyLLM{}
	flaky.failTimes.Store(1)
	p := newWorkerPipeline(flaky, &recordingBanks{}, &recordingAudit{})
	submitTestRun(t, p, "gen-worker-retry")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1)

	run := waitForRun(t, p, "gen-worker-retry", func(r *domain.GenerationRun) bool {
		return r.Status == domain.GenerationRunSucceeded || r.Status == domain.GenerationRunFailed
	})
	if run.Status != domain.GenerationRunSucceeded {
		t.Fatalf("瞬时失败后应重试成功，实际 %s（%s）", run.Status, run.Error)
	}
	if run.Attempts != 2 {
		t.Fatalf("应执行 2 次后成功，实际 attempts=%d", run.Attempts)
	}
}

// 持续失败：重试耗尽后进入 failed 终态，不落任何题目。
func TestGenerationWorkerExhaustsRetries(t *testing.T) {
	p := newWorkerPipeline(&alwaysFailLLM{}, &recordingBanks{}, &recordingAudit{})
	submitTestRun(t, p, "gen-worker-fail")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1)

	run := waitForRun(t, p, "gen-worker-fail", func(r *domain.GenerationRun) bool {
		return r.Status == domain.GenerationRunFailed
	})
	if run.Attempts != 3 || run.Error == "" {
		t.Fatalf("应执行 3 次后失败并记录原因，实际 attempts=%d error=%q", run.Attempts, run.Error)
	}
	ids, err := p.store.ListQuestions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("失败运行不应落题，实际 %d 道", len(ids))
	}
}

// 范围受限账号：题目自动归入其可见题库集合（无需出题人选择目标题库）。
func TestGenerationWorkerAssignsScopedBanks(t *testing.T) {
	banks := &recordingBanks{}
	audit := &recordingAudit{}
	p := newWorkerPipeline(&fakeLLM{}, banks, audit)
	spec, err := json.Marshal(GenerationSpec{
		Request: domain.GenerationRequest{
			Subject:         "消化",
			KnowledgePoints: []domain.KnowledgePoint{{ID: "110.4.3.1.1", Subject: "消化", Topic: "消化性溃疡", OutlineCode: "110.4.3.1.1"}},
			Count:           1,
		},
		Actor: "tester", OwnerID: "owner-1", ScopedBankIDs: []string{"bank-neike", "bank-xiaohua"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created, err := p.runs.CreateGenerationRun(context.Background(), domain.GenerationRun{
		ID: "gen-scoped-banks", OwnerID: "owner-1", Status: domain.GenerationRunPending,
		RequestedCount: 1, MaxAttempts: 3, RequestJSON: spec,
	}); err != nil || !created {
		t.Fatalf("提交运行失败: created=%v err=%v", created, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1)

	run := waitForRun(t, p, "gen-scoped-banks", func(r *domain.GenerationRun) bool {
		return r.Status == domain.GenerationRunSucceeded || r.Status == domain.GenerationRunFailed
	})
	if run.Status != domain.GenerationRunSucceeded {
		t.Fatalf("运行应成功，实际 %s（%s）", run.Status, run.Error)
	}
	q, err := p.store.GetQuestion(context.Background(), run.QuestionIDs[0])
	if err != nil || q == nil {
		t.Fatal(err)
	}
	if len(q.BankIDs) != 2 || q.BankIDs[0] != "bank-neike" || q.BankIDs[1] != "bank-xiaohua" {
		t.Fatalf("题目应归入受限账号的可见题库，实际 %v", q.BankIDs)
	}
	if len(banks.assigned) != 0 {
		t.Fatalf("受限路径不应再走专业自动归纳，实际 %v", banks.assigned)
	}
}

type emptyThenValidClient struct{ calls atomic.Int32 }

func (c *emptyThenValidClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	if c.calls.Add(1) < 3 {
		return "[]", nil
	}
	return (&fakeLLM{}).Complete(ctx, m, o)
}
func TestGenerationWorkerRetriesEmptyReplyBeforeSuccess(t *testing.T) {
	client := &emptyThenValidClient{}
	p := newWorkerPipeline(client, &recordingBanks{}, &recordingAudit{})
	submitTestRun(t, p, "empty-then-valid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1)
	run := waitForRun(t, p, "empty-then-valid", func(r *domain.GenerationRun) bool {
		return r.Status == domain.GenerationRunSucceeded || r.Status == domain.GenerationRunFailed
	})
	if run.Status != domain.GenerationRunSucceeded || run.Attempts != 3 || len(run.QuestionIDs) != 1 || client.calls.Load() != 3 {
		t.Fatalf("empty response retry failed: %+v calls=%d", run, client.calls.Load())
	}
}

// 基础医学选题范围调整是终态失败：不重试、不调用模型、不换考点补生。
func TestGenerationWorkerFailsBasicScopeWithoutRetry(t *testing.T) {
	counting := &countingLLM{}
	p := newWorkerPipeline(counting, &recordingBanks{}, &recordingAudit{})
	spec, err := json.Marshal(GenerationSpec{
		Request: domain.GenerationRequest{
			Subject: "生理",
			KnowledgePoints: []domain.KnowledgePoint{{
				ID: "20.1.1.1", Subject: "生理", Topic: "静息电位", OutlineCode: "20.1.1.1", Category: "基础医学",
			}},
			Count: 1,
			PromptSnapshot: &domain.GenerationPromptSnapshot{PolicyVersion: domain.CurrentA2GenerationPolicy, OwnerID: "owner-1"},
		},
		Actor: "tester", OwnerID: "owner-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.runs.CreateGenerationRun(context.Background(), domain.GenerationRun{
		ID: "gen-worker-basic-scope", OwnerID: "owner-1", Status: domain.GenerationRunPending,
		RequestedCount: 1, MaxAttempts: 3, RequestJSON: spec,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.StartGenerationWorkers(ctx, 1)

	run := waitForRun(t, p, "gen-worker-basic-scope", func(r *domain.GenerationRun) bool {
		return r.Status == domain.GenerationRunFailed
	})
	if run.Attempts != 1 {
		t.Fatalf("范围调整应一次终态失败，实际 attempts=%d", run.Attempts)
	}
	if !strings.Contains(run.Error, "选题范围调整") {
		t.Fatalf("失败原因应说明选题范围调整：%q", run.Error)
	}
	if calls := counting.calls.Load(); calls != 0 {
		t.Fatalf("不应调用模型，实际 %d 次", calls)
	}
}
