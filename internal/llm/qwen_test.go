package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQwenClientLocalAllowsNoAPIKeyAndUsesLocalThinkingDialect(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
		})
	}))
	defer server.Close()

	disabled := false
	client := NewQwenClient(QwenConfig{
		Deployment: DeploymentLocal,
		BaseURL:    server.URL + "/v1",
		Model:      "Qwen/Qwen3.5-35B-A3B",
	})
	result, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{
		EnableThinking: &disabled,
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if result != "ok" {
		t.Fatalf("Complete() = %q, want ok", result)
	}
	if gotAuth != "" {
		t.Errorf("local request leaked Authorization header: %q", gotAuth)
	}
	if gotBody["model"] != "Qwen/Qwen3.5-35B-A3B" {
		t.Errorf("model = %#v", gotBody["model"])
	}
	if _, exists := gotBody["enable_thinking"]; exists {
		t.Errorf("local request must not use top-level enable_thinking: %#v", gotBody)
	}
	kwargs, ok := gotBody["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs = %#v", gotBody["chat_template_kwargs"])
	}
}

func TestQwenClientCloudRequiresKeyAndUsesDashScopeThinkingDialect(t *testing.T) {
	t.Parallel()

	missingKeyClient := NewQwenClient(QwenConfig{
		Deployment: DeploymentCloud,
		BaseURL:    "https://example.invalid/v1",
		Model:      "qwen3.5-flash",
	})
	_, err := missingKeyClient.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
	if err == nil || !strings.Contains(err.Error(), "API_KEY") {
		t.Fatalf("missing cloud key error = %v", err)
	}

	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "cloud-ok"},
			}},
		})
	}))
	defer server.Close()

	enabled := true
	client := NewQwenClient(QwenConfig{
		Deployment: DeploymentCloud,
		APIKey:     "cloud-secret",
		BaseURL:    server.URL + "/v1/chat/completions",
		Model:      "qwen3.5-flash",
	})
	result, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{
		EnableThinking: &enabled,
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if result != "cloud-ok" {
		t.Errorf("Complete() = %q", result)
	}
	if gotAuth != "Bearer cloud-secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["enable_thinking"] != true {
		t.Errorf("enable_thinking = %#v", gotBody["enable_thinking"])
	}
	if _, exists := gotBody["max_tokens"]; exists {
		t.Errorf("zero MaxTokens must omit max_tokens: %#v", gotBody)
	}
	if _, exists := gotBody["chat_template_kwargs"]; exists {
		t.Errorf("cloud request must not use chat_template_kwargs: %#v", gotBody)
	}
}

func TestQwenClientLocalRequiresEndpointAndModel(t *testing.T) {
	t.Parallel()

	client := NewQwenClient(QwenConfig{Deployment: DeploymentLocal})
	_, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
	if err == nil || !strings.Contains(err.Error(), "QWEN_BASE_URL") {
		t.Fatalf("missing local base URL error = %v", err)
	}

	client = NewQwenClient(QwenConfig{Deployment: DeploymentLocal, BaseURL: "http://127.0.0.1:8000/v1"})
	_, err = client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
	if err == nil || !strings.Contains(err.Error(), "QWEN_MODEL") {
		t.Fatalf("missing local model error = %v", err)
	}
}
