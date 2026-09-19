package aicheck

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

// outcomeStoreStub 包装内存题目存储，附加可注入故障的事务化落库能力。
// 它模拟真实 PostgreSQL 事务的原子性：全部步骤无故障时一次性提交；
// 任一步骤注入故障时整体不落库，调用方收到错误以便重试。
type outcomeStoreStub struct {
	storage.QuestionStore
	reviews storage.AIReviewStore
	tasks   storage.AICheckTaskStore

	mu      sync.Mutex
	failAt  string   // 一次性故障注入点：question / discard / delete / task；空表示不注入
	applied []string // 成功提交的动作序列（观测用）
}

func newOutcomeStoreStub() *outcomeStoreStub {
	return &outcomeStoreStub{
		QuestionStore: testutil.NewMemoryStore(),
		reviews:       testutil.NewMemoryAIReviewStore(),
		tasks:         testutil.NewMemoryAICheckTaskStore(),
	}
}

func (o *outcomeStoreStub) ApplyAICheckOutcome(ctx context.Context, outcome storage.AICheckOutcome) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	steps := []string{"result"}
	if outcome.Question != nil {
		steps = append(steps, "question")
	}
	if outcome.Discard != nil {
		steps = append(steps, "discard")
	}
	if outcome.DeleteQuestionID != "" {
		steps = append(steps, "delete")
	}
	if outcome.CompleteTaskID != "" {
		steps = append(steps, "task")
	}
	for _, step := range steps {
		if o.failAt == step {
			o.failAt = ""
			return errors.New("注入的事务故障: " + step)
		}
	}
	// 无故障：按真实事务语义一次性提交
	if err := o.reviews.SaveReviewResult(ctx, outcome.Result); err != nil {
		return err
	}
	if outcome.Question != nil {
		if err := o.QuestionStore.SaveQuestion(ctx, *outcome.Question); err != nil {
			return err
		}
	}
	if outcome.Discard != nil {
		if err := o.reviews.SaveDiscardResult(ctx, *outcome.Discard); err != nil {
			return err
		}
	}
	if outcome.DeleteQuestionID != "" {
		if err := o.QuestionStore.DeleteQuestion(ctx, outcome.DeleteQuestionID); err != nil {
			return err
		}
	}
	if outcome.CompleteTaskID != "" {
		if err := o.tasks.CompleteCheckTask(ctx, outcome.CompleteTaskID); err != nil {
			return err
		}
	}
	o.applied = append(o.applied, steps...)
	return nil
}

func (o *outcomeStoreStub) injectFailure(step string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failAt = step
}

func (o *outcomeStoreStub) appliedSteps() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.applied...)
}

func newOutcomeTestService(client llm.Client) (*Service, *outcomeStoreStub) {
	stub := newOutcomeStoreStub()
	return NewService(client, stub, stub.reviews, stub.tasks, "test-model"), stub
}

func enqueueTask(t *testing.T, tasks storage.AICheckTaskStore, questionID string) string {
	t.Helper()
	task := domain.AICheckTask{
		ID:          "act-" + questionID,
		QuestionID:  questionID,
		Status:      domain.AICheckTaskPending,
		MaxAttempts: 3,
	}
	created, err := tasks.EnqueueCheckTask(context.Background(), task)
	if err != nil || !created {
		t.Fatalf("入队失败: created=%v err=%v", created, err)
	}
	return task.ID
}

func taskStatus(t *testing.T, tasks storage.AICheckTaskStore, ctx context.Context, questionID string) string {
	t.Helper()
	latest, err := tasks.LatestCheckTasksByQuestionIDs(ctx, []string{questionID})
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	task, ok := latest[questionID]
	if !ok {
		t.Fatal("任务不存在")
	}
	return task.Status
}

// 检查通过：结果落库、草稿推进为 ai_reviewed、任务同事务完成。
func TestOutcomeStorePassAdvancesDraftAndCompletesTask(t *testing.T) {
	svc, stub := newOutcomeTestService(&recordingClient{})
	ctx := context.Background()
	q := testDraft("q-pass", 1)
	if err := stub.SaveQuestion(ctx, q); err != nil {
		t.Fatalf("准备题目失败: %v", err)
	}
	taskID := enqueueTask(t, stub.tasks, q.ID)

	if _, err := svc.checkQuestion(ctx, q.ID, taskID); err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	saved, _ := stub.GetQuestion(ctx, q.ID)
	if saved == nil || saved.Status != domain.StatusAIReviewed {
		t.Fatalf("题目应推进为 ai_reviewed，实际 %+v", saved)
	}
	if saved.Version != 1 {
		t.Fatalf("状态推进不应改变版本号，实际 %d", saved.Version)
	}
	if got := taskStatus(t, stub.tasks, ctx, q.ID); got != domain.AICheckTaskSucceeded {
		t.Fatalf("任务应为 succeeded，实际 %s", got)
	}
	steps := stub.appliedSteps()
	if len(steps) != 3 || steps[0] != "result" || steps[1] != "question" || steps[2] != "task" {
		t.Fatalf("落库动作序列不符: %v", steps)
	}
}

// 首检不通过：淘汰档案留档、题目删除、任务完成，三者同事务。
// 淘汰档案同时记录归属人与完整题目快照，供前端"查看原题"使用。
func TestOutcomeStoreFailDeletesDraftAndArchivesDiscard(t *testing.T) {
	svc, stub := newOutcomeTestService(&failingCheckClient{})
	ctx := context.Background()
	q := testDraft("q-fail", 1)
	q.OwnerID = "owner-1"
	if err := stub.SaveQuestion(ctx, q); err != nil {
		t.Fatalf("准备题目失败: %v", err)
	}
	taskID := enqueueTask(t, stub.tasks, q.ID)

	if _, err := svc.checkQuestion(ctx, q.ID, taskID); err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if saved, _ := stub.GetQuestion(ctx, q.ID); saved != nil {
		t.Fatal("不通过的草稿应被删除")
	}
	discards, err := stub.reviews.ListDiscardResultsByQuestionIDs(ctx, []string{q.ID})
	if err != nil {
		t.Fatalf("查询淘汰档案失败: %v", err)
	}
	d, ok := discards[q.ID]
	if !ok {
		t.Fatal("淘汰档案应留档")
	}
	if !strings.Contains(d.StemSummary, "男，45岁") {
		t.Fatalf("淘汰档案应包含题干摘要: %q", d.StemSummary)
	}
	if d.OwnerID != "owner-1" {
		t.Fatalf("淘汰档案应记录归属人，实际 %q", d.OwnerID)
	}
	if d.Question == nil {
		t.Fatal("淘汰档案应保留完整题目快照")
	}
	if !strings.Contains(d.Question.ClinicalStem, "男，45岁") || len(d.Question.Options) != 5 || d.Question.Answer != "A" {
		t.Fatalf("题目快照应含题干、选项与答案: %+v", d.Question)
	}
	if got := taskStatus(t, stub.tasks, ctx, q.ID); got != domain.AICheckTaskSucceeded {
		t.Fatalf("任务应为 succeeded，实际 %s", got)
	}

	// 进度接口同样应带回评分、模型与题目快照，供淘汰详情展示。
	items, _, err := svc.ProgressByQuestionIDs(ctx, []string{q.ID})
	if err != nil || len(items) != 1 {
		t.Fatalf("查询进度失败: items=%v err=%v", items, err)
	}
	it := items[0]
	if !it.Discarded || it.DiscardOwnerID != "owner-1" || it.Scores == nil || it.Question == nil {
		t.Fatalf("淘汰进度项应带归属、评分与快照: %+v", it)
	}
}

// 故障注入：任一步失败整体回滚——草稿仍是 ai_draft、无结果、任务未完成，可安全重试。
// 这正是旧分步写入的缺陷场景：旧实现中“结果已保存但状态更新失败”会被当作成功。
func TestOutcomeStoreInjectedFailureRollsBackAtomically(t *testing.T) {
	svc, stub := newOutcomeTestService(&recordingClient{})
	ctx := context.Background()
	q := testDraft("q-atomic", 1)
	if err := stub.SaveQuestion(ctx, q); err != nil {
		t.Fatalf("准备题目失败: %v", err)
	}
	taskID := enqueueTask(t, stub.tasks, q.ID)

	stub.injectFailure("question")
	if _, err := svc.checkQuestion(ctx, q.ID, taskID); err == nil {
		t.Fatal("注入故障后检查应返回错误以便任务重试")
	}
	if saved, _ := stub.GetQuestion(ctx, q.ID); saved == nil || saved.Status != domain.StatusAIDraft {
		t.Fatal("事务回滚后草稿状态不得改变")
	}
	if r, _ := stub.reviews.GetLatestByQuestionID(ctx, q.ID); r != nil {
		t.Fatal("事务回滚后不得残留检查结果")
	}
	if got := taskStatus(t, stub.tasks, ctx, q.ID); got != domain.AICheckTaskPending {
		t.Fatalf("事务回滚后任务应保持待执行，实际 %s", got)
	}

	// 故障解除后重试成功
	if _, err := svc.checkQuestion(ctx, q.ID, taskID); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if saved, _ := stub.GetQuestion(ctx, q.ID); saved == nil || saved.Status != domain.StatusAIReviewed {
		t.Fatal("重试后题目应推进为 ai_reviewed")
	}
}

// 已进入人工流程的题目禁止复检。
func TestOutcomeStoreRejectsHumanFlowRecheck(t *testing.T) {
	svc, stub := newOutcomeTestService(&recordingClient{})
	ctx := context.Background()
	q := testDraft("q-reviewing", 1)
	q.Status = domain.StatusReviewing
	if err := stub.SaveQuestion(ctx, q); err != nil {
		t.Fatalf("准备题目失败: %v", err)
	}

	if _, err := svc.CheckQuestion(ctx, q.ID); err == nil {
		t.Fatal("审核中题目不应允许复检")
	}
	saved, _ := stub.GetQuestion(ctx, q.ID)
	if saved == nil || saved.Status != domain.StatusReviewing {
		t.Fatalf("审核中的题目状态不得被 AI 检查改动，实际 %+v", saved)
	}
	if steps := stub.appliedSteps(); len(steps) != 0 {
		t.Fatalf("拒绝复检不应落库，实际动作: %v", steps)
	}
}

// 已通过首次检查的题目禁止复检。
func TestOutcomeStoreRejectsReviewedQuestionRecheck(t *testing.T) {
	svc, stub := newOutcomeTestService(&failingCheckClient{})
	ctx := context.Background()
	q := testDraft("q-reviewed-fail", 1)
	q.Status = domain.StatusAIReviewed
	if err := stub.SaveQuestion(ctx, q); err != nil {
		t.Fatalf("准备题目失败: %v", err)
	}

	if _, err := svc.CheckQuestion(ctx, q.ID); err == nil {
		t.Fatal("已检查题目不应允许复检")
	}
	if saved, _ := stub.GetQuestion(ctx, q.ID); saved == nil {
		t.Fatal("非草稿题目不得被 AI 检查删除")
	}
	discards, _ := stub.reviews.ListDiscardResultsByQuestionIDs(ctx, []string{q.ID})
	if len(discards) != 0 {
		t.Fatal("非草稿题目不得产生淘汰档案")
	}
	if r, _ := stub.reviews.GetLatestByQuestionID(ctx, q.ID); r != nil {
		t.Fatal("拒绝复检不应生成第二份结果")
	}
}
