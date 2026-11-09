//go:build integration

package api

import (
	"aigo/internal/domain"
	"encoding/json"
	"strings"
	"testing"
)

func TestGlobalGuidelineAdmissionFreezesAndReplaysWithoutKBPermission(t *testing.T) {
	s, store, h, token, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	admin, _ := s.authSvc.GetUserByUsername("admin")
	card, e := s.knowledgeBaseSvc.Save(t.Context(), admin.ID, admin.Role, "", domain.KnowledgeCard{Kind: "authoring_guideline", Title: "全局方法", Source: "原培训", Content: domain.KnowledgeContent{CustomText: "全局方法唯一标记A"}})
	if e != nil {
		t.Fatal(e)
	}
	card, e = s.knowledgeBaseSvc.Transition(t.Context(), admin.ID, admin.Role, card.ID, "activate", card.Revision)
	if e != nil {
		t.Fatal(e)
	}
	// Shared methods belong to the system's authoring policy, so direct authors
	// receive them even without the optional medical-KB workflow permission.
	if _, e = store.DB().Exec(`UPDATE roles SET permissions=array_remove(array_remove(permissions,'kb:view'),'kb:use') WHERE id='super_admin'`); e != nil {
		t.Fatal(e)
	}
	input := generationPayload("global-guideline-freeze-a", point, 1)
	input["workflow_mode"] = "direct"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, input)
	if r.Code != 200 && r.Code != 202 {
		t.Fatal(r.Code, r.Body)
	}
	get := func(id string) *domain.GenerationPromptSnapshot {
		run, e := store.GetGenerationRun(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		var spec struct {
			Request domain.GenerationRequest `json:"request"`
		}
		if e = json.Unmarshal(run.RequestJSON, &spec); e != nil {
			t.Fatal(e)
		}
		return spec.Request.PromptSnapshot
	}
	cliFrozen, cliErr := s.pipe.FreezeAuthoringBaseline(t.Context(), []domain.GenerationRequest{{KnowledgePoints: []domain.KnowledgePoint{point}, Count: 1}})
	if cliErr != nil || len(cliFrozen.Guidelines) != 1 || !strings.Contains(cliFrozen.SystemPrompt, "全局方法唯一标记A") {
		t.Fatal("CLI admission lost shared method", cliErr)
	}
	frozen := get("global-guideline-freeze-a")
	if frozen == nil || len(frozen.Guidelines) != 1 || !strings.Contains(frozen.SystemPrompt, "全局方法唯一标记A") {
		t.Fatal("new task lost global method", frozen)
	}
	if _, e = store.DB().Exec(`UPDATE roles SET permissions=permissions||ARRAY['kb:view','kb:use'] WHERE id='super_admin'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.knowledgeBaseSvc.Transition(t.Context(), admin.ID, admin.Role, card.ID, "archive", card.Revision); e != nil {
		t.Fatal(e)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, input)
	if r.Code != 200 && r.Code != 202 {
		t.Fatal(r.Code, r.Body)
	}
	if got := get("global-guideline-freeze-a"); got.SystemPrompt != frozen.SystemPrompt {
		t.Fatal("idempotent replay replaced original policy")
	}
	input["run_id"] = "global-guideline-freeze-b"
	r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, input)
	if r.Code != 200 && r.Code != 202 {
		t.Fatal(r.Code, r.Body)
	}
	if got := get("global-guideline-freeze-b"); len(got.Guidelines) != 0 || strings.Contains(got.SystemPrompt, "全局方法唯一标记A") {
		t.Fatal("archived global method adopted by a new task")
	}
}
