package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func streamFrame(content string) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": content}}}})
	return "data: " + string(raw) + "\n\n"
}

const streamStop = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"

func TestChatStreamReceivesCompleteAnswerAndTailUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["temperature"] != 0.6 {
			t.Error("requested sampling changed")
		}
		if body["stream"] != true {
			t.Error("stream flag absent")
		}
		options, _ := body["stream_options"].(map[string]any)
		if options["include_usage"] != true {
			t.Error("tail usage not requested")
		}
		for _, key := range []string{"max_tokens", "max_output_tokens", "max_completion_tokens", "thinking_budget"} {
			if _, ok := body[key]; ok {
				t.Error("unexpected model cap", key)
			}
		}
		w.Header().Set("X-Request-ID", "stream-request-1")
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		parts := []string{": heartbeat\r\n\r\n", "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"PRIVATE_REASONING_MARKER\"}}]}\n\n", streamFrame("完整"), streamFrame("答案"), streamStop, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":9000,\"total_tokens\":9012,\"completion_tokens_details\":{\"reasoning_tokens\":8990}}}\n\n", "data: [DONE]\n\n"}
		for _, p := range parts {
			for i := 0; i < len(p); i += 3 {
				end := min(i+3, len(p))
				w.Write([]byte(p[i:end]))
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer server.Close()
	client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL, Model: "qwen3.5-flash"})
	meta := &modelCallMeta{}
	ctx := context.WithValue(t.Context(), modelCallMetaKey{}, meta)
	answer, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "full response"}}, GenerateOptions{StreamChat: true, Temperature: 0.6})
	if err != nil || answer != "完整答案" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	if meta.RequestedTemperature == nil || *meta.RequestedTemperature != 0.6 {
		t.Fatal("sampling audit missing", meta)
	}
	if meta.UpstreamRequestID != "stream-request-1" || meta.Stream.FinishReason != "stop" {
		t.Fatal("upstream completion evidence missing", meta)
	}
	if meta.Usage.TotalTokens != 9012 || !meta.UsageReported {
		t.Fatal("tail usage missing", meta.Usage)
	}
}
func TestChatStreamRejectsIncompleteAndUnusableResponses(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"missing terminal marker", streamFrame("[]") + streamStop},
		{"done without stop", streamFrame("[]") + "data: [DONE]\n\n"},
		{"length is not a completed answer", streamFrame("[]") + "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"},
		{"error after apparently complete JSON", streamFrame("[]") + "data: {\"error\":{\"message\":\"private provider failure\"}}\n\n"},
		{"refusal", "data: {\"choices\":[{\"index\":0,\"delta\":{\"refusal\":\"private refusal\"}}]}\n\n" + streamStop + "data: [DONE]\n\n"},
		{"multiple indexes", "data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"[]\"}}]}\n\n" + streamStop + "data: [DONE]\n\n"},
		{"extra content after stop", streamFrame("[]") + streamStop + streamFrame("extra") + "data: [DONE]\n\n"},
		{"reasoning without answer", "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private reasoning\"}}]}\n\n" + streamStop + "data: [DONE]\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			c := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL, Model: "qwen3.5-flash"})
			answer, err := c.Complete(t.Context(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{StreamChat: true})
			if err == nil || answer != "" {
				t.Fatal("partial or unusable stream accepted", answer, err)
			}
		})
	}
}
func TestChatStreamAcceptsMultilineEventsWithoutUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "\ufeffdata: {\"choices\":\n"+"data: [{\"index\":0,\"delta\":{\"content\":\"complete\"}}]}\n\n"+streamStop+"data: [DONE]")
	}))
	defer server.Close()
	c := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL, Model: "qwen3.5-flash"})
	m := &modelCallMeta{}
	answer, err := c.Complete(context.WithValue(t.Context(), modelCallMetaKey{}, m), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{StreamChat: true})
	if err != nil || answer != "complete" || m.UsageReported {
		t.Fatal(answer, err, m.UsageReported)
	}
}
func TestChatStreamCancellationClosesHTTPAndNeverRetries(t *testing.T) {
	started := make(chan struct{})
	ended := make(chan struct{})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrame("[]"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(ended)
	}))
	defer server.Close()
	client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL, Model: "qwen3.5-flash"})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		answer, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{StreamChat: true})
		if answer != "" {
			t.Error("partial response exposed")
		}
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancellation ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("read did not cancel")
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("upstream connection stayed open")
	}
	if calls != 1 {
		t.Fatal("client retried paid request", calls)
	}
}
func TestChatStreamFrameOverheadDoesNotConsumeContentBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writer := strings.Builder{}
		frame := streamFrame("x")
		for i := 0; i < 100000; i++ {
			writer.WriteString(frame)
		}
		writer.WriteString(streamStop + "data: [DONE]\n\n")
		fmt.Fprint(w, writer.String())
	}))
	defer server.Close()
	client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL, Model: "qwen3.5-flash"})
	answer, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{StreamChat: true})
	if err != nil || len(answer) != 100000 {
		t.Fatal("wire envelopes truncated valid content", len(answer), err)
	}
}

func TestChatStreamConcurrentEvidenceKeepsOnlySafeStatistics(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		label := body.Messages[0].Content
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"PRIVATE_THOUGHT\"}}]}\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-release
		fmt.Fprint(w, streamFrame(label)+streamStop+"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":12,\"total_tokens\":13,\"completion_tokens_details\":{\"reasoning_tokens\":9}}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	store := &captureCallStore{}
	client := NewDynamicQwenClient(nil, PurposeLearning, QwenConfig{APIKey: "PRIVATE_KEY", BaseURL: server.URL, Model: "qwen3.5-flash"})
	client.SetCallRecorder(store)
	errs := make(chan error, 2)
	for _, label := range []string{"account-A", "account-B"} {
		go func() {
			ctx := WithCallOperation(t.Context(), label)
			answer, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: label}}, GenerateOptions{StreamChat: true})
			if err == nil && answer != label {
				err = fmt.Errorf("response crossed account")
			}
			errs <- err
		}()
	}
	<-started
	<-started
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.rows) != 2 {
		t.Fatal("call evidence missing", store.rows)
	}
	seen := map[string]bool{}
	for _, r := range store.rows {
		seen[r.Operation] = true
		if !r.Streamed || r.StreamChunks < 4 || r.ReasoningTokens != 9 || !r.ReasoningTokensReported || r.ReasoningCharacters != len("PRIVATE_THOUGHT") || r.FirstReasoningMS == 0 || r.FirstAnswerMS == 0 || r.LastStreamContentMS < r.FirstAnswerMS {
			t.Fatal("stream statistics missing", r)
		}
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "PRIVATE_") {
			t.Fatal("private stream text or credentials persisted")
		}
	}
	if !seen["account-A"] || !seen["account-B"] {
		t.Fatal("operation identity crossed", seen)
	}
}

func TestChatStreamUnverifiedModelsKeepOriginalDelivery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		deployment DeploymentMode
		model      string
	}{{"other cloud", DeploymentCloud, "another-model"}, {"local Flash", DeploymentLocal, "qwen3.5-flash"}} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if _, ok := body["stream"]; ok {
					t.Error("unverified transport changed")
				}
				fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"complete"}}]}`)
			}))
			defer server.Close()
			client := NewQwenClient(QwenConfig{Deployment: tc.deployment, APIKey: "test", BaseURL: server.URL, Model: tc.model})
			answer, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "test"}}, GenerateOptions{StreamChat: true})
			if err != nil || answer != "complete" {
				t.Fatal(answer, err)
			}
		})
	}
}

func TestSummaryStreamSupportsVerifiedModelsAndExplicitZeroTemperature(t *testing.T) {
	for _, model := range []string{"qwen3.5-flash", "qwen3.6-flash", "qwen3.8-flash"} {
		t.Run(model, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if v, ok := b["temperature"]; !ok || v != float64(0) {
					t.Error("explicit zero omitted")
				}
				if b["stream"] != true || b["enable_thinking"] != true {
					t.Error("summary streaming/thinking changed")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, streamFrame("完整候选")+streamStop+"data: [DONE]\n\n")
			}))
			defer server.Close()
			on := true
			c := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL, Model: model, EnableThinking: &on})
			v, e := c.Complete(t.Context(), []Message{{Role: RoleUser, Content: "summary"}}, GenerateOptions{Temperature: 0, TemperatureSet: true, StreamChat: true})
			if e != nil || v != "完整候选" {
				t.Fatal(v, e)
			}
		})
	}
}
