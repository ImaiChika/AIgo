// LocalExecutor 健壮性验收测试（修复与待办指引.txt 第二批 4/5 项）：
// 全局并发上限、生成失败自动重试、失败项重跑、重启恢复并发受控。
package batch

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
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
	if _, err := executor.RetryFailed(context.Background(), jobID); err == nil {
		t.Fatal("rerun after import must be rejected")
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
