package postgres

import (
	"context"
	"sort"

	"github.com/lib/pq"
)

// WithLibraryTransaction locks all chosen questions in a fixed order before any
// validation or mutation. Nested callers share the transaction and audit writes.
func (s *Store) WithLibraryTransaction(ctx context.Context, ids []string, fn func(context.Context) error) error {
	return s.WithTransaction(ctx, func(txctx context.Context) error {
		ordered := append([]string(nil), ids...)
		sort.Strings(ordered)
		for i, id := range ordered {
			if i > 0 && id == ordered[i-1] {
				continue
			}
			if _, err := s.getQuestionForUpdate(txctx, reviewTransactionFromContext(txctx), id); err != nil {
				return err
			}
		}
		return fn(txctx)
	})
}

// HasInitialCheckPass uses durable evidence, not a mutable lifecycle label. Old
// imports predating check tasks retain a pass result as their explicit evidence.
// A current task must have actually run and completed with its matching result.
func (s *Store) HasInitialCheckPass(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := s.queryRowContext(ctx, `SELECT EXISTS (
	 SELECT 1 FROM ai_review_results ar JOIN questions q ON q.id=ar.question_id
	 WHERE ar.question_id=$1 AND ar.verdict='pass'
	 AND (ar.question_version IN (0,1) AND ar.question_version<=q.version)
	 AND (
	   NOT EXISTS (SELECT 1 FROM ai_check_tasks t WHERE t.question_id=q.id)
	   OR EXISTS (SELECT 1 FROM ai_check_tasks t WHERE t.question_id=q.id
	     AND t.status='succeeded' AND t.attempts>0 AND t.question_version=1 AND t.question_version=ar.question_version)
	 )
	)`, id).Scan(&ok)
	return ok, err
}

func (s *Store) HasActiveReviewTask(ctx context.Context, id string) (bool, error) {
	var active bool
	err := s.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_tasks WHERE question_id=$1 AND NOT(status=ANY($2)))`, id, pq.Array([]string{"published", "approved", "rejected", "archived"})).Scan(&active)
	return active, err
}
