//go:build integration

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"aigo/internal/audit"
	"aigo/internal/domain"
	"aigo/internal/questionlib"
	"aigo/internal/storage"
)

func TestLibraryConfirmationRequiresAllConcurrencyGuards(t *testing.T) {
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, version, "library-guard-contract", "患者，男，40岁。病例。该患者最可能的诊断是")
	for _, guard := range []string{"expected_version", "expected_library_revision", "expected_review_revision"} {
		t.Run(guard, func(t *testing.T) {
			item := map[string]any{"question_id": q.ID, "expected_version": q.Version, "expected_library_revision": q.LibraryRevision, "expected_review_revision": q.ReviewRevision}
			delete(item, guard)
			r := confirmViaAPI(t, h, teacher, map[string]any{"items": []map[string]any{item}})
			if r.Code != 400 {
				t.Fatalf("missing guard: %d %s", r.Code, r.Body)
			}
		})
	}
	r := confirmViaAPI(t, h, teacher, map[string]any{"items": []questionlib.ConfirmItem{}})
	if r.Code != 400 || strings.Count(r.Body.String(), "\"error\"") != 1 {
		t.Fatalf("empty items must produce one error response: %d %s", r.Code, r.Body)
	}
}

func TestLibraryConfirmationUsesPersistedEvidenceWithoutRunningServices(t *testing.T) {
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	good := seedPassedQuestion(t, s, h, teacher, version, "library-durable-good", "患者，男，40岁。病例。该患者最可能的诊断是")
	forged := good
	forged.ID = "library-forged-status"
	if err := s.questionStore.SaveQuestion(storage.WithQuestionChange(t.Context(), storage.QuestionChange{Actor: "lib-teacher", OwnerID: good.OwnerID}), forged); err != nil {
		t.Fatal(err)
	}
	// Neither model execution nor review execution is a zero-round prerequisite.
	s.aiCheckSvc = nil
	s.reviewSvc = nil
	r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, good.ID))
	if r.Code != 200 {
		t.Fatalf("durable evidence should work without services: %d %s", r.Code, r.Body)
	}
	r = confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, forged.ID))
	if r.Code == 200 {
		t.Fatal("mutable ai_reviewed status must not replace first-check evidence")
	}
	if mustLibraryQuestion(t, s, forged.ID).LibraryState == domain.LibraryActive {
		t.Fatal("forged status activated")
	}
}

func TestLibraryConfirmationBatchConflictRollsBackAll(t *testing.T) {
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, version, "library-batch-first", "患者，男，40岁。病例。该患者最可能的诊断是")
	missing := libraryItemForAPI(q)
	missing.QuestionID = "library-batch-missing"
	r := confirmViaAPI(t, h, teacher, map[string]any{"items": []questionlib.ConfirmItem{libraryItemForAPI(q), missing}})
	if r.Code == 200 {
		t.Fatal("batch conflict accepted")
	}
	got := mustLibraryQuestion(t, s, q.ID)
	if got.LibraryState == domain.LibraryActive || got.LibraryRevision != q.LibraryRevision {
		t.Fatalf("partial batch persisted: %+v", got)
	}
	logs, err := s.auditSvc.ListByQuestion(t.Context(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range logs {
		if log.Action == "library_confirm" {
			t.Fatal("rejected batch retained success audit")
		}
	}
}

type failingLibraryAudit struct{ storage.AuditStore }

func (s failingLibraryAudit) SaveLog(ctx context.Context, log domain.AuditLog) error {
	if strings.HasPrefix(log.Action, "library_") {
		return errors.New("injected library audit failure")
	}
	return s.AuditStore.SaveLog(ctx, log)
}
func TestLibraryAuditFailureRollsBackConfirmation(t *testing.T) {
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, version, "library-audit-rollback", "患者，男，40岁。病例。该患者最可能的诊断是")
	s.auditSvc = audit.NewService(failingLibraryAudit{s.questionStore.(storage.AuditStore)})
	r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID))
	if r.Code != 500 {
		t.Fatalf("failed audit must reject: %d %s", r.Code, r.Body)
	}
	got := mustLibraryQuestion(t, s, q.ID)
	if got.LibraryState == domain.LibraryActive || got.LibraryRevision != q.LibraryRevision {
		t.Fatal("confirmation survived failed audit")
	}
}

func TestLibraryConcurrentConfirmAndDeleteHaveOneWinner(t *testing.T) {
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, version, "library-concurrent", "患者，男，40岁。病例。该患者最可能的诊断是")
	payload := libraryItemsPayload(t, s, q.ID)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, path := range []string{"/api/questions/confirm", "/api/questions/library-delete"} {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			<-start
			resp := serveAuthJSON(t, h, "POST", path, teacher, "198.51.100.191", payload)
			codes <- resp.Code
		}(path)
	}
	close(start)
	wg.Wait()
	close(codes)
	success, conflicts := 0, 0
	for code := range codes {
		switch code {
		case 200:
			success++
		case 409:
			conflicts++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	got := mustLibraryQuestion(t, s, q.ID)
	if got.LibraryRevision != q.LibraryRevision+1 {
		t.Fatalf("more than one mutation committed: %+v", got)
	}
}

func TestLibraryReplayedConfirmationDoesNotDuplicateAuditOrBypassLaterDeletion(t *testing.T) {
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, version, "library-replay", "患者，男，40岁。病例。该患者最可能的诊断是")
	payload := libraryItemsPayload(t, s, q.ID)
	for i := 0; i < 2; i++ {
		r := confirmViaAPI(t, h, teacher, payload)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body)
		}
	}
	logs, err := s.auditSvc.ListByQuestion(t.Context(), q.ID)
	if err != nil {
		t.Fatal(err)
	}
	confirmations := 0
	for _, log := range logs {
		if log.Action == "library_confirm" {
			confirmations++
		}
	}
	if confirmations != 1 {
		t.Fatalf("replay wrote %d confirmation events", confirmations)
	}
	r := serveAuthJSON(t, h, "POST", "/api/questions/library-delete", teacher, "198.51.100.191", libraryItemsPayload(t, s, q.ID))
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	r = confirmViaAPI(t, h, teacher, payload)
	if r.Code != 409 {
		t.Fatalf("old confirmation resurrected deletion: %d %s", r.Code, r.Body)
	}
}

func TestLibraryPersistedActiveReviewBlocksAllMutationsWithoutReviewService(t *testing.T) {
	s, h, teacher, version, cleanup := libraryTestEnv(t)
	defer cleanup()
	q := seedPassedQuestion(t, s, h, teacher, version, "library-inflight", "患者，男，40岁。病例。该患者最可能的诊断是")
	reviewStore := s.questionStore.(storage.ReviewStore)
	flow := domain.ReviewFlowConfig{ID: "library-flow", Name: "在途流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{q.OwnerID}, RequiredCount: 1}}, CreatedAt: time.Now()}
	if err := reviewStore.SaveFlowConfig(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	if err := reviewStore.SaveTask(t.Context(), domain.ReviewTask{ID: "library-active-task", QuestionID: q.ID, FlowID: flow.ID, Status: domain.StatusReviewing, QuestionVersion: q.Version, CurrentRound: 1, AssignedTo: []string{q.OwnerID}, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.reviewSvc = nil
	for _, path := range []string{"/api/questions/confirm", "/api/questions/library-delete", "/api/questions/library-restore"} {
		r := serveAuthJSON(t, h, "POST", path, teacher, "198.51.100.191", libraryItemsPayload(t, s, q.ID))
		if r.Code != http.StatusConflict {
			t.Fatalf("%s bypassed durable task: %d %s", path, r.Code, r.Body)
		}
	}
}

func TestLibraryDeleteAndRestoreBatchConflictRollBackAll(t *testing.T) {
	for _, operation := range []string{"delete", "restore"} {
		t.Run(operation, func(t *testing.T) {
			s, h, teacher, version, cleanup := libraryTestEnv(t)
			defer cleanup()
			q := seedPassedQuestion(t, s, h, teacher, version, "library-atomic-"+operation, "患者，男，40岁。病例。该患者最可能的诊断是")
			if operation == "restore" {
				r := serveAuthJSON(t, h, "POST", "/api/questions/library-delete", teacher, "198.51.100.191", libraryItemsPayload(t, s, q.ID))
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body)
				}
			}
			before := mustLibraryQuestion(t, s, q.ID)
			missing := libraryItemForAPI(*before)
			missing.QuestionID = "missing-later-item"
			r := serveAuthJSON(t, h, "POST", "/api/questions/library-"+operation, teacher, "198.51.100.191", map[string]any{"items": []questionlib.ConfirmItem{libraryItemForAPI(*before), missing}})
			if r.Code == 200 {
				t.Fatal("invalid later item accepted")
			}
			after := mustLibraryQuestion(t, s, q.ID)
			if after.LibraryState != before.LibraryState || after.LibraryRevision != before.LibraryRevision {
				t.Fatalf("partial %s persisted", operation)
			}
		})
	}
}

func TestLibraryDeleteRespectsPersistedPendingAndApprovedShares(t *testing.T) {
	for _, status := range []domain.QuestionShareStatus{domain.QuestionSharePending, domain.QuestionShareApproved} {
		t.Run(string(status), func(t *testing.T) {
			s, h, teacher, version, cleanup := libraryTestEnv(t)
			defer cleanup()
			base := seedPassedQuestion(t, s, h, teacher, version, "library-share-original", "患者，男，40岁。病例。该患者最可能的诊断是")
			q := base
			q.ID = "library-share-" + string(status)
			q.Status = domain.StatusPublished
			q.LibraryState = domain.LibraryActive
			q.ConfirmedVersion = q.Version
			q.ConfirmationSource = domain.ConfirmSourceLegacy
			if err := s.questionStore.SaveQuestion(storage.WithQuestionChange(t.Context(), storage.QuestionChange{Actor: "lib-teacher", OwnerID: q.OwnerID}), q); err != nil {
				t.Fatal(err)
			}
			share := domain.QuestionShareRequest{ID: "share-" + q.ID, QuestionID: q.ID, OwnerID: q.OwnerID, Status: domain.QuestionSharePending, CreatedAt: time.Now()}
			if err := s.shareStore.CreateQuestionShare(t.Context(), share); err != nil {
				t.Fatal(err)
			}
			if status == domain.QuestionShareApproved {
				if _, err := s.shareStore.ReviewQuestionShare(t.Context(), share.ID, "test-reviewer", domain.QuestionShareApproved, "test approval"); err != nil {
					t.Fatal(err)
				}
			}
			r := serveAuthJSON(t, h, "POST", "/api/questions/library-delete", teacher, "198.51.100.191", libraryItemsPayload(t, s, q.ID))
			if r.Code != 409 {
				t.Fatalf("shared question deleted: %d %s", r.Code, r.Body)
			}
			if got := mustLibraryQuestion(t, s, q.ID); got.LibraryState != domain.LibraryActive {
				t.Fatal("share lock lost its source")
			}
		})
	}
}

func TestLibraryPersistedReviewChecksHistoryWithoutLockingArchivedTasks(t *testing.T) {
	for _, hiddenActive := range []bool{false, true} {
		name := "archived_history"
		if hiddenActive {
			name = "active_hidden_by_later_terminal"
		}
		t.Run(name, func(t *testing.T) {
			s, h, teacher, version, cleanup := libraryTestEnv(t)
			defer cleanup()
			q := seedPassedQuestion(t, s, h, teacher, version, "library-review-history", "患者，男，40岁。病例。该患者最可能的诊断是")
			store := s.questionStore.(storage.ReviewStore)
			if err := store.SaveFlowConfig(t.Context(), domain.ReviewFlowConfig{ID: "history-flow", Name: "历史流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{q.OwnerID}, RequiredCount: 1}}, CreatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			oldStatus := domain.StatusArchived
			if hiddenActive {
				oldStatus = domain.StatusReviewing
			}
			for _, item := range []struct {
				id     string
				status domain.QuestionStatus
				at     time.Time
			}{{"older", oldStatus, time.Now().Add(-time.Hour)}, {"newer", domain.StatusPublished, time.Now()}} {
				if err := store.SaveTask(t.Context(), domain.ReviewTask{ID: "history-" + item.id, QuestionID: q.ID, FlowID: "history-flow", Status: item.status, QuestionVersion: q.Version, CurrentRound: 1, CreatedAt: item.at, UpdatedAt: item.at}); err != nil {
					t.Fatal(err)
				}
			}
			s.reviewSvc = nil
			r := confirmViaAPI(t, h, teacher, libraryItemsPayload(t, s, q.ID))
			want := 200
			if hiddenActive {
				want = 409
			}
			if r.Code != want {
				t.Fatalf("history guard: want %d got %d %s", want, r.Code, r.Body)
			}
		})
	}
}
