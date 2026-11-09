package llm

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestOptionalNetworkDoesNotRejectValidAnswerWithoutSearch(t *testing.T) {
	for _, tc := range []struct {
		name, events string
	}{
		{"not_called", ""},
		{"only_claimed_usage", ""},
		{"failed", `{"type":"web_search_call","status":"failed","action":{"sources":[{"url":"https://example.test"}]}},`},
		{"fetch_failed", `{"type":"web_extractor_call","status":"failed"},`},
		{"no_sources", `{"type":"web_search_call","status":"completed"},`},
		{"invalid_sources", `{"type":"web_search_call","status":"completed","action":{"sources":[{"url":"not-a-url"}]}},`},
		{"completed", `{"type":"web_search_call","status":"completed","action":{"sources":[{"url":"https://PRIVATE-SOURCE.test/guide"}]}},`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct{ Input []Message }
				_ = json.NewDecoder(r.Body).Decode(&body)
				all, _ := json.Marshal(body.Input)
				if !strings.Contains(string(all), "可选联网工具") {
					t.Error("optional tool reminder missing")
				}
				usage := `{}`
				if tc.name == "only_claimed_usage" {
					usage = `{"x_tools":{"web_search":{"count":1}}}`
				}
				_, _ = w.Write([]byte(`{"status":"completed","usage":` + usage + `,"output":[` + tc.events + `{"type":"message","content":[{"type":"output_text","text":"PRIVATE-ANSWER"}]}]}`))
			}))
			defer server.Close()
			messages := []Message{{Role: RoleSystem, Content: "PRIVATE-BASE"}, {Role: RoleUser, Content: "PRIVATE-PROMPT"}}
			before := append([]Message(nil), messages...)
			client := NewQwenClient(QwenConfig{APIKey: "test", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash", NetworkTools: NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: tc.name == "fetch_failed"}})
			answer, err := client.Complete(t.Context(), messages, GenerateOptions{MedicalSearchHint: true})
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d answer=%q err=%v", calls, answer, err)
			}
			if answer != "PRIVATE-ANSWER" {
				t.Fatal("valid answer was discarded")
			}
			if !reflect.DeepEqual(messages, before) {
				t.Fatal("mutated frozen messages")
			}
		})
	}
}

func TestMedicalSearchHintHonorsCurrentSwitchWithoutGrantingTools(t *testing.T) {
	for _, deployment := range []DeploymentMode{DeploymentCloud, DeploymentLocal} {
		var bodies []map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodies = append(bodies, body)
			if r.URL.Path == "/v1/responses" {
				_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"web_search_call","status":"completed","action":{"sources":["https://example.test"]}},{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
			} else {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}
		}))
		client := NewDynamicQwenClient(nil, PurposeGeneration, QwenConfig{Deployment: deployment, APIKey: "test", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash"})
		policy := &mutableToolPolicy{}
		client.SetNetworkToolsResolver(policy)
		for _, value := range []NetworkTools{{}, {WebSearchEnabled: true}, {WebSearchEnabled: true, WebExtractorEnabled: true}, {}} {
			policy.value = value
			if _, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "base"}}, GenerateOptions{MedicalSearchHint: true}); err != nil {
				t.Fatal(err)
			}
		}
		server.Close()
		for i, body := range bodies {
			hasTask := strings.Contains(string(mustJSON(t, body)), "可选联网工具")
			wantTask := deployment == DeploymentCloud && (i == 1 || i == 2)
			if hasTask != wantTask {
				t.Fatalf("deployment=%s call=%d task=%v", deployment, i, hasTask)
			}
			if !wantTask && (body["tools"] != nil || body["enable_search"] != nil) {
				t.Fatal("verification intent granted a tool")
			}
			if i == 1 && wantTask && len(body["tools"].([]any)) != 1 {
				t.Fatal("search-only request granted fetch")
			}
			if i == 2 && wantTask && len(body["tools"].([]any)) != 2 {
				t.Fatal("permitted fetch was lost")
			}
		}
	}
}

func TestOptionalNetworkLogsZeroCallsAsSuccessWithoutPrivateContent(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"PRIVATE-ANSWER"}]}]}`))
	}))
	defer server.Close()
	client := NewDynamicQwenClient(nil, PurposeGeneration, QwenConfig{APIKey: "PRIVATE-KEY", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash"})
	client.SetNetworkToolsResolver(&mutableToolPolicy{value: NetworkTools{WebSearchEnabled: true}})
	if _, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "PRIVATE-PROMPT"}}, GenerateOptions{MedicalSearchHint: true}); err != nil {
		t.Fatal("optional search treated as a failure", err)
	}
	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["status"] != float64(200) || event["network_hint"] != "enabled" || event["level"] != "INFO" || event["error_kind"] != "none" || event["search_calls_completed"] != float64(0) {
		t.Fatalf("ambiguous log: %+v", event)
	}
	if _, exists := event["source_verification"]; exists {
		t.Fatal("obsolete mandatory verification state retained")
	}
	for _, private := range []string{"PRIVATE-KEY", "PRIVATE-ANSWER", "PRIVATE-PROMPT", server.URL} {
		if strings.Contains(output.String(), private) {
			t.Fatal("private content leaked")
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
