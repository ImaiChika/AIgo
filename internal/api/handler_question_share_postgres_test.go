package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestPersonalQuestionIsolationAndOneTimeGlobalShare(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	server.shareStore = store
	handler := server.Handler()
	adminToken := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")

	bank := domain.QuestionBank{ID: "share-test-bank", Name: "分享测试库", CreatedAt: time.Now()}
	if err := store.SaveBank(t.Context(), bank); err != nil {
		t.Fatal(err)
	}
	registration := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "203.0.113.50", map[string]any{
		"username": "share-owner", "password": "share-owner-password", "display_name": "分享题目老师",
	})
	if registration.Code != http.StatusCreated {
		t.Fatalf("register owner status=%d body=%s", registration.Code, registration.Body.String())
	}
	var owner struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(registration.Body.Bytes(), &owner); err != nil {
		t.Fatal(err)
	}
	updated := serveAuthJSON(t, handler, http.MethodPut, "/api/users/"+owner.ID, adminToken, "198.51.100.1", map[string]any{
		"display_name": "分享题目老师", "role": domain.RoleTeacher, "permissions": []string{}, "bank_ids": []string{}, "enabled": true,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("assign teacher role status=%d body=%s", updated.Code, updated.Body.String())
	}
	ownerToken := loginForAuthTest(t, handler, "share-owner", "share-owner-password", "203.0.113.50")

	q := domain.A2Question{
		ID: "share-q-owner", ClinicalStem: "分享测试题：患者出现典型临床表现，首选处理是什么？",
		Options: []domain.Option{{Label: "A", Text: "处理A"}, {Label: "B", Text: "处理B"}, {Label: "C", Text: "处理C"}, {Label: "D", Text: "处理D"}},
		Answer:  "A", Explanation: "根据临床表现和检查结果，首先选择处理A。", Difficulty: "0.65",
		Status: domain.StatusPublished, Version: 1, CreatedBy: "share-owner", OwnerID: owner.ID,
		BankIDs: []string{bank.ID}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	other := q
	other.ID = "share-q-other"
	other.CreatedBy = "other-owner"
	other.OwnerID = "other-user-id"
	if err := store.SaveQuestion(t.Context(), other); err != nil {
		t.Fatal(err)
	}

	personal := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=personal&tier=formal", ownerToken, "203.0.113.50", nil)
	if personal.Code != http.StatusOK || !strings.Contains(personal.Body.String(), q.ID) || strings.Contains(personal.Body.String(), other.ID) {
		t.Fatalf("personal question scope leaked or missed data: status=%d body=%s", personal.Code, personal.Body.String())
	}
	if strings.Contains(personal.Body.String(), "created_by") || strings.Contains(personal.Body.String(), "owner_id") {
		t.Fatalf("personal question API exposed creator fields: %s", personal.Body.String())
	}
	globalForbidden := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=global&tier=formal", ownerToken, "203.0.113.50", nil)
	if globalForbidden.Code != http.StatusForbidden {
		t.Fatalf("ordinary user accessed global bank: status=%d body=%s", globalForbidden.Code, globalForbidden.Body.String())
	}

	first := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/"+q.ID+"/share", ownerToken, "203.0.113.50", nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first share request status=%d body=%s", first.Code, first.Body.String())
	}
	repeat := serveAuthJSON(t, handler, http.MethodPost, "/api/questions/"+q.ID+"/share", ownerToken, "203.0.113.50", nil)
	if repeat.Code != http.StatusConflict {
		t.Fatalf("duplicate share request status=%d body=%s", repeat.Code, repeat.Body.String())
	}
	mine := serveAuthJSON(t, handler, http.MethodGet, "/api/question-shares?scope=mine", ownerToken, "203.0.113.50", nil)
	if mine.Code != http.StatusOK || !strings.Contains(mine.Body.String(), "pending") || strings.Contains(mine.Body.String(), "owner_name") {
		t.Fatalf("owner share list has wrong visibility: status=%d body=%s", mine.Code, mine.Body.String())
	}

	pending := serveAuthJSON(t, handler, http.MethodGet, "/api/question-shares?scope=pending", adminToken, "198.51.100.1", nil)
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), q.ID) || !strings.Contains(pending.Body.String(), "分享题目老师") {
		t.Fatalf("admin pending share queue wrong: status=%d body=%s", pending.Code, pending.Body.String())
	}
	var pendingPayload struct {
		Items []struct {
			Request struct {
				ID string `json:"id"`
			} `json:"request"`
		} `json:"items"`
	}
	if err := json.Unmarshal(pending.Body.Bytes(), &pendingPayload); err != nil {
		t.Fatal(err)
	}
	if len(pendingPayload.Items) != 1 {
		t.Fatalf("pending share count=%d, want 1", len(pendingPayload.Items))
	}
	shareID := pendingPayload.Items[0].Request.ID
	approved := serveAuthJSON(t, handler, http.MethodPost, "/api/question-shares/"+shareID+"/review", adminToken, "198.51.100.1", map[string]any{
		"status": "approved", "note": "内容已完成专家审核，批准进入全局正式库",
	})
	if approved.Code != http.StatusOK {
		t.Fatalf("approve share status=%d body=%s", approved.Code, approved.Body.String())
	}
	reapprove := serveAuthJSON(t, handler, http.MethodPost, "/api/question-shares/"+shareID+"/review", adminToken, "198.51.100.1", map[string]any{
		"status": "approved",
	})
	if reapprove.Code != http.StatusConflict {
		t.Fatalf("repeated approval status=%d body=%s", reapprove.Code, reapprove.Body.String())
	}
	globalFormal := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=global&tier=formal", adminToken, "198.51.100.1", nil)
	if globalFormal.Code != http.StatusOK || !strings.Contains(globalFormal.Body.String(), q.ID) || strings.Contains(globalFormal.Body.String(), "created_by") {
		t.Fatalf("approved question missing or creator leaked in global bank: status=%d body=%s", globalFormal.Code, globalFormal.Body.String())
	}
	legacy := q
	legacy.ID = "share-q-legacy"
	legacy.CreatedBy = "legacy-teacher"
	legacy.OwnerID = ""
	legacy.Status = domain.StatusAIReviewed
	if err := store.SaveQuestion(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	globalWorking := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=global&tier=working", adminToken, "198.51.100.1", nil)
	if globalWorking.Code != http.StatusOK || strings.Contains(globalWorking.Body.String(), q.ID) || !strings.Contains(globalWorking.Body.String(), legacy.ID) {
		t.Fatalf("global working bank lost legacy question or included approved question: status=%d body=%s", globalWorking.Code, globalWorking.Body.String())
	}
}
