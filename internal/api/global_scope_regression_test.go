package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestGlobalScopeWithoutTierPermissionDoesNotExposeQuestions(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	admin := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	viewer := createTierUser(t, handler, admin, "global-only-viewer", []string{domain.PermQuestionViewGlobal})
	for _, question := range []domain.A2Question{
		{ID: "global-only-private", OwnerID: "another-user", Status: domain.StatusPublished, ClinicalStem: "未分享的正式题"},
		{ID: "global-only-draft", OwnerID: "another-user", Status: domain.StatusAIDraft, ClinicalStem: "尚未检查的草稿"},
	} {
		question.Version = 1
		if question.Status == domain.StatusPublished {
			question.LibraryState = domain.LibraryActive
			question.ConfirmedVersion = question.Version
			question.ConfirmationSource = domain.ConfirmSourceLegacy
		}
		if err := store.SaveQuestion(t.Context(), question); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"/api/questions?scope=global",
		"/api/questions/search?scope=global&q=%E6%9C%AA%E5%88%86%E4%BA%AB",
	} {
		response := serveAuthJSON(t, handler, http.MethodGet, path, viewer, "203.0.113.10", nil)
		var page struct {
			Total     int                 `json:"total"`
			Questions []domain.A2Question `json:"questions"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &page) != nil || page.Total != 0 || len(page.Questions) != 0 {
			t.Fatalf("global-only viewer must receive an empty page for %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	explicit := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=global&tier=formal", viewer, "203.0.113.10", nil)
	if explicit.Code != http.StatusForbidden {
		t.Fatalf("explicit formal tier without permission: status=%d body=%s", explicit.Code, explicit.Body.String())
	}
}

func TestGlobalBankAndStatsUseOnlySharedQuestions(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	admin := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.1")
	viewer := createTierUser(t, handler, admin, "global-formal-viewer", []string{
		domain.PermQuestionViewGlobal, domain.PermQuestionViewFormal,
	})
	for _, question := range []domain.A2Question{
		{ID: "global-shared-1", OwnerID: "another-user", Status: domain.StatusPublished, ClinicalStem: "分享正式题一"},
		{ID: "global-shared-2", OwnerID: "another-user", Status: domain.StatusPublished, ClinicalStem: "分享正式题二"},
		{ID: "global-private", OwnerID: "another-user", Status: domain.StatusPublished, ClinicalStem: "未分享正式题"},
		{ID: "global-ownerless", Status: domain.StatusPublished, ClinicalStem: "无归属旧题"},
	} {
		question.Version = 1
		if question.Status == domain.StatusPublished {
			question.LibraryState = domain.LibraryActive
			question.ConfirmedVersion = question.Version
			question.ConfirmationSource = domain.ConfirmSourceLegacy
		}
		if err := store.SaveQuestion(t.Context(), question); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"global-shared-1", "global-shared-2"} {
		if err := store.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{
			ID: "share-" + id, QuestionID: id, OwnerID: "another-user",
			Status: domain.QuestionSharePending, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReviewQuestionShare(t.Context(), "share-"+id, "fixture-reviewer", domain.QuestionShareApproved, ""); err != nil {
			t.Fatal(err)
		}
	}
	list := serveAuthJSON(t, handler, http.MethodGet, "/api/questions?scope=global&tier=formal", viewer, "203.0.113.10", nil)
	var page struct {
		Total     int                 `json:"total"`
		Questions []domain.A2Question `json:"questions"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &page) != nil || page.Total != 2 || len(page.Questions) != 2 ||
		strings.Contains(list.Body.String(), "global-private") || strings.Contains(list.Body.String(), "global-ownerless") {
		t.Fatalf("global formal list must contain only approved shares: status=%d body=%s", list.Code, list.Body.String())
	}
	stats := serveAuthJSON(t, handler, http.MethodGet, "/api/stats?scope=global", viewer, "203.0.113.10", nil)
	var summary struct {
		TierCounts map[string]int `json:"tier_counts"`
	}
	if stats.Code != http.StatusOK || json.Unmarshal(stats.Body.Bytes(), &summary) != nil || summary.TierCounts["formal"] != page.Total {
		t.Fatalf("global formal stats must match list total=%d: status=%d body=%s", page.Total, stats.Code, stats.Body.String())
	}
}
