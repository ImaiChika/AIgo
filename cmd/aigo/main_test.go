package main

import (
	"aigo/internal/config"
	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"context"
	"reflect"
	"testing"
	"time"
)

type resolverProviderStore struct {
	storage.AIProviderConfigStore
	active *domain.AIProviderConfig
	reads  int
}

func (s *resolverProviderStore) GetActiveAIProviderConfig(context.Context) (*domain.AIProviderConfig, error) {
	s.reads++
	return s.active, nil
}
func (s *resolverProviderStore) ListAIProviderConfigs(context.Context) ([]domain.AIProviderConfig, error) {
	if s.active == nil {
		return nil, nil
	}
	return []domain.AIProviderConfig{*s.active}, nil
}
func (s *resolverProviderStore) SaveAIProviderConfig(_ context.Context, c domain.AIProviderConfig) error {
	s.active = &c
	return nil
}
func TestIndependentCheckRoutingSurvivesProviderBootstrapAndReplacement(t *testing.T) {
	on := true
	generation := llm.QwenConfig{Deployment: llm.DeploymentCloud, BaseURL: "https://gen.example/v1", Model: "gen", APIKey: "generation-test-key", EnableThinking: &on, HTTPTimeout: 300 * time.Second}
	check := llm.QwenConfig{Deployment: llm.DeploymentLocal, BaseURL: "http://check.example/v1", Model: "checker", APIKey: "check-test-key", EnableThinking: &on, HTTPTimeout: 630 * time.Second}
	for _, independent := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared_database_route", true: "independent_environment_route"}[independent], func(t *testing.T) {
			store := &resolverProviderStore{}
			cfg := config.Config{Qwen: generation, AICheck: config.AICheckConfig{Client: check, Independent: independent}}
			resolver := databaseQwenResolver{store: store, generationFallback: generation, checkFallback: check, independentCheck: independent}
			for _, stage := range []string{"initial_bootstrap", "manual_replacement", "delete", "restart_bootstrap"} {
				t.Run(stage, func(t *testing.T) {
					switch stage {
					case "initial_bootstrap", "restart_bootstrap":
						if err := bootstrapAIProviderConfig(context.Background(), store, cfg); err != nil {
							t.Fatal(err)
						}
					case "manual_replacement":
						store.active = &domain.AIProviderConfig{Deployment: "cloud", BaseURL: "https://changed.example/v1", APIKey: "changed-key", GenerationModel: "changed-generation", CheckModel: "changed-check"}
					case "delete":
						store.active = nil
					}
					want := check
					if independent {
						want.ConfigSource = "independent_environment"
					}
					if !independent && store.active != nil {
						want.ConfigID = store.active.ID
						want.ConfigUpdatedAt = store.active.UpdatedAt
						want.ConfigSource = "database"
						want.BaseURL = store.active.BaseURL
						want.APIKey = store.active.APIKey
						want.Model = store.active.CheckModel
						want.Deployment = llm.DeploymentMode(store.active.Deployment)
					}
					got, _, err := resolver.ResolveQwenConfig(context.Background(), llm.PurposeAICheck)
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("check config=%+v want=%+v err=%v", got, want, err)
					}
					got, _, err = resolver.ResolveQwenConfig(context.Background(), llm.PurposeGeneration)
					if err != nil {
						t.Fatal(err)
					}
					if store.active != nil && (got.BaseURL != store.active.BaseURL || got.APIKey != store.active.APIKey || got.Model != store.active.GenerationModel) {
						t.Fatal("generation stopped following active provider")
					}
				})
			}
		})
	}
}
