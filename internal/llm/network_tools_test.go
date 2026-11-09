package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type mutableToolPolicy struct {
	value NetworkTools
	err   error
}

type observingConfig struct {
	cfg  QwenConfig
	read chan struct{}
}

func (r observingConfig) ResolveQwenConfig(context.Context, ConfigPurpose) (QwenConfig, bool, error) {
	close(r.read)
	return r.cfg, true, nil
}

type observingPolicy struct {
	enabled atomic.Bool
	read    chan struct{}
}

func (p *observingPolicy) ResolveNetworkTools(context.Context) (NetworkTools, error) {
	close(p.read)
	return NetworkTools{WebSearchEnabled: p.enabled.Load()}, nil
}

func TestOffSwitchIsReadAfterEndpointRecoveryWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, exists := body["enable_search"]; exists {
			t.Error("stale policy escaped recovery wait")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	cfg := QwenConfig{APIKey: "test", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash"}
	recovery := NewEndpointRecovery()
	recovery.Pause = 150 * time.Millisecond
	for i := byte(1); i <= 3; i++ {
		recovery.observe(cfg, [32]byte{i}, &HTTPStatusError{Status: 503})
	}
	configRead := make(chan struct{})
	policy := &observingPolicy{read: make(chan struct{})}
	policy.enabled.Store(true)
	client := NewDynamicQwenClient(observingConfig{cfg, configRead}, PurposeGeneration, cfg)
	client.SetEndpointRecovery(recovery)
	client.SetNetworkToolsResolver(policy)
	done := make(chan error, 1)
	go func() {
		_, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
		done <- err
	}()
	<-configRead
	select {
	case <-policy.read:
		t.Error("policy was captured before recovery wait")
	case <-time.After(20 * time.Millisecond):
	}
	policy.enabled.Store(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not resume")
	}
}

func (p *mutableToolPolicy) ResolveNetworkTools(context.Context) (NetworkTools, error) {
	return p.value, p.err
}

func TestNetworkToolPolicyControlsBothPurposesOnEveryCall(t *testing.T) {
	for _, purpose := range []ConfigPurpose{PurposeGeneration, PurposeAICheck} {
		t.Run(string(purpose), func(t *testing.T) {
			var bodies []map[string]any
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies = append(bodies, body)
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/v1/responses" {
					_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"web_search_call"},{"type":"web_extractor_call"},{"type":"message","content":[{"type":"output_text","text":"[]"}]}]}`))
				} else {
					_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"[]"}}]}`))
				}
			}))
			defer server.Close()
			// A previously captured model configuration must not grant access after off.
			cfg := QwenConfig{Deployment: DeploymentCloud, APIKey: "test", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash", NetworkTools: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}}
			client := NewDynamicQwenClient(&fakeRuntimeResolver{config: cfg}, purpose, cfg)
			policy := &mutableToolPolicy{}
			client.SetNetworkToolsResolver(policy)
			for _, value := range []NetworkTools{{}, {WebSearchEnabled: true}, {WebSearchEnabled: true, WebExtractorEnabled: true}, {}} {
				policy.value = value
				got, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
				if err != nil || got != "[]" {
					t.Fatalf("value=%+v got=%q err=%v", value, got, err)
				}
			}
			for _, index := range []int{0, 3} {
				if _, ok := bodies[index]["enable_search"]; ok {
					t.Fatalf("disabled request: %#v", bodies[index])
				}
				if _, ok := bodies[index]["tools"]; ok {
					t.Fatal("tools escaped disabled policy")
				}
			}
			searchTools, _ := bodies[1]["tools"].([]any)
			if paths[1] != "/v1/responses" || len(searchTools) != 1 || searchTools[0].(map[string]any)["type"] != "web_search" {
				t.Fatalf("search request=%#v", bodies[1])
			}
			if paths[2] != "/v1/responses" {
				t.Fatalf("extractor endpoint=%s", paths[2])
			}
			tools, ok := bodies[2]["tools"].([]any)
			if !ok || len(tools) != 2 {
				t.Fatalf("tools=%#v", bodies[2]["tools"])
			}
			if tools[0].(map[string]any)["type"] != "web_search" || tools[1].(map[string]any)["type"] != "web_extractor" {
				t.Fatal("missing dependent tools")
			}
			if bodies[2]["enable_thinking"] != true {
				t.Fatal("extractor requires thinking")
			}
			policy.err = errors.New("unavailable")
			_, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
			if err == nil || len(bodies) != 4 {
				t.Fatal("policy failure must not send any request")
			}
		})
	}
}

func TestNetworkToolsRejectExtractorWithoutSearch(t *testing.T) {
	if err := (NetworkTools{WebExtractorEnabled: true}).Validate(); err == nil {
		t.Fatal("missing search dependency")
	}
}

func TestFlashSearchOnlyUsesResponsesWithoutGrantingExtractor(t *testing.T) {
	for _, model := range []string{"qwen3.5-flash", "qwen3.5-flash-2026-02-23"} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.URL.Path != "/v1/responses" {
					t.Errorf("search-only route = %s", r.URL.Path)
				}
				tools, _ := body["tools"].([]any)
				if len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search" {
					t.Errorf("search-only granted unexpected tools: %#v", tools)
				}
				if body["enable_thinking"] != true {
					t.Error("Flash Responses tools require thinking mode")
				}
				_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://example.test/source"}]}},{"type":"message","content":[{"type":"output_text","text":"final answer"}]}]}`))
			}))
			defer server.Close()
			client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL + "/v1/chat/completions", Model: model, NetworkTools: NetworkTools{WebSearchEnabled: true}})
			got, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "search"}}, GenerateOptions{})
			if err != nil || got != "final answer" {
				t.Fatalf("answer=%q error=%v", got, err)
			}
		})
	}
}

func TestOtherCloudSearchKeepsCompatibleChatRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/v1/chat/completions" || body["enable_search"] != true || body["include_tool_usage"] != true || body["tools"] != nil {
			t.Errorf("legacy cloud search changed: %s %#v", r.URL.Path, body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL + "/v1", Model: "qwen-plus", NetworkTools: NetworkTools{WebSearchEnabled: true}})
	if _, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "search"}}, GenerateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestExtractorRejectsUnfinishedOrInvalidResponse(t *testing.T) {
	for _, body := range []string{
		`{"status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"["}]}]}`,
		`{"status":"failed","error":{"message":"tool unavailable"}}`,
		`{"status":"completed","output":[{"type":"reasoning","content":[{"type":"output_text","text":"not an answer"}]}]}`,
		`{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"denied"}]}]}`,
		`{"status":"completed","output":[{"type":"message","status":"in_progress","content":[{"type":"output_text","text":"["}]}]}`,
		`{"status":"completed","output":`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL + "/v1/chat/completions", Model: "qwen3.5-flash", NetworkTools: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}})
			if _, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{}); err == nil {
				t.Fatal("must reject partial/empty/refused response")
			}
		})
	}
}

func TestExtractorHTTPFailureUsesExistingRetryErrorContract(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable"}}`))
			}))
			defer server.Close()
			client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash", NetworkTools: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}})
			_, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
			var upstream *HTTPStatusError
			if !errors.As(err, &upstream) || upstream.Status != status || calls != 1 {
				t.Fatalf("status=%d calls=%d err=%v", status, calls, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = client.Complete(ctx, []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{})
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("canceled request escaped: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestLocalModelDoesNotReceiveCloudTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, key := range []string{"enable_search", "search_options", "tools"} {
			if _, ok := body[key]; ok {
				t.Errorf("local request received %s", key)
			}
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Error("local route changed")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	client := NewQwenClient(QwenConfig{Deployment: DeploymentLocal, BaseURL: server.URL + "/v1", Model: "local", NetworkTools: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}})
	if got, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{}); err != nil || got != "ok" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}
