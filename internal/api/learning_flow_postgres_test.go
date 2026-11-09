//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/learning"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/storage"
)

type c5FlowClient struct {
	base                 workflowAPIClient
	mu                   sync.Mutex
	analyses             int
	generationPrompts    []string
	generationSystems    []string
	analysisReply        *string
	analysisGate         <-chan struct{}
	analysisStarted      chan struct{}
	analysisBadFirst     bool
	analysisTimeoutFirst bool
}

func (c *c5FlowClient) Complete(ctx context.Context, messages []llm.Message, opts llm.GenerateOptions) (string, error) {
	operation := llm.CallOperation(ctx)
	if strings.HasPrefix(operation, "learning:") {
		c.mu.Lock()
		c.analyses++
		attempt := c.analyses
		c.mu.Unlock()
		if c.analysisTimeoutFirst && attempt == 1 {
			return "", context.DeadlineExceeded
		}
		if c.analysisBadFirst && attempt == 1 {
			return `{"candidates":[]}`, nil
		}
		if c.analysisStarted != nil {
			select {
			case c.analysisStarted <- struct{}{}:
			default:
			}
		}
		if c.analysisGate != nil {
			select {
			case <-c.analysisGate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if c.analysisReply != nil {
			return *c.analysisReply, nil
		}
		candidates := []domain.LearningCandidate{}
		for _, style := range []string{"concise", "detailed", "checklist"} {
			candidates = append(candidates, domain.LearningCandidate{ID: style, Style: style, Title: "表达经验" + style, Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: style + "-one", Text: "保留病例的关键决策线索，删去重复的病例表达", Reason: "减少与考核任务无关的信息", Field: "clinical_stem", Before: "旧病例表达", After: "精简病例表达"}}})
		}
		raw, _ := json.Marshal(map[string]any{"candidates": candidates})
		return string(raw), nil
	}
	if strings.HasPrefix(operation, "generate:") || strings.HasPrefix(operation, "batch:") {
		c.mu.Lock()
		c.generationPrompts = append(c.generationPrompts, messages[len(messages)-1].Content)
		c.generationSystems = append(c.generationSystems, messages[0].Content)
		c.mu.Unlock()
	}
	return c.base.Complete(ctx, messages, opts)
}
func (c *c5FlowClient) count() int { c.mu.Lock(); defer c.mu.Unlock(); return c.analyses }

func learningFlowEnv(t *testing.T) (*Server, http.Handler, string, string, *c5FlowClient, func()) {
	t.Helper()
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	perms := append(append([]string{}, owner.DirectPermissions...), domain.PermLearningAnalyze, domain.PermLearningSave, domain.PermLearningUse)
	if _, err := s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, perms, nil, nil); err != nil {
		t.Fatal(err)
	}
	client := &c5FlowClient{}
	// Tests below deliberately exercise a serialized fixture, including holding
	// its only slot before revocation. Production now defaults to two accounts.
	quotaStore := s.questionStore.(storage.GenerationQuotaStore)
	q, _, err := quotaStore.GetGenerationQuota(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	q.LearningConcurrency = 1
	if err = quotaStore.SaveGenerationQuota(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	s.learningSvc = learning.NewService(s.questionStore.(storage.LearningStore), client)
	s.learningSvc.PollInterval = 5 * time.Millisecond
	s.knowledgeBaseSvc = &knowledgebase.Service{Store: s.questionStore.(knowledgebase.Store)}
	checker := aicheck.NewService(client, s.questionStore, s.questionStore.(storage.AIReviewStore), s.questionStore.(storage.AICheckTaskStore), "c5-flow")
	s.aiCheckSvc = checker
	s.pipe = pipeline.New(generator.NewService(client), nil, s.reviewSvc, nil, s.questionStore)
	s.pipe.SetDraftChecker(checker)
	ctx, cancel := context.WithCancel(t.Context())
	s.learningSvc.StartWorkers(ctx, 1)
	checker.StartWorkers(ctx, 1)
	s.pipe.StartGenerationWorkers(ctx, 1)
	return s, h, teacher, v, client, func() {
		cancel()
		select {
		case <-s.learningSvc.WorkersDone():
		case <-time.After(3 * time.Second):
			t.Error("learning worker did not stop")
		}
		cleanup()
	}
}
func learningRequest(q *domain.A2Question, key string) map[string]any {
	return map[string]any{"request_key": key, "expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision}
}
func waitLearningJob(t *testing.T, h http.Handler, token, id string) domain.LearningJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := serveGenerateJSON(t, h, "GET", "/api/learning-analyses/"+id, token, nil)
		var body struct {
			Job domain.LearningJob `json:"job"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &body) != nil {
			t.Fatal(r.Code, r.Body)
		}
		if body.Job.Status == "succeeded" || body.Job.Status == "failed" {
			return body.Job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("learning job did not finish")
	return domain.LearningJob{}
}

func TestLearningHTTPClosedLoopWithoutReviewOrSharedKnowledgePermissions(t *testing.T) {
	runLearningHTTPClosedLoop(t, false)
}
func TestLearningProgramFieldsClosedLoop(t *testing.T) { runLearningHTTPClosedLoop(t, true) }
func runLearningHTTPClosedLoop(t *testing.T, programFields bool) {
	s, h, teacher, v, client, cleanup := learningFlowEnv(t)
	defer cleanup()
	if programFields {
		candidates := []map[string]any{}
		for _, style := range []string{"concise", "detailed", "checklist"} {
			candidates = append(candidates, map[string]any{"style": style, "title": "表达经验" + style, "scope": map[string]string{"kind": "all"}, "rules": []map[string]any{{"text": "保留病例关键线索，删去重复表达", "reason": "便于下一次命题", "field": "clinical_stem", "medical_change": false}}})
		}
		raw, _ := json.Marshal(map[string]any{"candidates": candidates})
		reply := string(raw)
		client.analysisReply = &reply
	}

	t.Chdir(t.TempDir())
	q := seedPassedQuestion(t, s, h, teacher, v, "learning-source", "男，45岁。反复上腹痛2年。该患者最可能的诊断是")
	before := serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID+"/learning-comparison", teacher, nil)
	var cmp domain.LearningComparison
	if before.Code != 200 || json.Unmarshal(before.Body.Bytes(), &cmp) != nil || cmp.CanAnalyze || len(cmp.Changes) != 0 {
		t.Fatal(before.Code, before.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/learning-analyses", teacher, learningRequest(&q, "no-change")); r.Code != 409 {
		t.Fatal("v1 analysis reached queue", r.Code, r.Body)
	}
	edit := learningRequest(&q, "")
	delete(edit, "request_key")
	edit["clinical_stem"] = "男，45岁。上腹痛2年，保留决策线索。该患者最可能的诊断是"
	if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, teacher, edit); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	current := mustLibraryQuestion(t, s, q.ID)
	read := serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID+"/learning-comparison", teacher, nil)
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &cmp) != nil || !cmp.CanAnalyze || cmp.Original.Version != 1 || cmp.Current.Version != 2 || len(cmp.Changes) != 1 {
		t.Fatal(read.Code, read.Body)
	}
	request := learningRequest(current, "learn-once")
	posted := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/learning-analyses", teacher, request)
	var submitted struct {
		Job domain.LearningJob `json:"job"`
	}
	if posted.Code != 202 || json.Unmarshal(posted.Body.Bytes(), &submitted) != nil {
		t.Fatal(posted.Code, posted.Body)
	}
	if submitted.Job.ExecutionBudgetSeconds != 420 || submitted.Job.AttemptTimeoutSeconds != 180 || submitted.Job.SamplingPolicy != domain.LearningConfiguredSampling || submitted.Job.SamplingTemperature == nil || *submitted.Job.SamplingTemperature != 0.6 || submitted.Job.PromptPolicy != domain.LearningScopedReflectionPrompt || submitted.Job.OutputPolicy != domain.LearningProgramOutput {
		t.Fatal("new job did not freeze retry-capable timing", submitted.Job)
	}
	job := waitLearningJob(t, h, teacher, submitted.Job.ID)
	if programFields && job.Status == "succeeded" {
		for _, candidate := range job.Candidates {
			if candidate.Rules[0].ID != candidate.Style+"-r1" {
				t.Fatal("program fields lost stable identifiers", candidate)
			}
		}
	}

	if job.Status != "succeeded" || len(job.Candidates) != 3 || !job.CanImport || client.count() != 1 {
		t.Fatal(job, client.count())
	}
	replay := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/learning-analyses", teacher, request)
	var repeated struct {
		Job      domain.LearningJob `json:"job"`
		Replayed bool               `json:"replayed"`
	}
	json.Unmarshal(replay.Body.Bytes(), &repeated)
	if replay.Code != 202 || repeated.Job.ID != job.ID || !repeated.Replayed || client.count() != 1 {
		t.Fatal(replay.Code, replay.Body)
	}
	_, checks := client.base.counts()
	if checks != 0 {
		t.Fatal("learning triggered another first check", checks)
	}
	items := []domain.LearningImportItem{}
	for i, candidate := range job.Candidates[:2] {
		rules := append([]domain.LearningRule{}, candidate.Rules...)
		status := "draft"
		if i == 0 {
			status = "active"
			rules[0].Text = "本次个人经验采用标记：保留决策线索，避免重复病例表达"
		}
		items = append(items, domain.LearningImportItem{CandidateID: candidate.ID, Title: "老师编辑的经验" + candidate.Style, Scope: domain.ExperienceScope{Kind: "all"}, Rules: rules, Status: status})
	}
	guard := libraryItemForAPI(*current)
	importRequest := domain.LearningImportRequest{LearningExpectation: domain.LearningExpectation{QuestionID: q.ID, ExpectedVersion: current.Version, ExpectedLibraryRevision: guard.ExpectedLibraryRevision, ExpectedReviewRevision: guard.ExpectedReviewRevision}, JobID: job.ID, ExpectedRevision: job.Revision, ImportKey: "import-once", Items: items}
	importRequest.QuestionID = "" // Frontend binds the source through the owned job route.
	imported := serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/import", teacher, importRequest)
	var saved struct {
		Experiences []domain.AuthoringExperience `json:"experiences"`
	}
	if imported.Code != 200 || json.Unmarshal(imported.Body.Bytes(), &saved) != nil || len(saved.Experiences) != 2 {
		t.Fatal(imported.Code, imported.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/learning-analyses/"+job.ID+"/import", teacher, importRequest); r.Code != 200 {
		t.Fatal("import retry duplicated or rejected receipt", r.Code, r.Body)
	}
	list := serveGenerateJSON(t, h, "GET", "/api/personal-experiences?limit=1&offset=0", teacher, nil)
	var listing struct {
		Total       int
		Experiences []domain.AuthoringExperience `json:"experiences"`
	}
	json.Unmarshal(list.Body.Bytes(), &listing)
	if list.Code != 200 || listing.Total != 2 || len(listing.Experiences) != 1 {
		t.Fatal(list.Code, list.Body)
	}
	if r := serveGenerateJSON(t, h, "GET", "/api/knowledge-base", teacher, nil); r.Code != 403 {
		t.Fatal("learning permissions leaked shared KB", r.Code, r.Body)
	}
	generated := serveGenerateJSON(t, h, "POST", "/api/questions/generate", teacher, map[string]any{"run_id": "learn-adopted-run", "version_id": v, "outline_code": "lib.001", "count": 1, "apply_personal_experiences": true})
	if generated.Code != 200 {
		t.Fatal(generated.Code, generated.Body)
	}
	var createdID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := serveGenerateJSON(t, h, "GET", "/api/generation-runs/learn-adopted-run", teacher, nil)
		var result struct {
			Questions []domain.A2Question `json:"questions"`
		}
		json.Unmarshal(r.Body.Bytes(), &result)
		if len(result.Questions) == 1 {
			createdID = result.Questions[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if createdID == "" {
		t.Fatal("generation and mandatory check did not finish")
	}
	adoption := serveGenerateJSON(t, h, "GET", "/api/questions/"+createdID+"/personal-experiences", teacher, nil)
	var evidence struct {
		Snapshot *domain.PersonalExperienceSnapshot `json:"snapshot"`
	}
	if adoption.Code != 200 || json.Unmarshal(adoption.Body.Bytes(), &evidence) != nil || evidence.Snapshot == nil || len(evidence.Snapshot.Entries) != 1 || evidence.Snapshot.Entries[0].Revision != 1 {
		t.Fatal(adoption.Code, adoption.Body)
	}
	client.mu.Lock()
	prompts := append([]string{}, client.generationPrompts...)
	client.mu.Unlock()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "本次个人经验采用标记") {
		t.Fatal("saved experience did not reach next generation")
	}
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, createdID)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/export/xlsx", teacher, map[string]any{"scope": "personal", "question_ids": []string{createdID}}); r.Code != 200 {
		t.Fatal("learning disrupted zero-round export", r.Code, r.Body)
	}
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.244")
	if r := serveGenerateJSON(t, h, "GET", "/api/questions/"+createdID+"/personal-experiences", admin, nil); r.Code != 403 {
		t.Fatal("admin read another person's experience adoption", r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "GET", "/api/knowledge-base?kind=authoring_experience", admin, nil); r.Code == 200 && strings.Contains(r.Body.String(), "本次个人经验采用标记") {
		t.Fatal("private card appeared in shared KB")
	}
	if client.count() != 1 {
		t.Fatal("saving/generating/exporting secretly reanalyzed", client.count())
	}
	// Freeze before admission; stopping/editing the live card must not change a
	// queued batch or the private evidence saved with each generated question.
	limiter := storage.NewConcurrencyLimiter(1)
	release, err := limiter.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	executor := batch.NewLocalExecutorWithConfig(generator.NewService(client), s.questionStore, s.questionStore.(storage.BatchJobStore), "c5-test", batch.LocalConfig{Context: t.Context(), Limiter: limiter, Checker: s.aiCheckSvc, Pipeline: true, Concurrency: 1, CheckMaxAttempts: 3, RecoveryInterval: 20 * time.Millisecond})
	defer executor.Close()
	s.batchSvc = executor
	response := serveGenerateJSON(t, h, "POST", "/api/batch/submit", teacher, map[string]any{"version_id": v, "outline_codes": "lib.001", "mode": "random_total", "total_count": 2, "request_key": "experience-batch-frozen", "apply_personal_experiences": true})
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &accepted) != nil {
		t.Fatal(response.Code, response.Body)
	}
	var active domain.AuthoringExperience
	for _, card := range saved.Experiences {
		if card.Status == "active" {
			active = card
		}
	}
	updatedRules := append([]domain.LearningRule{}, active.Rules...)
	updatedRules[0].Text = "已停止使用的第二版经验，不能倒灌旧任务"
	changed := serveGenerateJSON(t, h, "PUT", "/api/personal-experiences/"+active.ID, teacher, map[string]any{"expected_revision": active.Revision, "title": active.Title, "scope": active.Scope, "rules": updatedRules, "status": "draft"})
	if changed.Code != 200 {
		t.Fatal(changed.Code, changed.Body)
	}
	release()
	var record *storage.BatchJobRecord
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		record, err = s.questionStore.(storage.BatchJobStore).GetBatchJob(t.Context(), accepted.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if record != nil && record.Status == "completed" && record.Completed == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if record == nil || record.Completed != 2 {
		t.Fatal("frozen batch did not finish", record)
	}
	var frozen domain.PersonalExperienceSnapshot
	if json.Unmarshal([]byte(record.ExperienceSnapshotJSON), &frozen) != nil || len(frozen.Entries) != 1 || frozen.Entries[0].Revision != 1 {
		t.Fatal("batch persisted current mutable cards instead of admitted snapshot", record)
	}
	var output struct {
		Items []struct {
			QuestionID string `json:"question_id"`
		} `json:"items"`
	}
	json.Unmarshal([]byte(record.OutputJSON), &output)
	if len(output.Items) != 2 || strings.Contains(record.OutputJSON, "pending_personal_experiences") {
		t.Fatal("checkpoint lost results or retained repeated private payload", record.OutputJSON)
	}
	for _, item := range output.Items {
		reader := s.questionStore.(interface {
			GetQuestionPersonalExperiences(context.Context, string) (*domain.PersonalExperienceSnapshot, error)
		})
		snapshot, e := reader.GetQuestionPersonalExperiences(t.Context(), item.QuestionID)
		if e != nil || snapshot == nil || snapshot.Entries[0].Revision != 1 {
			t.Fatal("batch question lost atomic frozen adoption", snapshot, e)
		}
	}
	client.mu.Lock()
	prompts = append([]string{}, client.generationPrompts...)
	client.mu.Unlock()
	for _, prompt := range prompts {
		if strings.Contains(prompt, "已停止使用的第二版经验") {
			t.Fatal("live card edit rewrote an old generation task")
		}
	}
}
