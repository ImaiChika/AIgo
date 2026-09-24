package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"aigo/internal/storage"
)

// 所有生成准入与配置更新共用一个短事务锁。它只保护计数和任务写入，
// 不覆盖任何模型调用；因此多个 HTTP 请求不能同时穿过同一剩余额度。
const generationQuotaLockKey int64 = 0x4149474f5f51554f // AIGO_QUO

func beginGenerationQuotaTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationQuotaLockKey); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func readGenerationQuota(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (storage.GenerationQuota, error) {
	var quota storage.GenerationQuota
	err := q.QueryRowContext(ctx, `SELECT single_max_questions, batch_max_questions, single_active_per_user,
		batch_active_per_user, global_pending_questions FROM generation_quota WHERE id=1`).Scan(
		&quota.SingleMaxQuestions, &quota.BatchMaxQuestions, &quota.SingleActivePerUser,
		&quota.BatchActivePerUser, &quota.GlobalPendingQuestions)
	return quota, err
}

func readGenerationQuotaUsage(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (storage.GenerationQuotaUsage, error) {
	var usage storage.GenerationQuotaUsage
	err := q.QueryRowContext(ctx, `WITH single_active AS (
		SELECT COUNT(*) AS runs, COALESCE(SUM(requested_count),0) AS units
		FROM generation_runs WHERE status IN ('pending','running')
	), batch_active AS (
		SELECT COUNT(*) AS jobs, COALESCE(SUM(GREATEST(total_count-completed-failed,0)),0) AS units
		FROM batch_jobs WHERE status IN ('pending','submitted','validating','in_progress','running')
	), check_active AS (
		SELECT COUNT(*) AS tasks FROM ai_check_tasks WHERE status IN ('pending','running')
	), import_jobs AS (
		SELECT COUNT(*) AS jobs FROM batch_jobs WHERE cardinality(import_reserved_ids)>0
	), import_reserved AS (
		SELECT COUNT(*) AS units FROM batch_jobs b
		CROSS JOIN LATERAL unnest(b.import_reserved_ids) AS reservation(question_id)
		WHERE cardinality(b.import_reserved_ids)>0
		  AND NOT EXISTS (SELECT 1 FROM ai_check_tasks t WHERE t.question_id=reservation.question_id)
		  AND NOT EXISTS (SELECT 1 FROM ai_check_discards d WHERE d.question_id=reservation.question_id)
		  AND NOT EXISTS (SELECT 1 FROM ai_review_results r WHERE r.question_id=reservation.question_id)
	), overlap AS (
		SELECT COUNT(*) AS units FROM ai_check_tasks t
		WHERE t.status IN ('pending','running') AND EXISTS (
			SELECT 1 FROM generation_runs gr
			WHERE gr.status IN ('pending','running') AND t.question_id=ANY(gr.question_ids)
		)
	)
	SELECT s.runs, b.jobs, c.tasks, ij.jobs, s.units, b.units, ir.units, o.units,
	       s.units+b.units+c.tasks+ir.units-o.units
	FROM single_active s CROSS JOIN batch_active b CROSS JOIN check_active c
	CROSS JOIN import_jobs ij CROSS JOIN import_reserved ir CROSS JOIN overlap o`).Scan(
		&usage.ActiveSingleRuns, &usage.ActiveBatchJobs, &usage.ActiveAICheckTasks, &usage.ActiveBatchImports,
		&usage.SinglePendingQuestions, &usage.BatchPendingQuestions, &usage.BatchImportReservedQuestions,
		&usage.OverlapQuestions, &usage.PendingQuestionUnits)
	return usage, err
}

func (s *Store) GetGenerationQuota(ctx context.Context) (storage.GenerationQuota, storage.GenerationQuotaUsage, error) {
	// 配置与各阶段计数来自同一快照，避免页面一轮刷新显示不一致的数字。
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return storage.GenerationQuota{}, storage.GenerationQuotaUsage{}, err
	}
	defer tx.Rollback()
	quota, err := readGenerationQuota(ctx, tx)
	if err != nil {
		return quota, storage.GenerationQuotaUsage{}, err
	}
	usage, err := readGenerationQuotaUsage(ctx, tx)
	if err != nil {
		return quota, usage, err
	}
	if err := tx.Commit(); err != nil {
		return quota, usage, err
	}
	return quota, usage, nil
}

func (s *Store) SaveGenerationQuota(ctx context.Context, quota storage.GenerationQuota) error {
	if err := quota.Validate(); err != nil {
		return err
	}
	tx, err := beginGenerationQuotaTx(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE generation_quota SET single_max_questions=$1, batch_max_questions=$2,
		single_active_per_user=$3, batch_active_per_user=$4, global_pending_questions=$5,
		updated_at=NOW() WHERE id=1`, quota.SingleMaxQuestions, quota.BatchMaxQuestions,
		quota.SingleActivePerUser, quota.BatchActivePerUser, quota.GlobalPendingQuestions)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func checkGenerationQuota(ctx context.Context, tx *sql.Tx, ownerID, kind string, requested int) error {
	quota, err := readGenerationQuota(ctx, tx)
	if err != nil {
		return err
	}
	usage, err := readGenerationQuotaUsage(ctx, tx)
	if err != nil {
		return err
	}
	if kind == "single" {
		if requested < 1 || requested > quota.SingleMaxQuestions {
			return &storage.GenerationQuotaError{Message: fmt.Sprintf("单次最多生成 %d 道题，请减少题数", quota.SingleMaxQuestions)}
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM generation_runs WHERE owner_id=$1 AND status IN ('pending','running')`, ownerID).Scan(&active); err != nil {
			return err
		}
		if active >= quota.SingleActivePerUser {
			return &storage.GenerationQuotaError{Message: fmt.Sprintf("您已有 %d 个未完成的单题任务，请等待完成后再提交", active)}
		}
	} else {
		if requested < 1 || requested > quota.BatchMaxQuestions {
			return &storage.GenerationQuotaError{Message: fmt.Sprintf("批量任务最多生成 %d 道题，请减少要点或每点题数", quota.BatchMaxQuestions)}
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM batch_jobs WHERE owner_id=$1 AND status IN ('pending','submitted','validating','in_progress','running')`, ownerID).Scan(&active); err != nil {
			return err
		}
		if active >= quota.BatchActivePerUser {
			return &storage.GenerationQuotaError{Message: fmt.Sprintf("您已有 %d 个进行中的批量任务，请等待完成后再提交", active)}
		}
	}
	if usage.PendingQuestionUnits+requested > quota.GlobalPendingQuestions {
		return &storage.GenerationQuotaError{Message: fmt.Sprintf("全站未完成出题/检查量已达 %d 道，本次需 %d 道，当前上限 %d 道，请稍后重试", usage.PendingQuestionUnits, requested, quota.GlobalPendingQuestions)}
	}
	return nil
}

func (s *Store) ReactivateBatchJob(ctx context.Context, job storage.BatchJobRecord) error {
	tx, err := beginGenerationQuotaTx(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentStatus, ownerID string
	if err := tx.QueryRowContext(ctx, `SELECT status, owner_id FROM batch_jobs WHERE id=$1`, job.ID).Scan(&currentStatus, &ownerID); err != nil {
		return err
	}
	if currentStatus != "completed" && currentStatus != "complete" && currentStatus != "failed" {
		return storage.ErrBatchJobAlreadyActive
	}
	if err := checkGenerationQuota(ctx, tx, ownerID, "batch", job.TotalCount-job.Completed-job.Failed); err != nil {
		return err
	}
	if err := updateBatchJob(ctx, tx, job); err != nil {
		return err
	}
	return tx.Commit()
}
