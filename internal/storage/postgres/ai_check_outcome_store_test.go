package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"

	"github.com/lib/pq"
)

// newAICheckOutcomeTestStore 在独立 schema 中建立全新迁移库，测试结束即清理。
func newAICheckOutcomeTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL AI check outcome integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	schemaName := fmt.Sprintf("aigo_ai_check_outcome_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)
		adminDB.Close()
	})

	testDSN, err := dsnWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	return store, ctx
}

func aiCheckTestQuestion(t *testing.T, store *Store, ctx context.Context, id string) domain.A2Question {
	t.Helper()
	now := time.Now()
	q := domain.A2Question{
		ID: id, ClinicalStem: "患者，男，60岁，进行性吞咽困难3个月，体重下降 8kg……",
		Options: []domain.Option{{Label: "A", Text: "食管癌"}, {Label: "B", Text: "贲门失弛缓症"}, {Label: "C", Text: "食管良性狭窄"}, {Label: "D", Text: "食管静脉曲张"}},
		Answer:  "A", Difficulty: domain.DifficultyMedium, Status: domain.StatusAIDraft,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatalf("准备题目失败: %v", err)
	}
	return q
}

func passOutcome(q domain.A2Question, taskID string) storage.AICheckOutcome {
	updated := q
	updated.Status = domain.StatusAIReviewed
	updated.UpdatedAt = time.Now()
	return storage.AICheckOutcome{
		Result: domain.AIReviewResult{
			ID: "air-" + q.ID, QuestionID: q.ID, QuestionVersion: q.Version, Verdict: "pass",
			Scores: domain.ReviewScores{Scientific: 90, Logic: 88, A2Fit: 92, Answer: 94},
			Model:  "test-model", CreatedAt: time.Now(),
		},
		Question:       &updated,
		CompleteTaskID: taskID,
	}
}

func failOutcome(q domain.A2Question, taskID string) storage.AICheckOutcome {
	return storage.AICheckOutcome{
		Result: domain.AIReviewResult{
			ID: "air-" + q.ID, QuestionID: q.ID, QuestionVersion: q.Version, Verdict: "reject",
			Scores: domain.ReviewScores{Scientific: 40, Logic: 50, A2Fit: 45, Answer: 55},
			Model:  "test-model", CreatedAt: time.Now(),
		},
		Discard: &domain.AICheckDiscard{
			ID: "aicd-" + q.ID, QuestionID: q.ID, Verdict: "reject",
			StemSummary: "患者，男，60岁……", CreatedAt: time.Now(),
		},
		DeleteQuestionID: q.ID,
		CompleteTaskID:   taskID,
	}
}

// 通过结论：结果、状态推进、任务完成三者同时可见。
func TestApplyAICheckOutcomePassPersistsAll(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, store, ctx, "q-outcome-pass")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "act-pass", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}

	if err := store.ApplyAICheckOutcome(ctx, passOutcome(q, "act-pass")); err != nil {
		t.Fatalf("应用通过结论失败: %v", err)
	}
	saved, err := store.GetQuestion(ctx, q.ID)
	if err != nil || saved == nil {
		t.Fatalf("题目应存在: %v", err)
	}
	if saved.Status != domain.StatusAIReviewed || saved.Version != 1 {
		t.Fatalf("题目应推进为 ai_reviewed 且版本不变，实际 %+v", saved)
	}
	result, err := store.GetLatestByQuestionID(ctx, q.ID)
	if err != nil || result == nil || result.Verdict != "pass" {
		t.Fatalf("检查结果应留档: %v %+v", err, result)
	}
	latest, err := store.LatestCheckTasksByQuestionIDs(ctx, []string{q.ID})
	if err != nil || latest[q.ID].Status != domain.AICheckTaskSucceeded {
		t.Fatalf("任务应成功完成: %v %+v", err, latest[q.ID])
	}
}

// 不通过结论：淘汰档案留档、题目级联删除、任务完成同时生效；
// 检查结果随题目外键级联清理，与题目删除前的非事务行为一致。
func TestApplyAICheckOutcomeFailDeletesDraftAndKeepsDiscard(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, store, ctx, "q-outcome-fail")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "act-fail", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}

	if err := store.ApplyAICheckOutcome(ctx, failOutcome(q, "act-fail")); err != nil {
		t.Fatalf("应用不通过结论失败: %v", err)
	}
	if saved, err := store.GetQuestion(ctx, q.ID); err != nil || saved != nil {
		t.Fatalf("题目应被删除: %v %+v", err, saved)
	}
	discards, err := store.ListDiscardResultsByQuestionIDs(ctx, []string{q.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := discards[q.ID]; !ok {
		t.Fatal("淘汰档案应随事务留档")
	}
	if result, err := store.GetLatestByQuestionID(ctx, q.ID); err != nil || result != nil {
		t.Fatalf("题目删除后检查结果应级联清理: %v %+v", err, result)
	}
	// ai_check_tasks 对题目同样级联删除：题目删除后任务行消失，
	// 不会残留对已删除题目的重试任务（与事务化之前的既有行为一致）。
	latest, err := store.LatestCheckTasksByQuestionIDs(ctx, []string{q.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := latest[q.ID]; ok {
		t.Fatal("题目删除后检查任务应随级联清理")
	}
}

// 回滚：状态更新失败（旧实现中被静默吞掉的缺陷场景）时整体回滚，
// 题目保持草稿、无结果残留、任务保持执行中，worker 据此记录失败并重试。
func TestApplyAICheckOutcomeRollsBackOnQuestionUpdateFailure(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, store, ctx, "q-outcome-rollback")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "act-rollback", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	// 模拟 worker 已抢占任务（running、attempts=1）
	claimed, err := store.ClaimNextCheckTask(ctx, time.Minute)
	if err != nil || claimed == nil || claimed.QuestionID != q.ID {
		t.Fatalf("应抢占到任务: %v %+v", err, claimed)
	}

	// 内容不变却把版本号写成 2 → saveQuestion 版本冲突，模拟落库中途失败
	broken := passOutcome(q, claimed.ID)
	broken.Question.Version = 2
	err = store.ApplyAICheckOutcome(ctx, broken)
	if err == nil {
		t.Fatal("版本冲突应导致落库失败")
	}
	if !errors.Is(err, domain.ErrQuestionVersionConflict) {
		t.Fatalf("应返回版本冲突错误，实际 %v", err)
	}
	saved, _ := store.GetQuestion(ctx, q.ID)
	if saved == nil || saved.Status != domain.StatusAIDraft {
		t.Fatalf("回滚后题目应保持草稿态，实际 %+v", saved)
	}
	if result, _ := store.GetLatestByQuestionID(ctx, q.ID); result != nil {
		t.Fatal("回滚后不得残留检查结果")
	}
	latest, err := store.LatestCheckTasksByQuestionIDs(ctx, []string{q.ID})
	if err != nil || latest[q.ID].Status != domain.AICheckTaskRunning || latest[q.ID].LastError != "" {
		t.Fatalf("任务应保持执行中等待 FailCheckTask 安排重试，实际 %+v", latest[q.ID])
	}
}

// 回滚：淘汰档案主键冲突时整体回滚，题目不被删除（旧实现会先写档案再删题）。
func TestApplyAICheckOutcomeRollsBackOnDiscardConflict(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, store, ctx, "q-outcome-dup")
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "act-dup", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}

	// 预置同 ID 淘汰档案，触发主键冲突
	other := q
	other.ID = "q-outcome-dup-other"
	if err := store.SaveQuestion(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveDiscardResult(ctx, domain.AICheckDiscard{ID: "aicd-" + q.ID, QuestionID: other.ID, Verdict: "reject", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	outcome := failOutcome(q, "act-dup")
	if err := store.ApplyAICheckOutcome(ctx, outcome); err == nil {
		t.Fatal("淘汰档案主键冲突应导致落库失败")
	}
	if saved, _ := store.GetQuestion(ctx, q.ID); saved == nil {
		t.Fatal("回滚后题目不得被删除")
	}
	if result, _ := store.GetLatestByQuestionID(ctx, q.ID); result != nil {
		t.Fatal("回滚后不得残留检查结果")
	}
}
