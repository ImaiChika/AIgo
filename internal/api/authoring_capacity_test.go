//go:build integration

package api

import (
	"aigo/internal/domain"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Explicitly owned local fixtures only. These are read-capacity measurements,
// not model latency tests or a production user-count guarantee.
func TestConcurrentAuthoringReadCapacity(t *testing.T) {
	root := os.Getenv("AIGO_C6_PERF_ROOT")
	if root == "" {
		t.Skip("opt-in owned capacity fixture")
	}
	var resource struct {
		Root, Database string
		Ports          []int
	}
	raw, e := os.ReadFile(filepath.Join(root, "resources.json"))
	if e != nil || json.Unmarshal(raw, &resource) != nil || resource.Root != root || !strings.HasPrefix(resource.Database, "aigo_c6_") {
		t.Fatal("owned capacity resource manifest required")
	}
	dsn, e := url.Parse(os.Getenv("AIGO_TEST_POSTGRES_DSN"))
	if e != nil || dsn.Hostname() != "127.0.0.1" || strings.TrimPrefix(dsn.Path, "/") != resource.Database || len(resource.Ports) < 1 || dsn.Port() != fmt.Sprint(resource.Ports[0]) {
		t.Fatal("capacity test requires its isolated loopback database")
	}
	s, store, _, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	h := s.Handler()
	type account struct {
		ID, Token string
		Want      map[string]int
	}
	users := []account{}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("capacity-%02d", i)
		pw := "owned-capacity-password"
		response := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": pw, "permissions": []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermQuestionConfirm, domain.PermKBView, domain.PermKBUse, domain.PermExperienceView, domain.PermExperienceUse}})
		if response.Code != 201 {
			t.Fatal(response.Code, response.Body)
		}
		u, e := s.authSvc.GetUserByUsername(name)
		if e != nil {
			t.Fatal(e)
		}
		token := loginForAuthTest(t, h, name, pw, fmt.Sprintf("198.51.100.%d", i+1))
		for j := 0; j < i+3; j++ {
			seedCustomerBrowserQuestion(t, s, u.ID, name, point, fmt.Sprintf("capacity-q-%02d-%02d", i, j), strings.Repeat("仅合成容量夹具。", 150))
		}
		old := serveGenerateJSON(t, h, "GET", "/api/my/summary", token, nil)
		fresh := serveGenerateJSON(t, h, "GET", "/api/my/summary?view=authoring", token, nil)
		if old.Code != 200 || fresh.Code != 200 {
			t.Fatal(old.Code, old.Body, fresh.Code, fresh.Body)
		}
		var prior, current map[string]int
		json.Unmarshal(old.Body.Bytes(), &prior)
		json.Unmarshal(fresh.Body.Bytes(), &current)
		for k, v := range current {
			if prior[k] != v {
				t.Fatalf("light summary changed %s: %d/%d", k, prior[k], v)
			}
		}
		if current["generated_count"] != i+3 {
			t.Fatalf("owner counts crossed accounts: %+v", current)
		}
		users = append(users, account{u.ID, token, current})
	}
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()
	client := httpServer.Client()
	client.Timeout = 20 * time.Second
	defer client.CloseIdleConnections()
	prepare, _ := json.Marshal(map[string]any{"knowledge_points": []domain.KnowledgePoint{point}})
	for _, tc := range []struct {
		name                 string
		concurrent, requests int
		mode                 string
		same                 bool
	}{
		{"baseline_summary", 30, 180, "baseline", false}, {"authoring_summary", 30, 180, "authoring", false}, {"mixed_accounts", 30, 180, "mixed", false}, {"same_account_windows", 10, 60, "mixed", true},
		{"five_poll_waves", 30, 150, "waves", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := store.DB().Stats()
			jobs := make(chan int)
			latency := make(chan time.Duration, tc.requests)
			failures := make(chan error, tc.requests)
			var wg sync.WaitGroup
			started := time.Now()
			for worker := 0; worker < tc.concurrent; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for n := range jobs {
						u := users[n%len(users)]
						if tc.same {
							u = users[0]
						}
						path := "/api/my/summary?view=authoring"
						method := "GET"
						var body io.Reader
						if tc.mode == "baseline" {
							path = "/api/my/summary"
						} else if tc.mode == "mixed" || tc.mode == "waves" {
							switch n % 3 {
							case 1:
								path = "/api/auth/me"
							case 2:
								path = "/api/knowledge-base/prepare"
								method = "POST"
								body = strings.NewReader(string(prepare))
							}
						}
						req, _ := http.NewRequestWithContext(t.Context(), method, httpServer.URL+path, body)
						req.Header.Set("Authorization", "Bearer "+u.Token)
						req.Header.Set("Content-Type", "application/json")
						start := time.Now()
						response, e := client.Do(req)
						if e != nil {
							failures <- e
							continue
						}
						data, e := io.ReadAll(response.Body)
						response.Body.Close()
						latency <- time.Since(start)
						if e != nil || response.StatusCode != 200 {
							failures <- fmt.Errorf("%s status=%d error=%v", path, response.StatusCode, e)
							continue
						}
						if strings.Contains(path, "summary") {
							var got map[string]int
							e = json.Unmarshal(data, &got)
							for k, v := range u.Want {
								if got[k] != v {
									e = fmt.Errorf("cross-user summary %s", k)
									break
								}
							}
						} else if strings.HasSuffix(path, "/me") {
							var got struct {
								ID string `json:"id"`
							}
							e = json.Unmarshal(data, &got)
							if got.ID != u.ID {
								e = fmt.Errorf("identity crossed account")
							}
						} else {
							var got struct {
								Bundles []struct {
									ID string `json:"knowledge_point_id"`
								} `json:"bundles"`
							}
							e = json.Unmarshal(data, &got)
							if len(got.Bundles) != 1 || got.Bundles[0].ID != point.ID {
								e = fmt.Errorf("bad frozen preview")
							}
						}
						if e != nil {
							failures <- e
						}
					}
				}()
			}
			for n := 0; n < tc.requests; n++ {
				if tc.mode == "waves" && n > 0 && n%30 == 0 {
					timer := time.NewTimer(5 * time.Second)
					select {
					case <-t.Context().Done():
						timer.Stop()
						close(jobs)
						wg.Wait()
						t.Fatal(t.Context().Err())
					case <-timer.C:
					}
				}
				jobs <- n
			}
			close(jobs)
			wg.Wait()
			close(latency)
			close(failures)
			for e := range failures {
				t.Error(e)
			}
			durations := []time.Duration{}
			for d := range latency {
				durations = append(durations, d)
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			if len(durations) == 0 {
				t.Fatal("no measured requests")
			}
			after := store.DB().Stats()
			accounts := len(users)
			if tc.same {
				accounts = 1
			}
			t.Logf("capacity case=%s accounts=%d concurrent=%d requests=%d p50=%v p95=%v elapsed=%v pool_max=%d wait_count=%d wait_duration=%v model_http=0", tc.name, accounts, tc.concurrent, len(durations), durations[len(durations)/2], durations[(len(durations)-1)*95/100], time.Since(started), after.MaxOpenConnections, after.WaitCount-before.WaitCount, after.WaitDuration-before.WaitDuration)
		})
	}
	t.Run("ten_account_submission_wave", func(t *testing.T) {
		errors := make(chan error, len(users))
		var wg sync.WaitGroup
		started := time.Now()
		for i, u := range users {
			wg.Add(1)
			go func(i int, u account) {
				defer wg.Done()
				body, _ := json.Marshal(map[string]any{"run_id": fmt.Sprintf("capacity-submit-%d", i), "knowledge_point_id": point.ID, "knowledge_points": []domain.KnowledgePoint{point}, "count": 1, "workflow_mode": "knowledge", "apply_personal_experiences": false})
				req, _ := http.NewRequestWithContext(t.Context(), "POST", httpServer.URL+"/api/questions/generate", strings.NewReader(string(body)))
				req.Header.Set("Authorization", "Bearer "+u.Token)
				req.Header.Set("Content-Type", "application/json")
				res, e := client.Do(req)
				if e != nil {
					errors <- e
					return
				}
				data, e := io.ReadAll(res.Body)
				res.Body.Close()
				if e != nil || res.StatusCode != 200 {
					errors <- fmt.Errorf("submission %d status=%d error=%v body=%s", i, res.StatusCode, e, data)
					return
				}
				var result struct {
					Run domain.GenerationRun `json:"run"`
				}
				if e = json.Unmarshal(data, &result); e != nil {
					errors <- e
					return
				}
				saved, e := store.GetGenerationRun(t.Context(), result.Run.ID)
				if e != nil {
					errors <- e
					return
				}
				if saved == nil || saved.OwnerID != u.ID {
					errors <- fmt.Errorf("submission ownership/receipt changed")
				}
			}(i, u)
		}
		wg.Wait()
		close(errors)
		for e := range errors {
			t.Error(e)
		}
		t.Logf("submission wave accounts=10 pool=%d elapsed=%v model_workers_started=false", store.DB().Stats().MaxOpenConnections, time.Since(started))
	})

}
