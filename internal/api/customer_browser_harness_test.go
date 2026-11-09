//go:build integration

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/learning"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/storage"
)

// TestCustomerBrowserAcceptance is deliberately opt-in. It owns a new schema,
// never reuses an existing user's questions, and only substitutes the LLM client.
// The Python driver exercises the production build through real HTTP and Chrome.
func TestCustomerBrowserAcceptance(t *testing.T) {
	root := os.Getenv("AIGO_C6_BROWSER_ROOT")
	if root == "" {
		t.Skip("set AIGO_C6_BROWSER_ROOT to an owned acceptance directory")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	var resources struct {
		Root, Database string
		Ports          []int
	}
	raw, err := os.ReadFile(filepath.Join(root, "resources.json"))
	if err != nil || json.Unmarshal(raw, &resources) != nil || filepath.Clean(resources.Root) != filepath.Clean(root) || !strings.HasPrefix(resources.Database, "aigo_c6") {
		t.Fatal("owned resources.json is required")
	}
	dsn, err := url.Parse(os.Getenv("AIGO_TEST_POSTGRES_DSN"))
	if err != nil || dsn == nil || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") || !net.ParseIP(dsn.Hostname()).IsLoopback() || strings.TrimPrefix(dsn.Path, "/") != resources.Database || len(resources.Ports) < 2 || dsn.Query().Get("search_path") != "" || dsn.Port() != strconv.Itoa(resources.Ports[0]) || resources.Ports[1] != 15672 {
		t.Fatal("explicit isolated loopback aigo_c6 PostgreSQL DSN required")
	}
	project, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(project, "tools/e2e/customer_acceptance.py")
	s, store, _, admin, point, _, _, cleanup := setupWorkflowAPI(t)
	defer cleanup()
	store.SetLLMEncryptionKey("browser-summary-config-fixture-secret")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client := &c5FlowClient{}
	modelClient := &delayedCustomerClient{client: client}
	checker := aicheck.NewService(modelClient, store, store, store, "c6-controlled-no-cloud")
	pipe := pipeline.New(generator.NewService(modelClient), nil, s.reviewSvc, nil, store)
	pipe.SetDraftChecker(checker)
	quota, _, err := store.GetGenerationQuota(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := storage.NewGenerationConcurrencyRuntime(quota)
	if err != nil {
		t.Fatal(err)
	}
	pipe.GenerationLimiter = runtime.Generation
	checker.WorkerLimiter = runtime.AICheck
	executor := batch.NewLocalExecutorWithConfig(generator.NewService(modelClient), store, store, "c6-controlled-no-cloud", batch.LocalConfig{Context: ctx, Limiter: runtime.Batch, Concurrency: 2, Pipeline: true, Checker: checker, CheckMaxAttempts: 3})
	defer executor.Close()
	s = NewServer(pipe, s.kpSvc, s.reviewSvc, s.auditSvc, store, s.authSvc, executor, checker, s.bankSvc, nil, true, true)
	s.SetGenerationConcurrencyRuntime(runtime)
	ls := learning.NewService(store, modelClient)
	s.SetLearningService(ls)
	h := s.Handler()
	type account struct {
		Username string `json:"username"`
		Password string `json:"password"`
		ID       string `json:"id"`
	}
	accounts := map[string]account{}
	accounts["admin"] = account{Username: "admin", Password: "admin-password"}
	teacherPerms := []string{domain.PermQuestionView, domain.PermQuestionEdit, domain.PermQuestionConfirm, domain.PermQuestionDownload, domain.PermQuestionGenerate, domain.PermBatchRun, domain.PermStatsView, domain.PermQuestionShare, domain.PermLearningAnalyze, domain.PermLearningSave, domain.PermLearningUse, domain.PermExperienceView, domain.PermExperienceManage}
	for _, item := range []struct {
		key, name   string
		permissions []string
		roles       []string
	}{
		{"teacher", "lib-teacher", teacherPerms, nil},
		{"business_admin", "c6-business-admin", nil, []string{domain.RoleAdmin}},
		{"other", "c6-other", []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermQuestionDownload, domain.PermQuestionConfirm}, nil},
		{"expert", "c6-expert", []string{domain.PermQuestionView, domain.PermReviewDo}, nil},
		{"review_author", "c6-review-author", []string{domain.PermQuestionView, domain.PermQuestionEdit, domain.PermQuestionConfirm, domain.PermQuestionDownload, domain.PermReviewSubmit}, nil},
		{"zero", "c6-zero", []string{}, nil},
		{"legacy", "c6-legacy-reader", []string{domain.PermQuestionViewFormal, domain.PermQuestionDownload}, nil},
		{"dual", "c6-dual", nil, []string{domain.RoleExpert, domain.RoleTeacher}},
		{"knowledge_reviewer", "c6-knowledge-reviewer", []string{domain.PermKBView, domain.PermKBReview}, nil},
		{"manual", "c6-manual", []string{domain.PermExperienceView, domain.PermExperienceManage}, nil},
		{"manual_author", "c6-manual-author", []string{domain.PermQuestionView, domain.PermQuestionGenerate, domain.PermQuestionConfirm, domain.PermExperienceView, domain.PermExperienceManage, domain.PermExperienceUse}, nil},
	} {
		passwordBytes := make([]byte, 16)
		if _, err := rand.Read(passwordBytes); err != nil {
			t.Fatal(err)
		}
		password := "C6-" + hex.EncodeToString(passwordBytes)
		response := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": item.name, "password": password, "display_name": "C6 " + item.key, "permissions": item.permissions, "roles": item.roles})
		if response.Code != 201 {
			t.Fatal(response.Code, response.Body)
		}
		owner, err := s.authSvc.GetUserByUsername(item.name)
		if err != nil {
			t.Fatal(err)
		}
		accounts[item.key] = account{Username: item.name, Password: password, ID: owner.ID}
	}
	points := []domain.KnowledgePoint{point}
	for i, topic := range []string{"C6临床鉴别诊断", "C6临床处理决策"} {
		added, err := s.kpSvc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: point.VersionID, Category: "临床综合", Subject: "消化", Unit: "C6验收", SubItem: "临床病例", Topic: topic, OutlineCode: []string{"wf.002", "wf.003"}[i]})
		if err != nil {
			t.Fatal(err)
		}
		points = append(points, *added)
	}
	// A separate draft syllabus exercises binding pagination/scrolling without
	// changing the three published generation candidates or the default version.
	bindingVersion, err := s.kpSvc.CreateVersion(ctx, "C6绑定专项目录", 2025, "controlled binding fixture")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 45; i++ {
		_, err := s.kpSvc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: bindingVersion.ID, Category: "临床综合", Subject: "绑定科室" + strconv.Itoa(i), Unit: "病例资料", SubItem: "适用范围", Topic: "浏览绑定考点" + strconv.Itoa(i), OutlineCode: "bind." + strconv.Itoa(i)})
		if err != nil {
			t.Fatal(err)
		}
	}
	seedCustomerBrowserQuestion(t, s, accounts["dual"].ID, accounts["dual"].Username, point, "c6-dual-mobile-editor", "女，60岁。"+strings.Repeat("长内容应在小屏幕内完整换行并可修改。", 20)+"该患者最可能的诊断是")
	teacher := loginForAuthTest(t, h, accounts["teacher"].Username, accounts["teacher"].Password, "198.51.100.200")
	ids := map[string]string{}
	for _, item := range []struct{ id, stem string }{
		{"c6-ui-source", "男，45岁。C6学习源题，反复上腹痛2年。该患者最可能的诊断是"},
		{"c6-ui-second", "女，55岁。C6第二题，上腹不适3天。该患者最可能的诊断是"},
		{"c6-ui-share", "男，48岁。C6公共原始快照，上腹痛1天。该患者最可能的诊断是"},
		{"c6-ui-long", "女，60岁。" + strings.Repeat("既往病史、症状变化与体检结果在这里按自然段核对，长内容不应遮住保存和导出入口。\n", 30) + "该患者最可能的诊断是"},
	} {
		q := seedCustomerBrowserQuestion(t, s, accounts["teacher"].ID, accounts["teacher"].Username, point, item.id, item.stem)
		ids[item.id] = q.ID
	}
	share := mustLibraryQuestion(t, s, ids["c6-ui-share"])
	if response := confirmViaAPI(t, h, teacher, map[string]any{"items": []any{libraryItemForAPI(*share)}}); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	q := seedCustomerBrowserQuestion(t, s, accounts["review_author"].ID, accounts["review_author"].Username, point, "c6-ui-review", "男，42岁。C6可选一轮审核，上腹痛2天。该患者最可能的诊断是")
	ids["c6-ui-review"] = q.ID
	flow := domain.ReviewFlowConfig{ID: "c6-ui-one-round", Name: "C6一轮末轮完成", CompletionMode: domain.ReviewCompletionOnLastRound, Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{accounts["expert"].ID}, RequiredCount: 1}}}
	if err := s.reviewSvc.CreateFlow(ctx, flow); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "app"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(root, "app"))
	nonceBytes := make([]byte, 24)
	if _, err := rand.Read(nonceBytes); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	static, err := WithStaticFrontend(h, filepath.Join(root, "ui-dist"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:15672")
	if err != nil {
		t.Fatal(err)
	}
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/__c6_fixture/"+nonce {
			writeJSON(w, 200, map[string]string{"protocol": "aigo-c6-browser-v1", "nonce": nonce, "model": "controlled-client-no-provider-http"})
			return
		}
		static.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: fixtureHandler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = srv.Shutdown(shutdownCtx)
		<-done
	}()
	checker.StartWorkers(ctx, 2)
	pipe.StartGenerationWorkers(ctx, 2)
	ls.StartWorkers(ctx, 1)
	defer func() {
		cancel()
		select {
		case <-ls.WorkersDone():
		case <-time.After(3 * time.Second):
			t.Error("learning worker did not stop")
		}
	}()
	fixture := map[string]any{"protocol": "aigo-c6-browser-v1", "root": root, "base_url": "http://127.0.0.1:15672", "nonce": nonce, "accounts": accounts, "points": points, "question_ids": ids, "flow_id": flow.ID, "binding_version": bindingVersion.ID, "model": "controlled-client-no-provider-http", "question_review_ui": os.Getenv("AIGO_C6_QUESTION_REVIEW_UI") == "true"}
	fixturePath := filepath.Join(root, "browser-fixture.json")
	if err := os.WriteFile(fixturePath, mustJSONBrowser(t, fixture), 0600); err != nil {
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithTimeout(ctx, 12*time.Minute)
	defer stopRun()
	command := exec.CommandContext(runCtx, "python3", script, "--fixture-json", fixturePath, "--artifact-dir", filepath.Join(root, "browser"))
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		t.Fatal("browser acceptance:", err)
	}
	generations, checks := client.base.counts()
	client.mu.Lock()
	prompts := append([]string(nil), client.generationPrompts...)
	systems := append([]string(nil), client.generationSystems...)
	client.mu.Unlock()
	if os.Getenv("AIGO_C6_BROWSER_FOCUS") == "binding" {
		if generations != 0 || checks != 0 || client.count() != 0 {
			t.Fatal("binding-only test unexpectedly called a model")
		}
		t.Log("binding-only real browser acceptance; provider0")
		return
	}
	adopted, manualAdopted, globalAdopted := false, false, false
	reviewedAdopted := 0
	for _, system := range systems {
		globalAdopted = globalAdopted || strings.Contains(system, "C6全局规范真实采用")
	}
	for _, prompt := range prompts {
		if strings.Contains(prompt, "C6审核共享采用标记") {
			reviewedAdopted++
		}
		adopted = adopted || strings.Contains(prompt, "C6已启用个人经验采用标记")
		globalAdopted = globalAdopted || strings.Contains(prompt, "C6全局规范真实采用")
		manualAdopted = manualAdopted || strings.Contains(prompt, "C6手工知识真实采用")
	}
	if generations != 11 || checks != 11 || client.count() != 3 || !adopted || !manualAdopted || !globalAdopted || reviewedAdopted < 3 {
		t.Fatalf("unexpected controlled model fanout/adoption: generation=%d check=%d learning=%d adopted=%v", generations, checks, client.count(), adopted)
	}
	t.Logf("controlled LLM only: generation=%d first_check=%d learning=%d actual_prompt_adoption=%v provider_http=0", generations, checks, client.count(), adopted)
}

func mustJSONBrowser(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func seedCustomerBrowserQuestion(t *testing.T, s *Server, owner, name string, point domain.KnowledgePoint, id, stem string) domain.A2Question {
	t.Helper()
	q := domain.A2Question{ID: id, OwnerID: owner, CreatedBy: name, Version: 1, Status: domain.StatusAIReviewed, ClinicalStem: stem, Options: []domain.Option{{Label: "A", Text: "胃溃疡"}, {Label: "B", Text: "十二指肠溃疡"}, {Label: "C", Text: "胃癌"}, {Label: "D", Text: "慢性胃炎"}, {Label: "E", Text: "功能性消化不良"}}, Answer: "A", Explanation: "正确答案为 A。该患者反复上腹痛，结合疼痛特点考虑胃溃疡。B 项疼痛规律更符合十二指肠溃疡；C 项缺乏消瘦等警示表现；D 项缺乏消瘦等警示表现；E 项应在排除器质性疾病后考虑。故选 A。", Difficulty: "0.65", KnowledgePoints: []domain.KnowledgePoint{point}, OutlineCode: point.OutlineCode}
	ctx := storage.WithQuestionChange(t.Context(), storage.QuestionChange{Actor: name, OwnerID: owner})
	if err := s.questionStore.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := s.questionStore.(storage.AIReviewStore).SaveReviewResult(ctx, domain.AIReviewResult{ID: "c6-browser-pass-" + id, QuestionID: id, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return *mustLibraryQuestion(t, s, id)
}

// Keep opt-in browser tasks asynchronous instead of relying on an instant LLM.
// This controlled 150ms delay is not a production model-latency simulation.
type delayedCustomerClient struct{ client *c5FlowClient }

func (c *delayedCustomerClient) Complete(ctx context.Context, messages []llm.Message, options llm.GenerateOptions) (string, error) {
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
	}
	return c.client.Complete(ctx, messages, options)
}
