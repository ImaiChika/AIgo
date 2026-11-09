package postgres

import (
	"context"
	"fmt"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"github.com/lib/pq"
)

func (s *Store) CommitGenerationRun(ctx context.Context, commit storage.GenerationRunCommit) error {
	return s.WithTransaction(ctx, func(ctx context.Context) error {
		tx := reviewTransactionFromContext(ctx)
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationQuotaLockKey); err != nil {
			return err
		}
		var status, owner string
		var attempt, requested int
		if err := tx.QueryRowContext(ctx, `SELECT status,owner_id,attempts,requested_count FROM generation_runs WHERE id=$1 FOR UPDATE`, commit.RunID).Scan(&status, &owner, &attempt, &requested); err != nil {
			return err
		}
		if status == domain.GenerationRunSucceeded {
			if attempt != commit.Attempt {
				return storage.ErrGenerationRunLeaseLost
			}
			return nil
		}
		if status != domain.GenerationRunRunning || attempt != commit.Attempt {
			return storage.ErrGenerationRunLeaseLost
		}
		if requested < 1 || len(commit.Questions) != requested {
			return fmt.Errorf("生成交接题数不一致: 请求%d道，提交%d道", requested, len(commit.Questions))
		}
		if commit.CheckMaxAttempts < 1 || commit.CheckMaxAttempts > 10 {
			return fmt.Errorf("首检尝试次数无效")
		}
		ids := make([]string, 0, len(commit.Questions))
		for _, q := range commit.Questions {
			existing, err := s.getQuestionForUpdate(ctx, tx, q.ID)
			if err != nil {
				return err
			}
			if existing != nil {
				return fmt.Errorf("题目ID已存在，不能覆盖已有题目")
			}
			q.OwnerID = owner
			q.Status = domain.StatusAIDraft
			if err := s.SaveQuestion(ctx, q); err != nil {
				return err
			}
			created, err := s.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "act-run-" + q.ID, QuestionID: q.ID, QuestionVersion: q.Version, Status: domain.AICheckTaskPending, MaxAttempts: commit.CheckMaxAttempts})
			if err != nil {
				return err
			}
			if !created {
				return fmt.Errorf("题目%s已有首次检查任务", q.ID)
			}
			ids = append(ids, q.ID)
		}
		_, err := tx.ExecContext(ctx, `UPDATE generation_runs SET status='succeeded',question_ids=$2,error='',completed_at=NOW(),updated_at=NOW(),leased_until=NULL WHERE id=$1`, commit.RunID, pq.Array(ids))
		return err
	})
}

var _ storage.GenerationRunCommitter = (*Store)(nil)
