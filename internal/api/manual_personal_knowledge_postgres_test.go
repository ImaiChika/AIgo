//go:build integration

package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/learning"
	"aigo/internal/storage"

	"github.com/lib/pq"
)

func manualKnowledgeInput(key, status string) map[string]any {
	return map[string]any{"create_key": key, "title": "本人命题表达要求", "scope": map[string]any{"kind": "all"}, "rules": []map[string]any{{"id": "manual-1", "text": "病例按就诊顺序叙述，保留鉴别线索", "field": "clinical_stem"}}, "status": status}
}

func TestManualKnowledgeUseAloneCanFreezePublicGenerationRequestWithoutPaidLearning(t *testing.T) {
	s, h, token, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	db := s.questionStore.(interface{ DB() *sql.DB }).DB()
	permissions := []string{"experience:view", "experience:manage", "experience:use", "question:generate"}
	if _, err := db.ExecContext(t.Context(), `UPDATE users SET permissions=$2 WHERE id=$1`, owner.ID, pq.Array(permissions)); err != nil {
		t.Fatal(err)
	}
	r := serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, manualKnowledgeInput("manual-generation", "active"))
	var card domain.AuthoringExperience
	if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &card) != nil {
		t.Fatal(r.Code, r.Body)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, map[string]any{"run_id": "manual-only-use-run", "version_id": version, "outline_code": "lib.001", "count": 1, "apply_personal_experiences": true})
	if r.Code != 200 {
		t.Fatal("manual-use grant could not enter generation", r.Code, r.Body)
	}
	var frozen []byte
	if err := db.QueryRowContext(t.Context(), `SELECT request_json FROM generation_runs WHERE id='manual-only-use-run'`).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Request struct {
			PersonalExperiences *domain.PersonalExperienceSnapshot `json:"personal_experiences"`
		} `json:"request"`
	}
	if err := json.Unmarshal(frozen, &spec); err != nil || spec.Request.PersonalExperiences == nil || len(spec.Request.PersonalExperiences.Entries) != 1 || spec.Request.PersonalExperiences.Entries[0].ID != card.ID {
		t.Fatalf("manual adopted snapshot lost: %s error=%v", frozen, err)
	}
	var calls int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM model_call_records`).Scan(&calls); err != nil || calls != 0 {
		t.Fatal("manual knowledge operations invoked paid AI", calls, err)
	}
}

func TestManualKnowledgeCreationReplayRevisionAndConcurrentSubmission(t *testing.T) {
	s, h, token, _, cleanup := libraryTestEnv(t)
	defer cleanup()
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	store := s.questionStore.(interface{ DB() *sql.DB })
	if _, err := store.DB().ExecContext(t.Context(), `UPDATE users SET permissions=$2 WHERE id=$1`, owner.ID, pq.Array([]string{"experience:view", "experience:manage"})); err != nil {
		t.Fatal(err)
	}
	body := manualKnowledgeInput("manual-idempotency", "draft")
	body["owner_id"] = "another-account"
	body["source"] = map[string]any{"kind": "legacy_personal_preference", "job_id": "forged-job", "origin_request_hash": "forged"}
	r := serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, body)
	var card domain.AuthoringExperience
	if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &card) != nil {
		t.Fatal(r.Code, r.Body)
	}
	if card.Source.Kind != "manual" || card.Source.JobID != "" || card.Source.QuestionID != "" || card.Source.OriginalVersion != 0 || card.Source.CurrentVersion != 0 || card.Source.CandidateID != "manual:manual-idempotency" || len(card.Source.OriginRequestHash) != 64 {
		t.Fatal("manual source forged an AI comparison", card.Source)
	}
	originalSource := card.Source
	r = serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, body)
	if r.Code != 200 {
		t.Fatal("same source key did not replay", r.Code, r.Body)
	}
	update := map[string]any{"title": "修订后的本人知识", "scope": card.Scope, "rules": []domain.LearningRule{{ID: "new-a", Text: "新增第一条要求", Field: "clinical_stem"}, {ID: "new-b", Text: "新增第二条要求", Field: "options"}}, "status": "active", "expected_revision": 1}
	r = serveGenerateJSON(t, h, "PUT", "/api/personal-experiences/"+card.ID, token, update)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, body)
	var replay domain.AuthoringExperience
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &replay) != nil || replay.ID != card.ID || replay.Revision != 2 || replay.Title != "修订后的本人知识" || !reflect.DeepEqual(replay.Source, originalSource) {
		t.Fatal("create replay overwrote later edits", r.Code, r.Body)
	}
	body["title"] = "same key with different intention"
	if r = serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, body); r.Code != 409 {
		t.Fatal(r.Code, r.Body)
	}
	if r = serveGenerateJSON(t, h, "PUT", "/api/personal-experiences/"+card.ID, token, update); r.Code != 409 {
		t.Fatal("stale revision accepted", r.Code, r.Body)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Go(func() {
			<-start
			codes <- serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, manualKnowledgeInput("manual-parallel", "draft")).Code
		})
	}
	close(start)
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[201] != 1 || counts[200] != 1 {
		t.Fatal("same key concurrent create did not replay", counts)
	}
	for table, want := range map[string]int{"personal_experiences": 2, "personal_experience_versions": 3, "learning_analysis_jobs": 0, "questions": 0, "model_call_records": 0} {
		var count int
		if err := store.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s=%d want=%d error=%v", table, count, want, err)
		}
	}
}

func TestManualMedicalFlagsCannotBeRemovedByRenamingRules(t *testing.T) {
	s, h, token, _, cleanup := libraryTestEnv(t)
	defer cleanup()
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	db := s.questionStore.(interface{ DB() *sql.DB }).DB()
	if _, err := db.ExecContext(t.Context(), `UPDATE users SET permissions=$2 WHERE id=$1`, owner.ID, pq.Array([]string{"experience:view", "experience:manage"})); err != nil {
		t.Fatal(err)
	}
	body := manualKnowledgeInput("manual-medical", "active")
	body["rules"] = []domain.LearningRule{{ID: "medical-original", Text: "答案适用条件需要人工核对", Field: "answer"}}
	if r := serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, body); r.Code != 400 {
		t.Fatal("unconfirmed medical knowledge enabled", r.Code, r.Body)
	}
	body["confirm_medical_changes"] = true
	r := serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, body)
	var card domain.AuthoringExperience
	if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &card) != nil || !card.MedicalConfirmed || !card.Rules[0].MedicalChange {
		t.Fatal(r.Code, r.Body)
	}
	edit := map[string]any{"title": card.Title, "scope": card.Scope, "rules": []domain.LearningRule{{ID: "renamed", Text: "替换后的知识仍需核对", Field: "clinical_stem"}}, "status": "active", "expected_revision": 1, "medical_confirmed": true}
	if r = serveGenerateJSON(t, h, "PUT", "/api/personal-experiences/"+card.ID, token, edit); r.Code != 400 {
		t.Fatal("medical flag cleared by changed ID or forged confirmed field", r.Code, r.Body)
	}
	edit["confirm_medical_changes"] = true
	r = serveGenerateJSON(t, h, "PUT", "/api/personal-experiences/"+card.ID, token, edit)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &card) != nil || !card.Rules[0].MedicalChange {
		t.Fatal(r.Code, r.Body)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE users SET permissions=ARRAY['experience:view'] WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if r = serveGenerateJSON(t, h, "GET", "/api/personal-experiences/"+card.ID+"/history", token, nil); r.Code != 200 {
		t.Fatal("view-only personal history rejected", r.Code, r.Body)
	}
	edit["expected_revision"] = 2
	if r = serveGenerateJSON(t, h, "PUT", "/api/personal-experiences/"+card.ID, token, edit); r.Code != 403 {
		t.Fatal("view-only user changed knowledge", r.Code, r.Body)
	}
	bad := manualKnowledgeInput("invalid-budget", "draft")
	bad["rules"] = []domain.LearningRule{{ID: "large", Text: strings.Repeat("字", 601), Field: "clinical_stem"}}
	if r = serveGenerateJSON(t, h, "POST", "/api/personal-experiences", token, bad); r.Code != 400 {
		t.Fatal("oversized manual field accepted", r.Code, r.Body)
	}
}

func TestManualUseFiltersAIOriginsBeforeDatabaseLimitAndCount(t *testing.T) {
	s, _, _, _, cleanup := libraryTestEnv(t)
	defer cleanup()
	owner, _ := s.authSvc.GetUserByUsername("lib-teacher")
	store := s.questionStore.(storage.LearningStore)
	db := s.questionStore.(interface{ DB() *sql.DB }).DB()
	if _, err := db.ExecContext(t.Context(), `UPDATE users SET permissions=$2 WHERE id=$1`, owner.ID, pq.Array([]string{"experience:view", "experience:use", "question:generate"})); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		kind := "learning"
		at := time.Now().Add(time.Duration(i) * time.Second)
		if i == 204 {
			kind = "manual"
			at = at.Add(-time.Hour)
		}
		card := domain.AuthoringExperience{ID: fmt.Sprintf("origin-%03d", i), Kind: "authoring_experience", ScopeType: "personal", Title: "来源筛选", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "r", Text: "保留临床病例线索", Field: "clinical_stem"}}, Status: "active", Revision: 1, Source: domain.ExperienceSource{Kind: kind, CandidateID: fmt.Sprint(i)}, CreatedAt: at, UpdatedAt: at}
		raw, _ := json.Marshal(card)
		if _, err := db.ExecContext(t.Context(), `INSERT INTO personal_experiences(id,owner_id,revision,status,source_job_id,source_candidate_id,body,created_at,updated_at)VALUES($1,$2,1,'active','fixture',$1,$3,$4,$4)`, card.ID, owner.ID, raw, at); err != nil {
			t.Fatal(err)
		}
	}
	service := learning.NewService(store, nil)
	point := domain.KnowledgePoint{ID: "manual-filter-point", Category: "临床综合", Subject: "消化", Topic: "消化性溃疡"}
	snapshot, err := service.FreezeExperiences(t.Context(), owner.ID, owner.Role, []domain.KnowledgePoint{point})
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].ID != "origin-204" || snapshot.ExcludedCount != 0 {
		t.Fatal("AI entries crowded out the permitted manual knowledge", snapshot, err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE users SET permissions=ARRAY['experience:use','question:generate'] WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.FreezeExperiences(t.Context(), owner.ID, owner.Role, []domain.KnowledgePoint{point}); err == nil {
		t.Fatal("latent missing-view manual use was activated")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE users SET permissions=ARRAY['learning:use','question:generate'] WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err = service.FreezeExperiences(t.Context(), owner.ID, owner.Role, []domain.KnowledgePoint{point})
	if err != nil || len(snapshot.Entries) == 0 || snapshot.Entries[0].ID == "origin-204" {
		t.Fatal("explicit AI adoption lost its existing ability", snapshot, err)
	}
}

func TestPersonalKnowledgeViewDoesNotRequireQuestionOrPaidLearningPermission(t *testing.T) {
	s, h, teacher, _, cleanup := libraryTestEnv(t)
	defer cleanup()
	owner, err := s.authSvc.GetUserByUsername("lib-teacher")
	if err != nil {
		t.Fatal(err)
	}
	// This represents an old rule-view-only account after the forward mapping.
	// No question contents or paid analysis capability is granted with it.
	store := s.questionStore.(interface{ DB() *sql.DB })
	if _, err := store.DB().ExecContext(t.Context(), `UPDATE users SET permissions=$2 WHERE id=$1`, owner.ID, pq.Array([]string{"experience:view"})); err != nil {
		t.Fatal(err)
	}
	response := serveGenerateJSON(t, h, "GET", "/api/personal-experiences", teacher, nil)
	if response.Code != 200 {
		t.Fatalf("own knowledge read should not require question view: %d %s", response.Code, response.Body)
	}
	for _, path := range []string{"/api/questions/my-new", "/api/questions/never-owned/learning-comparison"} {
		response := serveGenerateJSON(t, h, "GET", path, teacher, nil)
		if response.Code != 403 {
			t.Fatalf("knowledge read expanded question access: %s %d", path, response.Code)
		}
	}
}
