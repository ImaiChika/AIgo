//go:build integration

package postgres

import (
	"aigo/internal/llm"
	"aigo/internal/workflowtrace"
	"encoding/json"
	"testing"
	"time"
)

func TestWorkflowExecutionForwardMigrationAndImmutableReceipt(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	store, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()
	applyMigrationPrefix(t, ctx, store, configuredMigrations(baselineSchemaSQL)[:53])
	before := llm.ModelCallRecord{ID: "old-call", Operation: "generate:old", Stage: "model_call", Purpose: llm.PurposeGeneration, StartedAt: time.Now(), Outcome: "returned"}
	if err = store.SaveModelCall(ctx, before); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	record := workflowtrace.Record{ID: "execution", Operation: "generate:old", Phase: "generation", Attempt: 1, StartedAt: time.Now(), Outcome: "committed", QuestionIDs: []string{"old-q"}, ModelCalls: []workflowtrace.ModelReference{{ID: before.ID, Recorded: true}}, Stages: []workflowtrace.Stage{{Name: "handoff", Status: "committed"}}}
	if err = store.SaveWorkflowExecution(ctx, record); err != nil {
		t.Fatal(err)
	}
	record.Outcome = "failed"
	if err = store.SaveWorkflowExecution(ctx, record); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var saved workflowtrace.Record
	if err = store.DB().QueryRowContext(ctx, `SELECT record FROM workflow_execution_records WHERE id='execution'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &saved); err != nil || saved.Outcome != "committed" || saved.ModelCalls[0].ID != before.ID {
		t.Fatal(saved, err)
	}
	var count int
	if err = store.DB().QueryRowContext(ctx, `SELECT count(*) FROM model_call_records WHERE id='old-call'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("migration lost existing call evidence", count, err)
	}
	// FK-free diagnostic linkage cannot create or resurrect real questions/tasks.
	for _, table := range []string{"questions", "generation_runs", "ai_check_tasks"} {
		if err = store.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 0 {
			t.Fatal(table, count, err)
		}
	}
}
