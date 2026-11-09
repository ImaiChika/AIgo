//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestLibraryEditRespectsDeletionAndAllowsPersonalPublishedRevision(t *testing.T) {
	for _, state := range []string{"deleted", "published"} {
		t.Run(state, func(t *testing.T) {
			s, h, token, v, cleanup := libraryTestEnv(t)
			defer cleanup()
			q := seedPassedQuestion(t, s, h, token, v, "edit-"+state, "患者，男，45岁。反复上腹痛2年，今晨加重，最可能的诊断是")
			current, err := s.questionStore.GetQuestion(t.Context(), q.ID)
			if err != nil {
				t.Fatal(err)
			}
			q = *current
			if state == "deleted" {
				q.LibraryState = domain.LibraryDeleted
				q.DeletedReason = domain.DeleteReasonOwnerDelete
			} else {
				q.Status = domain.StatusPublished
				q.LibraryState = domain.LibraryActive
				q.ConfirmedVersion = q.Version
				q.ConfirmationSource = domain.ConfirmSourceLegacy
			}
			q.LibraryRevision++
			if err := s.questionStore.SaveQuestion(storage.WithLibraryRevision(t.Context(), q.LibraryRevision-1), q); err != nil {
				t.Fatal(err)
			}
			if state == "published" {
				flowStore := s.questionStore.(interface {
					SaveFlowConfig(context.Context, domain.ReviewFlowConfig) error
				})
				if err := flowStore.SaveFlowConfig(t.Context(), domain.ReviewFlowConfig{ID: "finished-edit-flow", Name: "本轮历史审核", Rounds: []domain.RoundConfig{{RoundNumber: 1, RequiredCount: 1}}}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "published" {
				store := s.questionStore.(interface {
					SaveTask(context.Context, domain.ReviewTask) error
				})
				if err := store.SaveTask(t.Context(), domain.ReviewTask{ID: "finished-edit-history", FlowID: "finished-edit-flow", QuestionID: q.ID, QuestionVersion: q.Version, Status: domain.StatusPublished, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			current, _ = s.questionStore.GetQuestion(t.Context(), q.ID)
			q = *current
			r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, token, map[string]any{"clinical_stem": q.ClinicalStem + "？", "expected_version": q.Version, "expected_review_revision": q.ReviewRevision, "expected_library_revision": q.LibraryRevision})
			if state == "deleted" {
				if r.Code != 409 {
					t.Fatalf("deleted question edited: %d %s", r.Code, r.Body)
				}
			} else {
				if r.Code != 200 {
					t.Fatalf("personal published revision blocked: %d %s", r.Code, r.Body)
				}
				fresh, _ := s.questionStore.GetQuestion(t.Context(), q.ID)
				if fresh.LibraryState != domain.LibraryUnfiled || fresh.ConfirmedVersion != 0 || fresh.Version != q.Version+1 || fresh.Status != domain.StatusAIReviewed {
					t.Fatalf("revision did not revoke confirmation: %+v", fresh)
				}
				list := serveGenerateJSON(t, h, "GET", "/api/questions/my-new?page=1&page_size=20", token, nil)
				if list.Code != 200 || !strings.Contains(list.Body.String(), q.ID) {
					t.Fatalf("finished review history hid author revision: %d %s", list.Code, list.Body)
				}
				summary := serveGenerateJSON(t, h, "GET", "/api/my/summary", token, nil)
				var counts map[string]any
				_ = json.Unmarshal(summary.Body.Bytes(), &counts)
				if summary.Code != 200 || counts["new_questions_count"] != float64(1) {
					t.Fatalf("arrangement count lost revision: %d %s", summary.Code, summary.Body)
				}
			}
		})
	}
}

func TestExportDownloadRevalidatesSavedVersionAndRequiresManifest(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	seeded := seedPassedQuestion(t, s, h, token, v, "export-recheck", "患者，男，45岁。反复上腹痛2年，最可能的诊断是")
	q, _ := s.questionStore.GetQuestion(t.Context(), seeded.ID)
	q.Status = domain.StatusPublished
	q.LibraryState = domain.LibraryActive
	q.ConfirmedVersion = q.Version
	q.ConfirmationSource = domain.ConfirmSourceLegacy
	q.LibraryRevision++
	if err := s.questionStore.SaveQuestion(storage.WithLibraryRevision(t.Context(), q.LibraryRevision-1), *q); err != nil {
		t.Fatal(err)
	}
	exported := serveGenerateJSON(t, h, "POST", "/api/export/xlsx", token, map[string]any{"scope": "personal", "question_ids": []string{q.ID}})
	if exported.Code != 200 {
		t.Fatal(exported.Code, exported.Body)
	}
	var body struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(exported.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	download := func(filename string) *httptest.ResponseRecorder {
		return serveGenerateJSON(t, h, http.MethodGet, "/api/export/download/"+url.PathEscape(filename), token, nil)
	}
	if r := download(body.Filename); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	q, _ = s.questionStore.GetQuestion(t.Context(), q.ID)
	q.Version++
	q.ClinicalStem += "（人工新修订）"
	q.Status = domain.StatusAIReviewed
	if err := s.questionStore.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	if r := download(body.Filename); r.Code != 409 {
		t.Errorf("old export remained downloadable after revision: %d", r.Code)
	}
	legacy := filepath.Join(exportDirectory(q.OwnerID), "without-manifest.xlsx")
	if err := os.WriteFile(legacy, []byte("old file without scope evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := download("without-manifest.xlsx"); r.Code != 409 {
		t.Errorf("unproven old export downloadable: %d", r.Code)
	}
}

func TestLegacyFormalOnlyPersonalScopeKeepsItsOriginalBoundary(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.199")
	created := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": "formal-only-c3", "password": "formal-reader-password", "permissions": []string{domain.PermQuestionViewFormal, domain.PermQuestionDownload}})
	if created.Code != 201 {
		t.Fatal(created.Code, created.Body)
	}
	owner, err := s.authSvc.GetUserByUsername("formal-only-c3")
	if err != nil {
		t.Fatal(err)
	}
	token := loginForAuthTest(t, h, "formal-only-c3", "formal-reader-password", "198.51.100.198")
	q := seedPassedQuestion(t, s, h, teacher, v, "formal-source", "患者，男，45岁。反复上腹痛2年，最可能的诊断是")
	q.ID = "formal-owned"
	q.OwnerID = owner.ID
	q.CreatedBy = owner.Username
	q.Status = domain.StatusPublished
	q.LibraryState = domain.LibraryActive
	q.ConfirmedVersion = 1
	q.ConfirmationSource = domain.ConfirmSourceLegacy
	if err := s.questionStore.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	r := serveGenerateJSON(t, h, "GET", "/api/questions?scope=personal&tier=formal", token, nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), q.ID) {
		t.Errorf("formal-only lost own formal list: %d %s", r.Code, r.Body)
	}
	r = serveGenerateJSON(t, h, "GET", "/api/questions/"+q.ID, token, nil)
	if r.Code != 200 {
		t.Errorf("formal-only lost own formal detail: %d", r.Code)
	}
	qptr, _ := s.questionStore.GetQuestion(t.Context(), q.ID)
	q = *qptr
	q.Version++
	q.Status = domain.StatusAIReviewed
	q.ClinicalStem += "（新修订）"
	if err := s.questionStore.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/questions/" + q.ID, "/api/questions/" + q.ID + "/versions"} {
		r = serveGenerateJSON(t, h, "GET", path, token, nil)
		if r.Code == 200 {
			t.Error("formal-only read new personal revision", path)
		}
	}
	r = serveGenerateJSON(t, h, "GET", "/api/questions?scope=personal&tier=working", token, nil)
	if r.Code == 200 && strings.Contains(r.Body.String(), q.ID) {
		t.Error("old formal permission expanded to working")
	}
}

func TestPublicQuestionProjectionDoesNotExposeLibraryActors(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, token, v, "public-origin", "患者，男，45岁。反复上腹痛2年，最可能的诊断是")
	q.ID = "public-library-actors"
	q.Status = domain.StatusPublished
	q.LibraryState = domain.LibraryActive
	q.ConfirmedVersion = q.Version
	q.ConfirmationSource = domain.ConfirmSourceLegacy
	q.ConfirmedBy = "private-confirm-actor"
	q.DeletedBy = "private-delete-actor"
	if err := s.questionStore.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := s.shareStore.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "public-library-share", QuestionID: q.ID, OwnerID: q.OwnerID, Status: domain.QuestionSharePending}); err != nil {
		t.Fatal(err)
	}
	admin, _ := s.authSvc.GetUserByUsername("admin")
	if _, err := s.shareStore.ReviewQuestionShare(t.Context(), "public-library-share", admin.ID, domain.QuestionShareApproved, ""); err != nil {
		t.Fatal(err)
	}
	adminToken := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.197")
	for _, path := range []string{"/api/questions/" + q.ID, "/api/questions/" + q.ID + "/versions", "/api/questions?scope=global&tier=formal"} {
		r := serveGenerateJSON(t, h, "GET", path, adminToken, nil)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
		if strings.Contains(r.Body.String(), "private-confirm-actor") || strings.Contains(r.Body.String(), "private-delete-actor") {
			t.Error("library actor leaked", path)
		}
	}
}

func TestLegacyPublishedWithoutInitialEvidenceStaysReadOnly(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, token, v, "unproven-legacy-source", "患者，男，45岁。反复上腹痛2年，最可能的诊断是")
	q.ID = "unproven-legacy-published"
	q.Status = domain.StatusPublished
	q.LibraryState = domain.LibraryActive
	q.ConfirmedVersion = q.Version
	q.ConfirmationSource = domain.ConfirmSourceLegacy
	// This distinct migrated fixture deliberately has no initial-pass result.
	if err := s.questionStore.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	current, _ := s.questionStore.GetQuestion(t.Context(), q.ID)
	r := serveGenerateJSON(t, h, "PUT", "/api/questions/"+q.ID, token, map[string]any{"clinical_stem": q.ClinicalStem + "？", "expected_version": current.Version, "expected_library_revision": current.LibraryRevision, "expected_review_revision": current.ReviewRevision})
	if r.Code != 409 {
		t.Fatalf("unproven legacy was edited into a state it cannot reconfirm: %d %s", r.Code, r.Body)
	}
	after, _ := s.questionStore.GetQuestion(t.Context(), q.ID)
	if after.Version != 1 || after.LibraryState != domain.LibraryActive || after.Status != domain.StatusPublished {
		t.Fatal("read-only legacy was changed", after)
	}
}

func TestPersonalLibraryHistoryRestoreCreatesNewVersionAndKeepsOriginal(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	original := seedPassedQuestion(t, s, h, token, v, "library-history-restore", "患者，男，45岁。反复上腹痛2年，最可能的诊断是")
	current := func() *domain.A2Question {
		t.Helper()
		q, err := s.questionStore.GetQuestion(t.Context(), original.ID)
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	guards := func(q *domain.A2Question) map[string]any {
		return map[string]any{"question_id": q.ID, "expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision}
	}
	confirm := func() {
		t.Helper()
		r := confirmViaAPI(t, h, token, map[string]any{"items": []any{guards(current())}})
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
	}
	confirm()
	body := guards(current())
	body["clinical_stem"] = original.ClinicalStem + "？"
	edited := serveGenerateJSON(t, h, "PUT", "/api/questions/"+original.ID, token, body)
	if edited.Code != 200 {
		t.Fatal(edited.Code, edited.Body)
	}
	confirm()
	q := current()
	body = guards(q)
	body["version"] = 1
	body["reason"] = "恢复原始内容但保留原版"
	restored := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/restore", token, body)
	if restored.Code != 200 {
		t.Fatal(restored.Code, restored.Body)
	}
	after := current()
	if after.Version != 3 || after.LibraryState != domain.LibraryUnfiled || after.ConfirmedVersion != 0 || after.ClinicalStem != original.ClinicalStem {
		t.Fatalf("history restore did not create a fresh unconfirmed version: %+v", after)
	}
	v1, err := s.questionStore.GetQuestionVersion(t.Context(), q.ID, 1)
	if err != nil || v1 == nil || v1.Snapshot.ClinicalStem != original.ClinicalStem {
		t.Fatal("v1 was overwritten", err)
	}
	stale := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/restore", token, body)
	if stale.Code != 409 {
		t.Fatal("stale history restoration was accepted", stale.Code)
	}
	confirm()
	final := current()
	if final.ConfirmedVersion != 3 || final.LibraryState != domain.LibraryActive {
		t.Fatal(final)
	}
}
