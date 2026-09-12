package postgres

import (
	"context"
	"fmt"

	"aigo/internal/storage"
	"github.com/lib/pq"
)

func (s *Store) PreviewQuestionShares(ctx context.Context, filter storage.QuestionFilter, limit int) ([]string, int, error) {
	if filter.OwnerID == "" {
		return nil, 0, storage.ErrQuestionShareNotOwner
	}
	filter.Status = "published"
	where, args := questionFilterWhere(filter)
	where += ` AND NOT EXISTS (SELECT 1 FROM question_share_requests qs WHERE qs.question_id=q.id)`
	// One statement keeps the count and IDs on the same database snapshot.
	rows, err := s.db.QueryContext(ctx, `SELECT q.id, COUNT(*) OVER () FROM questions q `+where+
		` ORDER BY q.created_at DESC, q.id LIMIT $`+fmt.Sprint(len(args)+1), append(args, limit)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids := []string{}
	total := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id, &total); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	return ids, total, rows.Err()
}

func (s *Store) CreateQuestionShares(ctx context.Context, ownerID string, ids []string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Lock the actual question rows before checking eligibility; concurrent withdrawal
	// and duplicate submissions cannot slip between validation and insertion.
	rows, err := tx.QueryContext(ctx, `SELECT owner_id FROM questions WHERE id=ANY($1) ORDER BY id FOR UPDATE`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			rows.Close()
			return nil, err
		}
		if ownerID == "" || owner != ownerID {
			rows.Close()
			return nil, storage.ErrQuestionShareNotOwner
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `INSERT INTO question_share_requests(id, question_id, owner_id, status)
		SELECT 'share-bulk-'||q.id, q.id, q.owner_id, 'pending'
		FROM questions q WHERE q.id=ANY($1) AND q.owner_id=$2 AND q.status='published'
		ON CONFLICT (question_id) DO NOTHING RETURNING question_id`, pq.Array(ids), ownerID)
	if err != nil {
		return nil, err
	}
	created := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		created = append(created, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return created, nil
}
