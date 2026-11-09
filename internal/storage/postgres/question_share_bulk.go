package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"fmt"
)

func (s *Store) PreviewQuestionShareItems(ctx context.Context, filter storage.QuestionFilter, limit int) ([]domain.QuestionShareExpectation, int, error) {
	if filter.OwnerID == "" {
		return nil, 0, storage.ErrQuestionShareNotOwner
	}
	filter.Status = ""
	filter.GlobalOnly = false
	filter.GlobalStatuses = nil
	where, args := questionFilterWhere(filter)
	where += ` AND q.status IN ('published','ai_reviewed') AND q.library_state='active' AND q.confirmed_version=q.version
 AND NOT EXISTS(SELECT 1 FROM question_share_requests qs WHERE qs.question_id=q.id)
 AND NOT EXISTS(SELECT 1 FROM review_tasks rt WHERE rt.question_id=q.id AND rt.status NOT IN ('published','approved','rejected','archived'))
 AND ((q.status='published' AND q.confirmation_source='legacy') OR EXISTS(SELECT 1 FROM ai_review_results ar WHERE ar.question_id=q.id AND ar.verdict='pass' AND ar.question_version IN (0,1) AND ar.question_version<=q.version AND (NOT EXISTS(SELECT 1 FROM ai_check_tasks t WHERE t.question_id=q.id) OR EXISTS(SELECT 1 FROM ai_check_tasks t WHERE t.question_id=q.id AND t.status='succeeded' AND t.attempts>0 AND t.question_version=1 AND t.question_version=ar.question_version))))`
	rows, err := s.queryContext(ctx, `SELECT q.id,q.version,q.library_revision,q.review_revision,COUNT(*) OVER() FROM questions q `+where+` ORDER BY q.created_at DESC,q.id LIMIT $`+fmt.Sprint(len(args)+1), append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []domain.QuestionShareExpectation{}
	total := 0
	for rows.Next() {
		var item domain.QuestionShareExpectation
		var library, review int64
		if err := rows.Scan(&item.QuestionID, &item.ExpectedVersion, &library, &review, &total); err != nil {
			return nil, 0, err
		}
		item.ExpectedLibraryRevision = &library
		item.ExpectedReviewRevision = &review
		items = append(items, item)
	}
	return items, total, rows.Err()
}
func (s *Store) PreviewQuestionShares(ctx context.Context, filter storage.QuestionFilter, limit int) ([]string, int, error) {
	items, total, err := s.PreviewQuestionShareItems(ctx, filter, limit)
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.QuestionID)
	}
	return ids, total, err
}
func (s *Store) CreateQuestionShareItems(ctx context.Context, ownerID string, items []domain.QuestionShareExpectation) ([]string, error) {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item.ExpectedVersion < 1 || item.ExpectedLibraryRevision == nil || item.ExpectedReviewRevision == nil {
			return nil, storage.ErrQuestionShareVersionConflict
		}
		ids = append(ids, item.QuestionID)
	}
	created := []string{}
	err := s.WithLibraryTransaction(ctx, ids, func(txctx context.Context) error {
		for _, item := range items {
			request := domain.QuestionShareRequest{ID: "share-bulk-" + item.QuestionID, QuestionID: item.QuestionID, OwnerID: ownerID, ExpectedVersion: item.ExpectedVersion, ExpectedLibraryRevision: item.ExpectedLibraryRevision, ExpectedReviewRevision: item.ExpectedReviewRevision}
			q, e := s.GetQuestion(txctx, item.QuestionID)
			if e != nil {
				return e
			}
			if e = s.validateShareSource(txctx, q, request); e != nil {
				return e
			}
			existing, e := s.GetQuestionShareByQuestionID(txctx, item.QuestionID)
			if e != nil {
				return e
			}
			if existing != nil {
				continue
			}
			if e = s.createQuestionShareSnapshot(txctx, request); e != nil {
				return e
			}
			created = append(created, item.QuestionID)
		}
		return nil
	})
	return created, err
}

// IDs-only compatibility is restricted to the former published entrance.
func (s *Store) CreateQuestionShares(ctx context.Context, ownerID string, ids []string) ([]string, error) {
	created := []string{}
	err := s.WithLibraryTransaction(ctx, ids, func(txctx context.Context) error {
		for _, id := range ids {
			q, e := s.GetQuestion(txctx, id)
			if e != nil {
				return e
			}
			if q == nil {
				continue
			}
			if ownerID == "" || q.OwnerID != ownerID {
				return storage.ErrQuestionShareNotOwner
			}
			if q.Status != domain.StatusPublished {
				continue
			}
			existing, e := s.GetQuestionShareByQuestionID(txctx, id)
			if e != nil {
				return e
			}
			if existing != nil {
				continue
			}
			e = s.createQuestionShareSnapshot(txctx, domain.QuestionShareRequest{ID: "share-bulk-" + id, QuestionID: id, OwnerID: ownerID})
			if e == storage.ErrQuestionShareNotFormal {
				continue
			}
			if e != nil {
				return e
			}
			created = append(created, id)
		}
		return nil
	})
	return created, err
}
