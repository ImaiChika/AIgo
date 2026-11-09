package generator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aigo/internal/llm"
)

// Live tool answers can still contain Markdown despite a JSON-only instruction.
// The transport must keep final text intact for the existing generator parser.
func TestExtractorFinalTextUsesExistingCodeFenceParser(t *testing.T) {
	raw := "```json\n[{\"clinical_stem\":\"患者，男，23岁。该患者最可能的诊断是\",\"answer\":\"A\"}]\n```"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("wrong tool endpoint: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{
			map[string]any{"type": "web_extractor_call", "content": "not question content"},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": raw}}},
		}})
	}))
	defer server.Close()
	client := llm.NewQwenClient(llm.QwenConfig{APIKey: "fixture", BaseURL: server.URL + "/v1", Model: "qwen3.5-flash", NetworkTools: llm.NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}})
	text, err := client.Complete(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: "只输出JSON"}}, llm.GenerateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := ParseQuestionsFromRaw(text)
	if err != nil || len(items) != 1 || items[0]["answer"] != "A" {
		t.Fatalf("existing parser lost tool answer: %+v %v", items, err)
	}
}
