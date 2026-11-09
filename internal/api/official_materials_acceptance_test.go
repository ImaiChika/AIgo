//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/config"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/learning"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/storage/postgres"
)

type officialCapture struct {
	mu         sync.Mutex
	gen, check llm.Client
	calls      int
	inputs     []map[string]any
}

func (c *officialCapture) Complete(ctx context.Context, m []llm.Message, o llm.GenerateOptions) (string, error) {
	checking := strings.HasSuffix(llm.CallOperation(ctx), ":first_check")
	c.mu.Lock()
	limit := 20
	if os.Getenv("AIGO_OFFICIAL_RETEST") == "1" {
		limit = 3
	}
	if c.calls >= limit {
		c.mu.Unlock()
		return "", fmt.Errorf("official local comparison call budget reached")
	}
	c.calls++
	c.inputs = append(c.inputs, map[string]any{"operation": llm.CallOperation(ctx), "checking": checking, "messages": m})
	c.mu.Unlock()
	if checking {
		return c.check.Complete(ctx, m, o)
	}
	return c.gen.Complete(ctx, m, o)
}

// Opt-in small local comparison. No fixture is automatically seeded at startup.
// Source files and sample questions remain separate from production data.
func TestOfficialMaterialsLocalAcceptance(t *testing.T) {
	root := os.Getenv("AIGO_OFFICIAL_ACCEPTANCE_ROOT")
	if root == "" {
		t.Skip("explicit owned local acceptance root required")
	}
	if !filepath.IsAbs(root) {
		t.Fatal("absolute owned root required")
	}
	raw, e := os.ReadFile("../../tools/fixtures/official_authoring_materials.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Import   knowledgebase.ImportDocument `json:"import_document"`
		Personal struct {
			Title string                `json:"title"`
			Rules []domain.LearningRule `json:"rules"`
		} `json:"personal_example"`
	}
	if e = json.Unmarshal(raw, &fixture); e != nil || len(fixture.Import.Cards) != 5 {
		t.Fatal(e)
	}
	s, store, h, token, _, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	admin, _ := s.authSvc.GetUserByUsername("admin")
	version, e := s.kpSvc.CreateVersion(t.Context(), "官方资料小样验收", 2026, "")
	if e != nil {
		t.Fatal(e)
	}
	points := []domain.KnowledgePoint{}
	for _, p := range []domain.KnowledgePoint{{Category: "临床综合", Subject: "一、呼吸系统", Unit: "肺炎", SubItem: "概述", Topic: "诊断与鉴别诊断", OutlineCode: "110.4.1.5.1.5"}, {Category: "临床综合", Subject: "三、消化系统", Unit: "食管、胃、十二指肠疾病", SubItem: "消化性溃疡", Topic: "并发症及其诊断治疗", OutlineCode: "110.4.3.1.6.8"}} {
		p.VersionID = version.ID
		got, e := s.kpSvc.CreatePoint(t.Context(), p)
		if e != nil {
			t.Fatal(e)
		}
		points = append(points, *got)
	}
	if e = s.kpSvc.PublishVersion(t.Context(), version.ID); e != nil {
		t.Fatal(e)
	}
	cards := []domain.KnowledgeCard{}
	for _, input := range fixture.Import.Cards {
		bound := []domain.KnowledgePoint{}
		for _, code := range input.PointCodes {
			for _, point := range points {
				if point.OutlineCode == code {
					bound = append(bound, point)
				}
			}
		}
		card, e := s.knowledgeBaseSvc.Save(t.Context(), admin.ID, admin.Role, "", domain.KnowledgeCard{Kind: input.Kind, Title: input.Title, Source: input.Source, Note: input.Note, KnowledgePoints: bound, Content: input.Content})
		if e != nil {
			t.Fatal(e)
		}
		cards = append(cards, card)
	}
	var gen, check llm.Client = &workflowAPIClient{}, &workflowAPIClient{}
	real := os.Getenv("AIGO_OFFICIAL_REAL") == "1"
	retest := os.Getenv("AIGO_OFFICIAL_RETEST") == "1"
	if real {
		cfg, e := config.Load()
		if e != nil {
			t.Fatal("private local config unavailable")
		}
		// Read the current local provider only; all questions/calls are written to the isolated store.
		daily, e := postgres.New(cfg.DB.DSN)
		if e != nil {
			t.Fatal("local config database unavailable")
		}
		defer daily.Close()
		daily.SetLLMEncryptionKey(cfg.JWTSecret)
		active, e := daily.GetActiveAIProviderConfig(t.Context())
		if e != nil {
			t.Fatal("current model config unavailable")
		}
		gc, cc := cfg.Qwen, cfg.AICheck.Client
		if active != nil {
			gc.Deployment = llm.DeploymentMode(active.Deployment)
			gc.BaseURL = active.BaseURL
			gc.APIKey = active.APIKey
			gc.Model = active.GenerationModel
			if !cfg.AICheck.Independent {
				cc = gc
				cc.Model = active.CheckModel
			}
		}
		if gc.Deployment != llm.DeploymentCloud || !strings.Contains(gc.BaseURL, "dashscope.aliyuncs.com") {
			t.Fatal("this limited experiment requires the current official cloud endpoint")
		}
		recovery := llm.NewEndpointRecovery()
		makeClient := func(p llm.ConfigPurpose, cfg llm.QwenConfig) *llm.DynamicQwenClient {
			c := llm.NewDynamicQwenClient(nil, p, cfg)
			c.SetCallRecorder(store)
			c.SetNetworkToolsResolver(store)
			c.SetEndpointRecovery(recovery)
			return c
		}
		gen, check = makeClient(llm.PurposeGeneration, gc), makeClient(llm.PurposeAICheck, cc)
	}
	capture := &officialCapture{gen: gen, check: check}
	checker := aicheck.NewService(capture, store, store, store, "official-materials-small-comparison")
	pipe := pipeline.New(generator.NewService(capture), nil, s.reviewSvc, nil, store)
	pipe.SetDraftChecker(checker)
	s.pipe = pipe
	s.aiCheckSvc = checker
	s.SetLearningService(learning.NewService(store, nil))
	h = s.Handler()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	checker.StartWorkers(ctx, 2)
	pipe.StartGenerationWorkers(ctx, 2)
	tasks := []map[string]any{}
	defer func() {
		capture.mu.Lock()
		defer capture.mu.Unlock()
		var calls, tokens int
		if err := store.DB().QueryRow(`SELECT count(*),COALESCE(sum((record->>'total_tokens')::bigint),0) FROM model_call_records WHERE (record->>'http_attempted')::boolean`).Scan(&calls, &tokens); err != nil {
			t.Error(err)
		}
		report := map[string]any{"real": real, "calls_started": capture.calls, "http": calls, "reported_tokens": tokens, "tasks": tasks, "inputs": capture.inputs}
		data, _ := json.MarshalIndent(report, "", "  ")
		if e := os.WriteFile(filepath.Join(root, "official-comparison.json"), data, 0600); e != nil {
			t.Error(e)
		}
		t.Logf("OFFICIAL_LOCAL real=%t calls=%d reported_tokens=%d", real, calls, tokens)
	}()
	run := func(name string, p domain.KnowledgePoint, mode string, personal bool) {
		input := generationPayload("official-"+name, p, 1)
		input["workflow_mode"] = mode
		input["apply_personal_experiences"] = personal
		r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, input)
		if r.Code != 200 {
			t.Fatal(name, r.Code, r.Body)
		}
		started := time.Now()
		deadline := started.Add(8 * time.Minute)
		for time.Now().Before(deadline) {
			run, e := store.GetGenerationRun(t.Context(), "official-"+name)
			if e != nil {
				t.Fatal(e)
			}
			if run.Status == domain.GenerationRunFailed {
				tasks = append(tasks, map[string]any{"name": name, "error": run.Error})
				t.Errorf("%s generation failed", name)
				return
			}
			if len(run.QuestionIDs) > 0 {
				_, progress, e := checker.ProgressByQuestionIDs(t.Context(), run.QuestionIDs)
				if e != nil {
					t.Fatal(e)
				}
				if progress["passed"]+progress["discarded"]+progress["exhausted"] == 1 {
					item := map[string]any{"name": name, "ms": time.Since(started).Milliseconds(), "progress": progress}
					id := run.QuestionIDs[0]
					q, e := store.GetQuestion(t.Context(), id)
					if e != nil {
						t.Fatal(e)
					}
					item["question"] = q
					if q == nil {
						discards, _ := store.ListDiscardResultsByQuestionIDs(t.Context(), []string{id})
						item["discard"] = discards[id]
					}
					var snapshot struct {
						Request domain.GenerationRequest `json:"request"`
					}
					json.Unmarshal(run.RequestJSON, &snapshot)
					item["guidelines"] = snapshot.Request.PromptSnapshot.Guidelines
					item["personal"] = snapshot.Request.PersonalExperiences
					tasks = append(tasks, item)
					t.Logf("OFFICIAL_TASK %s ms=%d pass=%d discarded=%d exhausted=%d", name, time.Since(started).Milliseconds(), progress["passed"], progress["discarded"], progress["exhausted"])
					if progress["exhausted"] > 0 {
						t.Errorf("%s exhausted", name)
					}
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("official comparison did not terminate", name)
	}
	if !retest {
		for i, p := range points {
			run(fmt.Sprintf("base-%d", i), p, "direct", false)
		}
	}
	if !retest {
		var e2 error
		cards[0], e2 = s.knowledgeBaseSvc.Transition(t.Context(), admin.ID, admin.Role, cards[0].ID, "activate", cards[0].Revision)
		if e2 != nil {
			t.Fatal(e2)
		}
		for i, p := range points {
			run(fmt.Sprintf("global-%d", i), p, "direct", false)
		}

	}
	for i := 1; i < len(cards); i++ {
		cards[i], e = s.knowledgeBaseSvc.Transition(t.Context(), admin.ID, admin.Role, cards[i].ID, "activate", cards[i].Revision)
		if e != nil {
			t.Fatal(e)
		}
	}
	if retest {
		run("retest-knowledge", points[1], "knowledge", false)
		return
	}
	for i, p := range points {
		run(fmt.Sprintf("knowledge-%d", i), p, "knowledge", false)
	}
	_, e = s.learningSvc.CreateExperience(t.Context(), admin.ID, admin.Role, domain.PersonalKnowledgeCreateRequest{CreateKey: "official-personal-example", Title: fixture.Personal.Title, Scope: domain.ExperienceScope{Kind: "point", PointIDs: []string{points[0].ID}}, Rules: fixture.Personal.Rules, Status: "active"})
	if e != nil {
		t.Fatal(e)
	}
	run("personal", points[0], "knowledge", true)
}
