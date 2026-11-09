//go:build integration

package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type summaryCancellationCapture struct {
	storage.QuestionStore
	reported chan error
}

func (s *summaryCancellationCapture) SearchQuestions(ctx context.Context, f storage.QuestionFilter, page, size int) ([]domain.A2Question, int, error) {
	q, n, e := s.QuestionStore.SearchQuestions(ctx, f, page, size)
	if e != nil {
		s.reported <- e
	}
	return q, n, e
}

func (s *summaryCancellationCapture) CountQuestionsByStatus(ctx context.Context, f storage.QuestionFilter) (map[string]int, error) {
	counts, err := s.QuestionStore.CountQuestionsByStatus(ctx, f)
	s.reported <- err
	return counts, err
}

func TestMySummaryRealPostgresCancellation(t *testing.T) {
	for _, path := range []string{"/api/my/summary", "/api/my/summary?view=authoring"} {
		t.Run(path, func(t *testing.T) { testSummaryRealCancellation(t, path) })
	}
}
func testSummaryRealCancellation(t *testing.T, path string) {
	s, store, cleanup := generationHandlerTestServer(t)
	defer cleanup()
	user, e := s.authSvc.GetUserByUsername("admin")
	if e != nil {
		t.Fatal(e)
	}
	lock, e := store.DB().BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Rollback()
	var lockPID int
	if e = lock.QueryRowContext(t.Context(), `SELECT pg_backend_pid()`).Scan(&lockPID); e != nil {
		t.Fatal(e)
	}
	if _, e = lock.ExecContext(t.Context(), `LOCK TABLE questions IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), auth.UserIDKey, user.ID))
	defer cancel()
	capture := &summaryCancellationCapture{QuestionStore: s.questionStore, reported: make(chan error, 1)}
	s.questionStore = capture
	r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.handleMySummary(w, r) }()
	deadline := time.Now().Add(3 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if e = store.DB().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, lockPID).Scan(&blocked); e != nil {
			t.Fatal(e)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("summary never entered the controlled database wait")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled summary kept querying")
	}
	e = <-capture.reported
	var state interface{ SQLState() string }
	if errors.As(e, &state) {
		t.Logf("driver cancellation type=%T SQLSTATE=%s context=%v", e, state.SQLState(), ctx.Err())
	} else {
		t.Logf("driver cancellation type=%T context=%v", e, ctx.Err())
	}
	if w.Code != 499 {
		t.Fatalf("actual PostgreSQL cancellation became %d: %s", w.Code, w.Body)
	}
}
