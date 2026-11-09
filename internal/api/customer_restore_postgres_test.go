//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/auth"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/learning"
	"aigo/internal/pipeline"
	"aigo/internal/storage"
	"aigo/internal/storage/postgres"
	"github.com/lib/pq"
)

// TestCustomerPopulatedBackupRestore is a native, opt-in whole-database drill.
// Both source and destination are new databases; the supplied database is used
// only for CREATE/DROP DATABASE. No historical data or normal local DB is read.
func TestCustomerPopulatedBackupRestore(t *testing.T) {
	if os.Getenv("AIGO_C6_RESTORE_ACCEPTANCE") != "1" {
		t.Skip("set AIGO_C6_RESTORE_ACCEPTANCE=1 for the native isolated restore drill")
	}
	u, err := url.Parse(os.Getenv("AIGO_TEST_POSTGRES_DSN"))
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Hostname() != "127.0.0.1" || u.Port() == "" || !strings.HasPrefix(u.Path, "/aigo_c6") || u.Query().Get("search_path") != "" {
		t.Fatal("restore drill requires an explicit loopback aigo_c6* management database without search_path")
	}
	bin := os.Getenv("AIGO_PG_BIN")
	if !filepath.IsAbs(bin) {
		t.Fatal("set AIGO_PG_BIN to the absolute native PostgreSQL binary directory")
	}
	admin, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := fmt.Sprint(time.Now().UnixNano())
	sourceURL, targetURL := *u, *u
	sourceURL.Path, targetURL.Path = "/aigo_c6_source_"+suffix, "/aigo_c6_restore_"+suffix
	for _, name := range []string{sourceURL.Path[1:], targetURL.Path[1:]} {
		if _, err = admin.ExecContext(t.Context(), `CREATE DATABASE `+pq.QuoteIdentifier(name)+` TEMPLATE template0`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, e := admin.ExecContext(cleanupCtx, `DROP DATABASE `+pq.QuoteIdentifier(name)+` WITH (FORCE)`); e != nil {
				t.Error("owned restore database cleanup failed", e)
			}
		}()
	}
	t.Setenv("AIGO_TEST_POSTGRES_DSN", sourceURL.String())
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	store := s.questionStore.(*postgres.Store)
	owner, err := s.authSvc.GetUserByUsername("lib-teacher")
	if err != nil {
		t.Fatal(err)
	}
	perms := append(slices.Clone(owner.DirectPermissions), domain.PermLearningAnalyze, domain.PermLearningSave, domain.PermLearningUse, domain.PermQuestionShare, domain.PermKBView, domain.PermKBUse, domain.PermKBSubmit)
	owner, err = s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, perms, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &c5FlowClient{}
	s.learningSvc = learning.NewService(store, client)
	s.learningSvc.PollInterval = 5 * time.Millisecond
	s.knowledgeBaseSvc = &knowledgebase.Service{Store: store}
	s.aiCheckSvc = aicheck.NewService(client, store, store, store, "c6-native-restore")
	s.pipe = pipeline.New(generator.NewService(client), nil, s.reviewSvc, nil, store)
	s.pipe.SetDraftChecker(s.aiCheckSvc)
	s.pipe.GenerationPollInterval = 5 * time.Millisecond
	liveCtx, stopLive := context.WithCancel(t.Context())
	defer stopLive()
	s.learningSvc.StartWorkers(liveCtx, 1)
	s.aiCheckSvc.StartWorkers(liveCtx, 1)
	s.pipe.StartGenerationWorkers(liveCtx, 1)
	q := seedPassedQuestion(t, s, h, teacher, version, "c6-restore-learning-source", "患者，男，45岁。旧病例表达。最可能的诊断是")
	edit := learningRequest(&q, "")
	delete(edit, "request_key")
	edit["clinical_stem"] = "患者，男，45岁。精简病例表达。最可能的诊断是"
	if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, teacher, edit); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	q = *mustLibraryQuestion(t, s, q.ID)
	job, _, err := s.learningSvc.Submit(t.Context(), owner.ID, owner.Role, domain.LearningSubmitRequest{LearningExpectation: domain.LearningExpectation{QuestionID: q.ID, ExpectedVersion: q.Version, ExpectedLibraryRevision: &q.LibraryRevision, ExpectedReviewRevision: &q.ReviewRevision}, RequestKey: "c6-learning-completed"})
	if err != nil {
		t.Fatal(err)
	}
	completedJob := waitLearningJob(t, h, teacher, job.ID)
	if completedJob.Status != "succeeded" || len(completedJob.Candidates) != 3 {
		t.Fatal("initial controlled learning failed", completedJob)
	}
	candidate := completedJob.Candidates[0]
	candidate.Rules[0].Text = "C6-FROZEN-V1：保留考核任务的关键决策线索"
	importRequest := domain.LearningImportRequest{LearningExpectation: domain.LearningExpectation{QuestionID: q.ID, ExpectedVersion: q.Version, ExpectedLibraryRevision: &q.LibraryRevision, ExpectedReviewRevision: &q.ReviewRevision}, JobID: job.ID, ExpectedRevision: completedJob.Revision, ImportKey: "c6-native-import", Items: []domain.LearningImportItem{{CandidateID: candidate.ID, Title: candidate.Title, Scope: candidate.Scope, Rules: candidate.Rules, Status: "active"}}}
	cards, replayed, err := s.learningSvc.Import(t.Context(), owner.ID, owner.Role, importRequest)
	if err != nil || replayed || len(cards) != 1 {
		t.Fatal("initial import failed", replayed, len(cards), err)
	}
	if r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", teacher, map[string]any{"run_id": "c6-completed-generation", "version_id": version, "outline_code": "lib.001", "count": 1, "apply_personal_experiences": true}); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	completed := waitWorkflowQuestions(t, h, teacher, "c6-completed-generation", 1)[0]
	if r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, completed.ID)); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+completed.ID+"/preference-feedback", teacher, map[string]any{"question_version": completed.Version, "revision": 0, "rating": "not_met", "comment": "恢复后应保留的个人反馈"}); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	// Share the generated question which really adopted a private experience;
	// a separate fixture without adoption would not prove snapshot isolation.
	shared := *mustLibraryQuestion(t, s, completed.ID)
	shareResponse := serveGenerateJSON(t, h, "POST", "/api/questions/"+shared.ID+"/share", teacher, map[string]any{"expected_version": shared.Version, "expected_library_revision": shared.LibraryRevision, "expected_review_revision": shared.ReviewRevision})
	var shareView questionShareView
	if shareResponse.Code != 201 || json.Unmarshal(shareResponse.Body.Bytes(), &shareView) != nil {
		t.Fatal(shareResponse.Code, shareResponse.Body)
	}
	adminToken := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.233")
	if r := serveGenerateJSON(t, h, "POST", "/api/question-shares/"+shareView.Request.ID+"/review", adminToken, map[string]any{"status": "approved", "expected_share_revision": shareView.Request.Revision}); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	sharedEdit := learningRequest(&shared, "")
	delete(sharedEdit, "request_key")
	sharedEdit["clinical_stem"] = "患者，男，45岁。PRIVATE-SECOND-VERSION。最可能的诊断是"
	if r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+shared.ID, teacher, sharedEdit); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	stopLive()
	select {
	case <-s.learningSvc.WorkersDone():
	case <-time.After(3 * time.Second):
		t.Fatal("initial learning worker did not stop")
	}
	// With no generation/check work left, create unfinished frozen jobs after
	// stopping the workers. Restored workers must execute these exact snapshots.
	if r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", teacher, map[string]any{"run_id": "c6-pending-generation", "version_id": version, "outline_code": "lib.001", "count": 1, "apply_personal_experiences": true}); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	pending, err := store.GetGenerationRun(t.Context(), "c6-pending-generation")
	if err != nil || pending.Status != domain.GenerationRunPending {
		t.Fatal("pending generation was not durable", pending, err)
	}
	var frozen pipeline.GenerationSpec
	if json.Unmarshal(pending.RequestJSON, &frozen) != nil || frozen.Request.PersonalExperiences == nil || len(frozen.Request.PersonalExperiences.Entries) != 1 {
		t.Fatal("pending generation did not freeze the private experience")
	}
	plan, err := batch.DrawRandomPlan(frozen.Request.KnowledgePoints, version, 2)
	if err != nil {
		t.Fatal(err)
	}
	planJSON, _ := json.Marshal(plan)
	pointsJSON, _ := json.Marshal(plan.UniquePlanPoints())
	experienceJSON, _ := json.Marshal(frozen.Request.PersonalExperiences)
	pendingBatch := storage.BatchJobRecord{ID: "c6-pending-batch", OwnerID: owner.ID, Backend: "local_single_api", BackendProfile: "single-question-pipeline", Model: "c6-native-restore", JobName: "C6恢复随机冻结计划", Status: "in_progress", TotalCount: 2, PointsJSON: string(pointsJSON), PlanJSON: string(planJSON), ExperienceSnapshotJSON: string(experienceJSON), OutputJSON: `{"questions":[],"items":[],"pipeline":true,"creator":"lib-teacher","check_max_attempts":3}`, CreatedAt: time.Now().Format(time.RFC3339Nano)}
	if err = store.SaveBatchJob(t.Context(), pendingBatch); err != nil {
		t.Fatal(err)
	}
	pendingJob, _, err := s.learningSvc.Submit(t.Context(), owner.ID, owner.Role, domain.LearningSubmitRequest{LearningExpectation: importRequest.LearningExpectation, RequestKey: "c6-learning-pending", Note: "保留第二个主动请求的冻结依据"})
	if err != nil || pendingJob.Status != "pending" {
		t.Fatal("pending learning was not durable", pendingJob, err)
	}
	updated := cards[0]
	updated.Rules = slices.Clone(updated.Rules)
	updated.Status, updated.Rules[0].Text = "draft", "C6-LIVE-V2-MUST-NOT-APPEAR"
	if _, err = s.learningSvc.SaveExperience(t.Context(), owner.ID, owner.Role, updated.ID, updated, updated.Revision); err != nil {
		t.Fatal(err)
	}
	// Explicitly revoke template rights before the backup; a startup must not
	// infer that missing permissions should be restored from built-in defaults.
	if _, err = store.DB().ExecContext(t.Context(), `UPDATE roles SET permissions=array_remove(array_remove(array_remove(permissions,'learning:analyze'),'learning:save'),'learning:use'),updated_at=NOW() WHERE id='teacher'`); err != nil {
		t.Fatal(err)
	}
	// Populate the pre-C5 shared KB and private knowledge through their normal
	// services. The manual card belongs to admin, so it cannot alter the
	// teacher's already frozen generation/batch contracts.
	administrator, err := s.authSvc.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	manualKnowledge, err := s.learningSvc.CreateExperience(t.Context(), administrator.ID, administrator.Role, domain.PersonalKnowledgeCreateRequest{CreateKey: "restore-manual", Title: "恢复手工个人知识", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "manual-text", Text: "C6-MANUAL-KNOWLEDGE：解析按选项分段表达", Field: "clinical_stem"}}, Status: "active"})
	if err != nil {
		t.Fatal("could not seed manual private knowledge", err)
	}

	oldCard, err := s.knowledgeBaseSvc.Save(t.Context(), administrator.ID, administrator.Role, "", domain.KnowledgeCard{Kind: "medical", Title: "C6恢复原有共享医学卡", Source: "C6本地受控恢复夹具", KnowledgePoints: frozen.Request.KnowledgePoints, Content: domain.KnowledgeContent{ExamTargets: []string{"诊断与鉴别诊断"}, Facts: []domain.KnowledgeFact{{ID: "c6-old-fact", Text: "C6-OLD-KB：恢复时保留的原有知识卡正文", AppliesWhen: "本地恢复验收夹具"}}}})
	if err != nil {
		t.Fatal("could not seed the existing shared knowledge service", err)
	}
	oldCard, err = s.knowledgeBaseSvc.Transition(t.Context(), administrator.ID, administrator.Role, oldCard.ID, "activate", oldCard.Revision)
	if err != nil {
		t.Fatal("could not activate the shared knowledge fixture", err)
	}
	const encryptionSecret = "c6-native-restore-fixture-secret"
	provider := domain.AIProviderConfig{
		ID: "c6-encrypted-provider", Name: "C6加密恢复夹具", Deployment: "local",
		BaseURL: "http://127.0.0.1:1/v1", BatchBaseURL: "http://127.0.0.1:1/batch",
		APIKey: "c6-fixture-generation-key-not-valid", BatchAPIKey: "c6-fixture-batch-key-not-valid",
		GenerationModel: "c6-fixture-generation", CheckModel: "c6-fixture-check", BatchModel: "c6-fixture-batch",
		Active: true,
	}
	store.SetLLMEncryptionKey(encryptionSecret)
	if err = store.SaveAIProviderConfig(t.Context(), provider); err != nil {
		t.Fatal("could not save encrypted provider fixture")
	}
	summaryConfig := domain.LearningProviderConfig{Independent: true, Deployment: "cloud", BaseURL: "https://summary.invalid/v1", APIKey: "restore-summary-fixture-key", Model: "qwen3.8-flash", Temperature: 0.6}
	if err = store.SaveLearningProviderConfig(t.Context(), summaryConfig); err != nil {
		t.Fatal("could not save summary restore fixture")
	}
	assertEncryptedProvider := func(db *sql.DB) {
		t.Helper()
		var ciphertext, batchCiphertext string
		if err := db.QueryRowContext(t.Context(), `SELECT api_key_ciphertext,batch_api_key_ciphertext FROM ai_provider_configs WHERE id=$1`, provider.ID).Scan(&ciphertext, &batchCiphertext); err != nil {
			t.Fatal("encrypted provider row is missing")
		}
		if ciphertext == "" || batchCiphertext == "" || strings.Contains(ciphertext, provider.APIKey) || strings.Contains(batchCiphertext, provider.BatchAPIKey) {
			t.Fatal("provider credentials were not encrypted; sensitive values suppressed")
		}
	}
	assertEncryptedProvider(store.DB())

	// Populate every Schema65 table through the same private/review services.
	privateCards, err := s.knowledgeBaseSvc.SavePrivate(t.Context(), owner.ID, owner.Role, domain.KnowledgeCardSaveRequest{Key: "restore-private-card", Cards: []domain.KnowledgeCard{{Title: "恢复私人知识卡", Kind: "custom", Status: "active", KnowledgePoints: oldCard.KnowledgePoints, Content: domain.KnowledgeContent{CustomText: "restore-new-private-marker"}}}}, []*domain.ExperienceSource{{Kind: "learning", JobID: completedJob.ID, CandidateID: completedJob.Candidates[2].ID, OriginalVersion: completedJob.OriginalVersion, CurrentVersion: completedJob.CurrentVersion, Evidence: completedJob.Candidates[2].Rules}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.knowledgeBaseSvc.ChooseCard(t.Context(), owner.ID, owner.Role, "private", privateCards[0].ID, true); err != nil {
		t.Fatal(err)
	}
	requests, err := s.knowledgeBaseSvc.SubmitCards(t.Context(), owner.ID, owner.Role, domain.KnowledgeShareRequest{Key: "restore-submit-card", Cards: []domain.KnowledgeCardRef{{ID: privateCards[0].ID, Revision: privateCards[0].Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := s.knowledgeBaseSvc.DecideCards(t.Context(), administrator.ID, administrator.Role, domain.KnowledgeDecisionRequest{Key: "restore-approve-card", IDs: []string{requests[0].ID}, Decision: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	if decisions[0].SharedCardID == "" {
		t.Fatal("review did not publish")
	}
	sampling, err := store.GetModelSamplingConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sampling.GenerationTemperature, sampling.CheckTemperature = 0.6, 0.6
	if err = store.SaveModelSamplingConfig(t.Context(), sampling); err != nil {
		t.Fatal(err)
	}
	sampling, err = store.GetModelSamplingConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err = store.DB().QueryRowContext(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	before := customerRestoreFingerprint(t, store.DB(), schema)
	for _, table := range []string{"model_sampling_config", "personal_knowledge_cards", "personal_knowledge_versions", "knowledge_card_choices", "knowledge_collection_operations", "knowledge_submissions", "knowledge_cards", "knowledge_card_versions", "ai_provider_configs", "learning_provider_config", "learning_analysis_jobs", "personal_experiences", "personal_experience_versions", "learning_import_operations", "question_experience_contexts", "question_share_requests", "generation_runs", "batch_jobs", "questions", "question_versions", "ai_check_tasks", "schema_migrations"} {
		if value, ok := before[table]; !ok || strings.HasPrefix(value, "0:") {
			t.Fatalf("required populated table %s is missing or empty: %q", table, value)
		}
	}
	artifact := filepath.Join(t.TempDir(), "c6-whole-database.dump")
	runNative := func(name string, args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(t.Context(), filepath.Join(bin, name), args...).CombinedOutput(); err != nil {
			t.Fatalf("native %s failed (%d diagnostic bytes, credentials suppressed)", name, len(output))
		}
	}
	runNative("pg_dump", "--format=custom", "--no-owner", "--no-acl", "--file="+artifact, sourceURL.String())
	runNative("pg_restore", "--list", artifact)
	runNative("pg_restore", "--exit-on-error", "--single-transaction", "--no-owner", "--no-acl", "--dbname="+targetURL.String(), artifact)
	query := targetURL.Query()
	query.Set("search_path", schema)
	targetURL.RawQuery = query.Encode()
	restored, err := postgres.New(targetURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after := customerRestoreFingerprint(t, restored.DB(), schema)
	if !reflect.DeepEqual(before, after) {
		for table, value := range before {
			if after[table] != value {
				t.Errorf("restored table differs: %s before=%s after=%s", table, value, after[table])
			}
		}
		t.Fatal("populated restore fingerprint mismatch")
	}
	assertEncryptedProvider(restored.DB())
	restored.SetLLMEncryptionKey("c6-fixture-wrong-restore-secret")
	badConfig, badKeyErr := restored.GetActiveAIProviderConfig(t.Context())
	if badKeyErr == nil || badConfig != nil {
		t.Fatal("wrong restoration secret must explicitly reject encrypted configuration")
	}
	if strings.Contains(badKeyErr.Error(), provider.APIKey) || strings.Contains(badKeyErr.Error(), provider.BatchAPIKey) {
		t.Fatal("decryption diagnostic exposed provider credentials")
	}
	restored.SetLLMEncryptionKey(encryptionSecret)
	recoveredProvider, err := restored.GetActiveAIProviderConfig(t.Context())
	if err != nil || recoveredProvider == nil || recoveredProvider.ID != provider.ID || recoveredProvider.APIKey != provider.APIKey || recoveredProvider.BatchAPIKey != provider.BatchAPIKey || recoveredProvider.BaseURL != provider.BaseURL || recoveredProvider.BatchBaseURL != provider.BatchBaseURL || recoveredProvider.GenerationModel != provider.GenerationModel || recoveredProvider.CheckModel != provider.CheckModel || recoveredProvider.BatchModel != provider.BatchModel || !recoveredProvider.Active || !recoveredProvider.APIKeyConfigured || !recoveredProvider.BatchAPIKeyConfigured {
		t.Fatal("same restoration secret must recover the active provider and both fixture credentials; values suppressed")
	}
	publicProviderJSON, err := json.Marshal(recoveredProvider)
	if err != nil || strings.Contains(string(publicProviderJSON), provider.APIKey) || strings.Contains(string(publicProviderJSON), provider.BatchAPIKey) {
		t.Fatal("provider JSON exposed decrypted credentials")
	}
	summaryRestored, summaryErr := restored.GetLearningProviderConfig(t.Context())
	if summaryErr != nil || summaryRestored == nil || !summaryRestored.Independent || summaryRestored.Model != summaryConfig.Model || summaryRestored.APIKey != summaryConfig.APIKey || summaryRestored.Temperature != 0.6 {
		t.Fatal("summary config restoration failed; credentials suppressed")
	}
	if err = restored.CheckSchemaVersion(t.Context()); err != nil {
		t.Fatal(err)
	}
	restoredSampling, err := restored.GetModelSamplingConfig(t.Context())
	if err != nil || restoredSampling != sampling {
		t.Fatal("sampling configuration changed during restore", restoredSampling, err)
	}
	if applied, err := restored.Migrate(t.Context()); err != nil || len(applied) != 0 {
		t.Fatal("restored schema must already be current", applied, err)
	}
	restartedAuth := auth.NewService(restored.DB(), "auth-handler-test-secret", time.Hour)
	if err = restartedAuth.InitBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	role, err := restartedAuth.GetRole(t.Context(), "teacher")
	if err != nil || slices.Contains(role.Permissions, domain.PermLearningAnalyze) || slices.Contains(role.Permissions, domain.PermLearningSave) || slices.Contains(role.Permissions, domain.PermLearningUse) {
		t.Fatal("restart regranted explicitly revoked learning rights", role, err)
	}
	manualCards, err := learning.NewService(restored, client).ListExperiences(t.Context(), administrator.ID, administrator.Role, true)
	manualFound := false
	for _, card := range manualCards {
		if card.ID == manualKnowledge.ID {
			manualFound = card.OwnerID == administrator.ID && card.Revision == manualKnowledge.Revision && card.Status == "active" && reflect.DeepEqual(card.Rules, manualKnowledge.Rules) && card.Source.Kind == "manual"
		}
	}
	if err != nil || !manualFound {
		t.Fatal("restore changed manual private knowledge", err)
	}

	restoredCards, err := (&knowledgebase.Service{Store: restored}).Detail(t.Context(), administrator.ID, administrator.Role, oldCard.ID, false)
	if err != nil || len(restoredCards) != 1 || restoredCards[0].Revision != oldCard.Revision || restoredCards[0].Status != "active" || restoredCards[0].Kind != "medical" || !reflect.DeepEqual(restoredCards[0].Content, oldCard.Content) {
		t.Fatal("restore changed the active shared medical card body/revision/status", err)
	}
	got, err := restored.GetGenerationRun(t.Context(), pending.ID)
	if err != nil || got == nil {
		t.Fatal("restore lost pending generation", err)
	}
	// JSONB is semantically authoritative; field order may be normalized.
	var left, right any
	if json.Unmarshal(pending.RequestJSON, &left) != nil || json.Unmarshal(got.RequestJSON, &right) != nil || !reflect.DeepEqual(left, right) {
		t.Fatal("restore changed pending generation snapshot")
	}
	restoredLearning := learning.NewService(restored, client)
	restoredLearning.PollInterval = 5 * time.Millisecond
	replayedCards, replayed, err := restoredLearning.Import(t.Context(), owner.ID, owner.Role, importRequest)
	if err != nil || !replayed || len(replayedCards) != 1 || replayedCards[0].ID != cards[0].ID {
		t.Fatal("restored import receipt did not replay the original experience", replayed, len(replayedCards), err)
	}
	history, err := restoredLearning.ExperienceVersions(t.Context(), owner.ID, owner.Role, cards[0].ID)
	if err != nil || len(history) != 2 || history[0].Revision != 2 || history[1].Revision != 1 || !strings.Contains(history[1].Rules[0].Text, "C6-FROZEN-V1") {
		t.Fatal("restore changed private immutable experience history", history, err)
	}
	check := aicheck.NewService(client, restored, restored, restored, "c6-native-restore")
	pipe := pipeline.New(generator.NewService(client), nil, nil, nil, restored)
	pipe.SetDraftChecker(check)
	pipe.GenerationPollInterval = 5 * time.Millisecond
	resumeCtx, stopResume := context.WithCancel(t.Context())
	defer func() {
		stopResume()
		select {
		case <-restoredLearning.WorkersDone():
		case <-time.After(3 * time.Second):
			t.Error("restored learning worker did not stop")
		}
	}()
	restoredLearning.StartWorkers(resumeCtx, 1)
	check.StartWorkers(resumeCtx, 1)
	pipe.StartGenerationWorkers(resumeCtx, 1)
	executor := batch.NewLocalExecutorWithConfig(generator.NewService(client), restored, restored, "c6-native-restore", batch.LocalConfig{Context: resumeCtx, Concurrency: 1, Checker: check, Pipeline: true, CheckMaxAttempts: 3, RecoveryInterval: 10 * time.Millisecond})
	defer executor.Close()
	deadline := time.Now().Add(12 * time.Second)
	var newIDs []string
	resumed := false
	for time.Now().Before(deadline) {
		run, err := restored.GetGenerationRun(t.Context(), pending.ID)
		if err != nil {
			t.Fatal(err)
		}
		batchRecord, err := restored.GetBatchJob(t.Context(), pendingBatch.ID)
		if err != nil {
			t.Fatal(err)
		}
		job, err := restoredLearning.GetJob(t.Context(), owner.ID, owner.Role, pendingJob.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.ExecutionBudgetSeconds != pendingJob.ExecutionBudgetSeconds || job.AttemptTimeoutSeconds != pendingJob.AttemptTimeoutSeconds || job.SamplingPolicy != pendingJob.SamplingPolicy || job.PromptPolicy != pendingJob.PromptPolicy || job.AnalysisTemperature() != pendingJob.AnalysisTemperature() {
			t.Fatal("restore changed frozen learning timing", job)
		}
		if run.Status == domain.GenerationRunSucceeded && len(run.QuestionIDs) == 1 && batchRecord.Status == "completed" && batchRecord.Completed == 2 && job.Status == "succeeded" {
			var output struct {
				Items []struct {
					QuestionID string `json:"question_id"`
				} `json:"items"`
			}
			if json.Unmarshal([]byte(batchRecord.OutputJSON), &output) != nil || len(output.Items) != 2 || batchRecord.PlanJSON != pendingBatch.PlanJSON || batchRecord.ExperienceSnapshotJSON != pendingBatch.ExperienceSnapshotJSON {
				t.Fatal("resumed batch changed frozen plan/experience or lost result IDs")
			}
			newIDs = slices.Clone(run.QuestionIDs)
			for _, item := range output.Items {
				newIDs = append(newIDs, item.QuestionID)
			}
			_, progress, err := check.ProgressByQuestionIDs(t.Context(), newIDs)
			if err == nil && progress["passed"] == 3 {
				resumed = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !resumed {
		t.Fatal("restored generation/batch/learning/first-check queues did not complete")
	}
	for _, id := range append(newIDs, completed.ID) {
		snapshot, err := restored.GetQuestionPersonalExperiences(t.Context(), id)
		if err != nil || snapshot == nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].Revision != 1 || !strings.Contains(snapshot.Entries[0].Rules[0].Text, "C6-FROZEN-V1") {
			t.Fatal("restored adoption did not retain admitted private revision", id, snapshot, err)
		}
	}
	if g, c := client.base.counts(); g != 4 || c != 4 || client.count() != 2 {
		t.Fatal("restore repeated completed paid work or lost unfinished work", g, c, client.count())
	}
	client.mu.Lock()
	prompts := slices.Clone(client.generationPrompts)
	client.mu.Unlock()
	for _, prompt := range prompts {
		if strings.Contains(prompt, "C6-LIVE-V2-MUST-NOT-APPEAR") || !strings.Contains(prompt, "C6-FROZEN-V1") {
			t.Fatal("restored task reloaded the mutable private experience")
		}
	}
	share, err := restored.GetQuestionShareByQuestionID(t.Context(), shared.ID)
	if err != nil || share == nil || share.Status != domain.QuestionShareApproved {
		t.Fatal("approved share did not survive", share, err)
	}
	public, _, err := restored.GetQuestionShareSnapshot(t.Context(), shared.ID)
	if err != nil || public == nil || public.Version != 1 || public.ClinicalStem != shared.ClinicalStem || strings.Contains(public.ClinicalStem, "PRIVATE") || public.PersonalExperiences != nil {
		t.Fatal("restored global read lost its fixed private-free v1", public, err)
	}
	info, err := os.Stat(artifact)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("C6_NATIVE_RESTORE tables=%d dump_bytes=%d schema=%d active_shared_medical_card=1 active_manual_knowledge=1 encrypted_provider=1 wrong_secret_rejected=1 resumed_generation=1 resumed_batch=2 resumed_learning=1 completed_not_rechecked=1 fixed_shared_version=1 private_adoptions=4", len(before), info.Size(), postgres.LatestSchemaVersion)
	for _, table := range []string{"knowledge_cards", "knowledge_card_versions", "ai_provider_configs", "learning_provider_config", "learning_analysis_jobs", "personal_experiences", "personal_experience_versions", "learning_import_operations", "question_experience_contexts", "question_share_requests", "schema_migrations"} {
		t.Logf("C6_RESTORE_BEFORE %s=%s", table, before[table])
	}
}

func customerRestoreFingerprint(t *testing.T, db *sql.DB, schema string) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT tablename FROM pg_tables WHERE schemaname=$1 ORDER BY tablename`, schema)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, table := range tables {
		query := `SELECT count(*)::text||':'||coalesce(md5(string_agg(md5(row_to_json(t)::text),'' ORDER BY md5(row_to_json(t)::text))),md5('')) FROM ` + pq.QuoteIdentifier(schema) + `.` + pq.QuoteIdentifier(table) + ` t`
		var value string
		if err = db.QueryRowContext(t.Context(), query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		values[table] = value
	}
	return values
}
