package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStructuredModelCallLogHasUsageAndNoContentsOrCredentials(t *testing.T) {
	var output bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"completed","usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7,"x_tools":{"web_extractor":{"count":1}}},"output":[{"type":"message","content":[{"type":"output_text","text":"PRIVATE-ANSWER"}]}]}`))
	}))
	defer server.Close()
	policy := &mutableToolPolicy{value: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}}
	client := NewDynamicQwenClient(nil, PurposeGeneration, QwenConfig{APIKey: "PRIVATE-KEY", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash"})
	client.SetNetworkToolsResolver(policy)
	ctx := WithCallOperation(context.Background(), "generate:run-log-test")
	_, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "PRIVATE-PROMPT https://private.example/document"}}, GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["msg"] != "model_call" || event["protocol"] != "responses" || event["operation"] != "generate:run-log-test" || event["reported_extractor_count"] != float64(1) || event["input_tokens"] != float64(3) || event["status"] != float64(200) {
		t.Fatalf("incorrect safe call event: %+v", event)
	}
	firstOutput := output.String()
	output.Reset()
	policy.err = errors.New("PRIVATE-POLICY-ERROR")
	if _, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "PRIVATE-PROMPT"}}, GenerateOptions{}); err == nil {
		t.Fatal("expected policy error")
	}
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["http_attempted"] != false || event["level"] != "WARN" {
		t.Fatalf("failed policy log: %+v", event)
	}
	for _, secret := range []string{"PRIVATE-KEY", "PRIVATE-PROMPT", "PRIVATE-ANSWER", "PRIVATE-POLICY-ERROR", "private.example", server.URL} {
		if strings.Contains(firstOutput+output.String(), secret) {
			t.Fatalf("sensitive content in log: %s", secret)
		}
	}
}

func TestToolLogsDistinguishMissingUsageZeroAndCompletedEvents(t *testing.T) {
	for _, tc := range []struct {
		name, usage       string
		reported          bool
		count             int
		extractorReported bool
	}{
		{"missing", `{}`, false, 0, false},
		{"explicit_zero", `{"x_tools":{"web_search":{"count":0}}}`, true, 0, false},
		{"empty_counter", `{"x_tools":{"web_search":{}}}`, false, 0, false},
		{"aliases", `{"x_tools":{"search":{"count":3},"web_search":{"count":2},"web_extractor":{"count":0}}}`, true, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"status":"completed","usage":` + tc.usage + `,"output":[{"type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://PRIVATE-SOURCE.test/x"},"https://PRIVATE-SOURCE.test/y"]}},{"type":"web_search_call","status":"failed"},{"type":"web_extractor_call","status":"completed","urls":["https://PRIVATE-SOURCE.test/x"],"output":"PRIVATE-EXTRACTED-CONTENT"},{"type":"message","content":[{"type":"output_text","text":"answer"}]}]}`))
			}))
			defer server.Close()
			client := NewDynamicQwenClient(nil, PurposeGeneration, QwenConfig{APIKey: "PRIVATE-KEY", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash"})
			client.SetNetworkToolsResolver(&mutableToolPolicy{value: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}})
			if _, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "PRIVATE-PROMPT"}}, GenerateOptions{}); err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(output.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event["search_usage_reported"] != tc.reported || event["extractor_usage_reported"] != tc.extractorReported || event["reported_search_count"] != float64(tc.count) || event["tool_events_observed"] != true || event["search_calls_observed"] != float64(2) || event["search_calls_completed"] != float64(1) || event["search_sources_returned"] != float64(2) || event["extractor_calls_completed"] != float64(1) || event["extractor_outputs_nonempty"] != float64(1) {
				t.Fatalf("ambiguous tool evidence: %+v", event)
			}
			for _, secret := range []string{"PRIVATE-SOURCE", "PRIVATE-EXTRACTED", "PRIVATE-KEY", "PRIVATE-PROMPT"} {
				if strings.Contains(output.String(), secret) {
					t.Fatalf("private tool content leaked: %s", secret)
				}
			}
		})
	}
}

func TestCompletedExtractorAccessFailureIsNotHiddenByNonemptyOutput(t *testing.T) {
	for _, output := range []string{
		"The provided webpage content could not be accessed. Please check the URL or file format.",
		"One page had useful text. The webpage content could not be processed for another page.",
	} {
		raw, _ := json.Marshal(output)
		evidence := toolEvents([]responsesOutputItem{{Type: "web_extractor_call", Status: "completed", Output: raw}})
		if evidence.ExtractorCompleted != 1 || evidence.ExtractorOutputs != 1 || evidence.ExtractorFailureNotices != 1 {
			t.Fatalf("completed/nonempty hid failure notice: %+v", evidence)
		}
		var log bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
		logModelCall(t.Context(), PurposeGeneration, QwenConfig{Model: "test"}, &modelCallMeta{Events: evidence}, time.Now(), nil)
		slog.SetDefault(previous)
		var event map[string]any
		if err := json.Unmarshal(log.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event["extractor_outputs_with_failure_notice"] != float64(1) || event["extractor_outputs_nonempty"] != float64(1) || strings.Contains(log.String(), output) {
			t.Fatal("ambiguous or unsafe extraction log", log.String())
		}
	}
}
