package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

type runtimeResolverFunc func(context.Context, ConfigPurpose) (QwenConfig, bool, error)

func (f runtimeResolverFunc) ResolveQwenConfig(ctx context.Context, purpose ConfigPurpose) (QwenConfig, bool, error) {
	return f(ctx, purpose)
}

func TestDynamicQwenClientConfigResolutionFailureNeverUsesFallback(t *testing.T) {
	for _, purpose := range []ConfigPurpose{PurposeGeneration, PurposeAICheck, PurposeLearning} {
		for _, withRecovery := range []bool{false, true} {
			t.Run(string(purpose)+"/recovery="+map[bool]string{false: "off", true: "on"}[withRecovery], func(t *testing.T) {
				var calls atomic.Int64
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"choices":[{"message":{"content":"fallback must not be used"},"finish_reason":"stop"}]}`))
				}))
				defer endpoint.Close()
				resolver := runtimeResolverFunc(func(_ context.Context, got ConfigPurpose) (QwenConfig, bool, error) {
					if got != purpose {
						t.Error("resolver received another call purpose")
					}
					return QwenConfig{APIKey: "PRIVATE-PARTIAL-KEY", BaseURL: "http://private.invalid", Model: "PRIVATE-PARTIAL-MODEL"}, true, errors.New("PRIVATE-RESOLVER-ERROR with PRIVATE-KEY")
				})
				fallback := QwenConfig{Deployment: DeploymentLocal, BaseURL: endpoint.URL + "/v1", Model: "PRIVATE-FALLBACK-MODEL", APIKey: "PRIVATE-FALLBACK-KEY"}
				client := NewDynamicQwenClient(resolver, purpose, fallback)
				if withRecovery {
					client.SetEndpointRecovery(NewEndpointRecovery())
				}
				sink := &captureCallStore{}
				client.SetCallRecorder(sink)
				ctx, identity := CaptureModelIdentity(WithCallOperation(t.Context(), "c6-config-resolution"))
				answer, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "PRIVATE-PROMPT"}}, GenerateOptions{})
				if err == nil || answer != "" {
					t.Error("configuration failure must return a safe error and no model answer")
				}
				if err != nil && (strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), endpoint.URL)) {
					t.Error("resolver diagnostics or fallback routing escaped in the error")
				}
				if calls.Load() != 0 {
					t.Errorf("configuration failure made %d fallback HTTP requests", calls.Load())
				}
				if client.Ready(t.Context()) {
					t.Error("unresolved configuration must pause queue admission")
				}
				if err := client.WaitReady(t.Context()); err == nil || strings.Contains(err.Error(), "PRIVATE") {
					t.Error("WaitReady must return the safe configuration failure")
				}
				if info := client.Info(); !reflect.DeepEqual(info, ClientInfo{}) {
					t.Error("Info must not advertise a fallback or partial route during a configuration failure")
				}
				if client.CurrentModel() != "" || identity.Model != "" {
					t.Error("configuration failure was attributed to an unused model")
				}
				if len(sink.rows) != 1 {
					t.Fatalf("want one local evidence row, got %d", len(sink.rows))
				}
				r := sink.rows[0]
				if r.Purpose != purpose || r.FailureStage != "config_resolution" || r.ConfigSource != "database_resolution_error" || r.ErrorKind != "config_resolution" || r.Outcome != "not_started" || r.Protocol != "none" || r.HTTPAttempted || r.HTTPStatus != 0 || r.UsageReported || r.TotalTokens != 0 || r.InputBytes != 0 || r.ResponseBytes != 0 || r.OutputBytes != 0 || r.Model != "" || r.ConfigID != "" {
					t.Error("configuration failure evidence must describe no vendor request, model, response, or usage")
				}
				if !identity.Recorded || identity.CallID != r.ID {
					t.Error("local failure evidence must retain its safe attempt reference")
				}
				raw, marshalErr := json.Marshal(r)
				if marshalErr != nil || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), endpoint.URL) {
					t.Error("local configuration failure evidence exposed private values")
				}
			})
		}
	}
}

func TestDynamicQwenClientMissingActiveConfigKeepsEnvironmentFallback(t *testing.T) {
	for _, purpose := range []ConfigPurpose{PurposeGeneration, PurposeAICheck, PurposeLearning} {
		t.Run(string(purpose), func(t *testing.T) {
			var calls atomic.Int64
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"choices":[{"message":{"content":"environment configured"},"finish_reason":"stop"}]}`))
			}))
			defer endpoint.Close()
			resolver := runtimeResolverFunc(func(context.Context, ConfigPurpose) (QwenConfig, bool, error) { return QwenConfig{}, false, nil })
			client := NewDynamicQwenClient(resolver, purpose, QwenConfig{Deployment: DeploymentLocal, BaseURL: endpoint.URL + "/v1", Model: "fixture-fallback"})
			sink := &captureCallStore{}
			client.SetCallRecorder(sink)
			if !client.Ready(t.Context()) || client.WaitReady(t.Context()) != nil {
				t.Fatal("absence of an active DB config must keep the valid environment route ready")
			}
			answer, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "fallback control"}}, GenerateOptions{})
			if err != nil || answer != "environment configured" || calls.Load() != 1 || client.Info().Model != "fixture-fallback" {
				t.Fatal("valid environment fallback no longer works")
			}
			if len(sink.rows) != 1 || sink.rows[0].ConfigSource != "environment_no_active_config" || !sink.rows[0].HTTPAttempted || sink.rows[0].FailureStage != "" {
				t.Fatal("environment fallback evidence has the wrong source or stage")
			}
		})
	}
}

func TestDynamicQwenClientCanceledConfigReadKeepsTaskCancellation(t *testing.T) {
	for _, kind := range []string{"canceled", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			want := context.Canceled
			if kind == "timeout" {
				cancel()
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			} else {
				cancel()
			}
			defer cancel()
			resolver := runtimeResolverFunc(func(ctx context.Context, _ ConfigPurpose) (QwenConfig, bool, error) {
				return QwenConfig{}, false, ctx.Err()
			})
			client := NewDynamicQwenClient(resolver, PurposeLearning, QwenConfig{Deployment: DeploymentLocal, BaseURL: "http://127.0.0.1:1/v1", Model: "must-not-be-used"})
			sink := &captureCallStore{}
			client.SetCallRecorder(sink)
			answer, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "unused"}}, GenerateOptions{})
			if answer != "" || !errors.Is(err, want) {
				t.Fatal("canceled configuration read must keep task cancellation/timeout semantics")
			}
			if len(sink.rows) != 1 || sink.rows[0].HTTPAttempted || sink.rows[0].ErrorKind != kind || sink.rows[0].FailureStage != "config_resolution" || sink.rows[0].UsageReported {
				t.Fatal("canceled configuration read must not appear as a provider request or configuration outage")
			}
		})
	}
}
