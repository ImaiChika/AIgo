package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"aigo/internal/domain"

	"github.com/lib/pq"
)

// TestAICheckTaskQueueSemantics 在真实 PostgreSQL 上验证检查任务队列的核心语义：
// 幂等入队、抢占互斥（FOR UPDATE SKIP LOCKED）、租约回收、重试耗尽与计数查询。
func TestAICheckTaskQueueSemantics(t *testing.T) {
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL AI check task queue integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	schemaName := fmt.Sprintf("aigo_ai_check_task_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	defer adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)

	testDSN, err := dsnWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if err := store.CheckSchemaVersion(ctx); err != nil {
		t.Fatalf("schema version check failed: %v", err)
	}

	now := time.Now()
	for _, id := range []string{"q-task-1", "q-task-2"} {
		question := domain.A2Question{
			ID: id, ClinicalStem: "患者，男，60岁，进行性吞咽困难3个月……",
			Options: []domain.Option{{Label: "A", Text: "食管癌"}, {Label: "B", Text: "贲门失弛缓症"}, {Label: "C", Text: "食管良性狭窄"}, {Label: "D", Text: "食管静脉曲张"}},
			Answer:  "A", Difficulty: domain.DifficultyMedium, Status: domain.StatusAIDraft,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.SaveQuestion(ctx, question); err != nil {
			t.Fatal(err)
		}
	}

	// 幂等入队：同题重复入队不新建
	created, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{
		ID: "act-q-task-1-1", QuestionID: "q-task-1", Status: domain.AICheckTaskPending, MaxAttempts: 3,
	})
	if err != nil || !created {
		t.Fatalf("首次入队应创建任务: created=%v err=%v", created, err)
	}
	created, err = store.EnqueueCheckTask(ctx, domain.AICheckTask{
		ID: "act-q-task-1-2", QuestionID: "q-task-1", Status: domain.AICheckTaskPending, MaxAttempts: 3,
	})
	if err != nil || created {
		t.Fatalf("重复入队不应新建任务: created=%v err=%v", created, err)
	}
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{
		ID: "act-q-task-2-1", QuestionID: "q-task-2", Status: domain.AICheckTaskPending, MaxAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}

	// 抢占互斥：两次抢占得到不同题目，第三次为空
	// 用短租约验证互斥与回收
	first, err := store.ClaimNextCheckTask(ctx, 50*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("第一次抢占应成功: %v", err)
	}
	second, err := store.ClaimNextCheckTask(ctx, 50*time.Millisecond)
	if err != nil || second == nil {
		t.Fatalf("第二次抢占应成功: %v", err)
	}
	if first.QuestionID == second.QuestionID {
		t.Fatalf("两个 worker 不得抢占同一任务: %s 与 %s", first.QuestionID, second.QuestionID)
	}
	if first.Attempts != 1 {
		t.Fatalf("抢占应递增执行次数为 1，实际 %d", first.Attempts)
	}
	third, err := store.ClaimNextCheckTask(ctx, 50*time.Millisecond)
	if err != nil || third != nil {
		t.Fatalf("队列应为空: %v, %v", third, err)
	}

	// 租约回收：租约过期后原任务可被重新抢占，执行次数递增
	time.Sleep(150 * time.Millisecond)
	reclaimed, err := store.ClaimNextCheckTask(ctx, time.Minute)
	if err != nil || reclaimed == nil {
		t.Fatalf("租约过期后应回收任务: %v", err)
	}
	if reclaimed.QuestionID != first.QuestionID || reclaimed.Attempts != 2 {
		t.Fatalf("应回收 %s 且执行次数为 2，实际 %s/%d", first.QuestionID, reclaimed.QuestionID, reclaimed.Attempts)
	}

	// 完成 + 失败耗尽
	if err := store.CompleteCheckTask(ctx, reclaimed.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FailCheckTask(ctx, second.ID, "模拟网关超时", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	latest, err := store.LatestCheckTasksByQuestionIDs(ctx, []string{"q-task-1", "q-task-2", "q-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if latest["q-task-1"].Status != domain.AICheckTaskSucceeded {
		t.Fatalf("q-task-1 应为 succeeded，实际 %s", latest["q-task-1"].Status)
	}
	if latest["q-task-2"].Status != domain.AICheckTaskPending || latest["q-task-2"].LastError == "" {
		t.Fatalf("q-task-2 失败后应回 pending 并记录错误，实际 %+v", latest["q-task-2"])
	}
	if _, ok := latest["q-missing"]; ok {
		t.Fatal("无任务记录的题目不应出现")
	}

	// 耗尽：真实序列 = 失败后退避到期，worker 重新抢占（attempts 递增）再失败，直至达到上限
	for i := 0; i < 2; i++ {
		time.Sleep(5 * time.Millisecond) // 等退避到期
		t2, err := store.ClaimNextCheckTask(ctx, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if t2 == nil || t2.QuestionID != "q-task-2" {
			t.Fatalf("第 %d 次应重新抢占 q-task-2，实际 %+v", i+2, t2)
		}
		if err := store.FailCheckTask(ctx, t2.ID, fmt.Sprintf("失败第%d次", i+2), time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	counts, err := store.CountCheckTasksByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[domain.AICheckTaskSucceeded] != 1 || counts[domain.AICheckTaskExhausted] != 1 {
		t.Fatalf("任务计数不正确: %v", counts)
	}
	latest, _ = store.LatestCheckTasksByQuestionIDs(ctx, []string{"q-task-2"})
	if _, ok := latest["q-task-2"]; ok {
		t.Fatal("耗尽题删除时任务级联清理")
	}
	discards, err := store.ListDiscardResultsByQuestionIDs(ctx, []string{"q-task-2"})
	if err != nil || discards["q-task-2"].Failure == nil || discards["q-task-2"].Failure.LastError != "失败第3次" || discards["q-task-2"].Failure.Attempts != 3 {
		t.Fatalf("故障留档应记录最终错误和次数: %+v %v", discards, err)
	}
}

func TestExhaustedCheckDiscardsDraftWithoutContent(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q := domain.A2Question{ID: "q-failed-discard", ClinicalStem: "unverified content", Answer: "A", OwnerID: "owner-check", Status: domain.StatusAIDraft, Version: 1}
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "task-failed-discard", QuestionID: q.ID, QuestionVersion: 1, Status: domain.AICheckTaskPending, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextCheckTask(ctx, time.Minute)
	if err != nil || task == nil {
		t.Fatalf("claim: %+v %v", task, err)
	}
	if err = store.FailCheckTask(ctx, task.ID, "检查超时", time.Second); err != nil {
		t.Fatal(err)
	}
	saved, err := store.GetQuestion(ctx, q.ID)
	if err != nil || saved != nil {
		t.Fatalf("exhausted draft must be deleted: %+v %v", saved, err)
	}
	discards, err := store.ListDiscardResultsByQuestionIDs(ctx, []string{q.ID})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := discards[q.ID]
	if !ok || d.Verdict != "check_failed" || d.OwnerID != q.OwnerID || d.Question != nil || d.StemSummary != "" {
		t.Fatalf("failure receipt must contain no unverified content: %+v", d)
	}
	counts, err := store.CountCheckTasksByStatusForOwner(ctx, q.OwnerID)
	if err != nil || counts[domain.AICheckTaskExhausted] != 1 {
		t.Fatalf("owner failure count: %+v %v", counts, err)
	}
	qualityDiscards, err := store.CountDiscardResults(ctx)
	if err != nil || qualityDiscards != 0 {
		t.Fatalf("system failure is not a quality verdict: %d %v", qualityDiscards, err)
	}
}

func TestExhaustedCheckDiscardIsAtomic(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, store, ctx, "atomic-check-failure")
	_, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "atomic-failure-task", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextCheckTask(ctx, time.Minute)
	if err != nil || task == nil {
		t.Fatalf("claim: %+v %v", task, err)
	}
	if _, err = store.db.ExecContext(ctx, `ALTER TABLE ai_check_discards ADD CONSTRAINT fail_receipt_test CHECK (verdict<>'check_failed')`); err != nil {
		t.Fatal(err)
	}
	if err = store.FailCheckTask(ctx, task.ID, "timeout", time.Second); err == nil {
		t.Fatal("receipt write should fail")
	}
	saved, _ := store.GetQuestion(ctx, q.ID)
	latest, _ := store.LatestCheckTasksByQuestionIDs(ctx, []string{q.ID})
	discards, _ := store.ListDiscardResultsByQuestionIDs(ctx, []string{q.ID})
	if saved == nil || latest[q.ID].Status != domain.AICheckTaskRunning || len(discards) != 0 {
		t.Fatalf("failure must rollback all writes: q=%+v task=%+v discards=%+v", saved, latest, discards)
	}
	if _, err = store.db.ExecContext(ctx, `ALTER TABLE ai_check_discards DROP CONSTRAINT fail_receipt_test`); err != nil {
		t.Fatal(err)
	}
	if err = store.FailCheckTask(ctx, task.ID, "timeout", time.Second); err != nil {
		t.Fatal(err)
	}
	if saved, _ = store.GetQuestion(ctx, q.ID); saved != nil {
		t.Fatal("after storage recovery final discard must succeed")
	}
}

func TestCheckRecoveryDiscardsExpiredFinalAttemptAndOldExhaustion(t *testing.T) {
	for _, status := range []string{"exhausted", "running", "pending"} {
		t.Run(status, func(t *testing.T) {
			store, ctx := newAICheckOutcomeTestStore(t)
			q := aiCheckTestQuestion(t, store, ctx, "reap-final-attempt")
			_, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "reap-task", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 3})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.db.ExecContext(ctx, `UPDATE ai_check_tasks SET status=$1,attempts=3,leased_until=NOW()-INTERVAL '1 second' WHERE id='reap-task'`, status); err != nil {
				t.Fatal(err)
			}
			task, err := store.ClaimNextCheckTask(ctx, time.Minute)
			if err != nil || task != nil {
				t.Fatalf("must not reclaim a fourth attempt: %+v %v", task, err)
			}
			saved, _ := store.GetQuestion(ctx, q.ID)
			discards, _ := store.ListDiscardResultsByQuestionIDs(ctx, []string{q.ID})
			if saved != nil || discards[q.ID].Failure == nil || discards[q.ID].Failure.Attempts != 3 {
				t.Fatalf("recovery must preserve terminal failure receipt: q=%+v receipts=%+v", saved, discards)
			}
			if _, err = store.ClaimNextCheckTask(ctx, time.Minute); err != nil {
				t.Fatal(err)
			}
			again, _ := store.ListDiscardResultsByQuestionIDs(ctx, []string{q.ID})
			if len(again) != 1 {
				t.Fatal("recovery must be idempotent")
			}
		})
	}
}

func TestCheckFailureDoesNotDeletePassedQuestion(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q := aiCheckTestQuestion(t, store, ctx, "protected-check-question")
	_, err := store.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "protected-check-task", QuestionID: q.ID, Status: domain.AICheckTaskPending, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextCheckTask(ctx, time.Minute)
	if err != nil || task == nil {
		t.Fatalf("claim: %+v %v", task, err)
	}
	q.Status = domain.StatusAIReviewed
	if err = store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err = store.FailCheckTask(ctx, task.ID, "late failure", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimNextCheckTask(ctx, time.Minute); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.GetQuestion(ctx, q.ID)
	if saved == nil || saved.Status != domain.StatusAIReviewed {
		t.Fatalf("passed question changed: %+v", saved)
	}
}
