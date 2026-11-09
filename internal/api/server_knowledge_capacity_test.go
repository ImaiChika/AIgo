//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/storage"
	"aigo/internal/storage/postgres"
	"aigo/internal/workflowtrace"
	"github.com/lib/pq"
)

type knowledgeCapacityModel struct {
	base         workflowAPIClient
	mu           sync.Mutex
	active, peak map[string]int
	prompts      map[string]string
	checkGate    chan struct{}
	checkStarted chan struct{}
}

func (c *knowledgeCapacityModel) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	op := llm.CallOperation(ctx)
	kind := "single"
	if strings.HasPrefix(op, "batch:") {
		kind = "batch"
	}
	if strings.HasSuffix(op, ":first_check") {
		kind = "check"
	}
	c.mu.Lock()
	c.active[kind]++
	c.peak[kind] = max(c.peak[kind], c.active[kind])
	if kind != "check" {
		c.prompts[op] = m[len(m)-1].Content
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.active[kind]--; c.mu.Unlock() }()
	if kind == "check" {
		select {
		case c.checkStarted <- struct{}{}:
		default:
		}
		select {
		case <-c.checkGate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	delay := 80 * time.Millisecond
	if kind == "check" {
		delay = 40 * time.Millisecond
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return c.base.Complete(ctx, m, o)
}

type knowledgeCapacityStore struct {
	*postgres.Store
	model   *knowledgeCapacityModel
	overlap atomic.Bool
}

func (s *knowledgeCapacityStore) ResolveKnowledgeCardPoints(ctx context.Context, p []domain.KnowledgePoint) ([]domain.KnowledgePoint, error) {
	s.model.mu.Lock()
	overlap := s.model.active["check"] > 0 && (s.model.active["single"]+s.model.active["batch"] > 0)
	s.model.mu.Unlock()
	if overlap {
		s.overlap.Store(true)
	}
	return s.Store.ResolveKnowledgeCardPoints(ctx, p)
}

// End-to-end owned HTTP/PG capacity experiment: 50 single tasks and 50 batches
// of two, all with selected owner-private cards. No real provider is contacted.
func TestEnterpriseHundredAccountKnowledgePipeline(t *testing.T) {
	root := os.Getenv("AIGO_ENTERPRISE_ROOT")
	if root == "" {
		t.Skip("owned enterprise capacity fixture required")
	}
	// Reuse the same explicit resource/DSN validation as the read capacity test.
	validateEnterpriseFixture(t, root)
	s, store, _, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model := &knowledgeCapacityModel{active: map[string]int{}, peak: map[string]int{}, prompts: map[string]string{}, checkGate: make(chan struct{}), checkStarted: make(chan struct{}, 2)}
	checker := aicheck.NewService(model, store, store, store, "controlled-knowledge-capacity")
	pipe := pipeline.New(generator.NewService(model), nil, s.reviewSvc, nil, store)
	pipe.SetDraftChecker(checker)
	q, _, err := store.GetGenerationQuota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := storage.NewGenerationConcurrencyRuntime(q)
	if err != nil {
		t.Fatal(err)
	}
	pipe.GenerationLimiter = runtime.Generation
	checker.WorkerLimiter = runtime.AICheck
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(model), store, store, "controlled-knowledge-capacity", batch.LocalConfig{Context: ctx, Limiter: runtime.Batch, Concurrency: 2, Checker: checker, Pipeline: true})
	defer ex.Close()
	s = NewServer(pipe, s.kpSvc, s.reviewSvc, s.auditSvc, store, s.authSvc, ex, checker, nil, nil, false, false)
	s.SetGenerationConcurrencyRuntime(runtime)
	probe := &knowledgeCapacityStore{Store: store, model: model}
	s.knowledgeBaseSvc = &knowledgebase.Service{Store: probe}
	if value := os.Getenv("AIGO_ENTERPRISE_HTTP_MAX"); value != "" {
		max, err := strconv.Atoi(value)
		if err != nil || max < 64 || max > 256 {
			t.Fatal("invalid isolated HTTP profile")
		}
		s.SetRequestLimits(RequestLimitConfig{MaxInFlight: max, ExpensiveMaxInFlight: 8, HeavyMaxInFlight: 2, RetryAfterSeconds: 1})
	}
	h := s.Handler()
	server := httptest.NewServer(h)
	defer server.Close()
	httpClient := &http.Client{Timeout: 15 * time.Second}
	var busy atomic.Int64
	call := func(token, method, path string, body any) ([]byte, error) {
		raw, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		for attempt := 0; attempt < 35; attempt++ {
			req, e := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
			if e != nil {
				return nil, e
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, e := httpClient.Do(req)
			if e != nil {
				return nil, e
			}
			data, e := io.ReadAll(resp.Body)
			resp.Body.Close()
			if e != nil {
				return nil, e
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return data, nil
			}
			if resp.StatusCode == 503 && resp.Header.Get("X-AIgo-Overload-Class") != "" {
				busy.Add(1)
				select {
				case <-time.After(time.Second):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
			return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		return nil, fmt.Errorf("overload retries exhausted")
	}
	must := func(token, method, path string, body any) []byte {
		t.Helper()
		data, e := call(token, method, path, body)
		if e != nil {
			t.Fatal(e)
		}
		return data
	}
	tokens := make([]string, 100)
	owners := make([]string, 100)
	cards := make([]domain.PrivateKnowledgeCard, 100)
	jobs := make([]string, 100)
	for i := range tokens {
		name := fmt.Sprintf("knowledge-capacity-%03d", i)
		must(admin, "POST", "/api/users", map[string]any{"username": name, "password": "owned-knowledge-capacity-password", "permissions": []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermBatchRun, domain.PermKBView, domain.PermKBUse, domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse}})
		tokens[i] = loginForAuthTest(t, h, name, "owned-knowledge-capacity-password", fmt.Sprintf("198.18.2.%d", i+1))
		u, e := s.authSvc.GetUserByUsername(name)
		if e != nil {
			t.Fatal(e)
		}
		owners[i] = u.ID
		var saved struct {
			Cards []domain.PrivateKnowledgeCard `json:"cards"`
		}
		data := must(tokens[i], "POST", "/api/private-knowledge", domain.KnowledgeCardSaveRequest{Key: fmt.Sprintf("create-%d", i), Cards: []domain.KnowledgeCard{{Kind: "custom", Status: "active", Title: name, KnowledgePoints: []domain.KnowledgePoint{point}, Note: "PRIVATE_CAPACITY_MAINTENANCE", Content: domain.KnowledgeContent{CustomText: fmt.Sprintf("OWNER_CAPACITY_%03d_V1", i)}}}})
		if json.Unmarshal(data, &saved) != nil || len(saved.Cards) != 1 {
			t.Fatal("missing private card")
		}
		cards[i] = saved.Cards[0]
		must(tokens[i], "PUT", "/api/knowledge-selections", map[string]any{"scope": "private", "id": cards[i].ID, "selected": true})
	}
	checker.StartWorkers(ctx, 2)
	pipe.StartGenerationWorkers(ctx, 2)
	defer func() {
		cancel()
		select {
		case <-model.checkGate:
		default:
			close(model.checkGate)
		}
	}()
	latencies := make([]float64, 100)
	submit := func(i int) error {
		begin := time.Now()
		var data []byte
		var e error
		if i < 50 {
			jobs[i] = fmt.Sprintf("knowledge-capacity-run-%03d", i)
			data, e = call(tokens[i], "POST", "/api/questions/generate", generationPayload(jobs[i], point, 1))
		} else {
			data, e = call(tokens[i], "POST", "/api/batch/submit", map[string]any{"request_key": fmt.Sprintf("knowledge-capacity-batch-%03d", i), "workflow_mode": "knowledge", "version_id": point.VersionID, "knowledge_point_ids": []string{point.ID}, "mode": "random_total", "total_count": 2})
			if e == nil {
				var job struct {
					ID string `json:"job_id"`
				}
				e = json.Unmarshal(data, &job)
				jobs[i] = job.ID
				if jobs[i] == "" {
					e = fmt.Errorf("missing batch ID %s", data)
				}
			}
		}
		latencies[i] = float64(time.Since(begin).Microseconds()) / 1000
		return e
	}
	started := time.Now()
	before := store.DB().Stats()
	// Prime checks, then overlap the other 98 accounts with an active pipeline.
	for i := range 2 {
		if e := submit(i); e != nil {
			t.Fatal(e)
		}
	}
	for range 2 {
		select {
		case <-model.checkStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("first checks did not start")
		}
	}
	primeMS := time.Since(started).Milliseconds()
	burstStart := time.Now()
	var wg sync.WaitGroup
	errors := make(chan error, 98)
	for i := 2; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if e := submit(i); e != nil {
				errors <- e
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		t.Error(e)
	}
	if t.Failed() {
		t.FailNow()
	}
	submitMS := time.Since(started).Milliseconds()
	burstMS := time.Since(burstStart).Milliseconds()
	for i := range 10 {
		card := cards[i].KnowledgeCard
		card.Content.CustomText = fmt.Sprintf("OWNER_CAPACITY_%03d_V2", i)
		must(tokens[i], "POST", "/api/private-knowledge", domain.KnowledgeCardSaveRequest{Key: fmt.Sprintf("revise-%d", i), Cards: []domain.KnowledgeCard{card}})
	}
	close(model.checkGate)
	deadline := time.Now().Add(40 * time.Second)
	var passed int
	for time.Now().Before(deadline) {
		if e := store.DB().QueryRow(`SELECT count(*) FROM questions WHERE owner_id=ANY($1) AND status='ai_reviewed'`, pq.Array(owners)).Scan(&passed); e != nil {
			t.Fatal(e)
		}
		if passed == 150 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if passed != 150 {
		t.Fatal("pipeline incomplete", passed)
	}
	for i, owner := range owners {
		// Inspect frozen private evidence and original first-check attempts via SQL.
		rows, e := store.DB().Query(`SELECT id FROM questions WHERE owner_id=$1`, owner)
		if e != nil {
			t.Fatal(e)
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				t.Fatal(e)
			}
			ids = append(ids, id)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
		want := 1
		if i >= 50 {
			want = 2
		}
		if len(ids) != want {
			t.Fatal("wrong owner question count", i, len(ids))
		}
		for _, id := range ids {
			frozen, e := store.GetQuestionGenerationContext(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(frozen)
			text := string(raw)
			if !strings.Contains(text, fmt.Sprintf("OWNER_CAPACITY_%03d_V1", i)) || strings.Contains(text, "_V2") {
				t.Fatal("frozen version lost", i)
			}
			if strings.Count(text, "OWNER_CAPACITY_") != 1 {
				t.Fatal("foreign private material", i)
			}
		}
	}
	prepareMS := []int64{}
	traces, e := store.DB().Query(`SELECT record FROM workflow_execution_records WHERE phase='submission'`)
	if e != nil {
		t.Fatal(e)
	}
	for traces.Next() {
		var raw []byte
		if e = traces.Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var record workflowtrace.Record
		if e = json.Unmarshal(raw, &record); e != nil {
			t.Fatal(e)
		}
		for _, stage := range record.Stages {
			if stage.Name == "knowledge_prepare" && stage.Status == "prepared" {
				prepareMS = append(prepareMS, stage.DurationMS)
			}
		}
	}
	if e = traces.Err(); e != nil {
		t.Fatal(e)
	}
	traces.Close()
	sort.Slice(prepareMS, func(i, j int) bool { return prepareMS[i] < prepareMS[j] })
	if len(prepareMS) != 100 {
		t.Fatal("missing preparation traces", len(prepareMS))
	}
	g, c := model.base.counts()
	if g != 150 || c != 150 {
		t.Fatal("duplicate or missing model calls", g, c)
	}
	if !probe.overlap.Load() {
		t.Fatal("knowledge preparation never overlapped generation and checks")
	}
	model.mu.Lock()
	peaks := map[string]int{}
	for k, v := range model.peak {
		peaks[k] = v
	}
	for _, p := range model.prompts {
		if strings.Contains(p, "PRIVATE_CAPACITY_MAINTENANCE") {
			t.Error("private note leaked to model")
		}
	}
	model.mu.Unlock()
	if peaks["single"] != 2 || peaks["batch"] != 2 || peaks["check"] != 2 {
		t.Fatal("pipeline failed to use independent pools", peaks)
	}
	sort.Float64s(latencies)
	after := store.DB().Stats()
	result := map[string]any{"prepare_p50_ms": prepareMS[49], "prepare_p95_ms": prepareMS[94], "prepare_max_ms": prepareMS[99], "accounts": 100, "single_tasks": 50, "batches": 50, "passed": passed, "generation_calls": g, "check_calls": c, "peak": peaks, "three_stage_overlap": true, "submit_p50_ms": latencies[49], "submit_p95_ms": latencies[94], "submit_max_ms": latencies[99], "submit_total_ms": submitMS, "prime_ms": primeMS, "burst_ms": burstMS, "all_available_ms": time.Since(started).Milliseconds(), "pool_wait_count": after.WaitCount - before.WaitCount, "pool_wait_ms": (after.WaitDuration - before.WaitDuration).Milliseconds(), "http_busy": busy.Load(), "provider_http": 0}
	raw, _ := json.MarshalIndent(result, "", "  ")
	label := os.Getenv("AIGO_ENTERPRISE_LABEL")
	if label == "" {
		label = "candidate"
	}
	if e := os.WriteFile(filepath.Join(root, "knowledge-"+label+os.Getenv("AIGO_ENTERPRISE_HTTP_MAX")+".json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log(string(raw))
}
