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
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type retrySecondPolicyClient struct {
	base     workflowAPIClient
	fail     atomic.Bool
	failures atomic.Int32
}

func (c *retrySecondPolicyClient) Complete(ctx context.Context, messages []llm.Message, options llm.GenerateOptions) (string, error) {
	if c.fail.Load() && strings.HasSuffix(llm.CallOperation(ctx), ":1") {
		c.failures.Add(1)
		return "", errors.New("controlled second unit outage")
	}
	return c.base.Complete(ctx, messages, options)
}

func TestCurrentPolicyPGFailedBatchRetryKeepsPassedQuestion(t *testing.T) {
	s, store, h, token, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	second := point
	second.ID = ""
	second.OutlineCode = "focused.retry"
	second.Topic = "重跑失败项"
	p2, err := s.kpSvc.CreatePoint(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	client := &retrySecondPolicyClient{}
	client.fail.Store(true)
	checker := aicheck.NewService(client, store, store, store, "retry-controlled")
	s.aiCheckSvc = checker
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "retry-controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer ex.Close()
	ex.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	s.batchSvc = ex
	r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"knowledge_point_ids": []string{point.ID, p2.ID}, "version_id": point.VersionID, "count": 1, "workflow_mode": "direct"})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	var submitted struct {
		ID string `json:"job_id"`
	}
	if err = json.Unmarshal(r.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	wait := func(completed, failed int) *batch.BatchJob {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			j, e := ex.GetJobStatus(t.Context(), submitted.ID)
			if e == nil && j.Status == "completed" && j.Completed == completed && j.Failed == failed {
				return j
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("batch did not reach expected counts", completed, failed)
		return nil
	}
	first := wait(1, 1)
	keptID := first.Items[0].QuestionID
	if keptID == "" || client.failures.Load() != 3 {
		t.Fatal(first, client.failures.Load())
	}
	before, err := store.GetBatchJob(t.Context(), submitted.ID)
	if err != nil {
		t.Fatal(err)
	}
	client.fail.Store(false)
	r = serveGenerateJSON(t, h, "POST", "/api/batch/retry-failed/"+submitted.ID, token, nil)
	if r.Code != 200 {
		t.Fatal("retry admission failed", r.Code, r.Body)
	}
	last := wait(2, 0)
	if last.Items[0].QuestionID != keptID {
		t.Fatal("old success regenerated")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, p, e := checker.ProgressByQuestionIDs(t.Context(), []string{keptID, last.Items[1].QuestionID})
		if e == nil && p["passed"] == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first checks incomplete", p, e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	after, err := store.GetBatchJob(t.Context(), submitted.ID)
	if err != nil || before.PromptSnapshotJSON != after.PromptSnapshotJSON {
		t.Fatal("frozen prompt changed", err)
	}
	var tasks, attempts int
	if err = store.DB().QueryRow(`SELECT count(*),sum(attempts) FROM ai_check_tasks WHERE question_id=ANY($1)`, []string{keptID, last.Items[1].QuestionID}).Scan(&tasks, &attempts); err != nil || tasks != 2 || attempts != 2 {
		t.Fatal("old question rechecked", tasks, attempts, err)
	}
}

func TestCurrentPolicyRestoresPreviouslyAcceptedCustomV3(t *testing.T) {
	s, store, h, token, p, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	client := &presentationAPIClient{}
	checker := aicheck.NewService(client, store, store, store, "old-v3-controlled")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	const id = "old-v3-custom-restored"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, p, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	run, err := store.GetGenerationRun(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var spec pipeline.GenerationSpec
	if err = json.Unmarshal(run.RequestJSON, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Request.PromptSnapshot = nil
	spec.Request.Workflow.Version = knowledgebase.PresentationWorkflowVersion
	spec.Request.Workflow.MaterialPolicy = ""
	order := domain.ClinicalSections()
	order[5], order[6] = order[6], order[5]
	spec.Request.Workflow.Presentation = &domain.PresentationRequirements{SectionOrder: order}
	raw, _ := json.Marshal(spec)
	// Isolated fixture models an existing pre-upgrade pending task only.
	if _, err = store.DB().Exec(`UPDATE generation_runs SET request_json=$1 WHERE id=$2`, raw, id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	q := waitWorkflowQuestions(t, h, token, id, 1)[0]
	frozen, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
	if err != nil || q.WorkflowVersion != knowledgebase.PresentationWorkflowVersion || frozen.Presentation.SectionOrder[5] != "imaging_results" || strings.Index(q.ClinicalStem, "影像结果") > strings.Index(q.ClinicalStem, "110g/L") {
		t.Fatal("old custom snapshot was upgraded", err)
	}
	after, _ := store.GetGenerationRun(t.Context(), id)
	var stable pipeline.GenerationSpec
	json.Unmarshal(after.RequestJSON, &stable)
	canonical, _ := json.Marshal(stable)
	if string(canonical) != string(raw) {
		t.Fatal("old request changed on restore")
	}
	if g, c := client.base.counts(); g != 1 || c != 1 {
		t.Fatal("old snapshot reworked", g, c)
	}
}

func TestCurrentGenerationPolicyDirectMultiBatchAndBasicExclusion(t *testing.T) {
	s, store, h, token, p, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	ex := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "policy-controlled", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: s.aiCheckSvc, CheckMaxAttempts: 3})
	defer ex.Close()
	s.batchSvc = ex
	payload := generationPayload("policy-direct-two", p, 2)
	payload["workflow_mode"] = "direct"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, payload)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	questions := waitWorkflowQuestions(t, h, token, "policy-direct-two", 2)
	for _, q := range questions {
		if q.WorkflowVersion != "" || q.GenerationContext != nil || !strings.Contains(q.ClinicalStem, "上腹痛") {
			t.Fatal("direct acquired KB or lost narrative", q.ID)
		}
	}
	run, err := store.GetGenerationRun(t.Context(), "policy-direct-two")
	if err != nil {
		t.Fatal(err)
	}
	var spec pipeline.GenerationSpec
	if err = json.Unmarshal(run.RequestJSON, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Request.PromptSnapshot == nil || spec.Request.PromptSnapshot.PolicyVersion != domain.CurrentA2GenerationPolicy || spec.Request.PromptSnapshot.Appendix != "" {
		t.Fatal("new request lost frozen policy or acquired a retired appendix")
	}
	waitBatch := func(payload map[string]any, total int) {
		t.Helper()
		r := serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, payload)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		var submitted struct {
			ID       string `json:"job_id"`
			Excluded int    `json:"excluded_basic_points"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &submitted); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			j, _ := ex.GetJobStatus(t.Context(), submitted.ID)
			if j != nil && j.Completed == total && j.Failed == 0 {
				ids := []string{}
				for _, item := range j.Items {
					ids = append(ids, item.QuestionID)
				}
				_, progress, e := s.aiCheckSvc.ProgressByQuestionIDs(t.Context(), ids)
				if e == nil && progress["passed"] == total {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("direct batch did not finish", submitted.ID)
	}
	waitBatch(map[string]any{"knowledge_point_ids": []string{p.ID}, "version_id": p.VersionID, "count": 2}, 2)
	if g, c := client.counts(); g != 3 || c != 4 {
		t.Fatal("direct multi/batch extra call", g, c)
	}
	basic := p
	basic.ID = "kp-policy-legacy-basic"
	basic.Revision = 0
	basic.Topic = "存量基础点（仅存量数据可出现，新增/导入已禁止）"
	basic.OutlineCode = "000.basic"
	basic.Category = "基础医学"
	basic.Subject = "生理学"
	// 写入边界已拒绝基础点；仅在测试中用SQL模拟迁移前存量数据，
	// 验证提交守卫仍会拦截伪装与显式引用。
	if _, err := store.DB().ExecContext(t.Context(), `INSERT INTO knowledge_points(id,version_id,category,subject,topic,outline_code) VALUES($1,$2,$3,$4,$5,$6)`, basic.ID, basic.VersionID, basic.Category, basic.Subject, basic.Topic, basic.OutlineCode); err != nil {
		t.Fatal(err)
	}
	created := &basic
	denied := generationPayload("policy-basic-denied", *created, 1)
	denied["category"] = "临床综合"
	denied["workflow_mode"] = "direct"
	if r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, denied); r.Code != 400 {
		t.Fatal("client disguised basic point", r.Code, r.Body)
	}
	if r = serveGenerateJSON(t, h, "POST", "/api/batch/submit", token, map[string]any{"version_id": p.VersionID, "knowledge_point_ids": []string{p.ID, created.ID}, "count": 1}); r.Code != 400 {
		t.Fatal("mixed explicit basic request accepted", r.Code, r.Body)
	}
	if missing, _ := store.GetGenerationRun(t.Context(), "policy-basic-denied"); missing != nil {
		t.Fatal("denied task persisted")
	}
	if point, e := s.kpSvc.ResolveForGeneration(t.Context(), p.VersionID, created.ID, ""); e != nil || point == nil {
		t.Fatal("basic knowledge was removed", e)
	}
	if g, c := client.counts(); g != 3 || c != 4 {
		t.Fatal("denied point invoked model", g, c)
	}
	// Automatic ranges exclude before the limit; the basic point sorts first.
	waitBatch(map[string]any{"version_id": p.VersionID, "limit": 1, "count": 1}, 1)
	if g, c := client.counts(); g != 4 || c != 5 {
		t.Fatal("automatic selection lost clinical point", g, c)
	}
}
