//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
)

type c3BoundedTransport struct {
	inner http.RoundTripper
	calls atomic.Int64
}

func (c *c3BoundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if c.calls.Add(1) > 6 {
		return nil, fmt.Errorf("C3 fixed sample HTTP budget exhausted")
	}
	return c.inner.RoundTrip(r)
}

// Explicit local harness: one real-model question at most. The browser drives
// ordinary authentication, generation/check, revision, library and file APIs.
func TestBrowserC3Harness(t *testing.T) {
	if os.Getenv("AIGO_RUN_BROWSER_C3_HARNESS") != "1" {
		t.Skip("explicit local C3 browser opt-in required")
	}
	s, store, h, admin, point, _, fake, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	// Export artifacts belong to the harness, never the daily project directory.
	t.Chdir(t.TempDir())
	created := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "c3-browser-teacher", "password": "c3-browser-password", "permissions": []string{domain.PermQuestionView, domain.PermQuestionEdit, domain.PermQuestionConfirm, domain.PermQuestionGenerate, domain.PermQuestionDownload, domain.PermBatchRun, domain.PermStatsView}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	if _, err := s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: point.VersionID, Category: "临床综合", Subject: "呼吸系统", Topic: "社区获得性肺炎的诊断", OutlineCode: "c3.pneumonia"}); err != nil {
		t.Fatal(err)
	}
	var gen, check llm.Client = fake, fake
	transport := &c3BoundedTransport{inner: http.DefaultTransport}
	if os.Getenv("AIGO_C3_REAL") == "1" {
		key := os.Getenv("AIGO_REAL_QWEN_KEY")
		if key == "" {
			t.Fatal("real key unavailable")
		}
		http.DefaultTransport = transport
		defer func() { http.DefaultTransport = transport.inner }()
		cfg := llm.QwenConfig{APIKey: key, BaseURL: os.Getenv("AIGO_REAL_QWEN_BASE"), Model: "qwen3.5-flash", HTTPTimeout: 150 * time.Second}
		makeClient := func(purpose llm.ConfigPurpose) *llm.DynamicQwenClient {
			c := llm.NewDynamicQwenClient(nil, purpose, cfg)
			c.SetCallRecorder(store)
			return c
		}
		gen, check = makeClient(llm.PurposeGeneration), makeClient(llm.PurposeAICheck)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker := aicheck.NewService(check, store, store, store, "c3-browser")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(gen), evaluator.NewService(), s.reviewSvc, nil, store)
	s.pipe.SetDraftChecker(checker)
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(gen), store, store, "c3-browser", batch.LocalConfig{Context: ctx, Pipeline: true, Checker: checker, Concurrency: 2, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	address := os.Getenv("AIGO_BROWSER_C3_ADDR")
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("C3 harness requires loopback")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(h)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	marker := os.Getenv("AIGO_C3_MARKER")
	raw, _ := json.Marshal(map[string]string{"version_id": point.VersionID, "address": address})
	if err = os.WriteFile(marker, raw, 0600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		var calls, input, output, total int
		err := store.DB().QueryRow(`SELECT count(*),coalesce(sum((record->>'input_tokens')::integer),0),coalesce(sum((record->>'output_tokens')::integer),0),coalesce(sum((record->>'total_tokens')::integer),0) FROM model_call_records`).Scan(&calls, &input, &output, &total)
		if err != nil {
			t.Error(err)
		}
		ledger, _ := json.Marshal(map[string]int64{"http": min(transport.calls.Load(), 6), "recorded": int64(calls), "input": int64(input), "output": int64(output), "total": int64(total)})
		_ = os.WriteFile(marker+".ledger.json", ledger, 0600)
		t.Logf("C3_BROWSER_MODEL %s", ledger)
	}()
	stop := os.Getenv("AIGO_C3_STOP")
	for {
		if _, err := os.Stat(stop); err == nil {
			return
		}
		select {
		case <-t.Context().Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
