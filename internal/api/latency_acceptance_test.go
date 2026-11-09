//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
)

type latencyFaultCall struct {
	Phase      string    `json:"phase"`
	Attempt    int       `json:"attempt"`
	Started    time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
}
type latencyFaultModel struct {
	base   c5FlowClient
	fault  string
	mu     sync.Mutex
	counts map[string]int
	calls  []latencyFaultCall
}

func (c *latencyFaultModel) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	phase := "generation"
	if strings.HasSuffix(llm.CallOperation(ctx), ":first_check") {
		phase = "check"
	}
	c.mu.Lock()
	c.counts[phase]++
	attempt := c.counts[phase]
	c.mu.Unlock()
	started := time.Now()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.calls = append(c.calls, latencyFaultCall{phase, attempt, started, time.Since(started).Milliseconds()})
	}()
	if c.fault == "check_timeout" && phase == "check" {
		<-ctx.Done()
		return "", ctx.Err()
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}
	if phase == "generation" && ((c.fault == "gen_json_once" && attempt == 1) || c.fault == "gen_exhaust") {
		return "[", nil
	}
	if phase == "generation" && c.fault == "gen_network_once" && attempt == 1 {
		return "", fmt.Errorf("controlled provider transport failure")
	}
	if phase == "check" && ((c.fault == "check_once" && attempt == 1) || c.fault == "check_exhaust") {
		return `{"verdict":"invalid-protocol"}`, nil
	}
	if phase == "check" && c.fault == "quality_reject" {
		return `{"verdict":"reject","scores":{"scientific":30,"logic":30,"a2_fit":30,"answer":30},"issues":[{"field":"answer","severity":"error","message":"受控质量淘汰，不是协议故障"}]}`, nil
	}
	return c.base.Complete(ctx, m, o)
}

// This opt-in measurement retains production retry delays and the 120-second
// per-attempt first-check timeout. It never contacts a model provider or changes defaults.
func TestLatencyDefaultFaultTiming(t *testing.T) {
	root := os.Getenv("AIGO_LATENCY_ROOT")
	if root == "" {
		t.Skip("explicit owned latency resource root required")
	}
	raw, err := os.ReadFile(filepath.Join(root, "resources.json"))
	var resources struct {
		Root, Database string
		Ports          []int
	}
	if err != nil || json.Unmarshal(raw, &resources) != nil || resources.Root != root || !strings.HasPrefix(resources.Database, "aigo_c6_latency_") {
		t.Fatal("invalid owned resources")
	}
	dsn, e := url.Parse(os.Getenv("AIGO_TEST_POSTGRES_DSN"))
	if e != nil || dsn.Hostname() != "127.0.0.1" || strings.TrimPrefix(dsn.Path, "/") != resources.Database || len(resources.Ports) == 0 || dsn.Port() != strconv.Itoa(resources.Ports[0]) || dsn.Query().Get("search_path") != "" {
		t.Fatal("explicit registered isolated DSN required")
	}
	results := []any{}
	for _, fault := range []string{"clean", "clean_5", "clean_20", "gen_json_once", "gen_network_once", "check_once", "gen_exhaust", "check_exhaust", "check_timeout", "quality_reject"} {
		t.Run(fault, func(t *testing.T) {
			requested := 1
			if fault == "clean_5" {
				requested = 5
			}
			if fault == "clean_20" {
				requested = 20
			}
			s, store, h, token, point, _, _, cleanup := setupWorkflowAPI(t)
			defer cleanup()
			model := &latencyFaultModel{fault: fault, counts: map[string]int{}}
			checker := aicheck.NewService(model, store, store, store, "controlled-latency")
			s.aiCheckSvc = checker
			s.pipe = pipeline.New(generator.NewService(model), nil, s.reviewSvc, nil, store)
			s.pipe.SetDraftChecker(checker)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checker.StartWorkers(ctx, 2)
			s.pipe.StartGenerationWorkers(ctx, 2)
			id := "latency-fault-" + fault
			payload := generationPayload(id, point, requested)
			payload["workflow_mode"] = "direct"
			started := time.Now()
			response := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, payload)
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body)
			}
			var run *domain.GenerationRun
			var pending, total, archived int
			observationBudget := 190 * time.Second
			if fault == "check_timeout" {
				observationBudget = 450 * time.Second
			}
			for time.Since(started) < observationBudget {
				run, err = store.GetGenerationRun(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.DB().QueryRowContext(t.Context(), `SELECT count(*) FILTER(WHERE status IN ('pending','running')),count(*) FROM ai_check_tasks`).Scan(&pending, &total); err != nil {
					t.Fatal(err)
				}
				if err = store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM ai_check_discards`).Scan(&archived); err != nil {
					t.Fatal(err)
				}
				if run.Status == domain.GenerationRunFailed || (run.Status == domain.GenerationRunSucceeded && total+archived == requested && pending == 0) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if run == nil || run.Status == domain.GenerationRunPending || run.Status == domain.GenerationRunRunning || pending > 0 || run.Status == domain.GenerationRunSucceeded && total+archived != requested {
				t.Fatal("fault did not terminate within observation budget")
			}
			var checks string
			if err = store.DB().QueryRowContext(t.Context(), `SELECT coalesce(json_agg(json_build_object('status',status,'attempts',attempts,'last_error',last_error)),'[]')::text FROM ai_check_tasks`).Scan(&checks); err != nil {
				t.Fatal(err)
			}
			model.mu.Lock()
			counts := map[string]int{}
			for k, v := range model.counts {
				counts[k] = v
			}
			calls := append([]latencyFaultCall(nil), model.calls...)
			model.mu.Unlock()
			if strings.HasPrefix(fault, "clean") && (counts["generation"] != 1 || counts["check"] != requested) {
				t.Fatal(counts)
			}
			if fault == "gen_exhaust" && (counts["generation"] != 3 || counts["check"] != 0) {
				t.Fatal(counts)
			}
			if fault == "check_exhaust" && counts["check"] != 3 {
				t.Fatal(counts)
			}
			if fault == "quality_reject" && counts["check"] != 1 {
				t.Fatal("quality rejection must not regenerate or recheck", counts)
			}
			if fault == "check_timeout" && (time.Since(started) < 400*time.Second || counts["check"] != 3) {
				t.Fatal("production timeout was shortened")
			}
			result := map[string]any{"scenario": fault, "requested": requested, "elapsed_ms": time.Since(started).Milliseconds(), "run_attempts": run.Attempts, "run_status": run.Status, "calls": calls, "counts": counts, "checks": json.RawMessage(checks), "archived": archived, "provider_http": 0}
			results = append(results, result)
			out, _ := json.MarshalIndent(results, "", "  ")
			if err = os.WriteFile(filepath.Join(root, "controlled-default-timings.json"), out, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s %.3fs attempts=%d counts=%v", fault, time.Since(started).Seconds(), run.Attempts, counts)
		})
	}
	for _, fault := range []string{"clean", "clean_10", "clean_100", "gen_json_once", "gen_exhaust", "check_once"} {
		t.Run("batch_"+fault, func(t *testing.T) {
			requested := 2
			actualFault := fault
			if fault == "clean_10" {
				requested = 10
				actualFault = "clean"
			}
			if fault == "clean_100" {
				requested = 100
				actualFault = "clean"
			}
			s, store, h, token, point, _, _, cleanup := setupWorkflowAPI(t)
			defer cleanup()
			model := &latencyFaultModel{fault: actualFault, counts: map[string]int{}}
			checker := aicheck.NewService(model, store, store, store, "controlled-latency")
			s.aiCheckSvc = checker
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checker.StartWorkers(ctx, 2)
			ex := batch.NewLocalExecutorWithConfig(generator.NewService(model), store, store, "controlled-latency", batch.LocalConfig{Context: ctx, Concurrency: 2, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
			defer ex.Close()
			s.batchSvc = ex
			started := time.Now()
			response := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "direct", "version_id": point.VersionID, "knowledge_point_ids": []string{point.ID}, "mode": "random_total", "total_count": requested, "request_key": "latency-batch-" + fault})
			var submitted struct {
				JobID string `json:"job_id"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &submitted) != nil {
				t.Fatal(response.Code, response.Body)
			}
			var job *batch.BatchJob
			var pending int
			for time.Since(started) < 90*time.Second {
				job, err = ex.GetJobStatus(t.Context(), submitted.JobID)
				if err != nil {
					t.Fatal(err)
				}
				if err = store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM ai_check_tasks WHERE status IN ('pending','running')`).Scan(&pending); err != nil {
					t.Fatal(err)
				}
				if job != nil && (job.Status == "completed" || job.Status == "failed") && pending == 0 {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if job == nil || (job.Status != "completed" && job.Status != "failed") || pending > 0 {
				t.Fatal("batch fault did not terminate")
			}
			var first sql.NullFloat64
			if err = store.DB().QueryRowContext(t.Context(), `SELECT EXTRACT(EPOCH FROM min(created_at)) FROM ai_review_results WHERE verdict='pass'`).Scan(&first); err != nil {
				t.Fatal(err)
			}
			model.mu.Lock()
			counts := map[string]int{}
			for k, v := range model.counts {
				counts[k] = v
			}
			calls := append([]latencyFaultCall(nil), model.calls...)
			model.mu.Unlock()
			if actualFault == "clean" && (counts["generation"] != requested || counts["check"] != requested) {
				t.Fatal(counts)
			}
			if fault == "gen_exhaust" && (counts["generation"] != 6 || counts["check"] != 0) {
				t.Fatal(counts)
			}
			result := map[string]any{"scenario": "batch_" + fault, "requested": requested, "elapsed_ms": time.Since(started).Milliseconds(), "completed": job.Completed, "failed": job.Failed, "counts": counts, "calls": calls, "provider_http": 0}
			if first.Valid {
				result["first_available_ms"] = (first.Float64 - float64(started.UnixNano())/1e9) * 1000
			}
			results = append(results, result)
			out, _ := json.MarshalIndent(results, "", "  ")
			if err = os.WriteFile(filepath.Join(root, "controlled-default-timings.json"), out, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("batch_%s %.3fs counts=%v", fault, time.Since(started).Seconds(), counts)
		})
	}

}
