//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"reflect"
	"testing"
)

func TestLearningProviderUpgrade68Preserves67Configuration(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, e := New(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.SetLLMEncryptionKey("migration-summary-key")
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:67])
	provider := domain.AIProviderConfig{ID: "old-route", Name: "原服务", Deployment: "cloud", BaseURL: "https://generation.invalid/v1", APIKey: "old-generation-key", GenerationModel: "generation", CheckModel: "check", Active: true}
	if e = s.SaveAIProviderConfig(t.Context(), provider); e != nil {
		t.Fatal(e)
	}
	q, _, e := s.GetGenerationQuota(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	var oldCipher string
	s.db.QueryRow(`SELECT api_key_ciphertext FROM ai_provider_configs WHERE id='old-route'`).Scan(&oldCipher)
	applied, e := s.Migrate(t.Context())
	if e != nil || len(applied) != int(LatestSchemaVersion-67) || applied[0].Version != 68 {
		t.Fatal(applied, e)
	}
	got, e := s.GetActiveAIProviderConfig(t.Context())
	if e != nil || got.APIKey != provider.APIKey || got.GenerationModel != provider.GenerationModel || got.CheckModel != provider.CheckModel {
		t.Fatal("old route changed")
	}
	var cipher string
	s.db.QueryRow(`SELECT api_key_ciphertext FROM ai_provider_configs WHERE id='old-route'`).Scan(&cipher)
	if cipher != oldCipher {
		t.Fatal("migration rewrote old credentials")
	}
	after, _, _ := s.GetGenerationQuota(t.Context())
	if !reflect.DeepEqual(after, q) {
		t.Fatal("migration changed concurrency or quotas")
	}
	c, e := s.GetLearningProviderConfig(t.Context())
	if e != nil || c != nil {
		t.Fatal("migration silently enabled independent summary")
	}
}
