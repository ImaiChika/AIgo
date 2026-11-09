//go:build integration

package postgres

import (
	"aigo/internal/llm"
	"aigo/internal/workflowtrace"
	"testing"
	"time"
)

func TestPreferenceMigrationPreservesModelAndStageEvidence(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	store, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	applyMigrationPrefix(t, ctx, store, configuredMigrations(baselineSchemaSQL)[:54])
	if err = store.SaveModelCall(ctx, llm.ModelCallRecord{ID: "preserve-call", Operation: "generate:prior", Purpose: llm.PurposeGeneration, Stage: "model_call", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveWorkflowExecution(ctx, workflowtrace.Record{ID: "preserve-stage", Operation: "generate:prior", Phase: "generation", Attempt: 1, StartedAt: time.Now(), Outcome: "committed"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"model_call_records", "workflow_execution_records"} {
		var count int
		if err = store.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 1 {
			t.Fatal("forward migration lost history", table, count, err)
		}
	}
	var count int
	if err = store.DB().QueryRowContext(ctx, `SELECT count(*) FROM question_preference_feedback`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration fabricated preferences", count, err)
	}
}
