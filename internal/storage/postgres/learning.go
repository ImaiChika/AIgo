package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
)

type learningAccessKey struct{}

func learningOwner(ctx context.Context, owner string) error {
	uid, _ := ctx.Value(learningAccessKey{}).(string)
	if owner == "" || uid != owner || reviewTransactionFromContext(ctx) == nil {
		return storage.ErrLearningDenied
	}
	return nil
}
func (s *Store) WithLearningAccess(ctx context.Context, uid, role string, fn func(context.Context, []string) error) error {
	err := s.WithKnowledgeBaseAccess(ctx, uid, role, func(txctx context.Context, permissions []string) error {
		return fn(context.WithValue(txctx, learningAccessKey{}, uid), permissions)
	})
	if errors.Is(err, knowledgebase.ErrDenied) {
		return storage.ErrLearningDenied
	}
	return err
}
func (s *Store) LoadLearningSource(ctx context.Context, owner, id string) (*domain.LearningSource, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	current, err := s.getQuestionForUpdate(ctx, reviewTransactionFromContext(ctx), id)
	if err != nil {
		return nil, err
	}
	if current == nil || current.OwnerID != owner {
		return nil, storage.ErrLearningNotFound
	}
	source := &domain.LearningSource{Current: current}
	original, err := s.GetQuestionVersion(ctx, id, 1)
	if err != nil {
		return nil, err
	}
	// A synthetic upgrade/import snapshot is not proof of the initial AI text.
	if original != nil && (original.ChangeType == "create" || original.ChangeType == "ai_generate" || original.ChangeType == "batch_generate") && strings.ToLower(strings.TrimSpace(original.Actor)) != "migration" {
		source.Original = &original.Snapshot
	}
	source.InitialCheckPassed, err = s.hasLearningInitialCheckPass(ctx, id)
	if err != nil {
		return nil, err
	}
	source.ActiveReview, err = s.HasActiveReviewTask(ctx, id)
	if err != nil {
		return nil, err
	}
	share, err := s.GetQuestionShareByQuestionID(ctx, id)
	if err != nil {
		return nil, err
	}
	if share != nil {
		source.ShareStatus = share.Status
		source.SharedSnapshotAvailable = share.Snapshot != nil && share.QuestionVersion > 0
	}
	return source, nil
}

// Hidden HTTP fields are explicitly serialized only into this private table.
type privateLearningJob struct {
	*domain.LearningJob
	Owner  string                 `json:"private_owner"`
	Role   string                 `json:"private_role"`
	Hash   string                 `json:"private_request_hash"`
	Source *domain.LearningSource `json:"private_source"`
}

func encodeLearningJob(job domain.LearningJob) ([]byte, error) {
	return json.Marshal(privateLearningJob{&job, job.OwnerID, job.RoleID, job.RequestHash, job.Source})
}

const learningJobColumns = `body,id,owner_id,role_id,request_key,request_hash,status,attempts,max_attempts,revision,lease_token,leased_until,first_started_at,deadline_at,next_attempt_at,completed_at,created_at,updated_at`

func scanLearningJob(row rowScanner) (*domain.LearningJob, error) {
	var raw []byte
	var job domain.LearningJob
	err := row.Scan(&raw, &job.ID, &job.OwnerID, &job.RoleID, &job.RequestKey, &job.RequestHash, &job.Status, &job.Attempts, &job.MaxAttempts, &job.Revision, &job.LeaseToken, &job.LeasedUntil, &job.FirstStartedAt, &job.DeadlineAt, &job.NextAttemptAt, &job.CompletedAt, &job.CreatedAt, &job.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var private privateLearningJob
	private.LearningJob = &domain.LearningJob{}
	if err = json.Unmarshal(raw, &private); err != nil {
		return nil, err
	}
	body := private.LearningJob
	body.ID = job.ID
	body.OwnerID = job.OwnerID
	body.RoleID = job.RoleID
	body.RequestKey = job.RequestKey
	body.RequestHash = job.RequestHash
	body.Source = private.Source
	body.Status = job.Status
	body.Attempts = job.Attempts
	body.MaxAttempts = job.MaxAttempts
	body.Revision = job.Revision
	body.LeaseToken = job.LeaseToken
	body.LeasedUntil = job.LeasedUntil
	body.FirstStartedAt = job.FirstStartedAt
	body.DeadlineAt = job.DeadlineAt
	body.NextAttemptAt = job.NextAttemptAt
	body.CompletedAt = job.CompletedAt
	body.CreatedAt = job.CreatedAt
	body.UpdatedAt = job.UpdatedAt
	return body, nil
}
func (s *Store) FindLearningJobByKey(ctx context.Context, owner, key string) (*domain.LearningJob, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	return scanLearningJob(s.queryRowContext(ctx, `SELECT `+learningJobColumns+` FROM learning_analysis_jobs WHERE owner_id=$1 AND request_key=$2`, owner, key))
}
func (s *Store) GetLearningJob(ctx context.Context, owner, id string) (*domain.LearningJob, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	return scanLearningJob(s.queryRowContext(ctx, `SELECT `+learningJobColumns+` FROM learning_analysis_jobs WHERE owner_id=$1 AND id=$2`, owner, id))
}
func (s *Store) ListLearningJobs(ctx context.Context, owner, question string, limit int) ([]domain.LearningJob, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.queryContext(ctx, `SELECT `+learningJobColumns+` FROM learning_analysis_jobs WHERE owner_id=$1 AND ($2='' OR body->>'question_id'=$2) ORDER BY created_at DESC,id DESC LIMIT $3`, owner, question, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.LearningJob{}
	for rows.Next() {
		job, e := scanLearningJob(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, *job)
	}
	return result, rows.Err()
}
func validLearningSource(source *domain.LearningSource) bool {
	return source != nil && source.Original != nil && source.Current != nil && source.Current.LibraryState != domain.LibraryDeleted && (source.Current.Status == domain.StatusAIReviewed || source.Current.Status == domain.StatusPublished) && source.InitialCheckPassed && !source.ActiveReview && source.ShareStatus != domain.QuestionSharePending && (source.ShareStatus != domain.QuestionShareApproved || source.SharedSnapshotAvailable)
}
func (s *Store) CreateLearningJob(ctx context.Context, job domain.LearningJob) error {
	if err := learningOwner(ctx, job.OwnerID); err != nil {
		return err
	}
	// Independent learning admission never consumes a generation-question slot.
	// Serialize all actors through one short admission lock to make the bound
	// exact even when distinct owners submit simultaneously.
	if _, err := s.execContext(ctx, `SELECT pg_advisory_xact_lock(4704385982817583292)`); err != nil {
		return err
	}
	existing, err := s.FindLearningJobByKey(ctx, job.OwnerID, job.RequestKey)
	if err != nil {
		return err
	}
	if existing != nil {
		return storage.ErrLearningConflict
	}
	var ownerActive, globalActive int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE owner_id=$1),COUNT(*) FROM learning_analysis_jobs WHERE status IN ('pending','running')`, job.OwnerID).Scan(&ownerActive, &globalActive); err != nil {
		return err
	}
	if ownerActive >= 1 || globalActive >= 20 {
		return storage.ErrLearningBusy
	}
	source, err := s.LoadLearningSource(ctx, job.OwnerID, job.QuestionID)
	if err != nil {
		return err
	}
	if !validLearningSource(source) || source.Current.Version != job.CurrentVersion || source.Current.LibraryRevision != job.LibraryRevision || source.Current.ReviewRevision != job.ReviewRevision || job.OriginalVersion != 1 || job.CurrentVersion < 2 || job.RequestKey == "" || job.RequestHash == "" {
		return storage.ErrLearningConflict
	}
	job.Source = source
	job.Status = "pending"
	job.Attempts = 0
	job.MaxAttempts = 2
	job.Revision = 1
	job.LeaseToken = ""
	job.LeasedUntil = nil
	raw, err := encodeLearningJob(job)
	if err != nil {
		return err
	}
	result, err := s.execContext(ctx, `INSERT INTO learning_analysis_jobs(id,owner_id,role_id,request_key,request_hash,status,body,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'pending',$6,NOW(),NOW()) ON CONFLICT DO NOTHING`, job.ID, job.OwnerID, job.RoleID, job.RequestKey, job.RequestHash, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storage.ErrLearningConflict
	}
	return nil
}

func (s *Store) ClaimLearningJob(ctx context.Context, lease time.Duration) (*domain.LearningJob, error) {
	var claimed *domain.LearningJob
	err := s.WithTransaction(ctx, func(txctx context.Context) error {
		if _, err := s.execContext(txctx, `SELECT pg_advisory_xact_lock(4704385982817583291)`); err != nil {
			return err
		}
		// Expiration fences the previous worker before reclaiming. The first-start
		// deadline remains unchanged across every restart/retry.
		if _, err := s.execContext(txctx, `UPDATE learning_analysis_jobs SET status=CASE WHEN attempts<max_attempts AND deadline_at>NOW() THEN 'pending' ELSE 'failed' END,lease_token='',leased_until=NULL,revision=revision+1,updated_at=NOW(),completed_at=CASE WHEN attempts>=max_attempts OR deadline_at<=NOW() THEN NOW() ELSE NULL END,body=body||jsonb_build_object('error_kind','lease_expired','error','学习任务执行超时，可查看失败记录') WHERE status='running' AND (leased_until<=NOW() OR deadline_at<=NOW())`); err != nil {
			return err
		}
		if _, err := s.execContext(txctx, `UPDATE learning_analysis_jobs SET status='failed',completed_at=NOW(),revision=revision+1,updated_at=NOW(),body=body||jsonb_build_object('error_kind','budget_exhausted','error','学习任务已达到尝试或时间上限') WHERE status='pending' AND (attempts>=max_attempts OR deadline_at<=NOW())`); err != nil {
			return err
		}
		var running, limit int
		if err := s.queryRowContext(txctx, `SELECT (SELECT count(*) FROM learning_analysis_jobs WHERE status='running'),learning_concurrency FROM generation_quota WHERE id=1 FOR SHARE`).Scan(&running, &limit); err != nil {
			return err
		}
		if limit < 1 || limit > 8 {
			return storage.ErrLearningUnavailable
		}
		if running >= limit {
			return nil
		}
		job, err := scanLearningJob(s.queryRowContext(txctx, `SELECT `+learningJobColumns+` FROM learning_analysis_jobs WHERE status='pending' AND (next_attempt_at IS NULL OR next_attempt_at<=NOW()) AND (deadline_at IS NULL OR deadline_at>NOW()) AND attempts<max_attempts ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`))
		if err != nil || job == nil {
			return err
		}
		token := make([]byte, 24)
		if _, err = rand.Read(token); err != nil {
			return err
		}
		jobLease := job.AttemptTimeout() + domain.LearningLeaseBuffer
		if lease > 0 && lease < jobLease {
			jobLease = lease
		}
		claimed, err = scanLearningJob(s.queryRowContext(txctx, `UPDATE learning_analysis_jobs SET status='running',attempts=attempts+1,revision=revision+1,lease_token=$2,first_started_at=COALESCE(first_started_at,NOW()),deadline_at=COALESCE(deadline_at,NOW()+make_interval(secs=>$4)),leased_until=LEAST(COALESCE(deadline_at,NOW()+make_interval(secs=>$4)),NOW()+make_interval(secs=>$3)),next_attempt_at=NULL,updated_at=NOW() WHERE id=$1 RETURNING `+learningJobColumns, job.ID, hex.EncodeToString(token), jobLease.Seconds(), job.ExecutionBudget().Seconds()))
		return err
	})
	return claimed, err
}
func (s *Store) FinishLearningJob(ctx context.Context, id string, attempt int, token string, outcome storage.LearningOutcome) error {
	if outcome.Status != "pending" && outcome.Status != "succeeded" && outcome.Status != "failed" {
		return storage.ErrLearningInvalid
	}
	fields := map[string]any{"candidates": outcome.Candidates, "error_kind": outcome.ErrorKind, "error": outcome.Error}
	if outcome.ModelCallIDs != nil {
		fields["model_call_ids"] = outcome.ModelCallIDs
	}
	patch, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	history := domain.LearningAttemptSummary{Attempt: attempt, Status: outcome.Status, ErrorKind: outcome.ErrorKind, CompletedAt: time.Now().UTC()}
	if history.Status == "pending" && attempt >= 2 {
		history.Status = "failed"
	}
	if len(outcome.ModelCallIDs) > 0 {
		history.ModelCallID = outcome.ModelCallIDs[len(outcome.ModelCallIDs)-1]
	}
	historyJSON, err := json.Marshal(history)
	if err != nil {
		return err
	}
	// A real deadline timeout is reported after that deadline. Preserve only
	// terminal failure evidence from the still-current owner; expired successes,
	// retries and any reclaimed attempt remain fenced out.
	terminalTimeout := outcome.Status == "failed" && outcome.ErrorKind == "timeout"
	result, err := s.execContext(ctx, `UPDATE learning_analysis_jobs SET status=CASE WHEN $4='pending' AND attempts>=max_attempts THEN 'failed' ELSE $4 END,body=jsonb_set(body||$5::jsonb,'{attempt_history}',COALESCE(body->'attempt_history','[]'::jsonb)||jsonb_build_array($7::jsonb)),revision=revision+1,lease_token='',leased_until=NULL,next_attempt_at=CASE WHEN $4='pending' AND attempts<max_attempts THEN $6::timestamptz ELSE NULL END,completed_at=CASE WHEN $4='pending' AND attempts<max_attempts THEN NULL ELSE NOW() END,updated_at=NOW() WHERE id=$1 AND attempts=$2 AND lease_token=$3 AND lease_token<>'' AND status='running' AND leased_until IS NOT NULL AND deadline_at IS NOT NULL AND ((leased_until>NOW() AND deadline_at>NOW()) OR $8)`, id, attempt, token, outcome.Status, patch, outcome.RetryAt, historyJSON, terminalTimeout)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storage.ErrLearningLeaseLost
	}
	return nil
}
func (s *Store) SaveLearningCandidates(ctx context.Context, owner, id string, revision int, candidates []domain.LearningCandidate) error {
	if err := learningOwner(ctx, owner); err != nil {
		return err
	}
	raw, err := json.Marshal(candidates)
	if err != nil {
		return err
	}
	result, err := s.execContext(ctx, `UPDATE learning_analysis_jobs SET body=jsonb_set(body,'{candidates}',$4::jsonb),revision=revision+1,updated_at=NOW() WHERE id=$1 AND owner_id=$2 AND revision=$3 AND status='succeeded'`, id, owner, revision, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return storage.ErrLearningConflict
	}
	return nil
}

var _ storage.LearningStore = (*Store)(nil)

// C3 accepts some historical version-zero results for existing exports. C5
// requires positive evidence that the original AI v1 actually passed its task.
func (s *Store) hasLearningInitialCheckPass(ctx context.Context, id string) (bool, error) {
	var pass bool
	err := s.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ai_review_results ar WHERE ar.question_id=$1 AND ar.question_version=1 AND ar.verdict='pass' AND (NOT EXISTS(SELECT 1 FROM ai_check_tasks t WHERE t.question_id=$1) OR EXISTS(SELECT 1 FROM ai_check_tasks t WHERE t.question_id=$1 AND t.question_version=1 AND t.status='succeeded' AND t.attempts>0)))`, id).Scan(&pass)
	return pass, err
}
