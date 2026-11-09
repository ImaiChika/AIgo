package generator

import (
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"
)

// A coarse or repeated wall clock must not cause count>1 or concurrent runs to
// overwrite each other. This deterministically reproduces the race failure.
func TestGenerateManyAtOneClockTickKeepsIDsUnique(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		req, item := workflowFixture()
		req.Workflow = nil
		req.Count = 20
		delete(item, "knowledge_basis")
		items := make([]any, req.Count)
		for i := range items {
			items[i] = item
		}
		raw, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		ids := map[string]bool{}
		for i := 0; i < 20; i++ {
			wg.Go(func() {
				client := &MockClient{Response: string(raw)}
				questions, err := NewService(client).Generate(t.Context(), req)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, q := range questions {
					if ids[q.ID] {
						t.Error("question ID collision at one clock tick", q.ID)
					}
					ids[q.ID] = true
				}
			})
		}
		wg.Wait()
		if len(ids) != 400 {
			t.Fatalf("got %d unique question IDs, want 400", len(ids))
		}
	})
}
