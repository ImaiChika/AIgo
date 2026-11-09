package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lib/pq"
	"net/http"
	"net/http/httptest"
	"testing"
)

type summaryFailureStore struct {
	storage.QuestionStore
	failAt, calls int
	err           error
}

func (s *summaryFailureStore) SearchQuestions(ctx context.Context, f storage.QuestionFilter, page, size int) ([]domain.A2Question, int, error) {
	s.calls++
	if s.calls == s.failAt {
		return nil, 0, s.err
	}
	return nil, 0, nil
}
func TestMySummaryQueryCancellation(t *testing.T) {
	for step := 1; step <= 5; step++ {
		for _, tc := range []struct {
			name   string
			cancel bool
			err    error
			want   int
		}{{"cancelled", true, context.Canceled, 499}, {"wrapped", true, fmt.Errorf("query: %w", context.Canceled), 499}, {"real_error", true, errors.New("database unavailable"), 500}, {"live_internal_cancel", false, context.Canceled, 500}, {"postgres_cancelled", true, &pq.Error{Code: "57014"}, 499}, {"wrapped_postgres_cancelled", true, fmt.Errorf("query: %w", &pq.Error{Code: "57014"}), 499}, {"live_statement_timeout", false, &pq.Error{Code: "57014"}, 500}, {"postgres_real_failure", true, &pq.Error{Code: "08006"}, 500}, {"deadline_is_not_cancel", true, context.DeadlineExceeded, 500}} {
			t.Run(fmt.Sprintf("query_%d/%s", step, tc.name), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.cancel {
					cancel()
				}
				store := &summaryFailureStore{failAt: step, err: tc.err}
				s := &Server{questionStore: store}
				r := httptest.NewRequest(http.MethodGet, "/api/my/summary", nil).WithContext(ctx)
				w := httptest.NewRecorder()
				s.withRequestObservability(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r.Pattern = "GET /api/my/summary"
					s.handleMySummary(w, r)
				})).ServeHTTP(w, r)
				metrics := s.httpMetrics.snapshot()
				if tc.want == 499 && (len(metrics.Routes) != 1 || metrics.Routes[0].ServerErrors != 0) {
					t.Fatal("cancellation polluted server-error metrics", metrics)
				}
				if w.Code != tc.want {
					t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body)
				}
			})
		}
	}
}

type authoringSummaryProbe struct {
	storage.QuestionStore
	filters  []storage.QuestionFilter
	searches int
	fail     error
}

func (s *authoringSummaryProbe) SearchQuestions(context.Context, storage.QuestionFilter, int, int) ([]domain.A2Question, int, error) {
	s.searches++
	return nil, 0, errors.New("authoring summary must not load question bodies")
}
func (s *authoringSummaryProbe) CountQuestionsByStatus(_ context.Context, f storage.QuestionFilter) (map[string]int, error) {
	s.filters = append(s.filters, f)
	if s.fail != nil {
		return nil, s.fail
	}
	n := 10
	if f.NoActiveReviewTask {
		n = 4
	} else if len(f.LibraryStates) > 0 {
		switch f.LibraryStates[0] {
		case string(domain.LibraryActive):
			n = 5
		case string(domain.LibraryUnfiled):
			n = 4
		case string(domain.LibraryDeleted):
			n = 1
		}
	}
	return map[string]int{"ai_reviewed": n}, nil
}
func TestAuthoringSummaryUsesCountsWithoutLegacyReviewOrBodies(t *testing.T) {
	probe := &authoringSummaryProbe{}
	s := &Server{questionStore: probe}
	ctx := context.WithValue(t.Context(), auth.UserIDKey, "author")
	w := httptest.NewRecorder()
	s.handleMySummary(w, httptest.NewRequest("GET", "/api/my/summary?view=authoring", nil).WithContext(ctx))
	if w.Code != 200 {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	var body map[string]int
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if probe.searches != 0 || len(probe.filters) != 5 || body["generated_count"] != 10 || body["new_questions_count"] != 4 || body["library_active_count"] != 5 || body["library_deleted_count"] != 1 {
		t.Fatalf("wrong summary: %+v searches=%d", body, probe.searches)
	}
	for _, f := range probe.filters {
		if f.OwnerID != "author" {
			t.Fatal("lost owner filter")
		}
	}
	if _, ok := body["pending_review_count"]; ok {
		t.Fatal("legacy review statistics leaked into light path")
	}
}
func TestAuthoringSummaryCancellationAndDatabaseFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel bool
		err    error
		want   int
	}{{"cancelled", true, &pq.Error{Code: "57014"}, 499}, {"live_failure", false, errors.New("db unavailable"), 500}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			s := &Server{questionStore: &authoringSummaryProbe{fail: tc.err}}
			w := httptest.NewRecorder()
			s.handleMySummary(w, httptest.NewRequest("GET", "/api/my/summary?view=authoring", nil).WithContext(ctx))
			if w.Code != tc.want {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
		})
	}
}
