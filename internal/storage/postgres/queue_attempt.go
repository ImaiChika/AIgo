package postgres

import (
	"context"
	"database/sql"

	"aigo/internal/storage"
)

func queueAttemptMutationResult(result sql.Result, err error, attempt int, lost error) error {
	if err != nil {
		return err
	}
	if attempt == 0 {
		return nil
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return lost
	}
	return nil
}

// Keep the same question -> task lock order as failed/exhausted writes. Once
// both are locked, a lease reclaim cannot advance the attempt during the atomic
// outcome commit. Legacy internal non-worker calls keep their existing path.
func (s *Store) lockAICheckAttempt(ctx context.Context, outcome storage.AICheckOutcome) error {
	attempt := storage.QueueAttemptFromContext(ctx, storage.QueueAICheck, outcome.CompleteTaskID)
	if attempt == 0 {
		return nil
	}
	var locked string
	if err := s.queryRowContext(ctx, `SELECT id FROM questions WHERE id=$1 FOR UPDATE`, outcome.Result.QuestionID).Scan(&locked); err != nil {
		if err == sql.ErrNoRows {
			return storage.ErrAICheckTaskLeaseLost
		}
		return err
	}
	err := s.queryRowContext(ctx, `SELECT id FROM ai_check_tasks WHERE id=$1 AND question_id=$2 AND status='running' AND attempts=$3 FOR UPDATE`, outcome.CompleteTaskID, outcome.Result.QuestionID, attempt).Scan(&locked)
	if err == sql.ErrNoRows {
		return storage.ErrAICheckTaskLeaseLost
	}
	return err
}
