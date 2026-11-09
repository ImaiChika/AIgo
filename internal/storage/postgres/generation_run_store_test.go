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

// TestGenerationRunStoreSemantics 在真实 PostgreSQL 上验证单题命题运行记录：
// 空 question_ids 可正常创建（NOT NULL 列）、重复创建幂等、完成/失败状态流转。
func TestGenerationRunStoreSemantics(t *testing.T) {
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL generation run store integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	schemaName := fmt.Sprintf("aigo_generation_run_%d", time.Now().UnixNano())
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

	// 创建时 QuestionIDs 为 nil，必须仍能落库（回归：NULL 触发 NOT NULL 约束）。
	created, err := store.CreateGenerationRun(ctx, domain.GenerationRun{
		ID: "gen-test-1", OwnerID: "user-1", Status: domain.GenerationRunRunning, RequestedCount: 2,
	})
	if err != nil || !created {
		t.Fatalf("创建命题运行应成功: created=%v err=%v", created, err)
	}

	run, err := store.GetGenerationRun(ctx, "gen-test-1")
	if err != nil || run == nil {
		t.Fatalf("读取命题运行失败: run=%v err=%v", run, err)
	}
	if run.Status != domain.GenerationRunRunning || run.RequestedCount != 2 || len(run.QuestionIDs) != 0 {
		t.Fatalf("运行记录字段不符: %+v", run)
	}

	// 幂等：同 ID 重复创建不新建，也不报错。
	created, err = store.CreateGenerationRun(ctx, domain.GenerationRun{
		ID: "gen-test-1", OwnerID: "user-1", Status: domain.GenerationRunRunning, RequestedCount: 2,
	})
	if err != nil || created {
		t.Fatalf("重复创建应幂等: created=%v err=%v", created, err)
	}

	// 完成：写入题目 ID 并置为 succeeded。
	if err := store.CompleteGenerationRun(ctx, "gen-test-1", []string{"q-1", "q-2"}); err != nil {
		t.Fatalf("完成命题运行失败: %v", err)
	}
	run, err = store.GetGenerationRun(ctx, "gen-test-1")
	if err != nil || run == nil {
		t.Fatalf("读取完成后的命题运行失败: run=%v err=%v", run, err)
	}
	if run.Status != domain.GenerationRunSucceeded || len(run.QuestionIDs) != 2 || run.CompletedAt == nil {
		t.Fatalf("完成状态不符: %+v", run)
	}

	// 失败：错误信息落库，且不覆盖已完成的运行。
	if err := store.FailGenerationRun(ctx, "gen-test-1", "不应覆盖"); err != nil {
		t.Fatalf("标记失败不应报错: %v", err)
	}
	run, _ = store.GetGenerationRun(ctx, "gen-test-1")
	if run.Status != domain.GenerationRunSucceeded {
		t.Fatalf("已完成运行不应被标记失败: %+v", run)
	}

	if _, err := store.CreateGenerationRun(ctx, domain.GenerationRun{
		ID: "gen-test-2", OwnerID: "user-1", Status: domain.GenerationRunRunning, RequestedCount: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.FailGenerationRun(ctx, "gen-test-2", "模型超时"); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	run, _ = store.GetGenerationRun(ctx, "gen-test-2")
	if run.Status != domain.GenerationRunFailed || run.Error != "模型超时" || run.CompletedAt == nil {
		t.Fatalf("失败状态不符: %+v", run)
	}
}
