//go:build integration

package api

import (
	"aigo/internal/domain"
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestKnowledgeCollectionHTTPReviewAndSelection(t *testing.T) {
	s, store, h, admin, point, baseCard, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	create := func(name string, perms []string) string {
		t.Helper()
		r := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": "isolation-password", "permissions": perms})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
		return loginForAuthTest(t, h, name, "isolation-password", "198.51.100.190")
	}
	teacher := create("collection-teacher", []string{domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse, domain.PermQuestionGenerate, domain.PermKBView, domain.PermKBUse, domain.PermKBSubmit})
	peer := create("collection-peer", []string{domain.PermExperienceView, domain.PermExperienceManage, domain.PermKBView, domain.PermKBUse})
	reviewer := create("collection-reviewer", []string{domain.PermKBView, domain.PermKBReview})
	call := func(method, path, token string, body any, want int) []byte {
		t.Helper()
		r := serveGenerateJSON(t, h, method, path, token, body)
		if r.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, r.Code, r.Body)
		}
		return r.Body.Bytes()
	}
	input := domain.KnowledgeCard{Kind: "custom", Title: "候选知识卡", Status: "active", KnowledgePoints: []domain.KnowledgePoint{point}, Content: domain.KnowledgeContent{CustomText: "collection-private-selected-only"}}
	var saved struct {
		Cards []domain.PrivateKnowledgeCard `json:"cards"`
	}
	json.Unmarshal(call("POST", "/api/private-knowledge", teacher, domain.KnowledgeCardSaveRequest{Key: "create", Cards: []domain.KnowledgeCard{input}}, 200), &saved)
	if len(saved.Cards) != 1 {
		t.Fatal(saved)
	}
	card := saved.Cards[0]
	call("GET", "/api/private-knowledge/"+card.ID+"/history", peer, nil, 404)
	call("PUT", "/api/knowledge-selections", teacher, map[string]any{"scope": "shared", "id": baseCard.ID, "selected": false}, 400)
	prepare := func(token string) domain.KnowledgeWorkflowSnapshot {
		t.Helper()
		var result domain.KnowledgeWorkflowSnapshot
		json.Unmarshal(call("POST", "/api/knowledge-base/prepare", token, map[string]any{"knowledge_points": []domain.KnowledgePoint{point}}, 200), &result)
		return result
	}
	has := func(snapshot domain.KnowledgeWorkflowSnapshot, id string) bool {
		for _, b := range snapshot.Bundles {
			for _, c := range b.Cards {
				if c.ID == id {
					return true
				}
			}
		}
		return false
	}
	if has(prepare(teacher), card.ID) {
		t.Fatal("unselected private card leaked")
	}
	call("PUT", "/api/knowledge-selections", teacher, map[string]any{"scope": "private", "id": card.ID, "selected": true}, 200)
	if !has(prepare(teacher), card.ID) {
		t.Fatal("selected private card missing")
	}
	call("PUT", "/api/knowledge-selections", peer, map[string]any{"scope": "private", "id": card.ID, "selected": true}, 403)
	var submissions struct {
		Submissions []domain.KnowledgeSubmission `json:"submissions"`
	}
	json.Unmarshal(call("POST", "/api/knowledge-submissions", teacher, domain.KnowledgeShareRequest{Key: "share", Cards: []domain.KnowledgeCardRef{{ID: card.ID, Revision: card.Revision}}}, 200), &submissions)
	id := submissions.Submissions[0].ID
	call("POST", "/api/knowledge-submissions/decisions", teacher, domain.KnowledgeDecisionRequest{Key: "unauthorized", IDs: []string{id}, Decision: "approved"}, 403)
	json.Unmarshal(call("POST", "/api/knowledge-submissions/decisions", reviewer, domain.KnowledgeDecisionRequest{Key: "approve", IDs: []string{id}, Decision: "approved"}, 200), &submissions)
	public := submissions.Submissions[0].SharedCardID
	if has(prepare(peer), public) {
		t.Fatal("new public card auto applied")
	}
	call("PUT", "/api/knowledge-selections", peer, map[string]any{"scope": "shared", "id": public, "selected": true}, 200)
	if !has(prepare(peer), public) {
		t.Fatal("selected shared card not applied")
	}
	call("PUT", "/api/knowledge-selections", peer, map[string]any{"scope": "shared", "id": public, "selected": false}, 200)
	if has(prepare(peer), public) {
		t.Fatal("deselected shared card still applied")
	}
	call("POST", "/api/knowledge-submissions/decisions", reviewer, domain.KnowledgeDecisionRequest{Key: "third-state", IDs: []string{id}, Decision: "revision_required"}, 400)
	// Granting review does not grant private-card access or any knowledge maintenance.
	call("GET", "/api/private-knowledge", reviewer, nil, http.StatusForbidden)
	// Private-only authors may use their explicitly selected medical card, without
	// acquiring shared knowledge rights through a private permission fallback.
	privateOnly := create("private-only", []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse})
	ownInput := input
	ownInput.Kind = "medical"
	ownInput.Source = "隔离测试资料"
	ownInput.Content.ExamTargets = []string{"测试目标"}
	ownInput.Content.Facts = []domain.KnowledgeFact{{ID: "f1", Text: "私人医学资料"}}
	var ownSaved struct {
		Cards []domain.PrivateKnowledgeCard `json:"cards"`
	}
	json.Unmarshal(call("POST", "/api/private-knowledge", privateOnly, domain.KnowledgeCardSaveRequest{Key: "private-only", Cards: []domain.KnowledgeCard{ownInput}}, 200), &ownSaved)
	call("POST", "/api/knowledge-base/prepare", privateOnly, map[string]any{"knowledge_points": []domain.KnowledgePoint{point}}, 403)
	call("PUT", "/api/knowledge-selections", privateOnly, map[string]any{"scope": "private", "id": ownSaved.Cards[0].ID, "selected": true}, 200)
	ownPrepared := prepare(privateOnly)
	if len(ownPrepared.Bundles) != 1 || len(ownPrepared.Bundles[0].Cards) != 1 || ownPrepared.Bundles[0].Cards[0].ID != ownSaved.Cards[0].ID {
		t.Fatal("shared data crossed private-only access", ownPrepared)
	}
	call("GET", "/api/knowledge-base", privateOnly, nil, 403)
	owner, e := s.authSvc.GetUserByUsername("private-only")
	if e != nil {
		t.Fatal(e)
	}
	question := seedCustomerBrowserQuestion(t, s, owner.ID, owner.Username, point, "private-context-read", "患者，男，45岁。隔离测试病例。该患者最可能的诊断是")
	frozen := domain.QuestionGenerationContext{WorkflowVersion: "knowledge-v1", Bundle: ownPrepared.Bundles[0]}
	raw, _ := json.Marshal(frozen)
	if _, e = store.DB().Exec(`INSERT INTO question_generation_context(question_id,owner_id,workflow_version,body) VALUES($1,$2,'knowledge-v1',$3)`, question.ID, owner.ID, raw); e != nil {
		t.Fatal(e)
	}
	call("GET", "/api/questions/"+question.ID+"/generation-context", privateOnly, nil, 200)
	call("GET", "/api/questions/"+question.ID, admin, nil, 200)
	call("GET", "/api/questions/"+question.ID+"/generation-context", admin, nil, 403)
	call("PUT", "/api/users/"+owner.ID, admin, map[string]any{"display_name": owner.DisplayName, "role": "", "roles": []string{}, "permissions": []string{domain.PermQuestionGenerate, domain.PermLearningUse}, "enabled": true}, 200)
	call("GET", "/api/private-knowledge", privateOnly, nil, 403)
	call("POST", "/api/knowledge-base/prepare", privateOnly, map[string]any{"knowledge_points": []domain.KnowledgePoint{point}}, 403)
	call("PUT", "/api/knowledge-selections", privateOnly, map[string]any{"scope": "private", "id": ownSaved.Cards[0].ID, "selected": true}, 403)

}

func TestLearningCandidatesBecomePrivateCardsWithoutAnotherModelCall(t *testing.T) {
	s, h, teacher, v, client, cleanup := learningFlowEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	points, e := s.questionStore.(interface {
		ListVersionPoints(context.Context, string) ([]domain.KnowledgePoint, error)
	}).ListVersionPoints(t.Context(), v)
	if e != nil || len(points) != 1 {
		t.Fatal(points, e)
	}
	owner, e := s.authSvc.GetUserByUsername("lib-teacher")
	if e != nil {
		t.Fatal(e)
	}
	q := seedCustomerBrowserQuestion(t, s, owner.ID, owner.Username, points[0], "knowledge-learning-source", "男，45岁。反复上腹痛2年。该患者最可能的诊断是")
	edit := learningRequest(&q, "")
	delete(edit, "request_key")
	edit["clinical_stem"] = "男，45岁。上腹痛2年，保留决策线索。该患者最可能的诊断是"
	r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, teacher, edit)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	current := mustLibraryQuestion(t, s, q.ID)
	r = serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/learning-analyses", teacher, learningRequest(current, "learn-card"))
	var submitted struct {
		Job domain.LearningJob `json:"job"`
	}
	if r.Code != 202 || json.Unmarshal(r.Body.Bytes(), &submitted) != nil {
		t.Fatal(r.Code, r.Body)
	}
	job := waitLearningJob(t, h, teacher, submitted.Job.ID)
	if job.Status != "succeeded" {
		t.Fatal(job)
	}
	candidate := job.Candidates[0]
	rules := append([]domain.LearningRule(nil), candidate.Rules...)
	rules[0].Text = "教师选改的规则，只导入这一条"
	req := domain.LearningImportRequest{LearningExpectation: domain.LearningExpectation{QuestionID: q.ID, ExpectedVersion: current.Version, ExpectedLibraryRevision: &current.LibraryRevision, ExpectedReviewRevision: &current.ReviewRevision}, ExpectedRevision: job.Revision, ImportKey: "candidate-card", Items: []domain.LearningImportItem{{CandidateID: candidate.ID, Title: "整理后的知识卡", Rules: rules, Status: "draft"}}}
	req.QuestionID = "" // Browser binds the source through the owned job route.
	r = serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/knowledge-cards", teacher, req)
	var result struct {
		Cards []domain.PrivateKnowledgeCard `json:"cards"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || len(result.Cards) != 1 {
		t.Fatal(r.Code, r.Body)
	}
	card := result.Cards[0]
	if card.Kind != "custom" || card.Content.CustomText != rules[0].Text || card.Origin == nil || card.Origin.CandidateID != candidate.ID || len(card.Origin.Evidence) != 1 || card.Origin.Evidence[0].Text != rules[0].Text || len(card.KnowledgePoints) == 0 {
		t.Fatal(card)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/knowledge-cards", teacher, req)
	if r.Code != 200 {
		t.Fatal("receipt", r.Code, r.Body)
	}
	reloaded := waitLearningJob(t, h, teacher, job.ID)
	if reloaded.ImportedExperiences[candidate.ID] != card.ID {
		t.Fatal("conversion missing from reopened candidates", reloaded.ImportedExperiences)
	}
	second := reloaded.Candidates[1]
	expressionRequest := req
	expressionRequest.ExpectedRevision = reloaded.Revision
	expressionRequest.ImportKey = "candidate-expression"
	expressionRequest.Items = []domain.LearningImportItem{{CandidateID: second.ID, Title: "明确表达要求", Rules: second.Rules, Status: "draft", AsExpression: true}}
	r = serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/knowledge-cards", teacher, expressionRequest)
	var expressionResult struct {
		Cards []domain.PrivateKnowledgeCard `json:"cards"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &expressionResult) != nil || len(expressionResult.Cards) != 1 {
		t.Fatal(r.Code, r.Body)
	}
	converted := expressionResult.Cards[0]
	if converted.Content.Expression == nil || converted.Content.Expression.Instructions != second.Rules[0].Text || converted.Content.CustomText != "" || converted.Origin == nil {
		t.Fatal("explicit candidate intent or evidence lost")
	}
	req.ImportKey = "forged"
	reloaded = waitLearningJob(t, h, teacher, job.ID)
	req.ExpectedRevision = reloaded.Revision
	req.Items[0].CandidateID = "not-in-model-result"
	r = serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/knowledge-cards", teacher, req)
	if r.Code != 400 {
		t.Fatal("forged candidate", r.Code, r.Body)
	}
	if client.count() != 1 {
		t.Fatal("conversion repeated paid analysis", client.count())
	}
	_, checks := client.base.counts()
	if checks != 0 {
		t.Fatal("conversion repeated mandatory check", checks)
	}
}
