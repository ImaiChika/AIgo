//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/batch"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
)

func TestOptionalNetworkDoesNotRetryValidSingleOrBatchOutput(t *testing.T) {
	s, store, h, token, point, _, checkClient, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	s.quotaStore = store
	var mode, wireCalls atomic.Int32
	var enabled atomic.Bool
	enabled.Store(true)
	generation := &workflowAPIClient{}
	wire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := wireCalls.Add(1)
		var body struct {
			Input    []llm.Message `json:"input"`
			Messages []llm.Message `json:"messages"`
			Tools    []any         `json:"tools"`
			Choice   any           `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		messages := body.Input
		if len(messages) == 0 {
			messages = body.Messages
		}
		if len(messages) == 0 {
			t.Error("missing messages")
			return
		}
		if enabled.Load() {
			if r.URL.Path != "/v1/responses" || !strings.Contains(messages[0].Content, "可选联网工具") || body.Choice != nil {
				t.Error("optional reminder or auto tool choice changed")
			}
		} else if r.URL.Path != "/v1/chat/completions" || strings.Contains(messages[0].Content, "可选联网工具") || len(body.Tools) != 0 {
			t.Error("closed switch did not remove tools and reminder")
		}
		if mode.Load() == 4 && n == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"message":"injected transport failure"}}`))
			return
		}
		raw, err := generation.Complete(r.Context(), messages, llm.GenerateOptions{})
		if err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/v1/chat/completions" {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": raw}}}})
			return
		}
		output := []any{}
		switch mode.Load() {
		case 1:
			output = append(output, map[string]any{"type": "web_search_call", "status": "failed"})
		case 2:
			output = append(output, map[string]any{"type": "web_search_call", "status": "completed"})
		case 3:
			output = append(output, map[string]any{"type": "web_search_call", "status": "completed", "action": map[string]any{"sources": []any{map[string]any{"url": "https://source.example.test/guide"}}}})
		}
		output = append(output, map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": raw}}})
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": output})
	}))
	defer wire.Close()
	client := llm.NewDynamicQwenClient(nil, llm.PurposeGeneration, llm.QwenConfig{APIKey: "test", BaseURL: wire.URL + "/v1", Model: "qwen3.5-flash"})
	client.SetNetworkToolsResolver(store)
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(s.aiCheckSvc)
	s.pipe.GenerationRetryBackoff = 20 * time.Millisecond
	s.pipe.GenerationPollInterval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 1)

	for i, id := range []string{"optional-unused-0001", "optional-failed-tool-0001", "optional-no-sources-0001", "optional-used-0001", "optional-http-retry-0001"} {
		mode.Store(int32(i))
		wireCalls.Store(0)
		r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, point, 1))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		_ = waitWorkflowQuestions(t, h, token, id, 1)
		run, err := store.GetGenerationRun(t.Context(), id)
		want := 1
		if i == 4 {
			want = 2
		}
		if err != nil || run.Attempts != want || int(wireCalls.Load()) != want || run.Error != "" {
			t.Fatal("optional use affected retry accounting", id, run, wireCalls.Load(), err)
		}
		_, checks := checkClient.counts()
		if checks != i+1 {
			t.Fatal("first check skipped or duplicated", checks)
		}
	}
	setPolicy := func(on bool) {
		t.Helper()
		quota, _, err := store.GetGenerationQuota(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		r := serveGenerateJSON(t, h, "PUT", "/api/system/generation-quota", token, map[string]any{"revision": quota.Revision, "single_max_questions": quota.SingleMaxQuestions, "batch_max_questions": quota.BatchMaxQuestions, "single_active_per_user": quota.SingleActivePerUser, "batch_active_per_user": quota.BatchActivePerUser, "global_pending_questions": quota.GlobalPendingQuestions, "model_tools": map[string]bool{"web_search_enabled": on, "web_extractor_enabled": on}})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		enabled.Store(on)
	}
	setPolicy(false)
	mode.Store(0)
	wireCalls.Store(0)
	id := "optional-off-0001"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, point, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	_ = waitWorkflowQuestions(t, h, token, id, 1)
	run, _ := store.GetGenerationRun(t.Context(), id)
	if run.Attempts != 1 || wireCalls.Load() != 1 {
		t.Fatal("off changed generation", run, wireCalls.Load())
	}
	setPolicy(true)
	wireCalls.Store(0)
	executor := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "test", batch.LocalConfig{Concurrency: 2, Pipeline: true, Checker: s.aiCheckSvc, CheckMaxAttempts: 3})
	defer executor.Close()
	executor.SetDraftChecker(s.aiCheckSvc)
	s.batchSvc = executor
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": point.VersionID, "knowledge_point_ids": []string{point.ID}, "count": 2})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	deadline := time.Now().Add(8 * time.Second)
	passed := 0
	for time.Now().Before(deadline) {
		if err := store.DB().QueryRow(`SELECT count(*) FROM questions WHERE status='ai_reviewed'`).Scan(&passed); err != nil {
			t.Fatal(err)
		}
		if passed == 8 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, checks := checkClient.counts()
	if passed != 8 || wireCalls.Load() != 2 || checks != 8 {
		t.Fatal("batch unused tools caused retry or duplicate checks", passed, wireCalls.Load(), checks)
	}
}
