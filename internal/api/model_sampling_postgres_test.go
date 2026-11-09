//go:build integration

package api

import (
	"aigo/internal/domain"
	"encoding/json"
	"testing"
)

func TestModelSamplingDefaultsPermissionsRevisionAndFrozenTasks(t *testing.T) {
	s, store, h, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	get := func() domain.ModelSamplingConfig {
		t.Helper()
		r := serveGenerateJSON(t, h, "GET", "/api/system/ai-sampling", admin, nil)
		var c domain.ModelSamplingConfig
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &c) != nil {
			t.Fatal(r.Code, r.Body)
		}
		return c
	}
	c := get()
	if c.GenerationTemperature != 0.4 || c.CheckTemperature != 0.2 || c.Revision != 0 {
		t.Fatal(c)
	}
	input := generationPayload("sampling-before", point, 1)
	if r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", admin, input); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	frozen := func(id string) float64 {
		t.Helper()
		run, e := store.GetGenerationRun(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		var spec struct {
			Request domain.GenerationRequest `json:"request"`
		}
		if json.Unmarshal(run.RequestJSON, &spec) != nil || spec.Request.PromptSnapshot.GenerationTemperature == nil {
			t.Fatal("temperature not frozen")
		}
		return *spec.Request.PromptSnapshot.GenerationTemperature
	}
	if frozen("sampling-before") != 0.4 {
		t.Fatal("default changed")
	}
	c.GenerationTemperature, c.CheckTemperature = 0, 0.6
	if r := serveGenerateJSON(t, h, "PUT", "/api/system/ai-sampling", admin, c); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "PUT", "/api/system/ai-sampling", admin, c); r.Code != 409 {
		t.Fatal("stale save overwrote parameters", r.Code)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", admin, input); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if frozen("sampling-before") != 0.4 {
		t.Fatal("idempotent replay changed accepted temperature")
	}
	input["run_id"] = "sampling-after"
	if r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", admin, input); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if frozen("sampling-after") != 0 {
		t.Fatal("explicit zero lost")
	}
	cli, e := s.pipe.FreezeAuthoringBaseline(t.Context(), []domain.GenerationRequest{{KnowledgePoints: []domain.KnowledgePoint{point}, Count: 1}})
	if e != nil || cli.GenerationTemperature == nil || *cli.GenerationTemperature != 0 {
		t.Fatal("CLI did not use sampling", e)
	}
	q := domain.A2Question{ID: "sampling-check", ClinicalStem: "患者，男，45岁。该患者最可能的诊断是", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}}, Answer: "A", Version: 1, Status: domain.StatusAIDraft}
	if e := store.SaveQuestion(t.Context(), q); e != nil {
		t.Fatal(e)
	}
	if _, e := store.EnqueueCheckTask(t.Context(), domain.AICheckTask{ID: "sampling-check-task", QuestionID: q.ID, QuestionVersion: 1, Status: domain.AICheckTaskPending, MaxAttempts: 3}); e != nil {
		t.Fatal(e)
	}
	c = get()
	c.CheckTemperature = 0.2
	if e := store.SaveModelSamplingConfig(t.Context(), c); e != nil {
		t.Fatal(e)
	}
	tasks, e := store.LatestCheckTasksByQuestionIDs(t.Context(), []string{q.ID})
	if e != nil || tasks[q.ID].SamplingTemperature == nil || *tasks[q.ID].SamplingTemperature != 0.6 {
		t.Fatal("queued check temperature changed", e, tasks)
	}
	r := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "sampling-teacher", "password": "sampling-password", "permissions": []string{domain.PermQuestionView, domain.PermQuestionGenerate}})
	if r.Code != 201 {
		t.Fatal(r.Code, r.Body)
	}
	teacher := loginForAuthTest(t, h, "sampling-teacher", "sampling-password", "198.51.100.201")
	for _, method := range []string{"GET", "PUT"} {
		if r := serveGenerateJSON(t, h, method, "/api/system/ai-sampling", teacher, c); r.Code != 403 {
			t.Fatal("sampling accessible to teacher", r.Code)
		}
	}
	bad := get()
	bad.GenerationTemperature = 2.1
	if r := serveGenerateJSON(t, h, "PUT", "/api/system/ai-sampling", admin, bad); r.Code != 400 {
		t.Fatal("invalid temperature allowed", r.Code)
	}
}
