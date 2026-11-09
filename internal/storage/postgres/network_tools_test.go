package postgres

import (
	"errors"
	"testing"

	"aigo/internal/llm"
	"aigo/internal/storage"
)

func TestModelNetworkToolsPersistAndLegacySavesPreservePolicy(t *testing.T) {
	store, ctx := newAICheckOutcomeTestStore(t)
	q, _, err := store.GetGenerationQuota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if q.ModelTools == nil || !q.ModelTools.WebSearchEnabled || !q.ModelTools.WebExtractorEnabled {
		t.Fatalf("new deployment must start enabled: %+v", q.ModelTools)
	}
	oldRevision := q.Revision
	q.ModelTools = &llm.NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}
	if err := store.SaveGenerationQuotaVersion(ctx, q, oldRevision); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveGenerationQuotaVersion(ctx, q, oldRevision); !errors.Is(err, storage.ErrGenerationQuotaConflict) {
		t.Fatalf("stale save=%v", err)
	}
	legacy := storage.DefaultGenerationQuota()
	legacy.GlobalPendingQuestions = 600
	if err := store.SaveGenerationQuota(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	policy, err := store.ResolveNetworkTools(ctx)
	if err != nil || !policy.WebSearchEnabled || !policy.WebExtractorEnabled {
		t.Fatalf("legacy reset tools: %+v %v", policy, err)
	}
	q, _, _ = store.GetGenerationQuota(ctx)
	q.ModelTools = &llm.NetworkTools{WebExtractorEnabled: true}
	if err := store.SaveGenerationQuotaVersion(ctx, q, q.Revision); err == nil {
		t.Fatal("invalid dependency saved")
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE generation_quota SET web_search_enabled=FALSE,web_extractor_enabled=TRUE WHERE id=1`); err == nil {
		t.Fatal("database constraint missing")
	}
	q.ModelTools = &llm.NetworkTools{}
	if err := store.SaveGenerationQuotaVersion(ctx, q, q.Revision); err != nil {
		t.Fatal(err)
	}
	policy, err = store.ResolveNetworkTools(ctx)
	if err != nil || policy != (llm.NetworkTools{}) {
		t.Fatalf("off failed: %+v %v", policy, err)
	}
}

func TestNetworkToolMigrationPreservesSchema46Values(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	store, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	migrations := configuredMigrations(baselineSchemaSQL)
	applyMigrationPrefix(t, t.Context(), store, migrations[:46])
	if _, err := store.DB().Exec(`UPDATE generation_quota SET batch_concurrency=4,revision=12,global_pending_questions=731 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	q, _, err := store.GetGenerationQuota(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if q.BatchConcurrency != 4 || q.Revision != 13 || q.LearningConcurrency != 2 || q.GlobalPendingQuestions != 731 || q.ModelTools == nil || *q.ModelTools != (llm.NetworkTools{}) {
		t.Fatalf("migration changed existing config: %+v", q)
	}
	if applied, err := store.Migrate(t.Context()); err != nil || len(applied) != 0 {
		t.Fatalf("migration not idempotent: %+v %v", applied, err)
	}
}
