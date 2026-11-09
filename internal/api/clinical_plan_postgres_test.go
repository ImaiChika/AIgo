//go:build integration

package api

import (
	"context"
	"encoding/json"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

func TestClinicalPlanLegacyRunStillExecutesAfterNewDefault(t *testing.T) {
	s, store, h, token, point, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	id := "legacy-case-plan-0001"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, point, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	// Simulate an already accepted v1 run, without changing any knowledge,
	// personal baseline or owner fields. Workers have not started yet.
	if _, err := store.DB().Exec(`UPDATE generation_runs SET request_json=jsonb_set(request_json #- '{request,knowledge_workflow,material_policy}' #- '{request,knowledge_workflow,presentation}' #- '{request,knowledge_workflow,reference_protocol}' #- '{request,prompt_snapshot}','{request,knowledge_workflow,version}','"knowledge-v1"'::jsonb) WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	q := waitWorkflowQuestions(t, h, token, id, 1)[0]
	if q.WorkflowVersion != knowledgebase.LegacyWorkflowVersion {
		t.Fatal(q.WorkflowVersion)
	}
	c, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
	if err != nil || c.Basis.CasePlan != nil {
		t.Fatal("legacy run was silently upgraded", c, err)
	}
	if g, c := client.counts(); g != 1 || c != 1 {
		t.Fatal(g, c)
	}
}

func TestClinicalPlanInitialIntegrityAndHumanRevision(t *testing.T) {
	s, store, h, token, point, _, client, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	id := "case-plan-integrity-0001"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, generationPayload(id, point, 1))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aiCheckSvc.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	q := waitWorkflowQuestions(t, h, token, id, 1)[0]
	c, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
	if err != nil || c == nil || c.Basis.CasePlan == nil {
		t.Fatal(c, err)
	}
	before, _ := json.Marshal(c)
	for i, mutate := range []func(*domain.A2Question, *domain.QuestionGenerationContext){
		func(q *domain.A2Question, c *domain.QuestionGenerationContext) {
			c.Basis.CasePlan.OptionEvidence[0].CaseRefs = []string{"not-in-case"}
		},
		func(q *domain.A2Question, c *domain.QuestionGenerationContext) {
			q.ClinicalStem += "心电图正常。"
		},
		func(q *domain.A2Question, c *domain.QuestionGenerationContext) { q.Answer = "B" },
	} {
		var cloned domain.QuestionGenerationContext
		_ = json.Unmarshal(before, &cloned)
		bad := q
		bad.ID = []string{"bad-case-map", "bad-case-text", "bad-case-answer"}[i]
		bad.Status = domain.StatusAIDraft
		bad.GenerationContext = &cloned
		mutate(&bad, &cloned)
		if err := store.SaveQuestion(t.Context(), bad); err == nil {
			t.Fatal("invalid initial artifact saved", bad.ID)
		}
		if found, err := store.GetQuestion(t.Context(), bad.ID); err != nil || found != nil {
			t.Fatal("partial question persisted", found, err)
		}
	}
	// Human changes are governed by the existing revision/review flow and do
	// not rewrite the immutable first-generation artifact or re-run AI checks.
	q.Explanation = ""
	q.Version++
	q.WorkflowVersion = "forged"
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterJSON, _ := json.Marshal(after)
	if string(afterJSON) != string(before) {
		t.Fatal("human edit rewrote original case plan")
	}
	saved, _ := store.GetQuestion(t.Context(), q.ID)
	if saved.WorkflowVersion != knowledgebase.CurrentClinicalWorkflowVersion || saved.Explanation != "" {
		t.Fatal("optional explanation/revision behavior changed", saved)
	}
	if _, err := s.aiCheckSvc.CheckQuestion(t.Context(), q.ID); err != nil {
		t.Fatal(err)
	}
	if g, c := client.counts(); g != 1 || c != 1 {
		t.Fatal("human edit caused generation/recheck", g, c)
	}
}
