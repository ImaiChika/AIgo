package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatRejectsUnfinishedRefusedOrAmbiguousFinalAnswer(t *testing.T) {
	for _, tc := range []struct {
		name, reason, content, refusal string
		choices                        int
	}{
		{"truncated but superficially valid JSON", "length", "[]", "", 1},
		{"content filtered", "content_filter", "[]", "", 1},
		{"tool call is not final text", "tool_calls", "[]", "", 1},
		{"refusal", "stop", "[]", "refused", 1},
		{"empty final text", "stop", "", "", 1},
		{"multiple choices", "stop", "[]", "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				choices := []any{}
				for i := 0; i < tc.choices; i++ {
					choices = append(choices, map[string]any{"finish_reason": tc.reason, "message": map[string]any{"role": "assistant", "content": tc.content, "refusal": tc.refusal}})
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": choices})
			}))
			defer s.Close()
			client := NewQwenClient(QwenConfig{Deployment: DeploymentLocal, BaseURL: s.URL, Model: "test"})
			if text, err := client.Complete(context.Background(), []Message{{Role: RoleUser, Content: "contract test"}}, GenerateOptions{}); err == nil {
				t.Fatal("unusable final response accepted", text)
			}
		})
	}
}
