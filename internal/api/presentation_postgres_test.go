//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/workflowtrace"
)

type presentationAPIClient struct {
	base      workflowAPIClient
	mu        sync.Mutex
	seen      map[string]bool
	failBatch bool
}

func (c *presentationAPIClient) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	raw, err := c.base.Complete(ctx, m, o)
	if err != nil || strings.Contains(m[len(m)-1].Content, "<knowledge_reference>") {
		return raw, err
	}
	if c.failBatch && strings.HasPrefix(llm.CallOperation(ctx), "batch:") {
		c.mu.Lock()
		if c.seen == nil {
			c.seen = map[string]bool{}
		}
		first := !c.seen[llm.CallOperation(ctx)]
		c.seen[llm.CallOperation(ctx)] = true
		c.mu.Unlock()
		if first {
			return "", &llm.HTTPStatusError{Status: 503, Message: "isolated provider fault"}
		}
	}
	var items []map[string]any
	if err = json.Unmarshal([]byte(raw), &items); err != nil {
		return "", err
	}
	for _, item := range items {
		basis, ok := item["knowledge_basis"].(map[string]any)
		if !ok {
			continue
		}
		plan, ok := basis["case_plan"].(map[string]any)
		if !ok {
			continue
		}
		plan["facts"] = []domain.ClinicalFact{
			{ID: "i1", Section: "imaging_results", Text: "仅用于结构验收的影像结果。"},
			{ID: "l1", Section: "lab_results", Text: "血红蛋白110g/L。"},
			{ID: "c1", Section: "chief_complaint", Text: "反复上腹痛2年，加重1天。"},
			{ID: "p1", Section: "past_history", Text: "此前体健，既往相关病史由本例提供。"},
			{ID: "l2", Section: "lab_results", Text: "另一合成检验值4.2mmol/L。"},
		}
	}
	out, err := json.Marshal(items)
	return string(out), err
}

func TestPresentationSingleMultiBatchSnapshotsAndOriginalFirstCheck(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &presentationAPIClient{failBatch: true}
	checker := aicheck.NewService(client, store, store, store, "presentation-controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	order := domain.ClinicalSections()
	request := generationPayload("presentation-single", p, 2)
	request["presentation"] = &domain.PresentationRequirements{SectionOrder: order}
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, request)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	before, err := store.GetGenerationRun(t.Context(), "presentation-single")
	if err != nil {
		t.Fatal(err)
	}
	original := string(before.RequestJSON)
	r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload("presentation-legacy", p, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	// Simulate a task accepted before the new mandatory default. No runtime
	// endpoint can downgrade new tasks or mutate their accepted snapshot.
	if _, err := store.DB().Exec(`UPDATE generation_runs SET request_json=jsonb_set(request_json #- '{request,knowledge_workflow,material_policy}' #- '{request,knowledge_workflow,presentation}' #- '{request,prompt_snapshot}','{request,knowledge_workflow,version}','"knowledge-v2.1"'::jsonb) WHERE id='presentation-legacy'`); err != nil {
		t.Fatal(err)
	}
	if g, c := client.base.counts(); g != 0 || c != 0 {
		t.Fatal("submission invoked model", g, c)
	}
	// Modifying the next-form choice and replaying cannot alter the accepted run.
	order[5], order[6] = order[6], order[5]
	r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, request)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	after, _ := store.GetGenerationRun(t.Context(), "presentation-single")
	if string(after.RequestJSON) != original {
		t.Fatal("run replay rewrote requirements")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	questions := waitWorkflowQuestions(t, h, token, "presentation-single", 2)
	check := func(q domain.A2Question, version string, want []string) {
		t.Helper()
		frozen, e := store.GetQuestionGenerationContext(t.Context(), q.ID)
		if e != nil {
			t.Fatal(e)
		}
		ids := []string{}
		for _, f := range frozen.Basis.CasePlan.Facts {
			ids = append(ids, f.ID)
		}
		if q.WorkflowVersion != version || !reflect.DeepEqual(ids, want) || !strings.Contains(q.ClinicalStem, "110g/L") || !strings.Contains(q.ClinicalStem, "4.2mmol/L") {
			t.Fatal("order/content/version mismatch", version, ids)
		}
		view := serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID+"/generation-context", token, nil)
		if view.Code != 200 {
			t.Fatal(view.Code, view.Body)
		}
		var payload struct {
			Feedback *domain.PresentationFeedback `json:"presentation_feedback"`
		}
		if e = json.Unmarshal(view.Body.Bytes(), &payload); e != nil {
			t.Fatal(e)
		}
		if version == knowledgebase.PresentationWorkflowVersion || version == knowledgebase.CurrentClinicalWorkflowVersion {
			if payload.Feedback == nil || payload.Feedback.State != "applied" {
				t.Fatal("missing program feedback")
			}
		} else if payload.Feedback != nil {
			t.Fatal("legacy invented new feedback")
		}
	}
	for _, q := range questions {
		check(q, knowledgebase.CurrentClinicalWorkflowVersion, []string{"c1", "p1", "l1", "l2", "i1"})
	}
	legacy := waitWorkflowQuestions(t, h, token, "presentation-legacy", 1)[0]
	check(legacy, knowledgebase.WorkflowVersion, []string{"i1", "l1", "c1", "p1", "l2"})
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "presentation-controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	ex.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	defer ex.Close()
	s.batchSvc = ex
	// A conflicting order is now explicitly refused, not silently ignored.
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": []string{p.ID}, "count": 2, "presentation": &domain.PresentationRequirements{SectionOrder: order}})
	if r.Code != 400 {
		t.Fatal("new conflicting order accepted", r.Code, r.Body)
	}
	order = domain.ClinicalSections()
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": []string{p.ID}, "count": 2, "presentation": &domain.PresentationRequirements{SectionOrder: order}})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var submitted struct {
		ID string `json:"job_id"`
	}
	if json.Unmarshal(r.Body.Bytes(), &submitted) != nil {
		t.Fatal(r.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	var job *batch.BatchJob
	for time.Now().Before(deadline) {
		job, _ = ex.GetJobStatus(t.Context(), submitted.ID)
		if job != nil && job.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job == nil || job.Completed != 2 || job.Failed != 0 {
		t.Fatal(job)
	}
	for _, item := range job.Items {
		if item.Attempts != 2 {
			t.Fatal("original retry lost", item)
		}
		passed := false
		for time.Now().Before(deadline) {
			q, _ := store.GetQuestion(t.Context(), item.QuestionID)
			if q != nil && q.Status == domain.StatusAIReviewed {
				check(*q, knowledgebase.CurrentClinicalWorkflowVersion, []string{"c1", "p1", "l1", "l2", "i1"})
				passed = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !passed {
			t.Fatal("batch first check did not finish", item.QuestionID)
		}
	}
	if g, c := client.base.counts(); g != 6 || c != 5 {
		t.Fatal("extra generation or repeated first check", g, c)
	}
	// Human editing keeps the initial artifact, with no additional first check.
	q := questions[0]
	frozen, _ := store.GetQuestionGenerationContext(t.Context(), q.ID)
	saved, _ := json.Marshal(frozen)
	q.Version++
	q.Explanation = ""
	if err = store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	frozen, _ = store.GetQuestionGenerationContext(t.Context(), q.ID)
	now, _ := json.Marshal(frozen)
	if string(saved) != string(now) {
		t.Fatal("human edit rewrote provenance")
	}
	if g, c := client.base.counts(); g != 6 || c != 5 {
		t.Fatal("human edit rechecked", g, c)
	}
	admin, err := s.authSvc.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "presentation-review", Name: "呈现工作流原审核回归", Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "原专家审核", ExpertIDs: []string{admin.ID}, RequiredCount: 1}}, CreatedAt: time.Now()}
	if err = store.SaveFlowConfig(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/review/submit", token, map[string]any{"question_id": q.ID, "flow_id": flow.ID})
	var task domain.ReviewTask
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &task) != nil {
		t.Fatal("v3 review submission", r.Code, r.Body)
	}
	for _, path := range []string{"/api/review/action", "/api/review/finalize"} {
		r = serveGenerateJSON(t, h, "POST", path, token, map[string]any{"task_id": task.ID, "action": "approved", "opinion": "仅原业务回归，不作医学结论"})
		if r.Code != 200 {
			t.Fatal(path, r.Code, r.Body)
		}
	}
	current, err := store.GetQuestion(t.Context(), q.ID)
	if err != nil || current.Status != domain.StatusPublished {
		t.Fatal("v3 formal publication failed", err)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/unpublish", token, map[string]any{"reason": "隔离回归撤回"})
	if r.Code != 200 {
		t.Fatal("v3 withdrawal", r.Code, r.Body)
	}
	current, err = store.GetQuestion(t.Context(), q.ID)
	if err != nil || current.Status != domain.StatusAIReviewed {
		t.Fatal("v3 withdrawal state", err)
	}
	frozen, _ = store.GetQuestionGenerationContext(t.Context(), q.ID)
	now, _ = json.Marshal(frozen)
	if string(saved) != string(now) {
		t.Fatal("review changed frozen presentation")
	}
	if g, c := client.base.counts(); g != 6 || c != 5 {
		t.Fatal("review/withdrawal rechecked", g, c)
	}
	if _, err = store.DB().Exec(`UPDATE roles SET permissions=array_remove(permissions,'kb:view') WHERE id IN ('super_admin','admin')`); err != nil {
		t.Fatal(err)
	}
	if r = serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID+"/generation-context", token, nil); r.Code != 403 {
		t.Fatal("revoked token retained provenance", r.Code)
	}
	request["run_id"] = "presentation-revoked"
	request["presentation"] = domain.StandardPresentation()
	if r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, request); r.Code != 403 {
		t.Fatal("revoked user submitted", r.Code)
	}
	if run, e := store.GetGenerationRun(t.Context(), "presentation-revoked"); e != nil || run != nil {
		t.Fatal("denied run persisted", e)
	}
}

func TestPresentationValidationDoesNotQueueOrCallModels(t *testing.T) {
	_, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	for _, tc := range []struct {
		name, mode string
		value      any
	}{
		{"empty", "knowledge", map[string]any{}}, {"partial", "knowledge", map[string]any{"section_order": []string{"lab_results"}}},
		{"unknown option", "knowledge", map[string]any{"section_order": domain.ClinicalSections(), "hidden": true}},
		{"wrong mode", "direct", &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := generationPayload("presentation-invalid-"+strings.ReplaceAll(tc.name, " ", "-"), p, 1)
			payload["workflow_mode"] = tc.mode
			payload["presentation"] = tc.value
			r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, payload)
			if r.Code != 400 {
				t.Fatal(r.Code, r.Body)
			}
		})
	}
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM generation_runs`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if g, c := client.counts(); g != 0 || c != 0 {
		t.Fatal("invalid request called model", g, c)
	}
	good := map[string]any{"knowledge_points": []domain.KnowledgePoint{p}, "presentation": &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}}
	r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/prepare", token, good)
	if r.Code != 200 || !strings.Contains(r.Body.String(), knowledgebase.CurrentClinicalWorkflowVersion) {
		t.Fatal(r.Code, r.Body)
	}
	if g, c := client.counts(); g != 0 || c != 0 {
		t.Fatal("preview invoked model", fmt.Sprint(g, c))
	}
}

func TestPresentationSingleTwentyAndBatchHundred(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &presentationAPIClient{}
	checker := aicheck.NewService(client, store, store, store, "presentation-scale-controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	presentation := &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
	request := generationPayload("presentation-twenty", p, 20)
	request["presentation"] = presentation
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, request)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	questions := waitWorkflowQuestions(t, h, token, "presentation-twenty", 20)
	for _, q := range questions {
		if q.WorkflowVersion != knowledgebase.CurrentClinicalWorkflowVersion || strings.Index(q.ClinicalStem, "110g/L") > strings.Index(q.ClinicalStem, "影像结果") {
			t.Fatal("multi-question presentation lost")
		}
	}
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "presentation-scale-controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": []string{p.ID}, "count": 20, "presentation": presentation})
	// Five distinct bound points keep the normal per-point count <=20 and the
	// normal total <=100; no quota increase is used for the scale acceptance.
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var first struct {
		ID string `json:"job_id"`
	}
	json.Unmarshal(r.Body.Bytes(), &first)
	deadline := time.Now().Add(45 * time.Second)
	waitBatch := func(id string, total int) {
		t.Helper()
		var job *batch.BatchJob
		for time.Now().Before(deadline) {
			job, _ = ex.GetJobStatus(t.Context(), id)
			if job != nil && job.Status == "completed" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if job == nil || job.Status != "completed" || job.Completed != total || job.Failed != 0 {
			t.Fatal(job)
		}
		for _, item := range job.Items {
			if item.Attempts != 1 {
				t.Fatal("unexpected scale retry", item)
			}
		}
	}
	waitBatch(first.ID, 20)
	ids := []string{p.ID}
	points := []domain.KnowledgePoint{p}
	for i := 1; i < 5; i++ {
		next := p
		next.ID = ""
		next.OutlineCode = fmt.Sprintf("presentation.%03d", i)
		created, err := s.kpSvc.CreatePoint(t.Context(), next)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
		points = append(points, *created)
	}
	cards, _, _, err := s.knowledgeBaseSvc.List(t.Context(), mustPresentationOwner(t, s), "super_admin", knowledgebase.Filter{})
	if err != nil || len(cards) != 1 {
		t.Fatal(cards, err)
	}
	card := cards[0]
	card.KnowledgePoints = points
	r = serveGenerateJSON(t, h, "PUT", "/api/knowledge-base/"+card.ID, token, card)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &card) != nil {
		t.Fatal(r.Code, r.Body)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/"+card.ID+"/activate", token, map[string]int{"revision": card.Revision})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": p.VersionID, "knowledge_point_ids": ids, "count": 20, "presentation": presentation})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var large struct {
		ID string `json:"job_id"`
	}
	json.Unmarshal(r.Body.Bytes(), &large)
	waitBatch(large.ID, 100)
	for time.Now().Before(deadline) {
		var n int
		if err := store.DB().QueryRow(`SELECT count(*) FROM ai_check_tasks WHERE status='succeeded'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 140 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var nq, nt, passed int
	if err = store.DB().QueryRow(`SELECT (SELECT count(*) FROM questions),(SELECT count(*) FROM ai_check_tasks),(SELECT count(*) FROM ai_check_tasks WHERE status='succeeded')`).Scan(&nq, &nt, &passed); err != nil || nq != 140 || nt != 140 || passed != 140 {
		t.Fatal(nq, nt, passed, err)
	}
	if g, c := client.base.counts(); g != 121 || c != 140 {
		t.Fatal("scale repeated generation or first check", g, c)
	}
	rows, err := store.DB().Query(`SELECT body FROM question_generation_context`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var c domain.QuestionGenerationContext
		if err = rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &c); err != nil || knowledgebase.ValidateContext(&c) != nil || knowledgebase.PresentationFeedbackFor(&c).State != "applied" {
			t.Fatal("invalid scale artifact", err)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func mustPresentationOwner(t *testing.T, s *Server) string {
	t.Helper()
	u, err := s.authSvc.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func TestPresentationInterruptedRunRestoresFrozenRequirements(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &restartReliabilityModel{started: make(chan struct{})}
	checker := aicheck.NewService(client, store, store, store, "presentation-restart-controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	s.pipe.GenerationRetryBackoff = time.Millisecond
	request := generationPayload("presentation-restart", p, 2)
	request["presentation"] = &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, request)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	original, err := store.GetGenerationRun(t.Context(), "presentation-restart")
	if err != nil {
		t.Fatal(err)
	}
	before := string(original.RequestJSON)
	ctxA, cancelA := context.WithCancel(t.Context())
	defer cancelA()
	s.pipe.StartGenerationWorkers(ctxA, 2)
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not start")
	}
	cancelA()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, _ := store.GetGenerationRun(t.Context(), "presentation-restart")
		if run != nil && run.Status == domain.GenerationRunPending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctxB, cancelB := context.WithCancel(t.Context())
	defer cancelB()
	checker.StartWorkers(ctxB, 2)
	s.pipe.StartGenerationWorkers(ctxB, 2)
	questions := waitWorkflowQuestions(t, h, token, "presentation-restart", 2)
	after, err := store.GetGenerationRun(t.Context(), "presentation-restart")
	if err != nil || after.Attempts != 2 || string(after.RequestJSON) != before {
		t.Fatal("restart changed accepted requirements", err)
	}
	for _, q := range questions {
		c, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
		if err != nil || c.Presentation == nil || !reflect.DeepEqual(c.Presentation.SectionOrder, domain.ClinicalSections()) || knowledgebase.PresentationFeedbackFor(c).State != "not_applicable" {
			t.Fatal("restart lost scope or invented sections", err)
		}
	}
	if g, c, h := client.base.counts(); g != 1 || c != 2 || h != 1 {
		t.Fatal("restart duplicated successful work", g, c, h)
	}

	traces := []workflowtrace.Record{}
	traceDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(traceDeadline) {
		rows, err := store.DB().QueryContext(t.Context(), `SELECT record FROM workflow_execution_records WHERE operation='generate:presentation-restart' AND phase='generation' ORDER BY attempt`)
		if err != nil {
			t.Fatal(err)
		}
		traces = nil
		for rows.Next() {
			var raw []byte
			var record workflowtrace.Record
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &record); err != nil {
				t.Fatal(err)
			}
			traces = append(traces, record)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if len(traces) == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(traces) != 2 || traces[0].Attempt != 1 || traces[0].Outcome != "generation_failed" || traces[1].Attempt != 2 || traces[1].Outcome != "committed" || len(traces[1].QuestionIDs) != 2 {
		t.Fatal("restart lost attempt evidence", traces)
	}
}
