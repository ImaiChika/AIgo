package aicheck

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

type liveCheckClient struct {
	started, allow chan struct{}
	active, calls  atomic.Int32
}

func (c *liveCheckClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	c.active.Add(1)
	c.calls.Add(1)
	defer c.active.Add(-1)
	c.started <- struct{}{}
	select {
	case <-c.allow:
		return (&recordingClient{}).Complete(ctx, m, o)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestCheckDispatcherLiveLimitsPreserveFirstCheckOnce(t *testing.T) {
	client := &liveCheckClient{started: make(chan struct{}, 8), allow: make(chan struct{}, 8)}
	svc, questions, _, tasks := newTestService(client)
	svc.WorkerLimiter = storage.NewConcurrencyLimiter(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := []string{"live-check-0", "live-check-1", "live-check-2", "live-check-3", "live-check-4"}
	for _, id := range ids {
		if err := questions.SaveQuestion(ctx, testDraft(id, 1)); err != nil {
			t.Fatal(err)
		}
	}
	svc.CheckAsync(ids...)
	svc.StartWorkers(ctx, 1)
	for i := 0; i < 2; i++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("initial check slots not used")
		}
	}
	_ = svc.WorkerLimiter.SetLimit(1)
	client.allow <- struct{}{}
	waitFor(t, time.Second, func() bool { return client.active.Load() == 1 })
	if client.calls.Load() != 2 {
		t.Fatal("shrinking checks started another before old work drained")
	}
	_ = svc.WorkerLimiter.SetLimit(3)
	for i := 0; i < 2; i++ {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatal("new check slots not used")
		}
	}
	for i := 0; i < 4; i++ {
		client.allow <- struct{}{}
	}
	waitFor(t, 2*time.Second, func() bool {
		counts, _ := tasks.CountCheckTasksByStatus(ctx)
		return counts[domain.AICheckTaskSucceeded] == 5
	})
	svc.CheckAsync(ids...)
	if client.calls.Load() != 5 {
		t.Fatal("settings change or re-enqueue repeated first checks")
	}
	for _, id := range ids {
		q, _ := questions.GetQuestion(ctx, id)
		if q == nil || q.Status != domain.StatusAIReviewed {
			t.Fatal("live checks lost outcome")
		}
	}
}

// recordingClient 返回固定 pass 结果的 LLM 客户端，并记录调用次数。
type recordingClient struct {
	calls atomic.Int64
}

func (c *recordingClient) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	c.calls.Add(1)
	raw, _ := json.Marshal(map[string]any{
		"verdict":    "pass",
		"scores":     map[string]int{"scientific": 90, "logic": 85, "a2_fit": 88, "answer": 92},
		"issues":     []any{},
		"suggestion": "质量良好",
	})
	return string(raw), nil
}

// failingCheckClient 返回 issues_found 结果。
type failingCheckClient struct{}

func (c *failingCheckClient) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	raw, _ := json.Marshal(map[string]any{
		"verdict": "issues_found",
		"scores":  map[string]int{"scientific": 75, "logic": 65, "a2_fit": 80, "answer": 88},
		"issues": []any{map[string]any{
			"field":    "explanation",
			"severity": "warning",
			"message":  "解析对干扰项的分析不够充分",
		}},
		"suggestion": "请补充解析",
	})
	return string(raw), nil
}

// metadataOnlyCheckClient 模拟旧检查模型把认知层次标签误判为硬错误。
type metadataOnlyCheckClient struct{}

func (c *metadataOnlyCheckClient) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	raw, _ := json.Marshal(map[string]any{
		"verdict": "reject",
		"scores":  map[string]int{"scientific": 95, "logic": 92, "a2_fit": 94, "answer": 96},
		"issues": []any{map[string]any{
			"field":    "cognitive_level",
			"severity": "error",
			"message":  "认知层次“简单应用”应改为“应用”",
		}},
		"suggestion": "应修正出题提示词",
	})
	return string(raw), nil
}

// errorClient 模拟 LLM 调用失败（网络/超时/网关错误），用于重试与耗尽测试。
type errorClient struct{}

func (c *errorClient) Complete(_ context.Context, _ []llm.Message, _ llm.GenerateOptions) (string, error) {
	return "", errors.New("模拟 LLM 调用失败")
}

func testDraft(id string, version int) domain.A2Question {
	return domain.A2Question{
		ID:           id,
		ClinicalStem: "男，45岁。反复上腹痛2年，加重1天。T 36.8℃，P 80次/分，R 18次/分，BP 120/80mmHg，腹软，上腹部压痛。该患者最可能的诊断是",
		Options: []domain.Option{
			{Label: "A", Text: "胃溃疡"},
			{Label: "B", Text: "十二指肠溃疡"},
			{Label: "C", Text: "胃癌"},
			{Label: "D", Text: "慢性胃炎"},
			{Label: "E", Text: "功能性消化不良"},
		},
		Answer:         "A",
		Explanation:    "正确答案为 A。该患者反复上腹痛，结合疼痛特点考虑胃溃疡。B 项疼痛规律更符合十二指肠溃疡；C 项缺乏消瘦等警示表现；D 项不能完整解释典型节律性疼痛；E 项应在排除器质性疾病后考虑。故选 A。",
		Difficulty:     "0.65",
		CognitiveLevel: "简单应用",
		ExamPoints:     "诊断与鉴别诊断，临床表现",
		OutlineCode:    "110.4.3.1.1",
		Profession:     "消化",
		System:         "消化系统",
		Status:         domain.StatusAIDraft,
		Version:        version,
	}
}

// newTestService 组装内存存储下的完整检查服务。
func newTestService(client llm.Client) (*Service, *testutil.MemoryStore, *testutil.MemoryAIReviewStore, *testutil.MemoryAICheckTaskStore) {
	store := testutil.NewMemoryStore()
	results := testutil.NewMemoryAIReviewStore()
	tasks := testutil.NewMemoryAICheckTaskStore()
	return NewService(client, store, results, tasks, "test-model"), store, results, tasks
}

// waitFor 等待条件成立或超时。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func TestCheckAsyncAdvancesDraftStatus(t *testing.T) {
	client := &recordingClient{}
	svc, store, results, tasks := newTestService(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.SaveQuestion(ctx, testDraft("q-async", 1)); err != nil {
		t.Fatal(err)
	}

	svc.StartWorkers(ctx, 1)
	svc.CheckAsync("q-async")

	waitFor(t, 3*time.Second, func() bool {
		stored, _ := store.GetQuestion(ctx, "q-async")
		return stored != nil && stored.Status == domain.StatusAIReviewed
	})
	result, err := results.GetLatestByQuestionID(ctx, "q-async")
	if err != nil || result == nil {
		t.Fatalf("应有持久化检查结果: %v", err)
	}
	if result.Verdict != "pass" {
		t.Fatalf("verdict 应为 pass，实际 %s", result.Verdict)
	}
	if result.QuestionVersion != 1 {
		t.Fatalf("结果应记录检查时版本 1，实际 %d", result.QuestionVersion)
	}
	// 任务应标记成功，进度统计显示已通过
	counts, err := tasks.CountCheckTasksByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[domain.AICheckTaskSucceeded] != 1 {
		t.Fatalf("任务应标记为 succeeded，实际 %v", counts)
	}
	items, progressCounts, err := svc.ProgressByQuestionIDs(ctx, []string{"q-async"})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].TaskStatus != domain.AICheckTaskSucceeded || items[0].Verdict != "pass" {
		t.Fatalf("进度快照不正确: %+v", items[0])
	}
	if items[0].CheckStartedAt == nil || items[0].CheckCompletedAt == nil || items[0].CheckCompletedAt.Before(*items[0].CheckStartedAt) {
		t.Fatalf("进度快照应包含可恢复的检查计时: %+v", items[0])
	}
	if progressCounts["passed"] != 1 {
		t.Fatalf("进度计数应包含 1 个通过，实际 %v", progressCounts)
	}
}

func TestCheckAsyncEnqueueIdempotent(t *testing.T) {
	svc, store, _, tasks := newTestService(&recordingClient{})

	ctx := context.Background()
	if err := store.SaveQuestion(ctx, testDraft("q-dedupe", 1)); err != nil {
		t.Fatal(err)
	}

	// worker 未启动时重复入队应合并为一条 pending 任务
	svc.CheckAsync("q-dedupe")
	svc.CheckAsync("q-dedupe")
	if got := svc.PendingCount(); got != 1 {
		t.Fatalf("重复入队应合并，实际排队 %d 个", got)
	}
	counts, _ := tasks.CountCheckTasksByStatus(ctx)
	if counts[domain.AICheckTaskPending] != 1 {
		t.Fatalf("任务表应只有 1 条 pending，实际 %v", counts)
	}
}

func TestClaimExclusive(t *testing.T) {
	svc, store, _, tasks := newTestService(&recordingClient{})
	svc.RetryBackoff = time.Millisecond

	ctx := context.Background()
	if err := store.SaveQuestion(ctx, testDraft("q-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveQuestion(ctx, testDraft("q-2", 1)); err != nil {
		t.Fatal(err)
	}
	svc.CheckAsync("q-1", "q-2")

	// 用短租约验证互斥与回收
	first, err := tasks.ClaimNextCheckTask(ctx, 50*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("第一次抢占应成功: %v", err)
	}
	second, err := tasks.ClaimNextCheckTask(ctx, 50*time.Millisecond)
	if err != nil || second == nil {
		t.Fatalf("第二次抢占应成功: %v", err)
	}
	if first.QuestionID == second.QuestionID {
		t.Fatalf("两个 worker 不得抢占同一任务: %s 与 %s", first.QuestionID, second.QuestionID)
	}
	third, err := tasks.ClaimNextCheckTask(ctx, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if third != nil {
		t.Fatalf("队列应为空，实际抢占到 %s", third.QuestionID)
	}

	// 租约过期后同一任务可被回收重占（模拟 worker 崩溃）
	time.Sleep(120 * time.Millisecond)
	reclaimed, err := tasks.ClaimNextCheckTask(ctx, time.Minute)
	if err != nil || reclaimed == nil {
		t.Fatalf("租约过期后应回收任务: %v", err)
	}
	if reclaimed.QuestionID != first.QuestionID {
		t.Fatalf("应回收原任务 %s，实际 %s", first.QuestionID, reclaimed.QuestionID)
	}
	if reclaimed.Attempts != 2 {
		t.Fatalf("回收重占应递增执行次数为 2，实际 %d", reclaimed.Attempts)
	}
}

func TestRetryThenExhaust(t *testing.T) {
	svc, store, results, tasks := newTestService(&errorClient{})
	svc.MaxAttempts = 2
	svc.RetryBackoff = time.Millisecond // 测试中立即重试

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.SaveQuestion(ctx, testDraft("q-retry", 1)); err != nil {
		t.Fatal(err)
	}

	svc.StartWorkers(ctx, 1)
	svc.CheckAsync("q-retry")

	waitFor(t, 5*time.Second, func() bool {
		counts, _ := tasks.CountCheckTasksByStatus(ctx)
		q, _ := store.GetQuestion(ctx, "q-retry")
		return counts[domain.AICheckTaskExhausted] == 1 && q == nil
	})
	// LLM调用耗尽不产生质量结论，删除草稿，仅留故障记录。
	items, counts, err := svc.ProgressByQuestionIDs(ctx, []string{"q-retry"})
	if err != nil || counts["exhausted"] != 1 || counts["discarded"] != 0 || len(items) != 1 || !items[0].CheckFailed || items[0].Question != nil || items[0].Attempts != 2 {
		t.Fatalf("故障进度应正确统计且无内容: %+v %+v %v", items, counts, err)
	}
	if r, _ := results.GetLatestByQuestionID(ctx, "q-retry"); r != nil {
		t.Fatal("LLM 调用失败不应产生检查结果")
	}
	if task, ok := func() (domain.AICheckTask, bool) {
		latest, err := tasks.LatestCheckTasksByQuestionIDs(ctx, []string{"q-retry"})
		if err != nil {
			t.Fatal(err)
		}
		task, ok := latest["q-retry"]
		return task, ok
	}(); !ok || task.LastError == "" || task.Attempts != 2 {
		t.Fatalf("耗尽任务应记录最后一次错误与执行次数，实际 %+v", task)
	}
}

// 首次检查任务耗尽后保持故障终态，不允许创建第二次检查。
func TestExhaustedTaskCannotBeRequeued(t *testing.T) {
	svc, store, _, tasks := newTestService(&recordingClient{})
	svc.MaxAttempts = 1
	svc.RetryBackoff = time.Millisecond

	ctx := context.Background()
	if err := store.SaveQuestion(ctx, testDraft("q-again", 1)); err != nil {
		t.Fatal(err)
	}
	svc.CheckAsync("q-again")
	// 不启动 worker，手工模拟一次失败至耗尽
	task, err := tasks.ClaimNextCheckTask(ctx, time.Minute)
	if err != nil || task == nil {
		t.Fatal("应抢占到任务")
	}
	if err := tasks.FailCheckTask(ctx, task.ID, "模拟失败", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	counts, _ := tasks.CountCheckTasksByStatus(ctx)
	if counts[domain.AICheckTaskExhausted] != 1 {
		t.Fatalf("任务应已耗尽，实际 %v", counts)
	}

	// 再次入队必须保持原耗尽任务，不创建第二轮检查。
	svc.CheckAsync("q-again")
	counts, _ = tasks.CountCheckTasksByStatus(ctx)
	if counts[domain.AICheckTaskPending] != 0 || counts[domain.AICheckTaskExhausted] != 1 {
		t.Fatalf("耗尽后不应再次入队，实际 %v", counts)
	}
}

func TestCheckAsyncDisabled(t *testing.T) {
	svc, _, _, _ := newTestService(&recordingClient{})
	svc.SetAutoCheckEnabled(false)

	svc.CheckAsync("q-disabled")
	if got := svc.PendingCount(); got != 0 {
		t.Fatalf("自动检查关闭时不应入队，实际排队 %d 个", got)
	}
}

// 已通过首次检查的题目禁止再次调用 AI 检查。
func TestCheckQuestionRejectsRecheckAfterAIReviewed(t *testing.T) {
	svc, store, _, _ := newTestService(&failingCheckClient{})

	ctx := context.Background()
	q := testDraft("q-keep", 1)
	q.Status = domain.StatusAIReviewed
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CheckQuestion(ctx, "q-keep"); err == nil {
		t.Fatal("ai_reviewed 题目不应允许复检")
	}
	stored, _ := store.GetQuestion(ctx, "q-keep")
	if stored == nil || stored.Status != domain.StatusAIReviewed {
		t.Fatalf("拒绝复检不应改动题目")
	}
}

func TestCheckQuestionDoesNotDiscardForMetadataOnlyIssue(t *testing.T) {
	svc, store, results, _ := newTestService(&metadataOnlyCheckClient{})

	ctx := context.Background()
	if err := store.SaveQuestion(ctx, testDraft("q-metadata-only", 1)); err != nil {
		t.Fatal(err)
	}

	result, err := svc.CheckQuestion(ctx, "q-metadata-only")
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "pass" {
		t.Fatalf("只有元数据问题时应通过 AI 检查，实际 %s", result.Verdict)
	}
	if len(result.Issues) != 1 || result.Issues[0].Severity != "info" {
		t.Fatalf("元数据问题应降为 info，实际 %+v", result.Issues)
	}
	stored, _ := store.GetQuestion(ctx, "q-metadata-only")
	if stored == nil || stored.Status != domain.StatusAIReviewed {
		t.Fatalf("元数据问题不应淘汰题目，实际 %+v", stored)
	}
	if discards, err := results.ListDiscardResultsByQuestionIDs(ctx, []string{"q-metadata-only"}); err != nil {
		t.Fatal(err)
	} else if len(discards) != 0 {
		t.Fatalf("元数据问题不应生成淘汰记录，实际 %+v", discards)
	}
}

// 首次检查不通过的草稿自动淘汰：题目物理删除、淘汰原因留档供生成页展示。
func TestCheckQuestionDiscardsFailedDraft(t *testing.T) {
	svc, store, results, _ := newTestService(&failingCheckClient{})

	ctx := context.Background()
	if err := store.SaveQuestion(ctx, testDraft("q-discard", 1)); err != nil {
		t.Fatal(err)
	}

	result, err := svc.CheckQuestion(ctx, "q-discard")
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "issues_found" {
		t.Fatalf("verdict 应为 issues_found，实际 %s", result.Verdict)
	}
	if stored, _ := store.GetQuestion(ctx, "q-discard"); stored != nil {
		t.Fatal("检查不通过的草稿应被自动淘汰删除")
	}
	discards, err := results.ListDiscardResultsByQuestionIDs(ctx, []string{"q-discard"})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := discards["q-discard"]
	if !ok {
		t.Fatal("应保留淘汰记录")
	}
	if d.Verdict != "issues_found" || d.StemSummary == "" {
		t.Fatalf("淘汰记录应含结论与题干摘要，实际 %+v", d)
	}
}

type slowCheckClient struct {
	delay time.Duration
	calls int
}

func (c *slowCheckClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	c.calls++
	select {
	case <-time.After(c.delay):
		return (&recordingClient{}).Complete(ctx, m, o)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestSlowCheckFitsDefaultBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &slowCheckClient{delay: 119 * time.Second}
		svc, store, _, tasks := newTestService(client)
		ctx := context.Background()
		if err := store.SaveQuestion(ctx, testDraft("slow-budget", 1)); err != nil {
			t.Fatal(err)
		}
		svc.CheckAsync("slow-budget")
		task, err := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration())
		if err != nil || task == nil {
			t.Fatalf("claim: %+v %v", task, err)
		}
		svc.executeTask(ctx, task)
		q, _ := store.GetQuestion(ctx, "slow-budget")
		if q == nil || q.Status != domain.StatusAIReviewed || client.calls != 1 {
			t.Fatalf("119-second check must pass once under default budget: %+v calls=%d", q, client.calls)
		}
		if svc.leaseDuration() != 150*time.Second {
			t.Fatalf("lease=%s", svc.leaseDuration())
		}
	})
}

func TestTimeoutExhaustionNeverStartsFourthCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &slowCheckClient{delay: time.Minute}
		svc, store, _, tasks := newTestService(client)
		svc.CheckTimeout = 10 * time.Second
		svc.RetryBackoff = time.Second
		ctx := context.Background()
		if err := store.SaveQuestion(ctx, testDraft("timeout-discard", 1)); err != nil {
			t.Fatal(err)
		}
		svc.CheckAsync("timeout-discard")
		for i := 0; i < 3; i++ {
			task, err := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration())
			if err != nil || task == nil {
				t.Fatalf("attempt %d claim: %+v %v", i+1, task, err)
			}
			svc.executeTask(ctx, task)
			time.Sleep(svc.retryBackoffFor(task.Attempts))
		}
		svc.CheckAsync("timeout-discard")
		task, err := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration())
		if err != nil || task != nil || client.calls != 3 {
			t.Fatalf("must stop after exactly 3 calls: %+v calls=%d err=%v", task, client.calls, err)
		}
		q, _ := store.GetQuestion(ctx, "timeout-discard")
		items, counts, err := svc.ProgressByQuestionIDs(ctx, []string{"timeout-discard"})
		if q != nil || err != nil || counts["exhausted"] != 1 || counts["passed"] != 0 || len(items) != 1 || items[0].Attempts != 3 || items[0].Scores != nil || items[0].StemSummary != "" {
			t.Fatalf("terminal failure must discard content and report zero success: q=%+v items=%+v counts=%+v err=%v", q, items, counts, err)
		}
	})
}

func TestDefaultTimeoutExhaustsInSixMinutesFortyFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &slowCheckClient{delay: 121 * time.Second}
		svc, store, _, tasks := newTestService(client)
		ctx := context.Background()
		if err := store.SaveQuestion(ctx, testDraft("default-timeout", 1)); err != nil {
			t.Fatal(err)
		}
		svc.CheckAsync("default-timeout")
		started := time.Now()
		for attempt := 1; attempt <= 3; attempt++ {
			task, err := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration())
			if err != nil || task == nil || task.Attempts != attempt {
				t.Fatalf("claim attempt %d: %+v %v", attempt, task, err)
			}
			svc.executeTask(ctx, task)
			if attempt < 3 {
				time.Sleep(svc.retryBackoffFor(attempt))
			}
		}
		if elapsed := time.Since(started); elapsed != 405*time.Second {
			t.Fatalf("default execution and backoff budget=%s, want 6m45s", elapsed)
		}
		task, err := tasks.ClaimNextCheckTask(ctx, svc.leaseDuration())
		if err != nil || task != nil || client.calls != 3 {
			t.Fatalf("unexpected fourth call: task=%+v calls=%d err=%v", task, client.calls, err)
		}
		q, _ := store.GetQuestion(ctx, "default-timeout")
		items, counts, err := svc.ProgressByQuestionIDs(ctx, []string{"default-timeout"})
		if q != nil || err != nil || counts["exhausted"] != 1 || len(items) != 1 || items[0].Attempts != 3 {
			t.Fatalf("default timeout must discard and report exhaustion: q=%+v items=%+v counts=%+v err=%v", q, items, counts, err)
		}
	})
}
