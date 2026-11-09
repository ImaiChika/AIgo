//go:build integration

package api

import (
	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage/postgres"
	"aigo/internal/storage/testutil"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type workflowAPIClient struct {
	mu                  sync.Mutex
	generations, checks int
	failFirst           bool
	checkPrompts        []string
}

func (c *workflowAPIClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompt := m[len(m)-1].Content
	if strings.Contains(prompt, "<knowledge_reference>") || strings.HasSuffix(llm.CallOperation(ctx), ":first_check") {
		c.checks++
		c.checkPrompts = append(c.checkPrompts, prompt)
		return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[],"suggestion":"测试通过"}`, nil
	}
	c.generations++
	if c.failFirst && c.generations == 1 {
		return "", errors.New("injected provider 503")
	}
	raw, _ := (&fakeAPILLM{}).Complete(ctx, m, o)
	if !strings.Contains(prompt, "<knowledge_material>") {
		return raw, nil
	}
	section := strings.SplitN(strings.SplitN(prompt, "<knowledge_material>", 2)[1], "</knowledge_material>", 2)[0]
	var prepared struct {
		Version   string                   `json:"workflow_version"`
		Materials []knowledgebase.Material `json:"materials"`
		Plans     []domain.WorkflowTarget  `json:"ordered_targets"`
	}
	if e := json.Unmarshal([]byte(section), &prepared); e != nil {
		return "", e
	}
	var original []map[string]any
	json.Unmarshal([]byte(raw), &original)
	items := []map[string]any{}
	for _, plan := range prepared.Plans {
		item := map[string]any{}
		for k, v := range original[0] {
			item[k] = v
		}
		var ref string
		for _, card := range prepared.Materials {
			if card.ID == plan.CardID {
				ref = card.Content.Facts[0].ID
			}
		}
		basis := domain.KnowledgeBasis{Target: plan.Target, FactRefs: []string{ref}, ConditionNotes: "仅用于测试的条件", AnswerSupport: "仅用于测试的依据"}
		if knowledgebase.UsesClinicalPlan(prepared.Version) {
			answer, _ := item["answer"].(string)
			basis.TargetID = plan.ID
			basis.CasePlan = testutil.ClinicalPlanFixture(answer, ref, "反复上腹痛2年，加重1天。")
		}
		item["knowledge_basis"] = basis
		items = append(items, item)
	}
	out, e := json.Marshal(items)
	return string(out), e
}
func (c *workflowAPIClient) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generations, c.checks
}
func setupWorkflowAPI(t *testing.T) (*Server, *postgres.Store, http.Handler, string, domain.KnowledgePoint, *domain.KnowledgeCard, *workflowAPIClient, func()) {
	t.Helper()
	server, store, cleanup := generationHandlerTestServer(t)
	server.knowledgeBaseSvc = &knowledgebase.Service{Store: store}
	client := &workflowAPIClient{}
	checker := aicheck.NewService(client, store, store, store, "test")
	server.aiCheckSvc = checker
	server.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	server.pipe.SetDraftChecker(checker)
	server.pipe.GenerationRetryBackoff = 10 * time.Millisecond
	version, e := server.kpSvc.CreateVersion(t.Context(), "工作流测试大纲", 2026, "")
	if e != nil {
		t.Fatal(e)
	}
	point, e := server.kpSvc.CreatePoint(t.Context(), domain.KnowledgePoint{VersionID: version.ID, Category: "临床综合", Subject: "消化", Unit: "测试", SubItem: "测试", Topic: "测试胃溃疡", OutlineCode: "wf.001"})
	if e != nil {
		t.Fatal(e)
	}
	if e = server.kpSvc.PublishVersion(t.Context(), version.ID); e != nil {
		t.Fatal(e)
	}
	h := server.Handler()
	token := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.164")
	user, e := server.authSvc.GetUserByUsername("admin")
	if e != nil {
		t.Fatal(e)
	}
	card, e := server.knowledgeBaseSvc.Save(t.Context(), user.ID, user.Role, "", domain.KnowledgeCard{Kind: "medical", Title: "冻结测试卡", Source: "测试教材", KnowledgePoints: []domain.KnowledgePoint{*point}, Content: domain.KnowledgeContent{ExamTargets: []string{"测试诊断", "测试处理"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "最初冻结事实", AppliesWhen: "测试条件"}}}})
	if e != nil {
		t.Fatal(e)
	}
	card, e = server.knowledgeBaseSvc.Transition(t.Context(), user.ID, user.Role, card.ID, "activate", card.Revision)
	if e != nil {
		t.Fatal(e)
	}
	return server, store, h, token, *point, &card, client, cleanup
}
func generationPayload(id string, p domain.KnowledgePoint, count int) map[string]any {
	return map[string]any{"run_id": id, "workflow_mode": "knowledge", "knowledge_point_id": p.ID, "version_id": p.VersionID, "count": count}
}
func waitWorkflowQuestions(t *testing.T, h http.Handler, token, id string, count int) []domain.A2Question {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		r := serveGenerateJSON(t, h, "GET", "/api/generation-runs/"+id, token, nil)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		var data struct {
			Run       domain.GenerationRun `json:"run"`
			Questions []domain.A2Question  `json:"questions"`
		}
		if e := json.Unmarshal(r.Body.Bytes(), &data); e != nil {
			t.Fatal(e)
		}
		if data.Run.Status == domain.GenerationRunFailed {
			t.Fatal(data.Run.Error)
		}
		if len(data.Questions) == count {
			return data.Questions
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("workflow did not finish")
	return nil
}
func TestKnowledgeWorkflowDurableSingleAndOriginalFirstCheck(t *testing.T) {
	s, store, h, token, p, card, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	req := generationPayload("workflow-single-0001", p, 2)
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, req)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	// Edit/disable the card after durable submission, before any model call.
	card.Content.Facts[0].Text = "提交后改变的事实"
	changed := serveGenerateJSON(t, h, "PUT", "/api/knowledge-base/"+card.ID, token, card)
	if changed.Code != 200 {
		t.Fatal(changed.Code, changed.Body)
	}
	duplicate := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, req)
	if duplicate.Code != 200 {
		t.Fatal("idempotency re-prepared cards", duplicate.Code, duplicate.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	questions := waitWorkflowQuestions(t, h, token, "workflow-single-0001", 2)
	gen, check := client.counts()
	if gen != 1 || check != 2 {
		t.Fatal("unexpected model fan-out", gen, check)
	}
	for _, q := range questions {
		if q.WorkflowVersion != knowledgebase.CurrentClinicalWorkflowVersion {
			t.Fatal(q)
		}
		frozen, e := store.GetQuestionGenerationContext(t.Context(), q.ID)
		if e != nil || frozen.Bundle.Cards[0].Content.Facts[0].Text != "最初冻结事实" {
			t.Fatal(frozen, e)
		}
		view := serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID+"/generation-context", token, nil)
		if view.Code != 200 || strings.Contains(view.Body.String(), "提交后改变的事实") {
			t.Fatal(view.Code, view.Body)
		}
	}

	// The independent view grant never expands ownership or bank scope.
	created := serveGenerateJSON(t, h, "POST", "/api/users", token, map[string]any{"username": "kb-reader", "password": "test-reader-password", "permissions": []string{"question:view", "kb:view"}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	reader := loginForAuthTest(t, h, "kb-reader", "test-reader-password", "198.51.100.165")
	denied := serveGenerateJSON(t, h, "GET", "/api/questions/"+questions[0].ID+"/generation-context", reader, nil)
	if (denied.Code != 403 && denied.Code != 404) || strings.Contains(denied.Body.String(), "最初冻结事实") {
		t.Fatal("cross-owner provenance leak", denied.Code, denied.Body)
	}
	if _, e := store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'kb:view') WHERE id IN ('super_admin','admin')`); e != nil {
		t.Fatal(e)
	}
	revoked := serveGenerateJSON(t, h, "GET", "/api/questions/"+questions[0].ID+"/generation-context", token, nil)
	if revoked.Code != 403 {
		t.Fatal("stale token read retained", revoked.Code, revoked.Body)
	}
	client.mu.Lock()
	for _, prompt := range client.checkPrompts {
		if !strings.Contains(prompt, "最初冻结事实") || strings.Contains(prompt, "提交后改变的事实") {
			t.Fatal("first check lost frozen knowledge")
		}
	}
	client.mu.Unlock()
}
func TestKnowledgeWorkflowMissingCoverageUsesOriginalGenerationAndStillChecksPermission(t *testing.T) {
	s, store, h, token, p, card, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	archived := serveGenerateJSON(t, h, "DELETE", "/api/knowledge-base/"+card.ID, token, map[string]int{"revision": card.Revision})
	if archived.Code != 200 {
		t.Fatal(archived.Code, archived.Body)
	}
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload("workflow-empty-0001", p, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	questions := waitWorkflowQuestions(t, h, token, "workflow-empty-0001", 1)
	if questions[0].WorkflowVersion != "" {
		t.Fatal("empty lookup manufactured medical citations")
	}
	run, e := store.GetGenerationRun(ctx, "workflow-empty-0001")
	if e != nil || run == nil {
		t.Fatal("empty lookup not persisted", e)
	}
	if _, e = store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'kb:use') WHERE id='super_admin' OR id='admin'`); e != nil {
		t.Fatal(e)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/prepare", token, map[string]any{"knowledge_points": []domain.KnowledgePoint{p}})
	if r.Code != 403 {
		t.Fatal(r.Code, r.Body)
	}
	if gen, check := client.counts(); gen != 1 || check != 1 {
		t.Fatal(gen, check)
	}
}
func TestKnowledgeWorkflowGenerationRetryAndBatchUseFrozenInputs(t *testing.T) {
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client.failFirst = true
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload("workflow-retry-0001", p, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	_ = waitWorkflowQuestions(t, h, token, "workflow-retry-0001", 1)
	if gen, check := client.counts(); gen != 2 || check != 1 {
		t.Fatal("retry rechecked/re-generated old results", gen, check)
	}
	executor := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "test", batch.LocalConfig{Concurrency: 2, Pipeline: true, Checker: s.aiCheckSvc, CheckMaxAttempts: 3})
	defer executor.Close()
	executor.SetDraftChecker(s.aiCheckSvc)
	s.batchSvc = executor
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": []string{p.ID}, "count": 2})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var submitted struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(r.Body.Bytes(), &submitted)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		job, e := store.GetBatchJob(t.Context(), submitted.JobID)
		if e != nil {
			t.Fatal(e)
		}
		if job != nil && job.Status == "completed" && job.Completed == 2 {
			if job.WorkflowSnapshotJSON == "" {
				t.Fatal("batch snapshot lost")
			}
			if strings.Contains(job.OutputJSON, "pending_knowledge_contexts") || strings.Contains(job.OutputJSON, "最初冻结事实") {
				t.Fatal("progress repeatedly persisted private references")
			}

			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	for time.Now().Before(deadline) {
		if gen, check := client.counts(); gen == 4 && check == 3 {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	gen, check := client.counts()
	t.Fatal("batch did not complete", gen, check)
}

func TestOptionalKnowledgeMixedBatchFreezesCardsAndEmptyLookups(t *testing.T) {
	s, store, h, token, point, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	empty := point
	empty.ID = ""
	empty.OutlineCode = "wf.empty"
	empty.Topic = "无卡临床考点"
	p, err := s.kpSvc.CreatePoint(t.Context(), empty)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	executor := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "test", batch.LocalConfig{Context: ctx, Concurrency: 2, Pipeline: true, Checker: s.aiCheckSvc, CheckMaxAttempts: 3})
	defer executor.Close()
	s.batchSvc = executor
	r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": point.VersionID, "knowledge_point_ids": []string{point.ID, p.ID}, "mode": "per_point", "count": 1, "skip_existing": false})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var submitted struct {
		JobID string `json:"job_id"`
	}
	json.Unmarshal(r.Body.Bytes(), &submitted)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		job, e := store.GetBatchJob(ctx, submitted.JobID)
		if e != nil {
			t.Fatal(e)
		}
		gen, check := client.counts()
		if job != nil && job.Status == "completed" && gen == 2 && check == 2 {
			if job.Completed != 2 || job.Failed != 0 {
				t.Fatal("missing card discarded a selected point", job)
			}
			var snapshot domain.KnowledgeWorkflowSnapshot
			if e = json.Unmarshal([]byte(job.WorkflowSnapshotJSON), &snapshot); e != nil {
				t.Fatal(e)
			}
			if snapshot.MaterialPolicy != knowledgebase.OptionalMaterialsPolicy || len(snapshot.Bundles) != 2 {
				t.Fatal("optional lookup not durable", snapshot)
			}
			b, e := knowledgebase.BundleFor(&snapshot, p.ID)
			if e != nil || len(b.Cards) != 0 {
				t.Fatal("empty result not frozen", e)
			}
			b, e = knowledgebase.BundleFor(&snapshot, point.ID)
			if e != nil || len(b.Cards) != 1 {
				t.Fatal("matched card lost", e)
			}
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("mixed batch failed to complete generation and mandatory checks")
}
