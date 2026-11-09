//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func reportPublishedQuestion(t *testing.T, s *Server, h http.Handler, token, version, id string) *domain.A2Question {
	t.Helper()
	q := seedPassedQuestion(t, s, h, token, version, id, "患者，男，45岁。反复上腹痛2年，该患者最可能的诊断是")
	current := mustLibraryQuestion(t, s, q.ID)
	current.Status = domain.StatusPublished
	if err := s.questionStore.SaveQuestion(storage.WithQuestionChange(t.Context(), storage.QuestionChange{Actor: "report-reviewer"}), *current); err != nil {
		t.Fatal(err)
	}
	return mustLibraryQuestion(t, s, q.ID)
}

func TestReportGlobalViewerCanExportWithoutPersonalViewAll(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	q := reportPublishedQuestion(t, s, h, teacher, v, "report-shared")
	if err := s.shareStore.CreateQuestionShare(t.Context(), domain.QuestionShareRequest{ID: "report-shared-request", QuestionID: q.ID, OwnerID: q.OwnerID, Status: domain.QuestionSharePending, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.shareStore.ReviewQuestionShare(t.Context(), "report-shared-request", "fixture-reviewer", domain.QuestionShareApproved, ""); err != nil {
		t.Fatal(err)
	}
	admin := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.210")
	for _, global := range []bool{true, false} {
		name := "report-no-global"
		perms := []string{domain.PermQuestionViewFormal, domain.PermQuestionDownload}
		if global {
			name = "report-global"
			perms = append(perms, domain.PermQuestionViewGlobal)
		}
		created := serveGenerateJSON(t, h, "POST", "/api/users", admin, map[string]any{"username": name, "password": "report-global-password", "permissions": perms})
		if created.Code != 201 {
			t.Fatal(created.Code, created.Body)
		}
		token := loginForAuthTest(t, h, name, "report-global-password", "198.51.100.211")
		if global {
			listed := serveGenerateJSON(t, h, "GET", "/api/questions?scope=global&tier=formal", token, nil)
			if listed.Code != 200 || !strings.Contains(listed.Body.String(), q.ID) {
				t.Fatal(listed.Code, listed.Body)
			}
		}
		exported := serveGenerateJSON(t, h, "POST", "/api/export/xlsx", token, map[string]any{"scope": "global", "question_ids": []string{q.ID}})
		want := 403
		if global {
			want = 200
		}
		if exported.Code != want {
			t.Errorf("global=%v export=%d want=%d body=%s", global, exported.Code, want, exported.Body)
		}
	}
}

func TestReportPublishedOwnerDeletionIsSupportedAndHiddenFromFormal(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	q := reportPublishedQuestion(t, s, h, token, v, "report-owner-published")
	r := serveGenerateJSON(t, h, "POST", "/api/questions/library-delete", token, libraryItemsPayload(t, s, q.ID))
	if r.Code != 200 {
		t.Fatal("new personal deletion must stay available", r.Code, r.Body)
	}
	deleted := mustLibraryQuestion(t, s, q.ID)
	if deleted.LibraryState != domain.LibraryDeleted || deleted.DeletedReason != domain.DeleteReasonOwnerDelete {
		t.Fatal(deleted)
	}
	listed := serveGenerateJSON(t, h, "GET", "/api/questions?scope=personal&tier=formal", token, nil)
	if listed.Code != 200 || strings.Contains(listed.Body.String(), q.ID) {
		t.Errorf("formal list still includes deleted item: %d %s", listed.Code, listed.Body)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/questions/library-restore", token, libraryItemsPayload(t, s, q.ID))
	if r.Code != 200 {
		t.Fatal("owner restore is intentional C3 behavior", r.Code, r.Body)
	}
	restored := mustLibraryQuestion(t, s, q.ID)
	if restored.Status != domain.StatusAIReviewed || restored.LibraryState != domain.LibraryUnfiled || restored.ConfirmedVersion != 0 {
		t.Fatal(restored)
	}
	r = confirmViaAPI(t, h, token, libraryItemsPayload(t, s, q.ID))
	if r.Code != 200 {
		t.Fatal("no manual review required after allowed restore", r.Code, r.Body)
	}
}

type reportShareFailure struct{ storage.QuestionShareStore }

func (s reportShareFailure) GetQuestionShareByQuestionID(context.Context, string) (*domain.QuestionShareRequest, error) {
	return nil, errors.New("injected unavailable share store")
}

func TestReportLegacyMutationsFailClosedWhenShareLookupFails(t *testing.T) {
	for _, operation := range []string{"archive", "unpublish"} {
		t.Run(operation, func(t *testing.T) {
			s, h, teacher, v, cleanup := libraryTestEnv(t)
			defer cleanup()
			q := reportPublishedQuestion(t, s, h, teacher, v, "report-share-failure-"+operation)
			s.shareStore = reportShareFailure{s.shareStore}
			// This is the authorized legacy management path. Ordinary authors
			// must use the recoverable three-axis library-delete endpoint.
			token := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.213")
			method, path := "DELETE", "/api/questions/"+q.ID
			if operation == "unpublish" {
				method = "POST"
				path += "/unpublish"
			}
			r := serveGenerateJSON(t, h, method, path, token, map[string]any{"reason": "report unavailable share guard"})
			if r.Code != 503 {
				t.Errorf("unavailable share guard: %d %s", r.Code, r.Body)
			}
			after := mustLibraryQuestion(t, s, q.ID)
			if after.Status != q.Status || after.LibraryState != q.LibraryState || after.LibraryRevision != q.LibraryRevision {
				t.Errorf("failed share lookup mutated question: %+v", after)
			}
		})
	}
}

func TestReportGenerationHistoryHidesDeletedBodyWithoutChangingCounts(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, token, v, "report-run-deleted", "患者，男，45岁。病例。该患者最可能的诊断是")
	run := domain.GenerationRun{ID: "gen-report-deleted", OwnerID: q.OwnerID, Status: domain.GenerationRunSucceeded, RequestedCount: 1, QuestionIDs: []string{q.ID}, MaxAttempts: 3}
	if _, err := s.generationRunStore.CreateGenerationRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	deleted := serveGenerateJSON(t, h, "POST", "/api/questions/library-delete", token, libraryItemsPayload(t, s, q.ID))
	if deleted.Code != 200 {
		t.Fatal(deleted.Code, deleted.Body)
	}
	r := serveGenerateJSON(t, h, "GET", "/api/generation-runs/"+run.ID, token, nil)
	var result struct {
		Run       domain.GenerationRun `json:"run"`
		Questions []domain.A2Question  `json:"questions"`
		Hidden    int                  `json:"hidden_deleted_count"`
		IDs       []string             `json:"hidden_deleted_ids"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil {
		t.Fatal(r.Code, r.Body)
	}
	if len(result.Questions) != 0 || result.Hidden != 1 || len(result.IDs) != 1 || len(result.Run.QuestionIDs) != 1 || result.Run.RequestedCount != 1 {
		t.Errorf("deleted result leaked or historical counts changed: %+v", result)
	}
}

type reportExportQueryProbe struct {
	storage.QuestionStore
	fullReads int
	filters   []storage.QuestionFilter
}

func (p *reportExportQueryProbe) ListQuestions(context.Context) ([]domain.A2Question, error) {
	p.fullReads++
	return nil, errors.New("unbounded export table read")
}
func (p *reportExportQueryProbe) SearchQuestions(ctx context.Context, f storage.QuestionFilter, page, size int) ([]domain.A2Question, int, error) {
	p.filters = append(p.filters, f)
	return p.QuestionStore.SearchQuestions(ctx, f, page, size)
}
func (p *reportExportQueryProbe) HasInitialCheckPass(ctx context.Context, id string) (bool, error) {
	return p.QuestionStore.(storage.InitialCheckEvidenceStore).HasInitialCheckPass(ctx, id)
}
func (p *reportExportQueryProbe) HasActiveReviewTask(ctx context.Context, id string) (bool, error) {
	return p.QuestionStore.(interface {
		HasActiveReviewTask(context.Context, string) (bool, error)
	}).HasActiveReviewTask(ctx, id)
}
func TestReportExportAllReadsBoundedOwnedCandidates(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	q := reportPublishedQuestion(t, s, h, token, v, "report-export-query")
	probe := &reportExportQueryProbe{QuestionStore: s.questionStore}
	s.questionStore = probe
	r := serveGenerateJSON(t, h, "POST", "/api/export/xlsx", token, map[string]any{"scope": "personal", "export_all": true, "q": "患者"})
	if r.Code != 200 {
		t.Fatalf("filtered bounded export failed: %d %s", r.Code, r.Body)
	}
	if probe.fullReads != 0 || len(probe.filters) == 0 || probe.filters[0].OwnerID != q.OwnerID || len(probe.filters[0].LibraryStates) != 1 {
		t.Fatalf("export did not prefilter scope/library: full=%d filters=%+v", probe.fullReads, probe.filters)
	}
}

func TestReportUnboundGenerationIsRejectedBeforeAdmission(t *testing.T) {
	s, h, token, _, cleanup := libraryTestEnv(t)
	defer cleanup()
	id := "gen-unbound-report"
	r := serveGenerateJSON(t, h, "POST", "/api/questions/generate", token, map[string]any{"run_id": id, "workflow_mode": "direct", "count": 1, "topic": "临床自由主题"})
	if r.Code != 400 {
		t.Errorf("missing syllabus binding admitted a paid generation: %d %s", r.Code, r.Body)
	}
	run, err := s.generationRunStore.GetGenerationRun(t.Context(), id)
	if err != nil || run != nil {
		t.Error("invalid input reserved a generation task", run != nil, err)
	}
}

func TestReportSharingApprovalDoesNotRevokeUnchangedPersonalExport(t *testing.T) {
	s, h, teacher, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	t.Chdir(t.TempDir())
	source := seedPassedQuestion(t, s, h, teacher, v, "report-policy-source", "患者，男，45岁。最可能的诊断是")
	admin, _ := s.authSvc.GetUserByUsername("admin")
	q := source
	q.ID = "report-policy-owned"
	q.OwnerID = admin.ID
	q.CreatedBy = admin.Username
	q.Status = domain.StatusPublished
	q.LibraryState = domain.LibraryActive
	q.ConfirmedVersion = q.Version
	q.ConfirmationSource = domain.ConfirmSourceLegacy
	if err := s.questionStore.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	token := loginForAuthTest(t, h, "admin", "admin-password", "198.51.100.230")
	exported := serveGenerateJSON(t, h, "POST", "/api/export/xlsx", token, map[string]any{"scope": "personal", "question_ids": []string{q.ID}})
	if exported.Code != 200 {
		t.Fatal(exported.Code, exported.Body)
	}
	var file struct {
		Filename string `json:"filename"`
	}
	_ = json.Unmarshal(exported.Body.Bytes(), &file)
	share := serveGenerateJSON(t, h, "POST", "/api/questions/"+q.ID+"/share", token, nil)
	if share.Code != 201 {
		t.Fatal(share.Code, share.Body)
	}
	var payload struct {
		Request domain.QuestionShareRequest `json:"request"`
	}
	_ = json.Unmarshal(share.Body.Bytes(), &payload)
	approved := serveGenerateJSON(t, h, "POST", "/api/question-shares/"+payload.Request.ID+"/review", token, map[string]any{"status": "approved"})
	if approved.Code != 200 {
		t.Fatal("explicitly authorized share reviewer may approve own request under current policy", approved.Code, approved.Body)
	}
	download := serveGenerateJSON(t, h, "GET", "/api/export/download/"+file.Filename, token, nil)
	if download.Code != 200 {
		t.Fatal("unchanged personal export was unnecessarily invalidated by separate sharing approval", download.Code, download.Body)
	}
}

func TestReportLegacyArchiveRequiresPermissionAndStaysDistinctFromRecoverableDeletion(t *testing.T) {
	s, h, token, v, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, token, v, "report-owner-archive", "患者，男，45岁。最可能的诊断是")
	r := serveGenerateJSON(t, h, "DELETE", "/api/questions/"+q.ID, token, nil)
	if r.Code != 409 || mustLibraryQuestion(t, s, q.ID).Status != domain.StatusAIReviewed {
		t.Fatal("ordinary author bypassed recoverable deletion", r.Code, r.Body)
	}
	owner, err := s.authSvc.GetUserByUsername("lib-teacher")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.authSvc.UpdateUserWithRoles(owner.ID, owner.DisplayName, owner.Role, owner.Roles, append(owner.DirectPermissions, domain.PermQuestionDelete), nil, nil); err != nil {
		t.Fatal(err)
	}
	r = serveGenerateJSON(t, h, "DELETE", "/api/questions/"+q.ID, token, nil)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	archived := mustLibraryQuestion(t, s, q.ID)
	if archived.DeletedReason != domain.DeleteReasonOwnerArchive || archived.Status != domain.StatusArchived {
		t.Fatal("legacy owner archive mislabelled", archived.DeletedReason, archived.Status)
	}
	r = serveGenerateJSON(t, h, "POST", "/api/questions/library-restore", token, libraryItemsPayload(t, s, q.ID))
	if r.Code == 200 {
		t.Fatal("legacy permanent archive was mistaken for recoverable owner_delete")
	}
}
