package workflowtrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type memoryRecorder struct {
	mu    sync.Mutex
	rows  []Record
	err   error
	wait  bool
	calls int
}

func (s *memoryRecorder) SaveWorkflowExecution(ctx context.Context, r Record) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	if s.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, r)
	return s.err
}
func TestTraceBoundsPrivacyAndIndependentAttempts(t *testing.T) {
	sink := &memoryRecorder{}
	for attempt := 1; attempt <= 2; attempt++ {
		ctx, e := Start(t.Context(), sink, "generate:owned", "generation", attempt, "knowledge-v3")
		stage := Begin(ctx, "json_parse", -1, "PRIVATE-MODEL-CONTENT")
		if attempt == 1 {
			stage.End("failed", "json_or_count_contract", nil)
		} else {
			stage.End("parsed", "", map[string]string{"private": "PRIVATE-CARD-AND-RULE"})
		}
		LinkModel(ctx, fmt.Sprintf("call-%d", attempt), true)
		if attempt == 2 {
			for i := 0; i < MaxStages+2; i++ {
				Begin(ctx, "a2_validation", i, nil).End("validated", "", nil)
			}
			e.Complete("committed", []string{"q-owned"})
		}
		e.Close()
		e.Close()
	}
	if len(sink.rows) != 2 || sink.rows[0].ID == sink.rows[1].ID || sink.rows[0].Outcome != "failed" || sink.rows[1].Outcome != "committed" || !sink.rows[1].StagesTruncated || len(sink.rows[1].Stages) != MaxStages {
		t.Fatal(sink.rows)
	}
	if len(sink.rows[0].Stages[0].Input.SHA256) != 64 || sink.rows[1].Attempt != 2 || sink.rows[1].ModelCalls[0].ID != "call-2" {
		t.Fatal(sink.rows)
	}
	raw, _ := json.Marshal(sink.rows)
	for _, secret := range []string{"PRIVATE-MODEL-CONTENT", "PRIVATE-CARD-AND-RULE"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("trace leaked raw contents")
		}
	}
}
func TestTracePersistenceCannotReplaceBusinessOutcome(t *testing.T) {
	for _, name := range []string{"failure", "canceled", "bounded_timeout", "near_deadline", "disabled"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := &memoryRecorder{err: errors.New("PRIVATE-STORAGE-DETAIL"), wait: name == "bounded_timeout"}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var store any = sink
				if name == "disabled" {
					store = struct{}{}
				}
				if name == "near_deadline" {
					var done context.CancelFunc
					ctx, done = context.WithTimeout(ctx, 100*time.Millisecond)
					defer done()
				}
				ctx, e := Start(ctx, store, "generate:owned", "generation", 1, "")
				Begin(ctx, "handoff", -1, nil).End("committed", "", nil)
				e.Complete("committed", []string{"q"})
				if name == "canceled" {
					cancel()
				}
				start := time.Now()
				e.Close()
				switch name {
				case "disabled", "near_deadline":
					if sink.calls != 0 {
						t.Fatal("unexpected diagnostic write")
					}
				case "bounded_timeout":
					if time.Since(start) != 200*time.Millisecond {
						t.Fatal("unbounded diagnostic persistence")
					}
				default:
					if len(sink.rows) != 1 || sink.rows[0].Outcome != "committed" {
						t.Fatal(sink.rows)
					}
				}
			})
		})
	}
}
func TestTraceConcurrentScopesRemainSeparate(t *testing.T) {
	sink := &memoryRecorder{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			operation := fmt.Sprintf("batch:owned:%d", i)
			ctx, e := Start(t.Context(), sink, operation, "generation", 1, "")
			Begin(ctx, "render", 0, nil).End("rendered", "", nil)
			e.Complete("committed", []string{fmt.Sprintf("q-%d", i)})
			e.Close()
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, r := range sink.rows {
		if seen[r.Operation] || r.QuestionIDs[0] == "" || r.Stages[0].Status != "rendered" {
			t.Fatal(r)
		}
		seen[r.Operation] = true
	}
	if len(seen) != 20 {
		t.Fatal(len(seen))
	}
}
