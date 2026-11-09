//go:build integration

package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"
)

// One API/coordinator instance with 100 independent owners. This measures
// bounded interactive traffic, not 100 concurrent provider requests or HA.
func TestEnterpriseHundredAccountReads(t *testing.T) {
	root := os.Getenv("AIGO_ENTERPRISE_ROOT")
	if root == "" {
		t.Skip("owned enterprise capacity fixture required")
	}
	validateEnterpriseFixture(t, root)
	s, store, _, admin, point, _, model, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	h := s.Handler()
	tokens := []string{}
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("enterprise-%03d", i)
		r := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": "enterprise-isolated-password", "permissions": []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermQuestionConfirm}})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
		token := loginForAuthTest(t, h, name, "enterprise-isolated-password", fmt.Sprintf("198.18.1.%d", i+1))
		u, err := s.authSvc.GetUserByUsername(name)
		if err != nil {
			t.Fatal(err)
		}
		seedCustomerBrowserQuestion(t, s, u.ID, name, point, fmt.Sprintf("enterprise-seed-%03d", i), "患者，男，45岁。仅容量夹具。该患者最可能的诊断是")
		tokens = append(tokens, token)
	}
	if value := os.Getenv("AIGO_ENTERPRISE_HTTP_MAX"); value != "" {
		max, e := strconv.Atoi(value)
		if e != nil || max < 64 || max > 256 {
			t.Fatal("invalid isolated HTTP profile")
		}
		fresh := NewServer(s.pipe, s.kpSvc, s.reviewSvc, s.auditSvc, store, s.authSvc, nil, s.aiCheckSvc, nil, nil, false, false)
		fresh.SetRequestLimits(RequestLimitConfig{MaxInFlight: max, ExpensiveMaxInFlight: 8, HeavyMaxInFlight: 2, RetryAfterSeconds: 1})
		h = fresh.Handler()
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	latencies := []float64{}
	var mu sync.Mutex
	failures := 0
	started := time.Now()
	for wave := 0; wave < 10; wave++ {
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, token := range tokens {
			wg.Add(1)
			go func(token string) {
				defer wg.Done()
				<-start
				begin := time.Now()
				ok := false
				for attempt := 0; attempt < 6; attempt++ {
					request, _ := http.NewRequest("GET", server.URL+"/api/my/summary?view=authoring", nil)
					request.Header.Set("Authorization", "Bearer "+token)
					response, err := client.Do(request)
					if err != nil {
						break
					}
					body, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if response.StatusCode == 200 {
						var data map[string]int
						ok = json.Unmarshal(body, &data) == nil && data["generated_count"] == 1
						break
					}
					if response.StatusCode != 429 && !(response.StatusCode == 503 && response.Header.Get("X-AIgo-Overload-Class") != "") {
						break
					}
					delay, _ := strconv.Atoi(response.Header.Get("Retry-After"))
					if delay < 1 {
						delay = 1
					}
					time.Sleep(time.Duration(delay) * time.Second)
				}
				mu.Lock()
				defer mu.Unlock()
				latencies = append(latencies, float64(time.Since(begin).Microseconds())/1000)
				if !ok {
					failures++
				}
			}(token)
		}
		close(start)
		wg.Wait()
	}
	readElapsed := time.Since(started).Milliseconds()
	// Retain the already-created default HTTP gates (64/8/2).
	var submitWG sync.WaitGroup
	accepted, busy, rejected := 0, 0, 0
	submitStart := time.Now()
	for i, token := range tokens {
		submitWG.Add(1)
		go func(i int, token string) {
			defer submitWG.Done()
			body, _ := json.Marshal(map[string]any{"run_id": fmt.Sprintf("enterprise-burst-%03d", i), "knowledge_point_id": point.ID, "version_id": point.VersionID, "workflow_mode": "direct", "count": 1})
			for attempt := 0; attempt < 6; attempt++ {
				request, _ := http.NewRequest("POST", server.URL+"/api/questions/generate", bytes.NewReader(body))
				request.Header.Set("Authorization", "Bearer "+token)
				request.Header.Set("Content-Type", "application/json")
				response, err := client.Do(request)
				if err != nil {
					mu.Lock()
					rejected++
					mu.Unlock()
					return
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode == 200 {
					mu.Lock()
					accepted++
					mu.Unlock()
					return
				}
				if response.StatusCode != 429 && !(response.StatusCode == 503 && response.Header.Get("X-AIgo-Overload-Class") != "") {
					mu.Lock()
					rejected++
					mu.Unlock()
					return
				}
				mu.Lock()
				busy++
				mu.Unlock()
				delay, _ := strconv.Atoi(response.Header.Get("Retry-After"))
				if delay < 1 {
					delay = 1
				}
				time.Sleep(time.Duration(delay) * time.Second)
			}
			mu.Lock()
			rejected++
			mu.Unlock()
		}(i, token)
	}
	submitWG.Wait()
	var queued int
	if err := store.DB().QueryRow(`SELECT count(*) FROM generation_runs WHERE id LIKE 'enterprise-burst-%'`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if accepted != 100 || queued != 100 || rejected != 0 {
		t.Fatalf("burst accepted=%d queued=%d errors=%d", accepted, queued, rejected)
	}
	if g, c := model.counts(); g != 0 || c != 0 {
		t.Fatal("admission called a model", g, c)
	}
	sort.Float64s(latencies)
	if len(latencies) != 1000 || failures != 0 {
		t.Fatalf("read count=%d errors=%d", len(latencies), failures)
	}
	result := map[string]any{"accounts": 100, "requests": len(latencies), "waves": 10, "errors": failures, "p50_ms": latencies[499], "p95_ms": latencies[949], "max_ms": latencies[999], "total_ms": readElapsed, "submit_ms": time.Since(submitStart).Milliseconds(), "accepted": accepted, "queued": queued, "busy_responses": busy, "rejected": rejected, "http_profile": os.Getenv("AIGO_ENTERPRISE_HTTP_MAX"), "provider_http": 0}
	out, _ := json.MarshalIndent(result, "", "  ")
	label := os.Getenv("AIGO_ENTERPRISE_LABEL")
	if label != "baseline" && label != "candidate" {
		label = "candidate"
	}
	if err := os.WriteFile(filepath.Join(root, "enterprise-"+label+os.Getenv("AIGO_ENTERPRISE_HTTP_MAX")+".json"), out, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(out))
}

func validateEnterpriseFixture(t *testing.T, root string) {
	t.Helper()
	var resource struct {
		Root, Database string
		Ports          []int
	}
	raw, e := os.ReadFile(filepath.Join(root, "resources.json"))
	if e != nil || json.Unmarshal(raw, &resource) != nil || resource.Root != root {
		t.Fatal("invalid owned resources")
	}
	dsn, e := url.Parse(os.Getenv("AIGO_TEST_POSTGRES_DSN"))
	if e != nil || dsn.Hostname() != "127.0.0.1" || strings.TrimPrefix(dsn.Path, "/") != resource.Database || len(resource.Ports) == 0 || dsn.Port() != strconv.Itoa(resource.Ports[0]) {
		t.Fatal("registered isolated DSN required")
	}
}
