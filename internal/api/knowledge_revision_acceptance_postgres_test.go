//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage/testutil"
)

type revisionDemo struct {
	ExpertConfirmed bool                         `json:"expert_confirmed"`
	AutoImport      bool                         `json:"auto_import"`
	Document        knowledgebase.ImportDocument `json:"import_document"`
	Examples        []struct {
		Key      string                `json:"key"`
		Point    domain.KnowledgePoint `json:"knowledge_point"`
		Revision string                `json:"custom_revision_b"`
	} `json:"examples"`
}

// The external provider deliberately adopts transmitted custom text. This tests
// the application contract; real-model adoption is measured separately.
type revisionDemoClient struct {
	mu      sync.Mutex
	gen     int
	checks  int
	prompts map[string]string
}

func (c *revisionDemoClient) Complete(ctx context.Context, m []llm.Message, opts llm.GenerateOptions) (string, error) {
	prompt := m[len(m)-1].Content
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(prompt, "请检查以下 A2 型试题的质量") {
		c.checks++
		return `{"verdict":"pass","scores":{"scientific":90,"logic":90,"a2_fit":90,"answer":90},"issues":[]}`, nil
	}
	c.gen++
	if c.prompts == nil {
		c.prompts = map[string]string{}
	}
	c.prompts[llm.CallOperation(ctx)] = prompt
	raw, err := (&fakeAPILLM{}).Complete(ctx, m, opts)
	if err != nil || !strings.Contains(prompt, "<knowledge_material>") {
		return raw, err
	}
	var prepared struct {
		Materials []knowledgebase.Material `json:"materials"`
		Plans     []domain.WorkflowTarget  `json:"ordered_targets"`
	}
	section := strings.SplitN(strings.SplitN(prompt, "<knowledge_material>", 2)[1], "</knowledge_material>", 2)[0]
	if err = json.Unmarshal([]byte(section), &prepared); err != nil {
		return "", err
	}
	var medical knowledgebase.Material
	var custom string
	var reference *domain.ExamReference
	for _, card := range prepared.Materials {
		switch card.Kind {
		case "medical":
			medical = card
		case "custom":
			custom = card.Content.CustomText
		case "exam_reference":
			reference = card.Content.Reference
		}
	}
	if len(prepared.Materials) != 3 || len(medical.Content.Facts) == 0 || custom == "" || reference == nil || reference.Stem == "" {
		return "", fmt.Errorf("imported three-dimensional material lost")
	}
	var original []map[string]any
	if err = json.Unmarshal([]byte(raw), &original); err != nil {
		return "", err
	}
	var result []map[string]any
	for _, target := range prepared.Plans {
		item := map[string]any{}
		for k, v := range original[0] {
			item[k] = v
		}
		parts := strings.SplitN(custom, "。", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("synthetic custom case lacks person and clinical facts")
		}
		fact := medical.Content.Facts[0]
		plan := testutil.ClinicalPlanFixture("A", fact.ID, parts[1])
		plan.Person = parts[0] + "。"
		plan.OptionEvidence[0].Reason = fact.Text
		item["clinical_stem"], item["explanation"] = "", ""
		item["knowledge_basis"] = domain.KnowledgeBasis{TargetID: target.ID, FactRefs: []string{fact.ID}, ConditionNotes: strings.Join(medical.Content.Preconditions, "；"), AnswerSupport: fact.Text, CasePlan: plan}
		result = append(result, item)
	}
	out, err := json.Marshal(result)
	return string(out), err
}

func TestKnowledgeRevisionImportAdoptionFrozenRequestsAndRevocation(t *testing.T) {
	s, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	s.knowledgeBaseSvc = &knowledgebase.Service{Store: store}
	h := s.Handler()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.217")
	raw, err := os.ReadFile("../../tools/fixtures/knowledge_workflow_revision_demo.json")
	if err != nil {
		t.Fatal(err)
	}
	var demo revisionDemo
	if err = json.Unmarshal(raw, &demo); err != nil || demo.AutoImport || demo.ExpertConfirmed || len(demo.Examples) != 2 || len(demo.Document.Cards) != 6 {
		t.Fatal("fixture must stay explicitly synthetic", err)
	}
	version, err := s.kpSvc.CreateVersion(t.Context(), "资料修订隔离验收", 2024, "非正式大纲")
	if err != nil {
		t.Fatal(err)
	}
	points := map[string]domain.KnowledgePoint{}
	for _, ex := range demo.Examples {
		point := ex.Point
		point.VersionID = version.ID
		created, err := s.kpSvc.CreatePoint(t.Context(), point)
		if err != nil {
			t.Fatal(err)
		}
		points[ex.Key] = *created
	}
	if err = s.kpSvc.PublishVersion(t.Context(), version.ID); err != nil {
		t.Fatal(err)
	}
	permissions := []string{domain.PermKBView, domain.PermKBManage, domain.PermKBUse, domain.PermExperienceView, domain.PermExperienceManage, domain.PermLearningUse}
	create := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "revision-author", "password": "isolated-revision-password", "role": "teacher", "permissions": permissions})
	if create.Code != 201 {
		t.Fatal(create.Code, create.Body)
	}
	var author struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(create.Body.Bytes(), &author); err != nil {
		t.Fatal(err)
	}
	authorToken := loginForAuthTest(t, h, "revision-author", "isolated-revision-password", "198.51.100.218")
	create = serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "revision-other", "password": "isolated-other-password", "role": "teacher", "permissions": []string{domain.PermKBView, domain.PermKBUse, domain.PermExperienceView, domain.PermExperienceManage, domain.PermLearningUse}})
	if create.Code != 201 {
		t.Fatal(create.Code, create.Body)
	}
	otherToken := loginForAuthTest(t, h, "revision-other", "isolated-other-password", "198.51.100.219")
	for _, tc := range []struct{ token, text string }{{authorToken, "甲老师专属偏好：主诉现病史后放既往史"}, {otherToken, "乙老师专属偏好：本次不固定病史分组顺序"}} {
		r := serveGenerateJSON(t, h, "POST", "/api/personal-experiences", tc.token, map[string]any{"create_key": "revision-manual", "title": "隔离个人偏好", "scope": map[string]any{"kind": "all"}, "rules": []map[string]any{{"id": "r1", "text": tc.text, "field": "clinical_stem"}}, "status": "active"})
		if r.Code != 201 {
			t.Fatal(r.Code, r.Body)
		}
	}
	doc, _ := json.Marshal(demo.Document)
	request := knowledgebase.ImportRequest{VersionID: version.ID, Document: string(doc)}
	r := serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/preview", authorToken, request)
	var preview knowledgebase.ImportPreview
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &preview) != nil || !preview.Valid {
		t.Fatal(r.Code, r.Body)
	}
	var count int
	if err = store.DB().QueryRow(`SELECT count(*) FROM knowledge_cards`).Scan(&count); err != nil || count != 0 {
		t.Fatal("preview wrote references", count, err)
	}
	request.Token = preview.Token
	r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/import/confirm", authorToken, request)
	var imported knowledgebase.ImportResult
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &imported) != nil || imported.Saved != 6 {
		t.Fatal(r.Code, r.Body)
	}
	customA := map[string]domain.KnowledgeCard{}
	for _, importedCard := range imported.Cards {
		r = serveGenerateJSON(t, h, "POST", "/api/knowledge-base/"+importedCard.ID+"/activate", authorToken, map[string]int{"revision": importedCard.Revision})
		var card domain.KnowledgeCard
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &card) != nil {
			t.Fatal(r.Code, r.Body)
		}
		if card.Kind == "custom" {
			customA[card.KnowledgePoints[0].OutlineCode] = card
		}
	}
	client := &revisionDemoClient{}
	checker := aicheck.NewService(client, store, store, store, "controlled-revision-demo")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), evaluator.NewService(), review.NewService(store, store, store, nil), nil, store)
	s.pipe.SetDraftChecker(checker)
	s.pipe.GenerationPollInterval = time.Millisecond
	before := map[string]string{}
	for _, ex := range demo.Examples {
		id := "revision-old-" + ex.Key
		r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", authorToken, generationPayload(id, points[ex.Key], 1))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		run, err := store.GetGenerationRun(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		before[id] = string(run.RequestJSON)
	}
	customB := map[string]domain.KnowledgeCard{}
	for _, ex := range demo.Examples {
		card := customA[ex.Point.OutlineCode]
		card.Content.CustomText = ex.Revision
		path := "/api/knowledge-base/" + card.ID
		r = serveGenerateJSON(t, h, "PUT", path, authorToken, card)
		var draft domain.KnowledgeCard
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &draft) != nil || draft.Status != "draft" {
			t.Fatal(r.Code, r.Body)
		}
		if stale := serveGenerateJSON(t, h, "PUT", path, authorToken, card); stale.Code != 409 {
			t.Fatal("stale edit accepted", stale.Code, stale.Body)
		}
		if denied := serveGenerateJSON(t, h, "POST", path+"/activate", otherToken, map[string]int{"revision": draft.Revision}); denied.Code != 403 {
			t.Fatal("reader activated reference", denied.Code)
		}
		r = serveGenerateJSON(t, h, "POST", path+"/activate", authorToken, map[string]int{"revision": draft.Revision})
		var active domain.KnowledgeCard
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &active) != nil {
			t.Fatal(r.Code, r.Body)
		}
		customB[ex.Point.OutlineCode] = active
	}
	// Accepted A runs must replay without re-reading current B cards.
	for _, ex := range demo.Examples {
		id := "revision-old-" + ex.Key
		r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", authorToken, generationPayload(id, points[ex.Key], 1))
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		run, err := store.GetGenerationRun(t.Context(), id)
		if err != nil || string(run.RequestJSON) != before[id] {
			t.Fatal("replayed A request changed", err)
		}
	}
	if client.gen != 0 || client.checks != 0 {
		t.Fatal("prepare, import or durable submission invoked a model")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	s.pipe.StartGenerationWorkers(ctx, 2)
	for _, ex := range demo.Examples {
		waitWorkflowQuestions(t, h, authorToken, "revision-old-"+ex.Key, 1)
	}
	for _, tc := range []struct {
		id, key, token, mode string
		count                int
	}{
		{"revision-direct", "pneumonia", authorToken, "direct", 1},
		{"revision-new-pneumonia", "pneumonia", authorToken, "knowledge", 2},
		{"revision-new-anaphylaxis", "anaphylaxis", authorToken, "knowledge", 1},
		{"revision-other", "anaphylaxis", otherToken, "knowledge", 1},
	} {
		payload := generationPayload(tc.id, points[tc.key], tc.count)
		payload["workflow_mode"] = tc.mode
		r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", tc.token, payload)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		waitWorkflowQuestions(t, h, tc.token, tc.id, tc.count)
	}
	for _, ex := range demo.Examples {
		for _, phase := range []string{"old", "new"} {
			id, n := "revision-"+phase+"-"+ex.Key, 1
			want := customA[ex.Point.OutlineCode]
			if phase == "new" {
				want = customB[ex.Point.OutlineCode]
				if ex.Key == "pneumonia" {
					n = 2
				}
			}
			questions := waitWorkflowQuestions(t, h, authorToken, id, n)
			for _, q := range questions {
				if !strings.Contains(strings.ReplaceAll(q.ClinicalStem, "。", ""), strings.ReplaceAll(want.Content.CustomText, "。", "")) {
					t.Fatal("custom case not adopted", id)
				}
				artifact, err := store.GetQuestionGenerationContext(t.Context(), q.ID)
				if err != nil || len(artifact.Bundle.Cards) != 3 {
					t.Fatal("lost frozen imported cards", err)
				}
				found := false
				for _, c := range artifact.Bundle.Cards {
					if c.ID == want.ID && c.Revision == want.Revision && c.Content.CustomText == want.Content.CustomText {
						found = true
					}
					if len(c.KnowledgePoints) != 1 || c.KnowledgePoints[0].ID != points[ex.Key].ID {
						t.Fatal("cross-point material", id)
					}
				}
				if !found {
					t.Fatal("wrong custom reference revision", id)
				}
			}
		}
	}
	waitWorkflowQuestions(t, h, authorToken, "revision-direct", 1)
	otherQuestions := waitWorkflowQuestions(t, h, otherToken, "revision-other", 1)
	if r = serveGenerateJSON(t, h, "GET", "/api/questions/"+otherQuestions[0].ID+"/generation-context", authorToken, nil); r.Code != 403 && r.Code != 404 {
		t.Fatal("cross-owner provenance visible", r.Code)
	}
	client.mu.Lock()
	if client.gen != 6 || client.checks != 7 {
		t.Error("unexpected fan-out", client.gen, client.checks)
	}
	for operation, prompt := range client.prompts {
		if operation == "generate:revision-other" {
			if strings.Contains(prompt, "甲老师专属") || !strings.Contains(prompt, "乙老师专属") {
				t.Error("personal preferences crossed owners")
			}
		} else if strings.Contains(prompt, "乙老师专属") || !strings.Contains(prompt, "甲老师专属") {
			t.Error("author preference lost")
		}
		if operation == "generate:revision-direct" && strings.Contains(prompt, "<knowledge_material>") {
			t.Error("direct baseline received knowledge")
		}
	}
	client.mu.Unlock()
	// Schema65 grants teacher templates shared use. Revoke both the inherited
	// role grant and the direct override while keeping the same valid identity.
	if _, e := store.DB().Exec(`UPDATE roles SET permissions=array_remove(array_remove(permissions,'kb:view'),'kb:use') WHERE id='teacher'`); e != nil {
		t.Fatal(e)
	}
	// Revocation is checked using the original, still-valid login token.
	permissions = []string{domain.PermExperienceView, domain.PermExperienceManage, domain.PermLearningUse}
	r = serveGenerateJSON(t, h, "PUT", "/api/users/"+author.ID, admin, map[string]any{"display_name": "revision-author", "role": "teacher", "permissions": permissions, "bank_ids": []string{}, "enabled": true})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	card := customB[demo.Examples[0].Point.OutlineCode]
	if r = serveGenerateJSON(t, h, "PUT", "/api/knowledge-base/"+card.ID, authorToken, card); r.Code != 403 {
		t.Fatal("revoked writer retained access", r.Code)
	}
	if r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", authorToken, generationPayload("revision-revoked", points["pneumonia"], 1)); r.Code != 403 {
		t.Fatal("revoked user queued knowledge", r.Code)
	}
	if run, err := store.GetGenerationRun(t.Context(), "revision-revoked"); err != nil || run != nil {
		t.Fatal("denied request persisted", err)
	}
}
