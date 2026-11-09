//go:build integration

package aicheck

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aigo/internal/config"
	"aigo/internal/llm"
)

// Real wall clock and a real HTTP connection: no manually shortened worker
// budget. Both inherited and explicit independent settings must cancel at 15s.
func TestConfiguredFifteenSecondBudgetCancelsRealSlowHTTP(t *testing.T) {
	t.Setenv("AIGO_ENV_FILE", "-")
	t.Setenv("QWEN_DEPLOYMENT", "local")
	t.Setenv("QWEN_MODEL", "controlled-checker")
	t.Setenv("AIGO_AICHECK_TIMEOUT", "15s")
	for _, tc := range []struct {
		name          string
		independent   bool
		providerDelay time.Duration
	}{
		{"shared_settings_slow_20s", false, 20 * time.Second}, {"independent_settings_slow_60s", true, 60 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cancelled := make(chan struct{}, 1)
			lateResponse := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-r.Context().Done():
					cancelled <- struct{}{}
				case <-time.After(tc.providerDelay):
					lateResponse <- struct{}{}
					w.WriteHeader(404)
					fmt.Fprint(w, "controlled late error")
				}
			}))
			defer server.Close()
			t.Setenv("QWEN_BASE_URL", server.URL)
			t.Setenv("AIGO_AICHECK_BASE_URL", "")
			t.Setenv("AIGO_AICHECK_MODEL", "")
			t.Setenv("AIGO_AICHECK_DEPLOYMENT", "")
			t.Setenv("AIGO_AICHECK_API_KEY", "")
			if tc.independent {
				t.Setenv("AIGO_AICHECK_BASE_URL", server.URL)
				t.Setenv("AIGO_AICHECK_MODEL", "independent-controlled-checker")
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AICheck.Timeout != 15*time.Second || cfg.AICheck.Client.HTTPTimeout != 45*time.Second || cfg.AICheck.Independent != tc.independent {
				t.Fatal("effective settings differ from requested settings", cfg.AICheck.Timeout, cfg.AICheck.Client.HTTPTimeout, cfg.AICheck.Independent)
			}
			svc, questions, _, tasks := newTestService(llm.NewQwenClient(cfg.AICheck.Client))
			svc.CheckTimeout = cfg.AICheck.Timeout
			svc.MaxAttempts = 1
			if err := questions.SaveQuestion(t.Context(), testDraft("fifteen-second-http", 1)); err != nil {
				t.Fatal(err)
			}
			svc.CheckAsync("fifteen-second-http")
			task, err := tasks.ClaimNextCheckTask(t.Context(), svc.leaseDuration())
			if err != nil || task == nil {
				t.Fatal(err)
			}
			start := time.Now()
			svc.executeTask(t.Context(), task)
			elapsed := time.Since(start)
			latest, err := tasks.LatestCheckTasksByQuestionIDs(t.Context(), []string{task.QuestionID})
			if err != nil {
				t.Fatal(err)
			}
			if elapsed < 14500*time.Millisecond || elapsed > 17*time.Second || !strings.Contains(latest[task.QuestionID].LastError, "15s") {
				t.Fatal("15s task limit not applied", elapsed, latest)
			}
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream request not cancelled")
			}
			select {
			case <-lateResponse:
				t.Fatal("waited for the provider's late response")
			default:
			}
			t.Logf("effective task=15s HTTP=45s independent=%v elapsed=%s provider_delay=%s", tc.independent, elapsed, tc.providerDelay)
		})
	}
}
