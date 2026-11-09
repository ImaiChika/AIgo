package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"

	"github.com/lib/pq"
)

func TestReviewTransactionRollsBackAcrossTables(t *testing.T) {
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL transaction integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	schemaName := fmt.Sprintf("aigo_review_tx_%d", time.Now().UnixNano())
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
	schemaSQL, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitSchema(string(schemaSQL)); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckReadiness(ctx); err != nil {
		t.Fatalf("fresh initialized schema should be ready: %v", err)
	}

	now := time.Now()
	question := domain.A2Question{
		ID: "q-tx", ClinicalStem: "患者出现持续胸痛，应首先考虑哪项诊断？",
		Options: []domain.Option{{Label: "A", Text: "诊断A"}, {Label: "B", Text: "诊断B"}, {Label: "C", Text: "诊断C"}, {Label: "D", Text: "诊断D"}},
		Answer:  "A", Difficulty: domain.DifficultyMedium, Status: domain.StatusAIDraft,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(ctx, question); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{
		ID: "flow-tx", Name: "事务测试流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1"}}}, CreatedAt: now,
	}
	if err := store.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	task := domain.ReviewTask{
		ID: "task-tx", QuestionID: question.ID, FlowID: flow.ID, CurrentRound: 1,
		Status: domain.StatusReviewing, AssignedTo: []string{"r1"}, QuestionPrevStatus: domain.StatusAIDraft,
		QuestionVersion: 1, RoundResults: []domain.RoundResult{{RoundNumber: 1}}, CreatedAt: now, UpdatedAt: now,
	}
	record := domain.ReviewRecord{
		ID: "record-tx", TaskID: task.ID, QuestionID: question.ID, RoundNumber: 1,
		ExpertID: "r1", Conclusion: domain.StatusApproved, CreatedAt: now,
	}
	injected := errors.New("injected rollback")
	err = store.WithReviewTransaction(ctx, store, func(txCtx context.Context) error {
		q, err := store.GetQuestion(txCtx, question.ID)
		if err != nil {
			return err
		}
		q.Status = domain.StatusReviewing
		q.UpdatedAt = time.Now()
		if err := store.SaveQuestion(txCtx, *q); err != nil {
			return err
		}
		if err := store.SaveTask(txCtx, task); err != nil {
			return err
		}
		if err := store.SaveRecord(txCtx, record); err != nil {
			return err
		}
		tasks, err := store.ListAllTasks(txCtx)
		if err != nil || len(tasks) != 1 {
			return fmt.Errorf("事务内读取任务失败: count=%d err=%v", len(tasks), err)
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected error, got %v", err)
	}

	storedQuestion, err := store.GetQuestion(ctx, question.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedQuestion.Status != domain.StatusAIDraft {
		t.Fatalf("question status after rollback = %s, want %s", storedQuestion.Status, domain.StatusAIDraft)
	}
	storedTask, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask != nil {
		t.Fatal("review task should be rolled back")
	}
	records, err := store.ListRecordsByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("review records after rollback = %d, want 0", len(records))
	}

	if err := store.WithReviewTransaction(ctx, store, func(txCtx context.Context) error {
		q, err := store.GetQuestion(txCtx, question.ID)
		if err != nil {
			return err
		}
		q.Status = domain.StatusReviewing
		if err := store.SaveQuestion(txCtx, *q); err != nil {
			return err
		}
		if err := store.SaveTask(txCtx, task); err != nil {
			return err
		}
		return store.SaveRecord(txCtx, record)
	}); err != nil {
		t.Fatal(err)
	}
	storedQuestion, _ = store.GetQuestion(ctx, question.ID)
	storedTask, _ = store.GetTask(ctx, task.ID)
	records, _ = store.ListRecordsByTaskID(ctx, task.ID)
	if storedQuestion.Status != domain.StatusReviewing || storedTask == nil || len(records) != 1 {
		t.Fatalf("committed transaction missing data: question=%s task=%v records=%d", storedQuestion.Status, storedTask != nil, len(records))
	}
	versions, err := store.ListQuestionVersions(ctx, question.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("status-only transaction should keep one content version, got %+v", versions)
	}

	store2, err := New(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	editableQuestion := question
	editableQuestion.ID = "q-concurrent-edit"
	editableQuestion.Status = domain.StatusAIDraft
	if err := store.SaveQuestion(ctx, editableQuestion); err != nil {
		t.Fatal(err)
	}
	edit1, _ := store.GetQuestion(ctx, editableQuestion.ID)
	edit2, _ := store2.GetQuestion(ctx, editableQuestion.ID)
	edit1.ClinicalStem = "并发修改版本A"
	edit2.ClinicalStem = "并发修改版本B"
	edit1.Version++
	edit2.Version++
	edit1.Status = domain.StatusAIDraft
	edit2.Status = domain.StatusAIDraft
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, edit := range []struct {
		store    *Store
		question domain.A2Question
	}{{store, *edit1}, {store2, *edit2}} {
		edit := edit
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- edit.store.SaveQuestion(ctx, edit.question)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrQuestionVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent edit error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent edits: successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	versions, _ = store.ListQuestionVersions(ctx, editableQuestion.ID)
	if len(versions) != 2 || versions[0].Version != 2 {
		t.Fatalf("concurrent edits should create exactly version 2 once, got %+v", versions)
	}

	if _, err := store.db.ExecContext(ctx, `DELETE FROM question_versions WHERE question_id=$1`, question.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE review_tasks SET question_version=0 WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, string(schemaSQL)); err != nil {
		t.Fatalf("recreate legacy baseline helpers: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE questions ADD COLUMN IF NOT EXISTS media_refs JSONB DEFAULT '[]'`); err != nil {
		t.Fatal(err)
	}
	applyMigrationPrefix(t, ctx, store, configuredMigrations(string(schemaSQL))[1:26])
	versions, _ = store.ListQuestionVersions(ctx, question.ID)
	storedTask, _ = store.GetTask(ctx, task.ID)
	if len(versions) != 1 || storedTask.QuestionVersion != storedQuestion.Version {
		t.Fatalf("legacy backfill failed: versions=%d taskVersion=%d questionVersion=%d", len(versions), storedTask.QuestionVersion, storedQuestion.Version)
	}

	// 回归真实旧库结构：旧表以 id 为主键、snapshot 为 TEXT、操作者列名为 changed_by。
	if _, err := store.db.ExecContext(ctx, `DROP TABLE question_versions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TABLE question_versions (
			id TEXT PRIMARY KEY,
			question_id TEXT NOT NULL REFERENCES questions(id),
			version INT NOT NULL,
			snapshot TEXT NOT NULL,
			change_note TEXT NOT NULL DEFAULT '',
			changed_by TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ DEFAULT NOW()
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE questions DROP CONSTRAINT questions_no_legacy_approved`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE review_tasks DROP CONSTRAINT review_tasks_no_legacy_approved`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE questions SET status='approved' WHERE id=$1`, storedQuestion.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE review_tasks SET status='approved' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	legacyQuestion := *storedQuestion
	legacyQuestion.Status = domain.StatusApproved
	legacySnapshot, _ := json.Marshal(&legacyQuestion)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO question_versions (id, question_id, version, snapshot, change_note, changed_by)
		VALUES ('legacy-v1',$1,$2,$3,'旧版本记录','legacy-user')
	`, storedQuestion.ID, storedQuestion.Version, string(legacySnapshot)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version>=2`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, string(schemaSQL)); err != nil {
		t.Fatalf("recreate legacy baseline helpers: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE questions ADD COLUMN IF NOT EXISTS media_refs JSONB DEFAULT '[]'`); err != nil {
		t.Fatal(err)
	}
	applyMigrationPrefix(t, ctx, store, configuredMigrations(string(schemaSQL))[1:26])
	var snapshotType string
	if err := store.db.QueryRowContext(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='question_versions' AND column_name='snapshot'
	`).Scan(&snapshotType); err != nil {
		t.Fatal(err)
	}
	legacyVersion, err := store.GetQuestionVersion(ctx, storedQuestion.ID, storedQuestion.Version)
	if err != nil {
		t.Fatal(err)
	}
	canonicalQuestion, _ := store.GetQuestion(ctx, storedQuestion.ID)
	canonicalTask, _ := store.GetTask(ctx, task.ID)
	if snapshotType != "jsonb" || legacyVersion == nil || legacyVersion.Actor != "legacy-user" ||
		legacyVersion.Snapshot.Status != domain.StatusPublished || canonicalQuestion.Status != domain.StatusPublished || canonicalTask.Status != domain.StatusPublished {
		t.Fatalf("legacy table conversion failed: snapshotType=%s version=%+v", snapshotType, legacyVersion)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE review_tasks DROP COLUMN question_version`); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckReadiness(ctx); err == nil {
		t.Fatal("readiness must fail when a critical schema column is missing")
	}
}

func dsnWithSearchPath(dsn, schemaName string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
