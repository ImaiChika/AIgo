package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage"
)

func TestTeacherSubmitsOwnQuestionsWithoutBankSelection(t *testing.T) {
	s, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	s.reviewSvc = review.NewService(store, store, store, s.authSvc)
	h := s.Handler()
	adminToken := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.1")
	created := serveAuthJSON(t, h, http.MethodPost, "/api/users", adminToken, "198.51.100.1", map[string]any{
		"username": "submission-teacher", "password": "submission-teacher-password", "display_name": "命题老师", "role": domain.RoleTeacher,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create teacher: %d %s", created.Code, created.Body)
	}
	var teacher struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &teacher); err != nil || teacher.ID == "" {
		t.Fatalf("parse teacher: %v %s", err, created.Body)
	}
	teacherToken := loginForAuthTest(t, h, "submission-teacher", "submission-teacher-password", "198.51.100.2")
	now := time.Now()
	flow := domain.ReviewFlowConfig{
		ID: "teacher-submit-flow", Name: "命题教师流程", CreatedAt: now,
		Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "审题老师审核", ExpertIDs: []string{teacher.ID}, RequiredCount: 1}},
	}
	if err := store.SaveFlowConfig(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	question := domain.A2Question{
		ID: "teacher-submit-own", OwnerID: teacher.ID, CreatedBy: "submission-teacher",
		ClinicalStem: "男，50岁。突发胸痛2小时。该患者最可能的诊断是",
		Options:      []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}},
		Answer:       "A", Status: domain.StatusAIReviewed, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(t.Context(), question); err != nil {
		t.Fatal(err)
	}
	other := question
	other.ID = "teacher-submit-other"
	other.OwnerID = "admin-user"
	if err := store.SaveQuestion(t.Context(), other); err != nil {
		t.Fatal(err)
	}

	flows := serveAuthJSON(t, h, http.MethodGet, "/api/review/available-flows", teacherToken, "198.51.100.2", nil)
	if flows.Code != http.StatusOK || strings.Contains(flows.Body.String(), teacher.ID) {
		t.Fatalf("teacher flow summary leaked reviewer details: %d %s", flows.Code, flows.Body)
	}
	submitted := serveAuthJSON(t, h, http.MethodPost, "/api/review/submit", teacherToken, "198.51.100.2", map[string]any{
		"question_id": question.ID, "flow_id": flow.ID,
	})
	if submitted.Code != http.StatusOK {
		t.Fatalf("teacher submit own question: %d %s", submitted.Code, submitted.Body)
	}
	var task domain.ReviewTask
	if err := json.Unmarshal(submitted.Body.Bytes(), &task); err != nil || task.SubmissionBankID != "" {
		t.Fatalf("teacher submission should keep empty bank snapshot: err=%v task=%+v", err, task)
	}
	forbidden := serveAuthJSON(t, h, http.MethodPost, "/api/review/submit", teacherToken, "198.51.100.2", map[string]any{
		"question_id": other.ID, "flow_id": flow.ID,
	})
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("teacher submitted another user's question: %d %s", forbidden.Code, forbidden.Body)
	}
}

func TestFiveRoundPersonalReviewAndBulkShare(t *testing.T) {
	s, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	s.reviewSvc = review.NewService(store, store, store, s.authSvc)
	h := s.Handler()
	token := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.1")
	var ownerID string
	if err := store.DB().QueryRow(`SELECT id FROM users WHERE username='admin'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := store.SaveBank(t.Context(), domain.QuestionBank{ID: "bulk-bank", Name: "分类甲", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "five-round", Name: "五轮流程", BankID: "bulk-bank", CreatedAt: now}
	for i := 1; i <= 5; i++ {
		flow.Rounds = append(flow.Rounds, domain.RoundConfig{RoundNumber: i, Name: fmt.Sprint(i), ExpertIDs: []string{ownerID}, RequiredCount: 1})
	}
	if err := store.SaveFlowConfig(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	q := domain.A2Question{ID: "personal-1", OwnerID: ownerID, CreatedBy: "admin", ClinicalStem: "患者出现症状，最可能的诊断是？", Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}}, Answer: "A", Status: domain.StatusAIReviewed, Version: 1, BankIDs: []string{"bulk-bank"}, CreatedAt: now, UpdatedAt: now}
	for _, id := range []string{"personal-1", "personal-2"} {
		q.ID = id
		if err := store.SaveQuestion(t.Context(), q); err != nil {
			t.Fatal(err)
		}
		response := serveAuthJSON(t, h, "POST", "/api/review/submit", token, "198.51.100.1", map[string]any{"question_id": id, "flow_id": flow.ID, "bank_id": "bulk-bank"})
		if response.Code != 200 {
			t.Fatalf("submit: %d %s", response.Code, response.Body)
		}
		var task domain.ReviewTask
		if err := json.Unmarshal(response.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			response = serveAuthJSON(t, h, "POST", "/api/review/action", token, "198.51.100.1", map[string]any{"task_id": task.ID, "action": "approved"})
			if response.Code != 200 {
				t.Fatalf("round %d: %d %s", i+1, response.Code, response.Body)
			}
		}
		response = serveAuthJSON(t, h, "POST", "/api/review/finalize", token, "198.51.100.1", map[string]any{"task_id": task.ID, "action": "approved"})
		if response.Code != 200 {
			t.Fatalf("final: %d %s", response.Code, response.Body)
		}
	}
	result := serveAuthJSON(t, h, "GET", "/api/review/results?scope=personal", token, "198.51.100.1", nil)
	var payload struct {
		Total int
		Stats review.ReviewStats
		Items []review.ReviewResultItem
	}
	if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if result.Code != 200 || payload.Total != 2 || payload.Stats.Total != 2 {
		t.Fatalf("personal review result: %d %s", result.Code, result.Body)
	}
	for _, item := range payload.Items {
		if item.FinalStatus != "published" || len(item.Task.RoundResults) != 5 || len(item.Records) != 6 {
			t.Fatalf("lost five-round history: %+v", item)
		}
	}
	result = serveAuthJSON(t, h, "GET", "/api/review/results?scope=global", token, "198.51.100.1", nil)
	if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total != 0 || payload.Stats.Total != 0 {
		t.Fatalf("unshared personal results leaked globally: %s", result.Body)
	}

	// Preview spans beyond the UI's 100-row page and only includes eligible own questions.
	q.Status = domain.StatusPublished
	q.Profession = "专业甲"
	for i := 0; i < 103; i++ {
		q.ID = fmt.Sprintf("filter-%03d", i)
		if err := store.SaveQuestion(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	preview := serveAuthJSON(t, h, "POST", "/api/question-shares/preview", token, "198.51.100.1", map[string]string{"profession": "专业甲", "bank_id": "bulk-bank", "q": "患者"})
	var candidates struct {
		QuestionIDs []string `json:"question_ids"`
		Total       int
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &candidates); err != nil {
		t.Fatal(err)
	}
	if preview.Code != 200 || candidates.Total != 103 || len(candidates.QuestionIDs) != 103 {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body)
	}
	// New matching questions after preview are not implicitly added to submission.
	q.ID = "after-preview"
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	// Withdraw one of the previewed questions before committing; it must be skipped.
	changed, _ := store.GetQuestion(t.Context(), candidates.QuestionIDs[0])
	changed.Status = domain.StatusAIReviewed
	if err := store.SaveQuestion(t.Context(), *changed); err != nil {
		t.Fatal(err)
	}
	batch := serveAuthJSON(t, h, "POST", "/api/question-shares/batch", token, "198.51.100.1", map[string]any{"question_ids": candidates.QuestionIDs})
	var summary struct {
		Submitted int
		Skipped   int
	}
	if err := json.Unmarshal(batch.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if batch.Code != 200 || summary.Submitted != 102 || summary.Skipped != 1 {
		t.Fatalf("batch: %d %s", batch.Code, batch.Body)
	}
	batch = serveAuthJSON(t, h, "POST", "/api/question-shares/batch", token, "198.51.100.1", map[string]any{"question_ids": candidates.QuestionIDs})
	if err := json.Unmarshal(batch.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Submitted != 0 || summary.Skipped != 103 {
		t.Fatalf("not idempotent: %s", batch.Body)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM question_share_requests WHERE status='pending'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 102 {
		t.Fatalf("pending count %d", count)
	}
	q.ID = "foreign-question"
	q.OwnerID = "other-owner"
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	batch = serveAuthJSON(t, h, "POST", "/api/question-shares/batch", token, "198.51.100.1", map[string]any{"question_ids": []string{"after-preview", q.ID}})
	if batch.Code != http.StatusForbidden {
		t.Fatalf("foreign owner not rejected: %d %s", batch.Code, batch.Body)
	}
	existing, err := store.GetQuestionShareByQuestionID(t.Context(), "after-preview")
	if err != nil || existing != nil {
		t.Fatalf("partial write on rejected batch: %+v %v", existing, err)
	}

	// Concurrent duplicate submissions produce exactly one application per question.
	var wg sync.WaitGroup
	results := make(chan []string, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := store.CreateQuestionShares(t.Context(), ownerID, []string{"personal-1", "personal-2"})
			results <- ids
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count = 0
	for ids := range results {
		count += len(ids)
	}
	if count != 2 {
		t.Fatalf("concurrent submitted %d", count)
	}
	// Still personal published, version and expert records unchanged by sharing.
	preserved, _ := store.GetQuestion(t.Context(), "personal-1")
	if preserved.Status != domain.StatusPublished || preserved.Version != 1 {
		t.Fatalf("sharing modified original: %+v", preserved)
	}
	_, total, err := store.PreviewQuestionShares(t.Context(), storage.QuestionFilter{OwnerID: ownerID, Professions: []string{"专业甲"}}, 500)
	if err != nil || total != 1 {
		t.Fatalf("preview must exclude already submitted and withdrawn: %d %v", total, err)
	}
	// Missing share permission cannot access either bulk endpoint.
	registration := serveAuthJSON(t, h, "POST", "/api/auth/register", "", "203.0.113.44", map[string]string{"username": "no-share", "password": "no-share-password"})
	if registration.Code != 201 {
		t.Fatalf("register: %s", registration.Body)
	}
	noShare := loginForAuthTest(t, h, "no-share", "no-share-password", "203.0.113.44")
	for _, path := range []string{"/api/question-shares/preview", "/api/question-shares/batch"} {
		if r := serveAuthJSON(t, h, "POST", path, noShare, "203.0.113.44", map[string]any{"question_ids": []string{"personal-1"}}); r.Code != 403 {
			t.Fatalf("permission bypass: %d", r.Code)
		}
	}
}
