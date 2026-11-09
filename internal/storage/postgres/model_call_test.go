//go:build integration

package postgres

import (
	"aigo/internal/llm"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestModelCallEvidencePersistsIndependentlyAndReplaysIdempotently(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	stamp := time.Now().UTC()
	record := llm.ModelCallRecord{ID: "call-evidence-1", Operation: "generate:isolated", Purpose: llm.PurposeGeneration, Stage: "model_call", Attempt: 2, StartedAt: stamp, Model: "actual-model-A", ConfigID: "provider-1", ToolsRevision: 17, Outcome: "returned", TotalTokens: 23, WorkflowVersion: "knowledge-v3"}
	if err := store.SaveModelCall(ctx, record); err != nil {
		t.Fatal(err)
	}
	// A replay never overwrites original evidence with a later hot configuration.
	record.Model = "replacement"
	record.TotalTokens = 0
	if err := store.SaveModelCall(ctx, record); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var saved llm.ModelCallRecord
	if err := store.DB().QueryRowContext(ctx, `SELECT record FROM model_call_records WHERE id=$1`, record.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Model != "actual-model-A" || saved.TotalTokens != 23 || saved.Operation != record.Operation || saved.ToolsRevision != 17 {
		t.Fatal(saved)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			r := record
			r.ID = fmt.Sprintf("parallel-%d", i)
			r.Operation = fmt.Sprintf("batch:job:%d", i)
			if err := store.SaveModelCall(ctx, r); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var n int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM model_call_records`).Scan(&n); err != nil || n != 21 {
		t.Fatal(n, err)
	}
	// No question/run FK: diagnostic evidence survives original quality discards.
	for _, table := range []string{"generation_runs", "ai_check_tasks", "questions"} {
		if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatal("evidence created business data", table, n, err)
		}
	}
}
