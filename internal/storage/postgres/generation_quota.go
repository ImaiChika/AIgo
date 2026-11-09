package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/llm"
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
	var tools llm.NetworkTools
	err := q.QueryRowContext(ctx, `SELECT single_max_questions, batch_max_questions, single_active_per_user,
		batch_active_per_user, global_pending_questions,generation_concurrency,batch_concurrency,ai_check_concurrency,revision,web_search_enabled,web_extractor_enabled,learning_concurrency FROM generation_quota WHERE id=1`).Scan(
		&quota.SingleMaxQuestions, &quota.BatchMaxQuestions, &quota.SingleActivePerUser,
		&quota.BatchActivePerUser, &quota.GlobalPendingQuestions, &quota.GenerationConcurrency, &quota.BatchConcurrency, &quota.AICheckConcurrency, &quota.Revision, &tools.WebSearchEnabled, &tools.WebExtractorEnabled, &quota.LearningConcurrency)
	quota.ModelTools = &tools
	return quota, err
}

// No usage aggregation or cache on the model path; a failed read never enables tools.
func (s *Store) ResolveNetworkTools(ctx context.Context) (llm.NetworkTools, error) {
	p, _, err := s.ResolveVersionedNetworkTools(ctx)
	return p, err
}

func (s *Store) ResolveVersionedNetworkTools(ctx context.Context) (llm.NetworkTools, int64, error) {
	var p llm.NetworkTools
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT web_search_enabled,web_extractor_enabled,revision FROM generation_quota WHERE id=1`).Scan(&p.WebSearchEnabled, &p.WebExtractorEnabled, &revision)
	return p, revision, err
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
	       s.units+b.units+c.tasks+ir.units-o.units,
 (SELECT count(*) FROM learning_analysis_jobs WHERE status='running'),(SELECT count(*) FROM learning_analysis_jobs WHERE status='pending')
	FROM single_active s CROSS JOIN batch_active b CROSS JOIN check_active c
	CROSS JOIN import_jobs ij CROSS JOIN import_reserved ir CROSS JOIN overlap o`).Scan(
		&usage.ActiveSingleRuns, &usage.ActiveBatchJobs, &usage.ActiveAICheckTasks, &usage.ActiveBatchImports,
		&usage.SinglePendingQuestions, &usage.BatchPendingQuestions, &usage.BatchImportReservedQuestions,
		&usage.OverlapQuestions, &usage.PendingQuestionUnits, &usage.RunningLearningJobs, &usage.PendingLearningJobs)
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
	return s.saveGenerationQuota(ctx, quota, 0)
}

func (s *Store) SaveGenerationQuotaVersion(ctx context.Context, quota storage.GenerationQuota, revision int64) error {
	if revision < 1 {
		return storage.ErrGenerationQuotaConflict
	}
	return s.saveGenerationQuota(ctx, quota, revision)
}

func (s *Store) SaveGenerationQuotaWithAudit(ctx context.Context, quota storage.GenerationQuota, revision int64, actor string) error {
	if revision < 0 || actor == "" {
		return fmt.Errorf("配额审计参数无效")
	}
	return s.saveGenerationQuota(ctx, quota, revision, actor)
}

func (s *Store) saveGenerationQuota(ctx context.Context, quota storage.GenerationQuota, revision int64, actors ...string) error {
	tx, err := beginGenerationQuotaTx(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := readGenerationQuota(ctx, tx)
	if err != nil {
		return err
	}
	if revision > 0 && current.Revision != revision {
		return storage.ErrGenerationQuotaConflict
	}
	// Old five-field callers omit concurrency. Preserve the current values inside
	// this same transaction rather than reading then overwriting a newer save.
	if quota.GenerationConcurrency == 0 {
		quota.GenerationConcurrency = current.GenerationConcurrency
	}
	if quota.BatchConcurrency == 0 {
		quota.BatchConcurrency = current.BatchConcurrency
	}
	if quota.AICheckConcurrency == 0 {
		quota.AICheckConcurrency = current.AICheckConcurrency
	}
	if quota.LearningConcurrency == 0 {
		quota.LearningConcurrency = current.LearningConcurrency
	}
	if quota.ModelTools == nil {
		quota.ModelTools = current.ModelTools
	}
	if err := quota.Validate(); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE generation_quota SET single_max_questions=$1, batch_max_questions=$2,
		single_active_per_user=$3, batch_active_per_user=$4, global_pending_questions=$5,
		generation_concurrency=$6,batch_concurrency=$7,ai_check_concurrency=$8,web_search_enabled=$9,web_extractor_enabled=$10,revision=revision+1,
		learning_concurrency=$11,updated_at=NOW() WHERE id=1`, quota.SingleMaxQuestions, quota.BatchMaxQuestions,
		quota.SingleActivePerUser, quota.BatchActivePerUser, quota.GlobalPendingQuestions,
		quota.GenerationConcurrency, quota.BatchConcurrency, quota.AICheckConcurrency, quota.ModelTools.WebSearchEnabled, quota.ModelTools.WebExtractorEnabled, quota.LearningConcurrency)
	if err != nil {
		return err
	}
	if len(actors) > 0 {
		state := func(v bool) string {
			if v {
				return "开启"
			}
			return "关闭"
		}
		detail := fmt.Sprintf("配置版本 %d → %d；联网搜索：%s → %s；网页抓取：%s → %s；单题/批量/首检并发：%d/%d/%d → %d/%d/%d；单次单题/批量上限：%d/%d → %d/%d；每人单题/批量活跃上限：%d/%d → %d/%d；全站队列上限：%d → %d",
			current.Revision, current.Revision+1, state(current.ModelTools.WebSearchEnabled), state(quota.ModelTools.WebSearchEnabled), state(current.ModelTools.WebExtractorEnabled), state(quota.ModelTools.WebExtractorEnabled),
			current.GenerationConcurrency, current.BatchConcurrency, current.AICheckConcurrency, quota.GenerationConcurrency, quota.BatchConcurrency, quota.AICheckConcurrency,
			current.SingleMaxQuestions, current.BatchMaxQuestions, quota.SingleMaxQuestions, quota.BatchMaxQuestions, current.SingleActivePerUser, current.BatchActivePerUser, quota.SingleActivePerUser, quota.BatchActivePerUser, current.GlobalPendingQuestions, quota.GlobalPendingQuestions)
		detail += fmt.Sprintf("；AI总结全局并发：%d → %d", current.LearningConcurrency, quota.LearningConcurrency)
		log := domain.AuditLog{ID: fmt.Sprintf("log-quota-%d", time.Now().UnixNano()), Action: "update_generation_quota", Actor: actors[0], Detail: detail, CreatedAt: time.Now()}
		if err := s.SaveLog(context.WithValue(ctx, reviewTransactionContextKey{}, tx), log); err != nil {
			return fmt.Errorf("保存配额操作日志失败: %w", err)
		}
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

// SaveBatchPipelineProgress 生成结果、首次检查入队和生成占用释放同时提交。
// 已提交单元只更新任务快照，不再保存草稿，避免覆盖通过状态或复活淘汰题。
func (s *Store) SaveBatchPipelineProgress(ctx context.Context, job storage.BatchJobRecord) error {
	return s.WithTransaction(ctx, func(txCtx context.Context) error {
		tx := reviewTransactionFromContext(txCtx)
		if _, err := tx.ExecContext(txCtx, `SELECT pg_advisory_xact_lock($1)`, generationQuotaLockKey); err != nil {
			return err
		}
		var previousJSON, profile, owner, status string
		var total, completed, failed int
		if err := tx.QueryRowContext(txCtx, `SELECT output_json::text,backend_profile,owner_id,status,total_count,completed,failed FROM batch_jobs WHERE id=$1 FOR UPDATE`, job.ID).Scan(&previousJSON, &profile, &owner, &status, &total, &completed, &failed); err != nil {
			return err
		}
		if profile != "single-question-pipeline" || job.BackendProfile != profile || job.TotalCount != total || job.OwnerID != owner {
			return fmt.Errorf("批内流水线任务快照不一致")
		}
		type unit struct {
			QuestionID  string `json:"question_id"`
			Status      string `json:"status"`
			Count       int    `json:"count"`
			CheckQueued bool   `json:"check_queued"`
		}
		var next, previous struct {
			PendingExperiences map[string]*domain.PersonalExperienceSnapshot `json:"pending_personal_experiences"`
			PendingContexts    map[string]*domain.QuestionGenerationContext  `json:"pending_knowledge_contexts"`
			Pipeline           bool                                          `json:"pipeline"`
			Creator            string                                        `json:"creator"`
			CheckMaxAttempts   int                                           `json:"check_max_attempts"`
			Questions          []domain.A2Question                           `json:"questions"`
			Items              []unit                                        `json:"items"`
		}
		if err := json.Unmarshal([]byte(job.OutputJSON), &next); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(previousJSON), &previous); err != nil {
			return err
		}
		if !next.Pipeline || !previous.Pipeline || next.CheckMaxAttempts != previous.CheckMaxAttempts || next.Creator != previous.Creator || len(next.Items) > total {
			return fmt.Errorf("批内流水线配置发生变化")
		}
		if next.CheckMaxAttempts < 1 || next.CheckMaxAttempts > 10 {
			return fmt.Errorf("首检尝试次数无效")
		}
		for pos, item := range previous.Items {
			if item.CheckQueued && (pos >= len(next.Items) || !next.Items[pos].CheckQueued || next.Items[pos].QuestionID != item.QuestionID || next.Items[pos].Status != "ok") {
				return fmt.Errorf("已提交单元%d不能被覆盖或重新生成", pos+1)
			}
		}
		byID := map[string]domain.A2Question{}
		for _, q := range next.Questions {
			byID[q.ID] = q
		}
		var incoming []domain.A2Question
		var ids []string
		nextCompleted, nextFailed := 0, 0
		seen := map[string]bool{}
		for pos, item := range next.Items {
			if item.Status == "failed" {
				nextFailed++
			}
			if item.Status != "ok" {
				continue
			}
			if !item.CheckQueued || item.QuestionID == "" || item.Count != 1 || seen[item.QuestionID] {
				return fmt.Errorf("生成单元%d的首检关联无效", pos+1)
			}
			seen[item.QuestionID] = true
			ids = append(ids, item.QuestionID)
			nextCompleted++
			if pos < len(previous.Items) && previous.Items[pos].CheckQueued {
				continue
			}
			q, ok := byID[item.QuestionID]
			if !ok {
				return fmt.Errorf("单元%d缺少题目结果", pos+1)
			}
			existing, err := s.getQuestionForUpdate(txCtx, tx, q.ID)
			if err != nil {
				return err
			}
			if existing != nil {
				return fmt.Errorf("生成单元%d与已有题目重复", pos+1)
			}
			q.GenerationContext = next.PendingContexts[q.ID]
			q.PersonalExperiences = next.PendingExperiences[q.ID]
			if q.WorkflowVersion != "" && (q.GenerationContext == nil || q.GenerationContext.WorkflowVersion != q.WorkflowVersion) {
				return fmt.Errorf("生成单元%d缺少冻结知识依据", pos+1)
			}
			q.OwnerID = owner
			q.CreatedBy = next.Creator
			q.Status = domain.StatusAIDraft
			incoming = append(incoming, q)
		}
		if job.Completed != nextCompleted || job.Failed != nextFailed || nextCompleted+nextFailed > total {
			return fmt.Errorf("批量进度计数不一致")
		}
		// 已受理的生成单元原本就占额，转交首检时不新增需求；管理员降低
		// 队列上限后仍继续既有任务，只有额外需求才需要再次核验额度。
		reserved := 0
		if status == "in_progress" || status == "running" || status == "pending" {
			reserved = max(0, total-completed-failed)
		}
		extra := max(0, len(incoming)-reserved)
		if extra > 0 {
			quota, err := readGenerationQuota(txCtx, tx)
			if err != nil {
				return err
			}
			usage, err := readGenerationQuotaUsage(txCtx, tx)
			if err != nil {
				return err
			}
			if usage.PendingQuestionUnits+extra > quota.GlobalPendingQuestions {
				return &storage.GenerationQuotaError{Message: "首检队列暂满，请等待已有任务释放额度"}
			}
		}
		for _, q := range incoming {
			questionCtx := storage.WithQuestionChange(txCtx, storage.QuestionChange{Actor: next.Creator, OwnerID: owner, ChangeType: "batch_generate", ChangeNote: "批量逐题生成并首检"})
			if err := s.SaveQuestion(questionCtx, q); err != nil {
				return err
			}
			created, err := s.EnqueueCheckTask(txCtx, domain.AICheckTask{ID: "act-pipe-" + q.ID, QuestionID: q.ID, QuestionVersion: q.Version, Status: domain.AICheckTaskPending, MaxAttempts: next.CheckMaxAttempts})
			if err != nil {
				return err
			}
			if !created {
				return fmt.Errorf("题目%s的首检已存在", q.ID)
			}
		}
		// Persist each new context once, alongside its question and first-check task.
		// Progress retains public question metadata, never repeatedly copied references.
		if len(next.PendingContexts) > 0 || len(next.PendingExperiences) > 0 {
			var clean map[string]json.RawMessage
			if err := json.Unmarshal([]byte(job.OutputJSON), &clean); err != nil {
				return err
			}
			delete(clean, "pending_knowledge_contexts")
			delete(clean, "pending_personal_experiences")
			raw, err := json.Marshal(clean)
			if err != nil {
				return err
			}
			job.OutputJSON = string(raw)
		}

		if err := updateBatchJob(txCtx, tx, job); err != nil {
			return err
		}
		var output struct {
			Items json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal([]byte(job.OutputJSON), &output); err != nil {
			return err
		}
		result := struct {
			Saved       int             `json:"saved"`
			Failed      int             `json:"failed"`
			Items       json.RawMessage `json:"items"`
			QuestionIDs []string        `json:"question_ids"`
		}{nextCompleted, nextFailed, output.Items, ids}
		payload, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(txCtx, `UPDATE batch_jobs SET import_result=$2::jsonb,
            imported_at=CASE WHEN status IN ('completed','complete') THEN COALESCE(imported_at,NOW()) ELSE imported_at END WHERE id=$1`, job.ID, string(payload))
		return err
	})
}
