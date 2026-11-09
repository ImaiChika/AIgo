package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type captureCallStore struct {
	mu         sync.Mutex
	rows       []ModelCallRecord
	fail, wait bool
}

func (s *captureCallStore) SaveModelCall(ctx context.Context, r ModelCallRecord) error {
	if ctx.Err() != nil {
		return errors.New("unexpected canceled evidence context")
	}
	if s.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, r)
	if s.fail {
		return errors.New("PRIVATE-STORAGE-ERROR")
	}
	return nil
}

type switchingCallConfig struct {
	mu  sync.Mutex
	cfg QwenConfig
}

func (s *switchingCallConfig) ResolveQwenConfig(context.Context, ConfigPurpose) (QwenConfig, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, true, nil
}
func (s *switchingCallConfig) set(c QwenConfig) { s.mu.Lock(); defer s.mu.Unlock(); s.cfg = c }

type revisionedTools struct {
	mu       sync.Mutex
	policy   NetworkTools
	revision int64
	err      error
}

func (p *revisionedTools) ResolveNetworkTools(ctx context.Context) (NetworkTools, error) {
	v, _, e := p.ResolveVersionedNetworkTools(ctx)
	return v, e
}
func (p *revisionedTools) ResolveVersionedNetworkTools(context.Context) (NetworkTools, int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy, p.revision, p.err
}

func TestCallEvidenceUsesActualPayloadConfigAndOptionalToolEvents(t *testing.T) {
	resolver := &switchingCallConfig{}
	policy := &revisionedTools{policy: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}, revision: 17}
	var requestBytes []byte
	reply := `{"status":"completed","usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16},"output":[{"type":"web_extractor_call","status":"completed","output":"The provided webpage content could not be accessed."},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"PRIVATE-ANSWER"}]}]}`
	changed := QwenConfig{ConfigID: "route-B", ConfigSource: "database", Model: "changed-model"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBytes, _ = io.ReadAll(r.Body)
		resolver.set(changed)
		policy.mu.Lock()
		policy.policy = NetworkTools{}
		policy.revision = 18
		policy.mu.Unlock()
		w.Write([]byte(reply))
	}))
	defer server.Close()
	stamp := time.Date(2026, 10, 23, 1, 2, 3, 123000000, time.UTC)
	resolver.set(QwenConfig{ConfigID: "route-A", ConfigUpdatedAt: stamp, BaseURL: server.URL + "/v1", APIKey: "PRIVATE-KEY", Model: "qwen3.5-flash"})
	sink := &captureCallStore{}
	client := NewDynamicQwenClient(resolver, PurposeGeneration, QwenConfig{})
	client.SetNetworkToolsResolver(policy)
	client.SetCallRecorder(sink)
	ctx := WithCallWorkflow(WithCallAttempt(WithCallOperation(t.Context(), "batch:test:3"), 2), "knowledge-v3")
	ctx, identity := CaptureModelIdentity(ctx)
	answer, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "PRIVATE-PROMPT"}}, GenerateOptions{MedicalSearchHint: true})
	if err != nil || answer != "PRIVATE-ANSWER" || identity.Model != "qwen3.5-flash" {
		t.Fatal(answer, identity, err)
	}
	if len(sink.rows) != 1 {
		t.Fatal(sink.rows)
	}
	r := sink.rows[0]
	if identity.CallID != r.ID || !identity.Recorded {
		t.Fatal("model reference missing", identity)
	}
	if r.ConfigID != "route-A" || r.ConfigUpdatedAt == nil || !r.ConfigUpdatedAt.Equal(stamp) || r.ConfigSource != "database" || r.Model != "qwen3.5-flash" || r.ToolsRevision != 17 || !r.WebSearch || !r.WebExtractor || r.Operation != "batch:test:3" || r.Attempt != 2 || r.WorkflowVersion != "knowledge-v3" {
		t.Fatalf("in-flight config was replaced: %+v", r)
	}
	if r.Outcome != "returned" || r.ErrorKind != "none" || r.Protocol != "responses" || r.HTTPStatus != 200 || !r.HTTPAttempted || r.InputSHA256 != hashBytes(requestBytes) || r.ResponseSHA256 != hashBytes([]byte(reply)) || r.OutputSHA256 != hashBytes([]byte(answer)) || r.InputBytes != len(requestBytes) || r.OutputBytes != len(answer) || !r.UsageReported || r.TotalTokens != 16 || !r.InputTokensReported || !r.OutputTokensReported || !r.TotalTokensReported || r.EnableThinking == nil || !*r.EnableThinking || r.ExtractorFailureNotices != 1 || r.SearchCalls != 0 {
		t.Fatal(r)
	}
	// No search, and an optional extractor failure, must not invalidate a complete answer.
	if !strings.Contains(string(requestBytes), "可使用") && !strings.Contains(string(requestBytes), "联网") {
		t.Fatal("actual optional reminder was not sent")
	}
	raw, _ := json.Marshal(r)
	for _, secret := range []string{"PRIVATE-KEY", "PRIVATE-PROMPT", "PRIVATE-ANSWER", server.URL, "webpage content"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private content persisted", secret)
		}
	}
	// Next attempt uses the newly resolved model and off policy, not the first snapshot.
	changed.BaseURL = server.URL + "/v1"
	changed.Deployment = DeploymentLocal
	resolver.set(changed)
	// Use a fresh chat responder to keep response protocols explicit.
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer chat.Close()
	changed.BaseURL = chat.URL + "/v1"
	resolver.set(changed)
	if _, err = client.Complete(WithCallAttempt(ctx, 3), []Message{{Role: RoleUser, Content: "next"}}, GenerateOptions{}); err != nil {
		t.Fatal(err)
	}
	r = sink.rows[1]
	if r.ConfigID != "route-B" || r.ToolsRevision != 18 || r.WebSearch || r.WebExtractor || r.Attempt != 3 || r.ID == sink.rows[0].ID || r.UsageReported || r.InputTokensReported || r.OutputTokensReported || r.TotalTokensReported || r.Protocol != "chat_completions" {
		t.Fatal(r)
	}
}

func TestCallEvidenceFailureDoesNotChangeModelOutcome(t *testing.T) {
	for _, mode := range []string{"storage_error", "storage_timeout", "canceled_provider", "http_error", "policy_error"} {
		t.Run(mode, func(t *testing.T) {
			sink := &captureCallStore{fail: mode == "storage_error", wait: mode == "storage_timeout"}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "http_error" {
					w.WriteHeader(503)
					w.Write([]byte(`{"error":{"message":"PRIVATE-UPSTREAM"}}`))
					return
				}
				w.Write([]byte(`{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0},"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()
			client := NewDynamicQwenClient(nil, PurposeAICheck, QwenConfig{Deployment: DeploymentLocal, BaseURL: server.URL, Model: "check"})
			client.SetCallRecorder(sink)
			if mode == "policy_error" {
				client.SetNetworkToolsResolver(&revisionedTools{err: errors.New("PRIVATE-POLICY")})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled_provider" {
				cancel()
			}
			started := time.Now()
			answer, err := client.Complete(WithCallOperation(ctx, "question:test:first_check"), []Message{{Role: RoleUser, Content: "private"}}, GenerateOptions{})
			if mode == "storage_timeout" {
				if answer != "ok" || err != nil || time.Since(started) > time.Second || calls != 1 {
					t.Fatal(answer, err, calls)
				}
				return
			}
			if len(sink.rows) != 1 {
				t.Fatal(sink.rows)
			}
			r := sink.rows[0]
			switch mode {
			case "storage_error":
				if answer != "ok" || err != nil || r.Outcome != "returned" || !r.UsageReported || calls != 1 {
					t.Fatal(answer, err, r)
				}
			case "http_error":
				if err == nil || r.ErrorKind != "upstream_http" || r.HTTPStatus != 503 || r.FailureStage != "provider" || r.OutputSHA256 != "" || calls != 1 {
					t.Fatal(err, r)
				}
			case "policy_error":
				if err == nil || calls != 0 || r.HTTPAttempted || r.Outcome != "not_started" || r.FailureStage != "tool_policy" {
					t.Fatal(err, r, calls)
				}
			case "canceled_provider":
				if !errors.Is(err, context.Canceled) || r.ErrorKind != "canceled" || calls != 0 {
					t.Fatal(err, r, calls)
				}
			}
		})
	}
}

func TestCallEvidenceConcurrentOperationsStaySeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	sink := &captureCallStore{}
	client := NewDynamicQwenClient(nil, PurposeGeneration, QwenConfig{Deployment: DeploymentLocal, BaseURL: server.URL, Model: "local"})
	client.SetCallRecorder(sink)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			ctx := WithCallOperation(t.Context(), fmt.Sprintf("generate:concurrent-%d", i))
			if _, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ids := map[string]bool{}
	operations := map[string]bool{}
	for _, r := range sink.rows {
		if ids[r.ID] || operations[r.Operation] || r.Outcome != "returned" || r.Operation == "" {
			t.Fatal(r)
		}
		ids[r.ID] = true
		operations[r.Operation] = true
	}
	if len(ids) != 20 {
		t.Fatal(len(ids))
	}
}

func TestSuccessfulCallNearDeadlineSkipsEvidenceInsteadOfWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &captureCallStore{wait: true}
		client := NewDynamicQwenClient(nil, PurposeGeneration, QwenConfig{})
		client.SetCallRecorder(sink)
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		started := time.Now()
		client.recordCall(ctx, QwenConfig{}, &modelCallMeta{}, started, "ok", nil)
		if ctx.Err() != nil || !time.Now().Equal(started) {
			t.Fatal("diagnostic write consumed task deadline")
		}
	})
}

func TestReusedCaptureDoesNotCarryPreviousPersistenceSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	sink := &captureCallStore{}
	client := NewDynamicQwenClient(nil, PurposeGeneration, QwenConfig{Deployment: DeploymentLocal, BaseURL: server.URL, Model: "local"})
	client.SetCallRecorder(sink)
	ctx, identity := CaptureModelIdentity(t.Context())
	if _, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "first"}}, GenerateOptions{}); err != nil || !identity.Recorded {
		t.Fatal(identity, err)
	}
	before := identity.CallID
	sink.fail = true
	if _, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "second"}}, GenerateOptions{}); err != nil || identity.Recorded || identity.CallID == before {
		t.Fatal("stale persistence acknowledgement", identity, err)
	}
}
