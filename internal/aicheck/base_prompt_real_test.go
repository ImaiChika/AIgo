//go:build integration

package aicheck

import (
	"aigo/internal/domain"
	"aigo/internal/llm"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

type baseCheckRecorder struct {
	mu      sync.Mutex
	records []llm.ModelCallRecord
}

func (r *baseCheckRecorder) SaveModelCall(_ context.Context, record llm.ModelCallRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

type baseCheckTools struct{}

func (baseCheckTools) ResolveNetworkTools(context.Context) (llm.NetworkTools, error) {
	return llm.NetworkTools{WebSearchEnabled: true, WebExtractorEnabled: true}, nil
}

// Offline checker comparison: fresh memory fixtures per policy, never a second
// check of a production question and never an import into any business bank.
func TestRealBaseCheckerReferenceComparison(t *testing.T) {
	if os.Getenv("AIGO_RUN_BASE_CHECK_COMPARISON") != "1" {
		t.Skip("explicit paid checker comparison opt-in required")
	}
	key := os.Getenv("AIGO_REAL_QWEN_KEY")
	if key == "" {
		t.Fatal("explicit real key required")
	}
	raw, err := os.ReadFile(os.Getenv("AIGO_BASE_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			ID       string            `json:"id"`
			Expected bool              `json:"expected_usable"`
			Question domain.A2Question `json:"question"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || len(fixture.Cases) < 1 || len(fixture.Cases) > 16 || (os.Getenv("AIGO_BASE_CHECK_ALLOW_CASE_SET") != "1" && len(fixture.Cases) != 7) {
		t.Fatal("invalid explicitly selected checker cases", err)
	}
	if only := os.Getenv("AIGO_BASE_CHECK_CASE"); only != "" {
		filtered := fixture.Cases[:0]
		for _, sample := range fixture.Cases {
			if sample.ID == only {
				filtered = append(filtered, sample)
			}
		}
		fixture.Cases = filtered
		if len(filtered) != 1 {
			t.Fatal("unknown case filter")
		}
	}
	formats := []string{domain.A2FormatContextual, domain.A2FormatFocused}
	if policy := os.Getenv("AIGO_BASE_CHECK_POLICY"); policy != "" {
		if policy != domain.A2FormatSecondWave && !domain.IsContextualA2Format(policy) {
			t.Fatal("unknown policy filter")
		}
		formats = []string{policy}
	}
	recorder := &baseCheckRecorder{}
	client := llm.NewDynamicQwenClient(nil, llm.PurposeAICheck, llm.QwenConfig{APIKey: key, BaseURL: os.Getenv("AIGO_REAL_QWEN_BASE"), Model: "qwen3.5-flash", HTTPTimeout: 150 * time.Second})
	client.SetNetworkToolsResolver(baseCheckTools{})
	client.SetCallRecorder(recorder)
	type outcome struct {
		Case     string                 `json:"case"`
		Policy   string                 `json:"policy"`
		Expected bool                   `json:"simulated_expected_usable"`
		MS       int64                  `json:"ms"`
		Result   *domain.AIReviewResult `json:"result,omitempty"`
		Error    string                 `json:"error,omitempty"`
	}
	results := []outcome{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, 2)
	for _, sample := range fixture.Cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			for _, format := range formats {
				svc, questions, _, _ := newTestService(client)
				q := sample.Question
				q.Options = append([]domain.Option(nil), sample.Question.Options...)
				q.ID = "sim-base-" + sample.ID + "-" + format
				q.FormatVersion = format
				q.NormalizeGeneratedA2()
				ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
				if e := questions.SaveQuestion(ctx, q); e != nil {
					cancel()
					t.Error(e)
					continue
				}
				started := time.Now()
				result, e := svc.CheckQuestion(ctx, q.ID)
				cancel()
				item := outcome{Case: sample.ID, Policy: format, Expected: sample.Expected, MS: time.Since(started).Milliseconds(), Result: result}
				if e != nil {
					item.Error = e.Error()
				}
				mu.Lock()
				results = append(results, item)
				mu.Unlock()
				if e != nil {
					t.Logf("BASE_CHECK case=%s policy=%s error=call_or_protocol", sample.ID, format)
				} else {
					t.Logf("BASE_CHECK case=%s policy=%s ms=%d expected_usable=%t verdict=%s", sample.ID, format, item.MS, sample.Expected, result.Verdict)
				}
			}
		}()
	}
	wg.Wait()
	input, output, total, search, fetch := 0, 0, 0, 0, 0
	for _, record := range recorder.records {
		input += record.InputTokens
		output += record.OutputTokens
		total += record.TotalTokens
		search += record.SearchCalls
		fetch += record.ExtractorCalls
	}
	t.Logf("BASE_CHECK_TOTAL http=%d input=%d output=%d total=%d search=%d fetch=%d", len(recorder.records), input, output, total, search, fetch)
	if path := os.Getenv("AIGO_BASE_CHECK_REPORT"); path != "" {
		raw, _ := json.Marshal(map[string]any{"outcomes": results, "calls": recorder.records})
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range results {
		if item.Error != "" {
			t.Errorf("checker comparison incomplete: %s %s", item.Case, item.Policy)
		}
		if domain.IsContextualA2Format(item.Policy) && item.Result != nil && (item.Result.Verdict == "pass") != item.Expected {
			t.Errorf("checker differs from simulated expected usability: %s expected=%t verdict=%s", item.Case, item.Expected, item.Result.Verdict)
		}
	}
	if len(results) != len(fixture.Cases)*len(formats) {
		t.Fatal("missing paired comparison results")
	}
}
