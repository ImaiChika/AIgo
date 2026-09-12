package aicheck

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage/testutil"
)

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
		return counts[domain.AICheckTaskExhausted] == 1
	})
	// LLM 调用始终失败：无检查结果，题目状态保持草稿
	stored, _ := store.GetQuestion(ctx, "q-retry")
	if stored.Status != domain.StatusAIDraft {
		t.Fatalf("检查耗尽的题目应保持 ai_draft，实际 %s", stored.Status)
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

// 重试耗尽的题目再次入队会开启新一轮检查周期（管理员补查路径）。
func TestRequeueAfterExhausted(t *testing.T) {
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

	// 再次入队：新建任务，开启新一轮重试
	svc.CheckAsync("q-again")
	counts, _ = tasks.CountCheckTasksByStatus(ctx)
	if counts[domain.AICheckTaskPending] != 1 {
		t.Fatalf("耗尽后再次入队应有 1 条新 pending 任务，实际 %v", counts)
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

// 已通过 AI 检查的题目（ai_reviewed）即使重新检查不通过，状态也保持不变：
// AI 检查只在首次创建时执行一次，人工修改后的把关责任在专家审核环节。
func TestCheckQuestionKeepsAIReviewedOnFailure(t *testing.T) {
	svc, store, results, _ := newTestService(&failingCheckClient{})

	ctx := context.Background()
	q := testDraft("q-keep", 1)
	q.Status = domain.StatusAIReviewed
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}

	result, err := svc.CheckQuestion(ctx, "q-keep")
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "issues_found" {
		t.Fatalf("verdict 应为 issues_found，实际 %s", result.Verdict)
	}
	stored, _ := store.GetQuestion(ctx, "q-keep")
	if stored == nil || stored.Status != domain.StatusAIReviewed {
		t.Fatalf("重新检查不通过不应改动或删除 ai_reviewed 题目")
	}
	if r, _ := results.GetLatestByQuestionID(ctx, "q-keep"); r == nil {
		t.Fatal("重新检查结果应已保存")
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
