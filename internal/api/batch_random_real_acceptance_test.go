//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
)

type boundedBatchRealTransport struct {
	inner http.RoundTripper
	calls atomic.Int64
}

func (b *boundedBatchRealTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if b.calls.Add(1) > 8 {
		return nil, fmt.Errorf("real C2 audit request budget exhausted")
	}
	return b.inner.RoundTrip(r)
}

// A separate, explicitly opted-in process only: two random clinical units, at
// most eight provider HTTP requests including ordinary automatic retries.
func TestRealRandomBatchAcceptance(t *testing.T) {
	if os.Getenv("AIGO_RUN_REAL_C2_ACCEPTANCE") != "1" {
		t.Skip("explicit real-model opt-in required")
	}
	key := os.Getenv("AIGO_REAL_QWEN_KEY")
	if key == "" {
		t.Fatal("real-model key unavailable")
	}
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	transport := &boundedBatchRealTransport{inner: http.DefaultTransport}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = transport.inner }()
	defer func() {
		var calls, generation, checks, input, output, total, search, fetch int
		err := store.DB().QueryRow(`SELECT count(*),count(*) FILTER(WHERE purpose='generation'),count(*) FILTER(WHERE purpose='ai_check'),coalesce(sum((record->>'input_tokens')::integer),0),coalesce(sum((record->>'output_tokens')::integer),0),coalesce(sum((record->>'total_tokens')::integer),0),coalesce(sum((record->>'search_calls')::integer),0),coalesce(sum((record->>'extractor_calls')::integer),0) FROM model_call_records`).Scan(&calls, &generation, &checks, &input, &output, &total, &search, &fetch)
		if err != nil {
			t.Error(err)
		}
		t.Logf("REAL_C2 http_attempts=%d recorded=%d generation=%d checks=%d input=%d output=%d total=%d search=%d fetch=%d", min(transport.calls.Load(), 8), calls, generation, checks, input, output, total, search, fetch)
	}()
	var ids []string
	for i, topic := range []string{"社区获得性肺炎的诊断", "消化性溃疡的诊断"} {
		subject := "呼吸系统"
		if i == 1 {
			subject = "消化系统"
		}
		point, err := s.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: p.VersionID, Category: "临床综合", Subject: subject, Topic: topic, OutlineCode: fmt.Sprintf("real.c2.%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, point.ID)
	}
	cfg := llm.QwenConfig{APIKey: key, BaseURL: os.Getenv("AIGO_REAL_QWEN_BASE"), Model: "qwen3.5-flash", HTTPTimeout: 150 * time.Second}
	makeClient := func(purpose llm.ConfigPurpose) *llm.DynamicQwenClient {
		c := llm.NewDynamicQwenClient(nil, purpose, cfg)
		c.SetCallRecorder(store)
		return c
	}
	checker := aicheck.NewService(makeClient(llm.PurposeAICheck), store, store, store, "qwen3.5-flash")
	s.aiCheckSvc = checker
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	checker.StartWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(makeClient(llm.PurposeGeneration)), store, store, "qwen3.5-flash", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	payload := map[string]any{"workflow_mode": "direct", "version_id": p.VersionID, "knowledge_point_ids": ids, "mode": "random_total", "total_count": 2, "request_key": "real-c2-fixed-two"}
	code, body, _ := batchSubmitJSON(t, h, token, payload)
	if code != 200 {
		t.Fatal("real batch submission failed", code)
	}
	jobID := body["job_id"].(string)
	record, err := store.GetBatchJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	frozen := record.PlanJSON
	code, body, _ = batchSubmitJSON(t, h, token, payload)
	if code != 200 || body["job_id"] != jobID || body["replayed"] != true {
		t.Fatal("real batch replay mismatch")
	}
	started := time.Now()
	for ctx.Err() == nil {
		record, err = store.GetBatchJob(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if record.PlanJSON != frozen || record.TotalCount != 2 {
			t.Fatal("frozen real plan changed")
		}
		var output struct {
			Items []batch.ImportItem `json:"items"`
		}
		if err = json.Unmarshal([]byte(record.OutputJSON), &output); err != nil {
			t.Fatal(err)
		}
		var questionIDs []string
		for _, item := range output.Items {
			if item.QuestionID != "" {
				questionIDs = append(questionIDs, item.QuestionID)
			}
		}
		if record.Status == "completed" || record.Status == "failed" {
			_, counts, e := checker.ProgressByQuestionIDs(ctx, questionIDs)
			if e != nil {
				t.Fatal(e)
			}
			done := counts["passed"] + counts["discarded"] + counts["exhausted"]
			if done+record.Failed == 2 {
				t.Logf("REAL_C2 units=2 candidates=2 elapsed_ms=%d generated=%d failed=%d passed=%d discarded=%d exhausted=%d", time.Since(started).Milliseconds(), record.Completed, record.Failed, counts["passed"], counts["discarded"], counts["exhausted"])
				if record.Completed != 2 {
					t.Fatal("real sample has generation failures; do not reroll")
				}
				return
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("real sample timed out; no extra sample authorized")
}
