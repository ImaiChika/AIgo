// LocalExecutor 健壮性验收测试（修复与待办指引.txt 第二批 4/5 项）：
// 全局并发上限、生成失败自动重试、失败项重跑、重启恢复并发受控。
package batch

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/postgres"
	"aigo/internal/storage/testutil"
	"github.com/lib/pq"
)

const robustQuestionJSON = `[{"clinical_stem":"男，50岁。突发胸痛2小时。该患者最可能的诊断是","options":[{"label":"A","text":"急性心肌梗死"},{"label":"B","text":"主动脉夹层"},{"label":"C","text":"肺栓塞"},{"label":"D","text":"气胸"},{"label":"E","text":"心包炎"}],"answer":"A","explanation":"正确答案为A。病例表现符合急性心肌梗死。B项、C项、D项、E项均与现有表现不符。故选A。","difficulty":"0.65","cognitive_level":"应用","exam_points":"诊断与鉴别诊断"}]`

// robustLLM 可编程的生成客户端：支持前 N 次失败、按要点主题永久失败、
// 并发在途计数（用于断言全局并发上限）。
type robustLLM struct {
	mu           sync.Mutex
	failFirstN   int
	failCalls    int
	failTopics   map[string]bool
	alwaysFails  bool
	delayPerCall time.Duration

	inFlight    int32
	maxInFlight int32
	totalCalls  int32
}

func (f *robustLLM) Complete(_ context.Context, messages []llm.Message, _ llm.GenerateOptions) (string, error) {
	atomic.AddInt32(&f.totalCalls, 1)
	cur := atomic.AddInt32(&f.inFlight, 1)
	for {
		maxSeen := atomic.LoadInt32(&f.maxInFlight)
		if cur <= maxSeen || atomic.CompareAndSwapInt32(&f.maxInFlight, maxSeen, cur) {
			break
		}
	}
	if f.delayPerCall > 0 {
		time.Sleep(f.delayPerCall)
	}
	atomic.AddInt32(&f.inFlight, -1)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alwaysFails {
		return "", context.DeadlineExceeded
	}
	if f.failCalls < f.failFirstN {
		f.failCalls++
		return "", context.DeadlineExceeded
	}
	if f.failTopics != nil {
		for topic := range f.failTopics {
			if messagesContainTopic(messages, topic) {
				return "", context.DeadlineExceeded
			}
		}
	}
	return robustQuestionJSON, nil
}

func (f *robustLLM) setFailTopics(topics ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failTopics == nil {
		f.failTopics = map[string]bool{}
	}
	for _, t := range topics {
		f.failTopics[t] = true
	}
}

func (f *robustLLM) clearFailTopics() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failTopics = nil
}

func (f *robustLLM) maxConcurrent() int32 { return atomic.LoadInt32(&f.maxInFlight) }
func (f *robustLLM) calls() int32         { return atomic.LoadInt32(&f.totalCalls) }

func messagesContainTopic(messages []llm.Message, topic string) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, topic) {
			return true
		}
	}
	return false
}

func waitForJobs(t *testing.T, executor *LocalExecutor, jobIDs []string, deadline time.Duration) []*BatchJob {
	t.Helper()
	var jobs []*BatchJob
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		allDone := true
		jobs = jobs[:0]
		for _, id := range jobIDs {
			job, err := executor.GetJobStatus(context.Background(), id)
			if err != nil {
				t.Fatalf("get job %s: %v", id, err)
			}
			jobs = append(jobs, job)
			if job.Status != "completed" && job.Status != "failed" {
				allDone = false
			}
		}
		if allDone {
			return jobs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("jobs did not finish in time: %+v", jobs)
	return nil
}

// One batch must fill the available slots, rather than only different jobs being concurrent.
func TestLocalExecutorParallelWithinJob(t *testing.T) {
	client := &robustLLM{delayPerCall: 50 * time.Millisecond}
	jobs := &localBatchStore{}
	executor := NewLocalExecutor(generator.NewService(client), testutil.NewMemoryStore(), jobs, "test-model")
	t.Cleanup(executor.Close)
	points := []domain.KnowledgePoint{
		{ID: "parallel-a", Topic: "胸痛", Subject: "循环系统", OutlineCode: "parallel.1"},
		{ID: "parallel-b", Topic: "肺炎", Subject: "呼吸系统", OutlineCode: "parallel.2"},
		{ID: "parallel-c", Topic: "胃溃疡", Subject: "消化系统", OutlineCode: "parallel.3"},
	}
	jobID, _, err := executor.GenerateAndSubmit(context.Background(), points, 2, "one parallel batch")
	if err != nil {
		t.Fatal(err)
	}
	job := waitForJobs(t, executor, []string{jobID}, 3*time.Second)[0]
	if job.Completed != 6 || job.Failed != 0 || client.calls() != 6 {
		t.Fatalf("unexpected batch result: %+v, calls=%d", job, client.calls())
	}
	if got := client.maxConcurrent(); got != 2 {
		t.Fatalf("one batch must reach concurrency 2, got %d", got)
	}
	record, _ := jobs.GetBatchJob(context.Background(), jobID)
	var output localBatchOutput
	if err := json.Unmarshal([]byte(record.OutputJSON), &output); err != nil {
		t.Fatal(err)
	}
	for pos, question := range output.Questions {
		if question.KnowledgePoints[0].ID != points[pos/2].ID || output.Items[pos].OutlineCode != question.OutlineCode {
			t.Fatalf("unit %d lost submission order or knowledge snapshot", pos)
		}
	}
}

type outOfOrderLLM struct {
	blocked chan struct{}
	started chan struct{}
	calls   atomic.Int32
}

type liveLimitLLM struct {
	started            chan struct{}
	allow              chan struct{}
	active, calls, max atomic.Int32
}

func (c *liveLimitLLM) Complete(ctx context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.calls.Add(1)
	active := c.active.Add(1)
	defer c.active.Add(-1)
	for {
		previous := c.max.Load()
		if active <= previous || c.max.CompareAndSwap(previous, active) {
			break
		}
	}
	c.started <- struct{}{}
	select {
	case <-c.allow:
		return robustQuestionJSON, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestLocalExecutorChangesLiveLimitInsideRunningBatch(t *testing.T) {
	client := &liveLimitLLM{started: make(chan struct{}, 8), allow: make(chan struct{}, 8)}
	limit := storage.NewConcurrencyLimiter(2)
	jobs := &localBatchStore{}
	executor := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), jobs, "test", LocalConfig{Limiter: limit})
	t.Cleanup(executor.Close)
	id, _, err := executor.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "live", Subject: "呼吸系统", Topic: "live", OutlineCode: "live.1"}}, 6, "live settings")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("initial capacity not used")
		}
	}
	if err := limit.SetLimit(1); err != nil {
		t.Fatal(err)
	}
	client.allow <- struct{}{}
	deadline := time.Now().Add(time.Second)
	for client.active.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("old call did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if client.calls.Load() != 2 {
		t.Fatal("shrink started another call before old work drained")
	}
	if err := limit.SetLimit(3); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("running batch could not use raised capacity")
		}
	}
	if executor.Capabilities().Concurrency != 3 || client.active.Load() != 3 {
		t.Fatal("runtime capabilities do not match active limit")
	}
	for i := 0; i < 5; i++ {
		client.allow <- struct{}{}
	}
	job := waitForJobs(t, executor, []string{id}, time.Second)[0]
	if job.Completed != 6 || job.Failed != 0 || client.calls.Load() != 6 || client.max.Load() > 3 {
		t.Fatalf("live changes cancelled/duplicated work or exceeded limit: %+v calls=%d max=%d", job, client.calls.Load(), client.max.Load())
	}
}

func (c *outOfOrderLLM) Complete(ctx context.Context, m []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.calls.Add(1)
	if messagesContainTopic(m, "held-unit") {
		close(c.started)
		select {
		case <-c.blocked:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return robustQuestionJSON, nil
}

func TestLocalExecutorResumesSparseOutOfOrderResults(t *testing.T) {
	client := &outOfOrderLLM{blocked: make(chan struct{}), started: make(chan struct{})}
	jobs := &localBatchStore{}
	questions := testutil.NewMemoryStore()
	executor := NewLocalExecutor(generator.NewService(client), questions, jobs, "test")
	t.Cleanup(executor.Close)
	points := []domain.KnowledgePoint{{ID: "held", Subject: "心血管系统", Topic: "held-unit", OutlineCode: "sparse.0"}, {ID: "fast1", Subject: "心血管系统", Topic: "fast-unit-1", OutlineCode: "sparse.1"}, {ID: "fast2", Subject: "心血管系统", Topic: "fast-unit-2", OutlineCode: "sparse.2"}}
	id, _, err := executor.GenerateAndSubmit(context.Background(), points, 1, "sparse")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("first unit not dispatched")
	}
	deadline := time.Now().Add(2 * time.Second)
	var record *storage.BatchJobRecord
	for {
		record, _ = jobs.GetBatchJob(context.Background(), id)
		if record.Completed == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocked first item prevented later progress: completed=%d failed=%d output=%s", record.Completed, record.Failed, record.OutputJSON)
		}
		time.Sleep(time.Millisecond)
	}
	var before localBatchOutput
	if err := json.Unmarshal([]byte(record.OutputJSON), &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Items) != 3 || before.Items[0].Status != "running" || before.Items[1].Status != "ok" || before.Items[2].Status != "ok" {
		t.Fatalf("sparse snapshot: %+v", before.Items)
	}
	if before.Questions[0].OutlineCode != "sparse.1" || before.Questions[1].OutlineCode != "sparse.2" {
		t.Fatal("completion order corrupted question association")
	}
	executor.Close()
	resumedClient := &robustLLM{}
	resumed := NewLocalExecutor(generator.NewService(resumedClient), questions, jobs, "test")
	t.Cleanup(resumed.Close)
	job := waitForJobs(t, resumed, []string{id}, 2*time.Second)[0]
	if job.Completed != 3 || job.Failed != 0 || resumedClient.calls() != 1 {
		t.Fatalf("resume must only generate the missing unit: %+v calls=%d", job, resumedClient.calls())
	}
	record, _ = jobs.GetBatchJob(context.Background(), id)
	var after localBatchOutput
	if err := json.Unmarshal([]byte(record.OutputJSON), &after); err != nil {
		t.Fatal(err)
	}
	for pos, q := range after.Questions {
		if q.OutlineCode != points[pos].OutlineCode || after.Items[pos].QuestionID != q.ID {
			t.Fatalf("unit %d association changed", pos)
		}
		if pos > 0 && q.ID != before.Questions[pos-1].ID {
			t.Fatal("already durable success was regenerated")
		}
	}
	result, err := resumed.ImportResults(context.Background(), id, nil)
	if err != nil || result.Saved != 3 {
		t.Fatalf("sparse results import: %+v %v", result, err)
	}
}

type failingProgressStore struct {
	localBatchStore
	updates atomic.Int32
}

// Hold a terminal update after its durable commit, before the old queue releases
// its in-process ownership. An immediate retry must not be silently dropped.
type terminalGapStore struct {
	localBatchStore
	committed, release chan struct{}
	once               sync.Once
}

func (s *terminalGapStore) UpdateBatchJob(ctx context.Context, job storage.BatchJobRecord) error {
	if err := s.localBatchStore.UpdateBatchJob(ctx, job); err != nil {
		return err
	}
	if job.Status == "completed" {
		s.once.Do(func() { close(s.committed); <-s.release })
	}
	return nil
}

func TestLocalExecutorRetryDuringTerminalCommitGap(t *testing.T) {
	client := &robustLLM{}
	client.setFailTopics("retry-gap-unit")
	jobs := &terminalGapStore{committed: make(chan struct{}), release: make(chan struct{})}
	executor := NewLocalExecutor(generator.NewService(client), testutil.NewMemoryStore(), jobs, "test")
	executor.RetryDelays = []time.Duration{time.Millisecond}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(jobs.release) }); executor.Close() })
	id, _, err := executor.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "gap", Subject: "呼吸系统", Topic: "retry-gap-unit", OutlineCode: "gap.1"}}, 1, "gap")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-jobs.committed:
	case <-time.After(time.Second):
		t.Fatal("terminal update not committed")
	}
	client.clearFailTopics()
	retried := make(chan error, 1)
	go func() { _, err := executor.RetryFailed(context.Background(), id); retried <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		job, _ := jobs.GetBatchJob(context.Background(), id)
		if job.Status == "in_progress" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry not reactivated")
		}
		time.Sleep(time.Millisecond)
	}
	release.Do(func() { close(jobs.release) })
	select {
	case err := <-retried:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry blocked after old queue finished")
	}
	job := waitForJobs(t, executor, []string{id}, time.Second)[0]
	if job.Completed != 1 || job.Failed != 0 || client.calls() != 4 {
		t.Fatalf("immediate retry was lost or duplicated: %+v calls=%d", job, client.calls())
	}
}

func (s *failingProgressStore) UpdateBatchJob(context.Context, storage.BatchJobRecord) error {
	s.updates.Add(1)
	return errors.New("injected progress write failure")
}

func TestLocalExecutorStopsBeforeGenerationWhenCheckpointFails(t *testing.T) {
	client := &robustLLM{}
	jobs := &failingProgressStore{}
	executor := NewLocalExecutor(generator.NewService(client), testutil.NewMemoryStore(), jobs, "test")
	t.Cleanup(executor.Close)
	id, _, err := executor.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "checkpoint", Topic: "checkpoint", OutlineCode: "checkpoint"}}, 1, "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for jobs.updates.Load() < 3 {
		if time.Now().After(deadline) {
			t.Fatal("checkpoint was not retried")
		}
		time.Sleep(time.Millisecond)
	}
	executor.Close()
	job, _ := jobs.GetBatchJob(context.Background(), id)
	if client.calls() != 0 || job.Status != "in_progress" || job.Completed != 0 {
		t.Fatalf("lost checkpoint must not produce a fake completion: %+v calls=%d", job, client.calls())
	}
	if _, _, err := executor.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "closed"}}, 1, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed executor accepted work: %v", err)
	}
}

func TestLocalExecutorConfiguredConcurrencyAndCancellation(t *testing.T) {
	client := &outOfOrderLLM{blocked: make(chan struct{}), started: make(chan struct{})}
	jobs := &localBatchStore{}
	executor := NewLocalExecutorWithConfig(generator.NewService(client), testutil.NewMemoryStore(), jobs, "test", LocalConfig{Concurrency: 1})
	t.Cleanup(executor.Close)
	if executor.Capabilities().Concurrency != 1 {
		t.Fatal("concurrency option was not applied before startup")
	}
	id, _, err := executor.GenerateAndSubmit(context.Background(), []domain.KnowledgePoint{{ID: "held", Topic: "held-unit"}, {ID: "wait", Topic: "waiting-unit"}}, 1, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("unit not started")
	}
	done := make(chan struct{})
	go func() { executor.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel an active model call")
	}
	job, _ := jobs.GetBatchJob(context.Background(), id)
	if job.Status != "in_progress" || job.Completed != 0 || job.Failed != 0 || client.calls.Load() != 1 || executor.limiter.Snapshot().Active != 0 {
		t.Fatalf("cancellation corrupted progress or leaked slots: %+v", job)
	}
}

func TestLocalExecutorCapsGlobalConcurrency(t *testing.T) {
	llmClient := &robustLLM{delayPerCall: 60 * time.Millisecond}
	executor := NewLocalExecutor(generator.NewService(llmClient), testutil.NewMemoryStore(), &localBatchStore{}, "test-model")
	ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{Actor: "teacher", OwnerID: "owner-1"})

	var jobIDs []string
	for i := 0; i < 3; i++ {
		points := []domain.KnowledgePoint{{ID: "kp-c1", Topic: "胸痛", Subject: "心血管系统", OutlineCode: "1.1"}}
		jobID, _, err := executor.GenerateAndSubmit(ctx, points, 1, "并发上限")
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, jobID)
	}
	jobs := waitForJobs(t, executor, jobIDs, 10*time.Second)
	for _, job := range jobs {
		if job.Completed != 1 || job.Failed != 0 {
			t.Fatalf("job %s did not fully complete: %+v", job.JobID, job)
		}
	}
	if got := llmClient.maxConcurrent(); got > 2 {
		t.Fatalf("concurrent generate calls exceeded cap: %d", got)
	}
	if got := llmClient.maxConcurrent(); got < 2 {
		t.Fatalf("expected parallel execution to reach the cap, max observed: %d", got)
	}
}

func TestLocalExecutorRetriesTransientFailure(t *testing.T) {
	llmClient := &robustLLM{failFirstN: 2}
	executor := NewLocalExecutor(generator.NewService(llmClient), testutil.NewMemoryStore(), &localBatchStore{}, "test-model")
	executor.RetryDelays = []time.Duration{time.Millisecond}
	ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{Actor: "teacher", OwnerID: "owner-1"})

	jobID, _, err := executor.GenerateAndSubmit(ctx, []domain.KnowledgePoint{{ID: "kp-r1", Topic: "胸痛", Subject: "心血管系统", OutlineCode: "1.1"}}, 1, "重试验证")
	if err != nil {
		t.Fatal(err)
	}
	jobs := waitForJobs(t, executor, []string{jobID}, 10*time.Second)
	if jobs[0].Completed != 1 || jobs[0].Failed != 0 {
		t.Fatalf("transient failure should be retried to success: %+v", jobs[0])
	}
	if got := llmClient.calls(); got < 3 {
		t.Fatalf("expected 3 attempts (2 failures + 1 success), got %d", got)
	}
}

func TestLocalExecutorRetryFailedUnits(t *testing.T) {
	llmClient := &robustLLM{delayPerCall: 5 * time.Millisecond}
	llmClient.setFailTopics("难产要点")
	executor := NewLocalExecutor(generator.NewService(llmClient), testutil.NewMemoryStore(), &localBatchStore{}, "test-model")
	executor.RetryDelays = []time.Duration{time.Millisecond}
	ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{Actor: "teacher", OwnerID: "owner-1"})

	points := []domain.KnowledgePoint{
		{ID: "kp-ok", Topic: "胸痛", Subject: "心血管系统", OutlineCode: "1.1"},
		{ID: "kp-bad", Topic: "难产要点", Subject: "妇产科学", OutlineCode: "1.2"},
	}
	jobID, total, err := executor.GenerateAndSubmit(ctx, points, 1, "重跑验证")
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
	jobs := waitForJobs(t, executor, []string{jobID}, 10*time.Second)
	if jobs[0].Completed != 1 || jobs[0].Failed != 1 {
		t.Fatalf("expected 1 ok + 1 failed before rerun: %+v", jobs[0])
	}

	// 故障恢复后重跑失败项
	llmClient.clearFailTopics()
	retried, err := executor.RetryFailed(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "in_progress" {
		t.Fatalf("rerun should flip job to in_progress, got %s", retried.Status)
	}
	jobs = waitForJobs(t, executor, []string{jobID}, 10*time.Second)
	if jobs[0].Completed != 2 || jobs[0].Failed != 0 {
		t.Fatalf("rerun should clear failures: %+v", jobs[0])
	}

	// 重跑完成后可正常导入全部 2 题
	result, err := executor.ImportResults(ctx, jobID, nil)
	if err != nil || result.Saved != 2 {
		t.Fatalf("import after rerun: result=%+v err=%v", result, err)
	}

	// 导入后重跑必须被拒绝（失败项视为放弃）
	if _, err := executor.RetryFailed(context.Background(), jobID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("rerun after import must report a recognizable state conflict: %v", err)
	}
}

func TestLocalExecutorResumeKeepsConcurrencyCap(t *testing.T) {
	llmClient := &robustLLM{delayPerCall: 40 * time.Millisecond}
	jobStore := &localBatchStore{}
	ctx := storage.WithQuestionChange(context.Background(), storage.QuestionChange{Actor: "teacher", OwnerID: "owner-1"})

	// 预置 4 个 in_progress 任务模拟重启前的遗留队列
	var jobIDs []string
	for i := 0; i < 4; i++ {
		points := []domain.KnowledgePoint{{ID: "kp-resume", Topic: "胸痛", Subject: "心血管系统", OutlineCode: "1.1"}}
		pointsJSON, _ := json.Marshal(points)
		record := storage.BatchJobRecord{
			ID: "local-resume-" + string(rune('a'+i)), OwnerID: "owner-1", Backend: localSingleAPIBackend,
			BackendProfile: "single-question-api", Model: "test-model", JobName: "恢复验证", Status: "in_progress",
			TotalCount: 1, PointsJSON: string(pointsJSON), OutputJSON: `{"questions":[],"items":[]}`,
			CreatedAt: time.Now().Format(time.RFC3339Nano),
		}
		if err := jobStore.SaveBatchJob(ctx, record); err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, record.ID)
	}

	// 构造即触发 resumePending：恢复执行受同一全局并发上限约束
	executor := NewLocalExecutor(generator.NewService(llmClient), testutil.NewMemoryStore(), jobStore, "test-model")
	jobs := waitForJobs(t, executor, jobIDs, 10*time.Second)
	for _, job := range jobs {
		if job.Completed != 1 || job.Failed != 0 {
			t.Fatalf("resumed job should complete: %+v", job)
		}
	}
	if got := llmClient.maxConcurrent(); got > 2 {
		t.Fatalf("resumed concurrency exceeded cap: %d", got)
	}
}

type pipelineGateClient struct {
	calls      atomic.Int32
	gate       chan struct{}
	failSecond bool
}

func (c *pipelineGateClient) Complete(ctx context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	n := c.calls.Add(1)
	if c.failSecond && n == 2 {
		return "", errors.New("second unit generation failed")
	}
	if !c.failSecond && n > 1 {
		select {
		case <-c.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return robustQuestionJSON, nil
}
func pipelineTestPG(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema := fmt.Sprintf("aigo_batch_pipeline_%d", time.Now().UnixNano())
	if _, err = db.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	store, err := postgres.New(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err = store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return store
}
func TestBatchPipelineQueuesFirstCheckBeforeSlowGenerationCompletes(t *testing.T) {
	store := pipelineTestPG(t)
	client := &pipelineGateClient{gate: make(chan struct{})}
	executor := NewLocalExecutorWithConfig(generator.NewService(client), store, store, "test", LocalConfig{Concurrency: 1, Pipeline: true})
	defer executor.Close()
	ctx := storage.WithQuestionChange(t.Context(), storage.QuestionChange{OwnerID: "pipe-user", Actor: "pipe-teacher"})
	id, _, err := executor.GenerateAndSubmit(ctx, []domain.KnowledgePoint{{OutlineCode: "pipe.1", Subject: "内科", Topic: "first"}, {OutlineCode: "pipe.2", Subject: "内科", Topic: "slow"}}, 1, "pipeline")
	if err != nil {
		t.Fatal(err)
	}
	var early *BatchJob
	waitPipelineCondition(t, 10*time.Second, func() bool {
		early, _ = executor.GetJobStatus(ctx, id)
		return early != nil && early.Completed == 1 && client.calls.Load() == 2
	})
	if early.Status != "in_progress" || !early.Pipeline || early.PipelineResult == nil || len(early.PipelineResult.QuestionIDs) != 1 {
		t.Fatalf("early result missing: %+v", early)
	}
	qid := early.PipelineResult.QuestionIDs[0]
	tasks, _ := store.LatestCheckTasksByQuestionIDs(ctx, []string{qid})
	if tasks[qid].Status != domain.AICheckTaskPending {
		t.Fatal("first check waits for whole batch")
	}
	q, _ := store.GetQuestion(ctx, qid)
	q.Status = domain.StatusAIReviewed
	if err = store.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteCheckTask(ctx, tasks[qid].ID); err != nil {
		t.Fatal(err)
	}
	// First usable question exists while generation of the second is still blocked.
	still, _ := executor.GetJobStatus(ctx, id)
	if still.Status != "in_progress" {
		t.Fatal("generation barrier was not held")
	}
	close(client.gate)
	waitPipelineCondition(t, 10*time.Second, func() bool { job, _ := executor.GetJobStatus(ctx, id); return job != nil && job.Status == "completed" })
	imported, err := executor.ImportResults(ctx, id, nil)
	if err != nil || imported.Saved != 2 {
		t.Fatalf("read-only result: %+v %v", imported, err)
	}
	q, _ = store.GetQuestion(ctx, qid)
	if q.Status != domain.StatusAIReviewed {
		t.Fatal("completion downgraded early pass")
	}
}
func TestBatchPipelineRetryGenerationFailuresKeepsCheckedUnits(t *testing.T) {
	store := pipelineTestPG(t)
	client := &pipelineGateClient{failSecond: true}
	executor := NewLocalExecutorWithConfig(generator.NewService(client), store, store, "test", LocalConfig{Concurrency: 1, Pipeline: true})
	defer executor.Close()
	executor.MaxAttempts = 1
	ctx := storage.WithQuestionChange(t.Context(), storage.QuestionChange{OwnerID: "retry-user", Actor: "retry-teacher"})
	id, _, err := executor.GenerateAndSubmit(ctx, []domain.KnowledgePoint{{OutlineCode: "pipe.1", Subject: "内科", Topic: "first"}, {OutlineCode: "pipe.2", Subject: "内科", Topic: "retry"}}, 1, "retry")
	if err != nil {
		t.Fatal(err)
	}
	var first *BatchJob
	waitPipelineCondition(t, 10*time.Second, func() bool {
		first, _ = executor.GetJobStatus(ctx, id)
		return first != nil && first.Status == "completed"
	})
	if first.PipelineResult == nil || len(first.PipelineResult.QuestionIDs) == 0 {
		t.Fatalf("fixture failed to generate: %+v calls=%d", first, client.calls.Load())
	}
	qid := first.PipelineResult.QuestionIDs[0]
	q, _ := store.GetQuestion(ctx, qid)
	q.Status = domain.StatusAIReviewed
	if err = store.SaveQuestion(ctx, *q); err != nil {
		t.Fatal(err)
	}
	tasks, _ := store.LatestCheckTasksByQuestionIDs(ctx, []string{qid})
	if err = store.CompleteCheckTask(ctx, tasks[qid].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = executor.RetryFailed(ctx, id); err != nil {
		t.Fatalf("streamed successful units must not block generation failure retry: %v", err)
	}
	waitPipelineCondition(t, 10*time.Second, func() bool {
		job, _ := executor.GetJobStatus(ctx, id)
		return job != nil && job.Status == "completed" && job.Completed == 2
	})
	q, _ = store.GetQuestion(ctx, qid)
	if q.Status != domain.StatusAIReviewed || client.calls.Load() != 3 {
		t.Fatalf("retry regenerated or overwrote success: %+v calls=%d", q, client.calls.Load())
	}
	tasks, _ = store.LatestCheckTasksByQuestionIDs(ctx, []string{qid})
	if tasks[qid].Attempts != 0 || tasks[qid].Status != domain.AICheckTaskSucceeded {
		t.Fatal("successful unit was checked again")
	}
}

func waitPipelineCondition(t *testing.T, budget time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("pipeline condition did not complete")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBatchPipelineRestartSkipsPreviouslyCheckedUnit(t *testing.T) {
	store := pipelineTestPG(t)
	client := &pipelineGateClient{gate: make(chan struct{})}
	first := NewLocalExecutorWithConfig(generator.NewService(client), store, store, "test", LocalConfig{Concurrency: 1, Pipeline: true})
	ctx := storage.WithQuestionChange(t.Context(), storage.QuestionChange{OwnerID: "restart-user", Actor: "restart-teacher"})
	id, _, err := first.GenerateAndSubmit(ctx, []domain.KnowledgePoint{{OutlineCode: "pipe.1", Subject: "内科", Topic: "first"}, {OutlineCode: "pipe.2", Subject: "内科", Topic: "slow"}}, 1, "restart")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	var early *BatchJob
	waitPipelineCondition(t, 10*time.Second, func() bool {
		early, _ = first.GetJobStatus(ctx, id)
		return early != nil && early.Completed == 1 && client.calls.Load() == 2
	})
	qid := early.PipelineResult.QuestionIDs[0]
	q, _ := store.GetQuestion(ctx, qid)
	q.Status = domain.StatusAIReviewed
	if err = store.SaveQuestion(ctx, *q); err != nil {
		first.Close()
		t.Fatal(err)
	}
	tasks, _ := store.LatestCheckTasksByQuestionIDs(ctx, []string{qid})
	if err = store.CompleteCheckTask(ctx, tasks[qid].ID); err != nil {
		first.Close()
		t.Fatal(err)
	}
	first.Close()
	resumedClient := &robustLLM{}
	resumed := NewLocalExecutorWithConfig(generator.NewService(resumedClient), store, store, "test", LocalConfig{Concurrency: 1, Pipeline: true})
	defer resumed.Close()
	waitPipelineCondition(t, 10*time.Second, func() bool {
		job, _ := resumed.GetJobStatus(ctx, id)
		return job != nil && job.Status == "completed" && job.Completed == 2
	})
	if atomic.LoadInt32(&resumedClient.totalCalls) != 1 {
		t.Fatal("restart regenerated completed unit")
	}
	q, _ = store.GetQuestion(ctx, qid)
	if q == nil || q.Status != domain.StatusAIReviewed || q.OwnerID != "restart-user" {
		t.Fatalf("restart changed checked result: %+v", q)
	}
}
