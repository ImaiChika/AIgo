//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	"aigo/internal/storage/testutil"
)

// Only the external model is controlled. References are deliberately synthetic;
// this verifies transport, adoption and persistence, not clinical correctness.
type workflowAcceptanceClient struct {
	mu                  sync.Mutex
	generations, checks int
}

func (c *workflowAcceptanceClient) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generations, c.checks
}

func (c *workflowAcceptanceClient) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prompt := messages[len(messages)-1].Content
	if strings.Contains(prompt, "<knowledge_reference>") {
		c.checks++
		if !strings.Contains(prompt, `"kind":"exam_reference"`) || !strings.Contains(prompt, `"kind":"custom"`) || !strings.Contains(prompt, `"case_plan"`) {
			return "", fmt.Errorf("first check lost reference types or generated evidence")
		}
		return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`, nil
	}
	c.generations++
	if !opts.MedicalSearchHint {
		return "", fmt.Errorf("optional network reminder lost")
	}
	parts := strings.SplitN(prompt, "<knowledge_material>", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("generation lost prepared knowledge")
	}
	var prepared struct {
		Version   string                   `json:"workflow_version"`
		Materials []knowledgebase.Material `json:"materials"`
		Plans     []domain.WorkflowTarget  `json:"ordered_targets"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(parts[1], "</knowledge_material>", 2)[0]), &prepared); err != nil {
		return "", err
	}
	if prepared.Version != knowledgebase.CurrentClinicalWorkflowVersion || len(prepared.Materials) != 3 {
		return "", fmt.Errorf("unexpected workflow or missing card: %s/%d", prepared.Version, len(prepared.Materials))
	}
	var medical knowledgebase.Material
	var complaint, lead string
	for _, card := range prepared.Materials {
		switch card.Kind {
		case "medical":
			medical = card
		case "custom":
			complaint = card.Content.CustomText
		case "exam_reference":
			if card.Content.Reference != nil {
				stem := card.Content.Reference.Stem
				lead = stem[strings.LastIndex(stem, "。")+len("。"):]
			}
		}
	}
	if len(medical.Content.Facts) == 0 || complaint == "" || lead == "" {
		return "", fmt.Errorf("three card contents were not transmitted")
	}
	raw, err := (&fakeAPILLM{}).Complete(ctx, messages, opts)
	if err != nil {
		return "", err
	}
	var original []map[string]any
	if err = json.Unmarshal([]byte(raw), &original); err != nil {
		return "", err
	}
	items := make([]map[string]any, 0, len(prepared.Plans))
	for _, target := range prepared.Plans {
		item := make(map[string]any, len(original[0])+1)
		for key, value := range original[0] {
			item[key] = value
		}
		ref := medical.Content.Facts[0].ID
		plan := testutil.ClinicalPlanFixture("A", ref, complaint)
		plan.LeadIn = lead
		plan.OptionEvidence[0].Reason = medical.Content.Facts[0].Text
		item["clinical_stem"], item["explanation"] = "", ""
		item["knowledge_basis"] = domain.KnowledgeBasis{TargetID: target.ID, FactRefs: []string{ref}, ConditionNotes: "仅隔离测试的资料传递", AnswerSupport: medical.Content.Facts[0].Text, CasePlan: plan}
		items = append(items, item)
	}
	result, err := json.Marshal(items)
	return string(result), err
}

func TestKnowledgeWorkflowThreeKindsAndScaleAcceptance(t *testing.T) {
	server, store, handler, token, point, medical, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	points := []domain.KnowledgePoint{point}
	for i := 2; i <= 5; i++ {
		p := point
		p.ID, p.Topic, p.OutlineCode = "", fmt.Sprintf("隔离验收考点%d", i), fmt.Sprintf("wf.%03d", i)
		created, err := server.kpSvc.CreatePoint(t.Context(), p)
		if err != nil {
			t.Fatal(err)
		}
		points = append(points, *created)
	}
	saveActive := func(card domain.KnowledgeCard) domain.KnowledgeCard {
		t.Helper()
		method, path := http.MethodPost, "/api/knowledge-base"
		if card.ID != "" {
			method, path = http.MethodPut, path+"/"+card.ID
		}
		response := serveGenerateJSON(t, handler, method, path, token, card)
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body)
		}
		var saved domain.KnowledgeCard
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		response = serveGenerateJSON(t, handler, http.MethodPost, "/api/knowledge-base/"+saved.ID+"/activate", token, map[string]int{"revision": saved.Revision})
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return saved
	}
	medical.KnowledgePoints = points
	medical.Content.ExamTargets = []string{"旧版目标甲", "旧版目标乙"}
	medical.Content.Facts[0].Text = "旧版合成事实只用于资料采用验收。"
	cards := []domain.KnowledgeCard{
		saveActive(*medical),
		saveActive(domain.KnowledgeCard{Kind: "exam_reference", Title: "隔离验收参考题", Source: "人工合成，非真实试题", KnowledgePoints: points, Content: domain.KnowledgeContent{Reference: &domain.ExamReference{Stem: "患者，男，45岁。反复上腹痛2年。该患者最可能的诊断是"}}}),
		saveActive(domain.KnowledgeCard{Kind: "custom", Title: "隔离验收自定义资料", KnowledgePoints: points, Content: domain.KnowledgeContent{CustomText: "反复上腹痛2年，加重1天"}}),
	}
	preview := serveGenerateJSON(t, handler, http.MethodPost, "/api/knowledge-base/prepare", token, map[string]any{"knowledge_points": points})
	if preview.Code != http.StatusOK || !strings.Contains(preview.Header().Get("Content-Type"), "application/json") {
		t.Fatal(preview.Code, preview.Body)
	}
	client := &workflowAcceptanceClient{}
	checker := aicheck.NewService(client, store, store, store, "acceptance-controlled-model")
	server.aiCheckSvc = checker
	server.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	server.pipe.SetDraftChecker(checker)
	oldRun := "workflow-acceptance-old"
	response := serveGenerateJSON(t, handler, http.MethodPost, "/api/questions/generate", token, generationPayload(oldRun, point, 1))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	if gen, checks := client.counts(); gen != 0 || checks != 0 {
		t.Fatal("preparation called the model", gen, checks)
	}
	oldCards, _ := json.Marshal(cards)
	for i := range cards {
		switch cards[i].Kind {
		case "medical":
			cards[i].Content.ExamTargets = []string{"新版目标甲", "新版目标乙"}
			cards[i].Content.Facts[0].Text = "新版合成事实只用于资料采用验收。"
		case "custom":
			cards[i].Content.CustomText = "间歇上腹不适3年，加重2天"
		case "exam_reference":
			cards[i].Content.Reference.Stem = "患者，男，45岁。间歇上腹不适3年。该患者最符合的诊断为"
		}
		cards[i] = saveActive(cards[i])
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	server.pipe.StartGenerationWorkers(ctx, 2)
	start := time.Now()
	oldQuestions := waitWorkflowQuestions(t, handler, token, oldRun, 1)
	t.Logf("single_1 elapsed=%s", time.Since(start))
	assertAdoption := func(q domain.A2Question, old bool) {
		t.Helper()
		view := serveGenerateJSON(t, handler, http.MethodGet, "/api/questions/"+q.ID+"/generation-context", token, nil)
		if view.Code != http.StatusOK {
			t.Fatal(view.Code, view.Body)
		}
		var artifact domain.QuestionGenerationContext
		if err := json.Unmarshal(view.Body.Bytes(), &artifact); err != nil {
			t.Fatal(err)
		}
		wantCards := cards
		if old {
			wantCards = nil
			if err := json.Unmarshal(oldCards, &wantCards); err != nil {
				t.Fatal(err)
			}
		}
		if len(artifact.Bundle.Cards) != 3 || artifact.WorkflowVersion != knowledgebase.CurrentClinicalWorkflowVersion {
			t.Fatal("lost frozen materials", artifact)
		}
		for _, expected := range wantCards {
			found := false
			for _, actual := range artifact.Bundle.Cards {
				if actual.ID == expected.ID && actual.Revision == expected.Revision {
					found = true
				}
			}
			if !found {
				t.Fatal("wrong card revision", expected.ID, expected.Revision)
			}
		}
		if artifact.Target.Target != wantCards[0].Content.ExamTargets[0] && artifact.Target.Target != wantCards[0].Content.ExamTargets[1] {
			t.Fatal("wrong knowledge target", artifact.Target)
		}
		if !strings.Contains(q.ClinicalStem, wantCards[2].Content.CustomText) || !strings.HasSuffix(q.ClinicalStem, artifact.Basis.CasePlan.LeadIn) || !strings.Contains(wantCards[1].Content.Reference.Stem, artifact.Basis.CasePlan.LeadIn) || !strings.Contains(q.Explanation, wantCards[0].Content.Facts[0].Text) {
			t.Fatal("question did not adopt the transmitted three card contents", q)
		}
		if q.Status != domain.StatusAIReviewed {
			t.Fatal("first-check boundary changed", q.Status)
		}
	}
	assertAdoption(oldQuestions[0], true)
	executor := batch.NewLocalExecutorWithConfig(generator.NewService(client), store, store, "acceptance-controlled-model", batch.LocalConfig{Concurrency: 2, Context: ctx, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer executor.Close()
	server.batchSvc = executor
	newRun := "workflow-acceptance-new-ten"
	response = serveGenerateJSON(t, handler, http.MethodPost, "/api/questions/generate", token, generationPayload(newRun, point, 10))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body)
	}
	allIDs := []string{oldQuestions[0].ID}
	for _, size := range []int{10, 100} {
		start = time.Now()
		selected, count := []domain.KnowledgePoint{point}, size
		if size == 100 {
			selected, count = points, 20
		}
		ids := make([]string, len(selected))
		for i, p := range selected {
			ids[i] = p.ID
		}
		response = serveGenerateJSON(t, handler, http.MethodPost, "/api/batch/submit", token, map[string]any{"workflow_mode": "knowledge", "version_id": point.VersionID, "knowledge_point_ids": ids, "count": count, "job_name": fmt.Sprintf("隔离验收%d题", size)})
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body)
		}
		var submitted struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &submitted); err != nil {
			t.Fatal(err)
		}
		var job batch.BatchJob
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			response = serveGenerateJSON(t, handler, http.MethodGet, "/api/batch/status/"+submitted.JobID, token, nil)
			if response.Code != http.StatusOK {
				t.Fatal(response.Code, response.Body)
			}
			if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
			if job.Failed > 0 || job.Status == "failed" {
				t.Fatal("batch failed", response.Body)
			}
			if job.Status == "completed" && job.Completed == size && job.PipelineResult != nil && len(job.PipelineResult.QuestionIDs) == size {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if job.Status != "completed" || job.Completed != size || job.PipelineResult == nil || len(job.PipelineResult.QuestionIDs) != size {
			t.Fatal("batch did not complete", job)
		}
		batchIDs := job.PipelineResult.QuestionIDs
		for time.Now().Before(deadline) {
			progress := serveGenerateJSON(t, handler, http.MethodPost, "/api/ai-check/progress", token, map[string]any{"question_ids": batchIDs})
			var result struct {
				Counts map[string]int          `json:"counts"`
				Items  []aicheck.CheckProgress `json:"items"`
			}
			if progress.Code != http.StatusOK || json.Unmarshal(progress.Body.Bytes(), &result) != nil {
				t.Fatal(progress.Code, progress.Body)
			}
			if result.Counts["passed"] == size && len(result.Items) == size {
				for _, item := range result.Items {
					if item.Attempts != 1 || item.Discarded || item.CheckFailed {
						t.Fatal("unexpected first-check retries", item)
					}
				}
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		for _, id := range batchIDs {
			q, err := store.GetQuestion(t.Context(), id)
			if err != nil || q == nil {
				t.Fatal(id, err)
			}
			assertAdoption(*q, false)
		}
		allIDs = append(allIDs, batchIDs...)
		t.Logf("batch_%d end_to_end=%s", size, time.Since(start))
	}
	newQuestions := waitWorkflowQuestions(t, handler, token, newRun, 10)
	for _, q := range newQuestions {
		assertAdoption(q, false)
		allIDs = append(allIDs, q.ID)
	}
	if gen, checks := client.counts(); gen != 112 || checks != 121 || len(allIDs) != 121 {
		t.Fatal("duplicate model calls or missing results", gen, checks, len(allIDs))
	}
	var questions, tasks, contexts int
	if err := store.DB().QueryRow(`SELECT (SELECT count(*) FROM questions),(SELECT count(*) FROM ai_check_tasks),(SELECT count(*) FROM question_generation_context)`).Scan(&questions, &tasks, &contexts); err != nil || questions != 121 || tasks != 121 || contexts != 121 {
		t.Fatal("durable workflow counts disagree", questions, tasks, contexts, err)
	}
}
