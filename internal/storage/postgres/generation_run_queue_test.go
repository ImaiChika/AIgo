package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/pipeline"

	"github.com/lib/pq"
)

// TestGenerationRunQueueSemantics 在真实 PostgreSQL 上验证单题生成持久化队列的核心语义：
// 快照落库、抢占互斥（FOR UPDATE SKIP LOCKED）、租约回收、退避重试与耗尽终态。
func TestGenerationRunQueueSemantics(t *testing.T) {
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL generation run queue integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	schemaName := fmt.Sprintf("aigo_generation_queue_%d", time.Now().UnixNano())
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

	spec, err := json.Marshal(pipeline.GenerationSpec{
		Request: domain.GenerationRequest{Subject: "消化", Count: 1},
		Actor:   "tester", OwnerID: "owner-1", BankID: "bank-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 提交：pending + 快照落库
	created, err := store.CreateGenerationRun(ctx, domain.GenerationRun{
		ID: "gen-q-1", OwnerID: "owner-1", Status: domain.GenerationRunPending,
		RequestedCount: 1, MaxAttempts: 3, RequestJSON: spec,
	})
	if err != nil || !created {
		t.Fatalf("提交运行失败: created=%v err=%v", created, err)
	}
	created, err = store.CreateGenerationRun(ctx, domain.GenerationRun{ID: "gen-q-1", OwnerID: "owner-1", Status: domain.GenerationRunPending})
	if err != nil || created {
		t.Fatalf("重复提交应幂等跳过: created=%v err=%v", created, err)
	}
	created, err = store.CreateGenerationRun(ctx, domain.GenerationRun{
		ID: "gen-q-2", OwnerID: "owner-2", Status: domain.GenerationRunPending,
		RequestedCount: 2, MaxAttempts: 3, RequestJSON: spec,
	})
	if err != nil || !created {
		t.Fatalf("提交第二运行失败: created=%v err=%v", created, err)
	}

	// 抢占互斥：两次抢占得到不同运行，第三次为空
	first, specFirst, err := store.ClaimNextGenerationRun(ctx, 50*time.Millisecond)
	if err != nil || first == nil {
		t.Fatalf("第一次抢占应成功: %v", err)
	}
	if first.Attempts != 1 || first.LeasedUntil.IsZero() {
		t.Fatalf("抢占应递增执行次数并续租: %+v", first)
	}
	if len(specFirst) == 0 {
		t.Fatal("抢占应返回请求快照")
	}
	var decoded pipeline.GenerationSpec
	if err := json.Unmarshal(specFirst, &decoded); err != nil {
		t.Fatalf("快照应可反序列化: %v", err)
	}
	if decoded.OwnerID != "owner-1" || decoded.BankID != "bank-1" {
		t.Fatalf("快照内容不符: %+v", decoded)
	}
	second, _, err := store.ClaimNextGenerationRun(ctx, 50*time.Millisecond)
	if err != nil || second == nil {
		t.Fatalf("第二次抢占应成功: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("两个 worker 不得抢占同一运行: %s", first.ID)
	}
	third, _, err := store.ClaimNextGenerationRun(ctx, 50*time.Millisecond)
	if err != nil || third != nil {
		t.Fatalf("队列应为空: %v %v", third, err)
	}

	// 租约回收：租约过期后运行可被重新抢占（模拟进程崩溃遗留）
	time.Sleep(80 * time.Millisecond)
	reclaimed, _, err := store.ClaimNextGenerationRun(ctx, time.Minute)
	if err != nil || reclaimed == nil {
		t.Fatalf("租约过期后应回收运行: %v", err)
	}
	if reclaimed.ID != first.ID || reclaimed.Attempts != 2 {
		t.Fatalf("应回收 %s 且执行次数为 2，实际 %s/%d", first.ID, reclaimed.ID, reclaimed.Attempts)
	}

	// 可重试失败：未达上限回 pending 并退避
	scheduled, err := store.RetryGenerationRun(ctx, reclaimed.ID, "生成失败: 模拟瞬时错误", time.Millisecond)
	if err != nil || !scheduled {
		t.Fatalf("未达上限应安排重试: scheduled=%v err=%v", scheduled, err)
	}
	time.Sleep(10 * time.Millisecond)
	retried, _, err := store.ClaimNextGenerationRun(ctx, time.Minute)
	if err != nil || retried == nil || retried.ID != reclaimed.ID || retried.Attempts != 3 {
		t.Fatalf("退避后应重新抢占且次数递增: %+v %v", retried, err)
	}

	// 耗尽：第 3 次失败后进入 failed 终态
	scheduled, err = store.RetryGenerationRun(ctx, retried.ID, "生成失败: 持续失败", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if scheduled {
		t.Fatal("达到重试上限后不应再安排重试")
	}
	final, err := store.GetGenerationRun(ctx, reclaimed.ID)
	if err != nil || final == nil || final.Status != domain.GenerationRunFailed || final.Error == "" {
		t.Fatalf("运行应进入 failed 终态: %+v %v", final, err)
	}

	// 成功路径：Complete 仅在 running 态生效
	if err := store.CompleteGenerationRun(ctx, second.ID, []string{"q-1"}); err != nil {
		t.Fatal(err)
	}
	done, err := store.GetGenerationRun(ctx, second.ID)
	if err != nil || done == nil || done.Status != domain.GenerationRunSucceeded {
		t.Fatalf("运行应成功完成: %+v %v", done, err)
	}
}
