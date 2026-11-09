//go:build integration

package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"aigo/internal/aicheck"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/pipeline"
)

func TestKnowledgeExpressionSelectionSharingFrozenRenderingAndFirstCheck(t *testing.T) {
	s, store, h, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	call := func(token, method, path string, input any, want int) []byte {
		t.Helper()
		r := serveGenerateJSON(t, h, method, path, token, input)
		if r.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, r.Code, r.Body)
		}
		return r.Body.Bytes()
	}
	create := func(name string, perms []string) string {
		t.Helper()
		call(admin, "POST", "/api/users", map[string]any{"username": name, "password": "expression-test-password", "permissions": perms}, 201)
		return loginForAuthTest(t, h, name, "expression-test-password", "198.51.100.211")
	}
	perms := []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermQuestionEdit, domain.PermQuestionConfirm, domain.PermKBView, domain.PermKBUse, domain.PermKBSubmit, domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse}
	owner, peer := create("expression-author", perms), create("expression-peer", perms)
	reviewer := create("expression-reviewer", []string{domain.PermKBView, domain.PermKBReview})
	note := "教学提示：先比较本例证据。"
	instruction := "每项理由先说明本例依据。"
	input := domain.KnowledgeCard{Title: "显式表达", Kind: "custom", Status: "active", KnowledgePoints: []domain.KnowledgePoint{point}, Note: "PRIVATE_MAINTENANCE_NOT_FOR_MODEL", Content: domain.KnowledgeContent{Expression: &domain.ExpressionRequirements{Instructions: instruction, ExplanationNote: note}}}
	var saved struct {
		Cards []domain.PrivateKnowledgeCard `json:"cards"`
	}
	json.Unmarshal(call(owner, "POST", "/api/private-knowledge", domain.KnowledgeCardSaveRequest{Key: "expression-create", Cards: []domain.KnowledgeCard{input}}, 200), &saved)
	if len(saved.Cards) != 1 {
		t.Fatal(saved)
	}
	private := saved.Cards[0]
	prepare := func(token string) domain.KnowledgeWorkflowSnapshot {
		var value domain.KnowledgeWorkflowSnapshot
		json.Unmarshal(call(token, "POST", "/api/knowledge-base/prepare", map[string]any{"knowledge_points": []domain.KnowledgePoint{point}}, 200), &value)
		return value
	}
	if prepare(owner).Version != knowledgebase.CurrentClinicalWorkflowVersion {
		t.Fatal("unselected intent changed ordinary workflow")
	}
	call(peer, "PUT", "/api/knowledge-selections", map[string]any{"scope": "private", "id": private.ID, "selected": true}, 404)
	call(owner, "PUT", "/api/knowledge-selections", map[string]any{"scope": "private", "id": private.ID, "selected": true}, 200)
	if snapshot := prepare(owner); snapshot.Version != knowledgebase.ExpressionWorkflowVersion || snapshot.ReferenceProtocol != "" {
		t.Fatal("selected intent has no versioned workflow")
	}
	var summary struct {
		Supported bool                          `json:"supports_expression"`
		Items     []domain.KnowledgeCardSummary `json:"items"`
	}
	json.Unmarshal(call(owner, "GET", "/api/private-knowledge?view=summary", nil, 200), &summary)
	if !summary.Supported || len(summary.Items) != 1 || !summary.Items[0].HasExpression || summary.Items[0].Excerpt != instruction {
		t.Fatal("summary lost expression identity", summary)
	}
	var submissions struct {
		Submissions []domain.KnowledgeSubmission `json:"submissions"`
	}
	json.Unmarshal(call(owner, "POST", "/api/knowledge-submissions", domain.KnowledgeShareRequest{Key: "share-expression", Cards: []domain.KnowledgeCardRef{{ID: private.ID, Revision: private.Revision}}}, 200), &submissions)
	json.Unmarshal(call(reviewer, "POST", "/api/knowledge-submissions/decisions", domain.KnowledgeDecisionRequest{Key: "approve-expression", IDs: []string{submissions.Submissions[0].ID}, Decision: "approved"}, 200), &submissions)
	shared := submissions.Submissions[0].SharedCardID
	if prepare(peer).Version != knowledgebase.CurrentClinicalWorkflowVersion {
		t.Fatal("reviewed expression auto applied to peer")
	}
	call(peer, "PUT", "/api/knowledge-selections", map[string]any{"scope": "shared", "id": shared, "selected": true}, 200)
	if prepare(peer).Version != knowledgebase.ExpressionWorkflowVersion {
		t.Fatal("shared expression not adopted")
	}
	for i, token := range []string{owner, peer} {
		call(token, "POST", "/api/questions/generate", generationPayload([]string{"expression-own-run", "expression-shared-run"}[i], point, 1), 200)
	}
	changed := private.KnowledgeCard
	changed.Content.Expression = &domain.ExpressionRequirements{Instructions: "下一版本规则", ExplanationNote: "下一版本附言。"}
	call(owner, "POST", "/api/private-knowledge", domain.KnowledgeCardSaveRequest{Key: "expression-revise", Cards: []domain.KnowledgeCard{changed}}, 200)
	client := &c5FlowClient{}
	checker := aicheck.NewService(client, store, store, store, "expression-controlled")
	s.pipe = pipeline.New(generator.NewService(client), nil, s.reviewSvc, nil, store)
	s.pipe.SetDraftChecker(checker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	for i, token := range []string{owner, peer} {
		q := waitWorkflowQuestions(t, h, token, []string{"expression-own-run", "expression-shared-run"}[i], 1)[0]
		if q.WorkflowVersion != knowledgebase.ExpressionWorkflowVersion || strings.Count(q.Explanation, note) != 1 || strings.Contains(q.Explanation, "下一版本") {
			t.Fatal("frozen note missing, duplicated or changed", q.Explanation)
		}
		frozen, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = knowledgebase.ValidateContext(frozen); err != nil {
			t.Fatal(err)
		}
		if len(frozen.Basis.CasePlan.ExplanationNotes) != 1 || frozen.Basis.CasePlan.ExplanationNotes[0] != note {
			t.Fatal("rendering lost note provenance")
		}
		frozen.Basis.CasePlan.ExplanationNotes = []string{"model-invented-note"}
		if knowledgebase.ValidateContext(frozen) == nil {
			t.Fatal("invented note was accepted")
		}
		if i == 0 {
			call(token, "PUT", "/api/questions/"+q.ID, map[string]any{"expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision, "explanation": "老师可移除附言并自主编写解析。"}, 200)
			current, err := store.GetQuestion(t.Context(), q.ID)
			if err != nil || current.Explanation != "老师可移除附言并自主编写解析。" {
				t.Fatal("teacher could not freely revise generated note", err)
			}
		}
		call(token, "POST", "/api/questions/confirm", libraryItemsPayload(t, s, q.ID), 200)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.generationPrompts) != 2 {
		t.Fatal("unexpected model calls")
	}
	for _, prompt := range client.generationPrompts {
		if !strings.Contains(prompt, "<author_expression_requirements>") || !strings.Contains(prompt, instruction) || strings.Contains(prompt, "PRIVATE_MAINTENANCE") || strings.Contains(prompt, "下一版本") {
			t.Fatal("expression channel or privacy/freeze wrong")
		}
	}
	client.base.mu.Lock()
	defer client.base.mu.Unlock()
	if client.base.checks != 2 {
		t.Fatal("expression created extra or missing first checks")
	}
	for _, prompt := range client.base.checkPrompts {
		if !strings.Contains(prompt, note) {
			t.Fatal("note bypassed first check")
		}
	}
	call(admin, "POST", "/api/knowledge-base", input, 400)
}
