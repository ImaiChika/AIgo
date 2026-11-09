package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"aigo/internal/llm"
	"aigo/internal/storage"
)

func TestModelToolQuotaAPIRequiresRevisionAndExistingAuthorization(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.quotaStore = store
	handler := server.Handler()
	token := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.180")
	_, teacher := quotaHTTPTeacher(t, handler, token, "network-teacher")
	q, _, err := store.GetGenerationQuota(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q.ModelTools = &llm.NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if r := serveGenerateJSON(t, handler, method, "/api/system/generation-quota", teacher, q); r.Code != 403 {
			t.Fatalf("teacher %s=%d", method, r.Code)
		}
	}
	r := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", token, q)
	if r.Code != 200 {
		t.Fatalf("save=%d %s", r.Code, r.Body)
	}
	var reply struct {
		Quota storage.GenerationQuota `json:"quota"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Quota.ModelTools == nil || !reply.Quota.ModelTools.WebExtractorEnabled {
		t.Fatal("saved policy not returned")
	}
	if r := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", token, q); r.Code != 409 {
		t.Fatalf("stale=%d", r.Code)
	}
	q = reply.Quota
	q.ModelTools = &llm.NetworkTools{WebExtractorEnabled: true}
	if r := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", token, q); r.Code != 400 {
		t.Fatalf("dependency=%d %s", r.Code, r.Body)
	}
	payload := map[string]any{"single_max_questions": 20, "batch_max_questions": 100, "single_active_per_user": 2, "batch_active_per_user": 1, "global_pending_questions": 500, "model_tools": map[string]any{"web_search_enabled": true}}
	if r := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", token, payload); r.Code != 400 {
		t.Fatalf("missing revision=%d", r.Code)
	}
	payload["revision"] = reply.Quota.Revision
	payload["model_tools"] = map[string]any{"web_search_enabled": "true"}
	if r := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", token, payload); r.Code != 400 {
		t.Fatalf("invalid boolean=%d", r.Code)
	}
	delete(payload, "model_tools")
	delete(payload, "revision")
	if r := serveGenerateJSON(t, handler, http.MethodPut, "/api/system/generation-quota", token, payload); r.Code != 200 {
		t.Fatalf("legacy=%d %s", r.Code, r.Body)
	}
	p, err := store.ResolveNetworkTools(context.Background())
	if err != nil || !p.WebSearchEnabled || !p.WebExtractorEnabled {
		t.Fatalf("legacy save lost tools: %+v %v", p, err)
	}
}
