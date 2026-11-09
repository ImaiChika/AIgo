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
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
)

// Only the model is controlled. HTTP authorization, PostgreSQL, frozen bundles,
// generation workers, first checks, sharing and confirmation use production code.
type concurrentKnowledgeClient struct {
	base    workflowAPIClient
	mu      sync.Mutex
	prompts map[string]string
	started chan struct{}
	release chan struct{}
}

func (c *concurrentKnowledgeClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	op := llm.CallOperation(ctx)
	if strings.HasPrefix(op, "generate:") {
		c.mu.Lock()
		c.prompts[op] = m[len(m)-1].Content
		c.mu.Unlock()
		select {
		case c.started <- struct{}{}:
		default:
		}
		select {
		case <-c.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return c.base.Complete(ctx, m, o)
}

func TestConcurrentKnowledgeCardsSharingAndFrozenGeneration(t *testing.T) {
	s, store, h, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	httpServer := httptest.NewServer(h)
	defer httpServer.Close()
	httpClient := &http.Client{Timeout: 10 * time.Second}
	call := func(token, method, path string, input any, want int) ([]byte, error) {
		raw, e := json.Marshal(input)
		if e != nil {
			return nil, e
		}
		req, e := http.NewRequestWithContext(t.Context(), method, httpServer.URL+path, bytes.NewReader(raw))
		if e != nil {
			return nil, e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, e := httpClient.Do(req)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		body, e := io.ReadAll(resp.Body)
		if e == nil && resp.StatusCode != want {
			e = fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, body)
		}
		return body, e
	}
	must := func(token, method, path string, input any, want int) []byte {
		t.Helper()
		raw, e := call(token, method, path, input, want)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	create := func(name string, permissions []string) string {
		t.Helper()
		must(admin, "POST", "/api/users", map[string]any{"username": name, "password": "concurrent-isolation-password", "permissions": permissions}, 201)
		return loginForAuthTest(t, h, name, "concurrent-isolation-password", "198.51.100.201")
	}
	const n = 10
	tokens := make([]string, n)
	cards := make([]domain.PrivateKnowledgeCard, n)
	perms := []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermQuestionConfirm, domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse, domain.PermKBView, domain.PermKBUse, domain.PermKBSubmit}
	for i := range tokens {
		tokens[i] = create(fmt.Sprintf("kb-concurrent-%d", i), perms)
	}
	reviewer := create("kb-concurrent-reviewer", []string{domain.PermKBView, domain.PermKBReview})
	parallel := func(f func(int) error) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if e := f(i); e != nil {
					errs <- e
				}
			}()
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			t.Error(e)
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	marker := func(i int, v int) string { return fmt.Sprintf("OWNER_PRIVATE_%02d_VERSION_%d", i, v) }
	parallel(func(i int) error {
		input := domain.KnowledgeCard{Kind: "custom", Title: fmt.Sprintf("本人资料%d", i), Status: "active", KnowledgePoints: []domain.KnowledgePoint{point}, Note: fmt.Sprintf("NEVER_PROMPT_NOTE_%d", i), Content: domain.KnowledgeContent{CustomText: marker(i, 1)}}
		raw, e := call(tokens[i], "POST", "/api/private-knowledge", domain.KnowledgeCardSaveRequest{Key: fmt.Sprintf("create-%d", i), Cards: []domain.KnowledgeCard{input}}, 200)
		if e != nil {
			return e
		}
		var response struct {
			Cards []domain.PrivateKnowledgeCard `json:"cards"`
		}
		if e = json.Unmarshal(raw, &response); e != nil {
			return e
		}
		if len(response.Cards) != 1 {
			return fmt.Errorf("missing private card %d", i)
		}
		cards[i] = response.Cards[0]
		_, e = call(tokens[i], "PUT", "/api/knowledge-selections", map[string]any{"scope": "private", "id": cards[i].ID, "selected": true}, 200)
		return e
	})
	parallel(func(i int) error {
		_, e := call(tokens[i], "POST", "/api/questions/generate", generationPayload(fmt.Sprintf("concurrent-kb-run-%d", i), point, 1), 200)
		return e
	})
	client := &concurrentKnowledgeClient{prompts: map[string]string{}, started: make(chan struct{}, 2), release: make(chan struct{})}
	checker := aicheck.NewService(client, store, store, store, "concurrent-controlled")
	s.pipe = pipeline.New(generator.NewService(client), nil, s.reviewSvc, nil, store)
	s.pipe.SetDraftChecker(checker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	defer func() {
		select {
		case <-client.release:
		default:
			close(client.release)
		}
	}()
	for range 2 {
		select {
		case <-client.started:
		case <-time.After(5 * time.Second):
			t.Fatal("generation workers did not overlap")
		}
	}
	// While the first two models are in flight and the other accounts are queued,
	// share fixed v1 snapshots, revise all ten private cards, then approve/reject.
	subIDs := make([]string, 2)
	for i := range 2 {
		var response struct {
			Submissions []domain.KnowledgeSubmission `json:"submissions"`
		}
		json.Unmarshal(must(tokens[i], "POST", "/api/knowledge-submissions", domain.KnowledgeShareRequest{Key: fmt.Sprintf("submit-%d", i), Cards: []domain.KnowledgeCardRef{{ID: cards[i].ID, Revision: cards[i].Revision}}}, 200), &response)
		subIDs[i] = response.Submissions[0].ID
	}
	parallel(func(i int) error {
		card := cards[i].KnowledgeCard
		card.Content.CustomText = marker(i, 2)
		_, e := call(tokens[i], "POST", "/api/private-knowledge", domain.KnowledgeCardSaveRequest{Key: fmt.Sprintf("revise-%d", i), Cards: []domain.KnowledgeCard{card}}, 200)
		return e
	})
	var approved struct {
		Submissions []domain.KnowledgeSubmission `json:"submissions"`
	}
	json.Unmarshal(must(reviewer, "POST", "/api/knowledge-submissions/decisions", domain.KnowledgeDecisionRequest{Key: "approve", IDs: []string{subIDs[0]}, Decision: "approved"}, 200), &approved)
	sharedID := approved.Submissions[0].SharedCardID
	must(reviewer, "POST", "/api/knowledge-submissions/decisions", domain.KnowledgeDecisionRequest{Key: "reject", IDs: []string{subIDs[1]}, Decision: "rejected", Note: "仅用于一次审核拒绝分支验证"}, 200)
	must(tokens[2], "PUT", "/api/knowledge-selections", map[string]any{"scope": "shared", "id": sharedID, "selected": true}, 200)
	close(client.release)
	for i := range n {
		questions := waitWorkflowQuestions(t, h, tokens[i], fmt.Sprintf("concurrent-kb-run-%d", i), 1)
		q := questions[0]
		owner, e := s.authSvc.GetUserByUsername(fmt.Sprintf("kb-concurrent-%d", i))
		if e != nil || q.OwnerID != owner.ID {
			t.Fatal("question owner mixed", e)
		}
		frozen, e := store.GetQuestionGenerationContext(t.Context(), q.ID)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(frozen)
		if !strings.Contains(string(raw), marker(i, 1)) || strings.Contains(string(raw), marker(i, 2)) || strings.Contains(string(raw), sharedID) {
			t.Fatalf("queued task %d did not preserve its own accepted bundle", i)
		}
		client.mu.Lock()
		prompt := client.prompts["generate:"+fmt.Sprintf("concurrent-kb-run-%d", i)]
		client.mu.Unlock()
		if !strings.Contains(prompt, marker(i, 1)) || strings.Contains(prompt, "NEVER_PROMPT_NOTE_") {
			t.Fatalf("private prompt projection wrong for %d", i)
		}
		for j := range n {
			if j != i && strings.Contains(prompt, marker(j, 1)) {
				t.Fatalf("account %d received account %d private prompt", i, j)
			}
		}
		must(tokens[i], "POST", "/api/questions/confirm", libraryItemsPayload(t, s, q.ID), 200)
	}
	// A later explicitly selected shared card is allowed, while the owner's
	// subsequent private revision remains independent and confidential.
	must(tokens[2], "POST", "/api/questions/generate", generationPayload("concurrent-kb-next", point, 1), 200)
	questions := waitWorkflowQuestions(t, h, tokens[2], "concurrent-kb-next", 1)
	frozen, e := store.GetQuestionGenerationContext(t.Context(), questions[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(frozen)
	if !strings.Contains(string(raw), sharedID) || !strings.Contains(string(raw), marker(0, 1)) || !strings.Contains(string(raw), marker(2, 2)) || strings.Contains(string(raw), marker(0, 2)) {
		t.Fatal("new task did not use selected shared v1 alongside own private v2")
	}
	public, e := store.GetKnowledgeCard(t.Context(), sharedID)
	if e != nil || public == nil || strings.Contains(public.Note, "NEVER_PROMPT") {
		t.Fatal("private note was published", e)
	}
	must(tokens[2], "GET", "/api/private-knowledge/"+cards[0].ID, nil, 404)
	gen, checks := client.base.counts()
	if gen != 11 || checks != 11 {
		t.Fatalf("unexpected calls: generation=%d first_check=%d", gen, checks)
	}
	t.Log("10 concurrent accounts: private creation/revision, 10 frozen tasks, concurrent shared approval/rejection; later selected shared adoption; generation=11 first_check=11, provider HTTP=0")
}
