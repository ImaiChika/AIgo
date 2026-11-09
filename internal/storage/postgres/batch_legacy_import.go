package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"github.com/lib/pq"
)

func (s *Store) CommitLegacyBatchImport(ctx context.Context, id string, change storage.QuestionChange, maxAttempts int) (payload json.RawMessage, err error) {
	if maxAttempts < 1 || maxAttempts > 10 {
		return nil, fmt.Errorf("首检尝试次数无效")
	}
	err = s.WithTransaction(ctx, func(ctx context.Context) error {
		tx := reviewTransactionFromContext(ctx)
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, generationQuotaLockKey); err != nil {
			return err
		}
		var backend, profile, status, owner, raw, replay string
		if err := tx.QueryRowContext(ctx, `SELECT backend,backend_profile,status,owner_id,COALESCE(output_json::text,''),COALESCE(import_result::text,'') FROM batch_jobs WHERE id=$1 FOR UPDATE`, id).Scan(&backend, &profile, &status, &owner, &raw, &replay); err != nil {
			return err
		}
		if backend != "local_single_api" || profile == "single-question-pipeline" || (status != "completed" && status != "complete") {
			return fmt.Errorf("旧批量任务尚不可导入")
		}
		if owner != "" && change.OwnerID != "" && owner != change.OwnerID {
			return fmt.Errorf("旧批量任务归属不一致")
		}
		if replay != "" {
			if !json.Valid([]byte(replay)) {
				return fmt.Errorf("旧批量导入结果损坏")
			}
			payload = []byte(replay)
			return nil
		}
		var output struct {
			PendingExperiences map[string]*domain.PersonalExperienceSnapshot `json:"pending_personal_experiences"`
			Questions          []domain.A2Question                           `json:"questions"`
			Items              json.RawMessage                               `json:"items"`
			PendingContexts    map[string]*domain.QuestionGenerationContext  `json:"pending_knowledge_contexts"`
			Creator            string                                        `json:"creator"`
		}
		if err := json.Unmarshal([]byte(raw), &output); err != nil {
			return fmt.Errorf("解析旧批量结果失败: %w", err)
		}
		actor := change.Actor
		if actor == "" {
			actor = output.Creator
		}
		if actor == "" {
			actor = "ai"
		}
		if owner == "" {
			owner = change.OwnerID
		}
		var incoming []domain.A2Question
		var needCheck []domain.A2Question
		ids := make([]string, 0, len(output.Questions))
		seen := map[string]bool{}
		for _, q := range output.Questions {
			if q.ID == "" || seen[q.ID] {
				return fmt.Errorf("旧批量结果题目身份无效或重复")
			}
			seen[q.ID] = true
			ids = append(ids, q.ID)
			if q.Version <= 0 {
				q.Version = 1
			}
			if q.Version != 1 {
				return fmt.Errorf("旧生成结果不是初始版本")
			}
			existing, err := s.getQuestionForUpdate(ctx, tx, q.ID)
			if err != nil {
				return err
			}
			var hasTask, hasResult, discarded bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_check_tasks WHERE question_id=$1),EXISTS(SELECT 1 FROM ai_review_results WHERE question_id=$1),EXISTS(SELECT 1 FROM ai_check_discards WHERE question_id=$1)`, q.ID).Scan(&hasTask, &hasResult, &discarded); err != nil {
				return err
			}
			if discarded {
				continue
			} // Never restore a quality/fault-discarded question.
			if existing != nil {
				if existing.OwnerID != owner {
					return fmt.Errorf("旧批量题目与现存题目归属不一致")
				}
				original, err := s.GetQuestionVersion(ctx, q.ID, 1)
				if err != nil {
					return err
				}
				if original == nil || !domain.QuestionContentEqual(original.Snapshot, q) {
					return fmt.Errorf("旧批量题目与已保存初始快照不一致")
				}
				if hasTask || hasResult {
					continue
				} // Keep the current saved/edited/check state.
				if existing.Status != domain.StatusAIDraft || existing.Version != 1 {
					return fmt.Errorf("旧导入题缺少可恢复的初检交接依据")
				}
				needCheck = append(needCheck, *existing)
				continue
			}
			if hasTask || hasResult {
				return fmt.Errorf("旧批量首检证据存在但题目缺失")
			}
			q.OwnerID = owner
			q.CreatedBy = actor
			q.Status = domain.StatusAIDraft
			q.LibraryState = domain.LibraryUnfiled
			q.LibraryRevision = 1
			q.ConfirmedVersion = 0
			q.ConfirmationSource = ""
			q.ConfirmedBy = ""
			q.ConfirmedAt = nil
			q.DeletedReason = ""
			q.DeletedBy = ""
			q.DeletedFromState = ""
			q.GenerationContext = output.PendingContexts[q.ID]
			q.PersonalExperiences = output.PendingExperiences[q.ID]
			if q.WorkflowVersion != "" && (q.GenerationContext == nil || q.GenerationContext.WorkflowVersion != q.WorkflowVersion) {
				return fmt.Errorf("旧批量题缺少冻结知识依据")
			}
			incoming = append(incoming, q)
			needCheck = append(needCheck, q)
		}
		// An old claim already reserves these exact IDs. Only demand beyond that
		// reservation is new quota; a handoff must not count the same unit twice.
		var reserved int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM batch_jobs b CROSS JOIN LATERAL unnest(b.import_reserved_ids) item(id) WHERE b.id=$1 AND item.id=ANY($2) AND NOT EXISTS(SELECT 1 FROM ai_check_tasks t WHERE t.question_id=item.id) AND NOT EXISTS(SELECT 1 FROM ai_review_results r WHERE r.question_id=item.id) AND NOT EXISTS(SELECT 1 FROM ai_check_discards d WHERE d.question_id=item.id)`, id, pq.Array(ids)).Scan(&reserved); err != nil {
			return err
		}
		extra := max(0, len(needCheck)-reserved)
		if extra > 0 {
			quota, err := readGenerationQuota(ctx, tx)
			if err != nil {
				return err
			}
			usage, err := readGenerationQuotaUsage(ctx, tx)
			if err != nil {
				return err
			}
			if usage.PendingQuestionUnits+extra > quota.GlobalPendingQuestions {
				return &storage.GenerationQuotaError{Message: "旧批量结果首次检查队列暂满，请等待已有任务释放额度"}
			}
		}
		questionCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: actor, OwnerID: owner, ChangeType: "batch_generate", ChangeNote: "旧批量结果原子交接首次检查"})
		for _, q := range incoming {
			if err := s.SaveQuestion(questionCtx, q); err != nil {
				return err
			}
		}
		for _, q := range needCheck {
			if _, err := s.EnqueueCheckTask(ctx, domain.AICheckTask{ID: "act-legacy-" + q.ID, QuestionID: q.ID, QuestionVersion: 1, Status: domain.AICheckTaskPending, MaxAttempts: maxAttempts}); err != nil {
				return err
			}
		}
		if len(output.Items) == 0 || string(output.Items) == "null" {
			output.Items = json.RawMessage(`[]`)
		}
		result := struct {
			Saved       int             `json:"saved"`
			Failed      int             `json:"failed"`
			Items       json.RawMessage `json:"items"`
			QuestionIDs []string        `json:"question_ids"`
		}{len(ids), 0, output.Items, ids}
		rawResult, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE batch_jobs SET import_result=$2::jsonb,imported_at=COALESCE(imported_at,NOW()),import_reserved_ids='{}',updated_at=NOW() WHERE id=$1`, id, string(rawResult)); err != nil {
			return err
		}
		payload = rawResult
		return nil
	})
	return payload, err
}

var _ storage.LegacyBatchImportStore = (*Store)(nil)
