package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeRuntimeResolver struct {
	config QwenConfig
}

func (r *fakeRuntimeResolver) ResolveQwenConfig(_ context.Context, _ ConfigPurpose) (QwenConfig, bool, error) {
	return r.config, true, nil
}

func TestDynamicQwenClientUsesRuntimeModel(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		receivedModel = payload.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()

	resolver := &fakeRuntimeResolver{config: QwenConfig{
		Deployment: DeploymentCloud,
		APIKey:     "runtime-key",
		BaseURL:    server.URL + "/v1",
		Model:      "runtime-generation-model",
	}}
	client := NewDynamicQwenClient(resolver, PurposeGeneration, QwenConfig{Model: "fallback-model"})
	if _, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "hello"}}, GenerateOptions{}); err != nil {
		t.Fatal(err)
	}
	if receivedModel != "runtime-generation-model" {
		t.Fatalf("request model=%q, want runtime model", receivedModel)
	}
	if got := client.Info().Model; got != "runtime-generation-model" {
		t.Fatalf("Info model=%q, want runtime model", got)
	}
}
