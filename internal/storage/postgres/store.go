package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"

	"github.com/lib/pq"
)

// Store PostgreSQL 存储实现。
type Store struct {
	db               *sql.DB
	llmEncryptionKey []byte
}

type reviewTransactionContextKey struct{}

type sqlExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type sqlQueryExecer interface {
	sqlExecer
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func reviewTransactionFromContext(ctx context.Context) *sql.Tx {
	tx, _ := ctx.Value(reviewTransactionContextKey{}).(*sql.Tx)
	return tx
}

func (s *Store) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if tx := reviewTransactionFromContext(ctx); tx != nil {
		return tx.ExecContext(ctx, query, args...)
	}
	return s.db.ExecContext(ctx, query, args...)
}

func (s *Store) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if tx := reviewTransactionFromContext(ctx); tx != nil {
		return tx.QueryContext(ctx, query, args...)
	}
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if tx := reviewTransactionFromContext(ctx); tx != nil {
		return tx.QueryRowContext(ctx, query, args...)
	}
	return s.db.QueryRowContext(ctx, query, args...)
}

// WithReviewTransaction 在同一 PostgreSQL 事务中执行审核任务、审核记录和题目状态写入。
func (s *Store) WithReviewTransaction(ctx context.Context, questionStore storage.QuestionStore, fn func(context.Context) error) (err error) {
	questionPG, ok := questionStore.(*Store)
	if !ok || questionPG != s {
		return fmt.Errorf("审核事务要求审核存储与题目存储使用同一 PostgreSQL Store")
	}
	if reviewTransactionFromContext(ctx) != nil {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始审核事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	txCtx := context.WithValue(ctx, reviewTransactionContextKey{}, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交审核事务失败: %w", err)
	}
	committed = true
	return nil
}

// WithTransaction 在单个数据库事务中执行 fn（已处于事务上下文时直接复用）。
// fn 内通过 txCtx 调用的存储写入（业务与审计）自动并入同一事务，任一失败整体回滚。
func (s *Store) WithTransaction(ctx context.Context, fn func(txCtx context.Context) error) error {
	if reviewTransactionFromContext(ctx) != nil {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	txCtx := context.WithValue(ctx, reviewTransactionContextKey{}, tx)
	if err := fn(txCtx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	committed = true
	return nil
}

// New 创建 PostgreSQL 存储。
func New(dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("数据库 ping 失败: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	return &Store{db: db}, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// DB 返回底层数据库连接。
func (s *Store) DB() *sql.DB {
	return s.db
}

// SetLLMEncryptionKey 设置系统级 AI 配置的加密密钥。
// 生产环境由 JWT_SECRET 派生，必须在读取/写入 AI 配置前设置；密钥本身不落库。
func (s *Store) SetLLMEncryptionKey(secret string) {
	if strings.TrimSpace(secret) == "" {
		s.llmEncryptionKey = nil
		return
	}
	sum := sha256.Sum256([]byte("aigo-ai-provider-config-v1:" + secret))
	s.llmEncryptionKey = append([]byte(nil), sum[:]...)
}

// CheckReadiness 检查数据库连接以及当前业务运行所需的关键表/列。
// LLM 和批量推理不是题库浏览/人工审核的强依赖，不纳入进程 readiness。
func (s *Store) CheckReadiness(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库连接不可用: %w", err)
	}
	if err := s.CheckSchemaVersion(ctx); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			q.version, q.status,
			q.owner_id,
			qv.version,
			rt.question_version, rt.status,
			u.permissions, u.bank_ids,
			r.permissions,
			qb.professions,
			kp.id, kp.version_id, kp.revision,
			rf.rounds,
			bj.backend,
			ar.question_version,
			act.status
		FROM questions q
		CROSS JOIN question_versions qv
		CROSS JOIN review_tasks rt
		CROSS JOIN users u
		CROSS JOIN roles r
		CROSS JOIN question_banks qb
		CROSS JOIN knowledge_points kp
		CROSS JOIN review_flows rf
		CROSS JOIN batch_jobs bj
			CROSS JOIN ai_review_results ar
			CROSS JOIN ai_check_tasks act
			CROSS JOIN question_share_requests qsr
			CROSS JOIN auth_login_limits allm
			CROSS JOIN generation_runs gr
		LIMIT 0
	`)
	if err != nil {
		return fmt.Errorf("关键数据库 Schema 不完整: %w", err)
	}
	return rows.Close()
}

// reviewMutationLockKey 是审核状态变更使用的数据库级全局锁。
// 审核写入频率远低于题库查询，采用单锁可以避免为每个等待者长期占用连接，
// 同时保证多进程部署下送审、投票、决断和撤销不会互相覆盖。
const reviewMutationLockKey int64 = 0x4149474f5f524556 // "AIGO_REV"

// AcquireReviewMutationLock 获取 PostgreSQL session advisory lock。
// 等待者使用 try-lock 轮询并立即归还连接，防止连接池被等待锁的请求占满后死锁。
func (s *Store) AcquireReviewMutationLock(ctx context.Context) (func() error, error) {
	const retryInterval = 10 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		conn, err := s.db.Conn(ctx)
		if err != nil {
			return nil, fmt.Errorf("获取审核锁连接失败: %w", err)
		}
		var acquired bool
		err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, reviewMutationLockKey).Scan(&acquired)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("获取审核锁失败: %w", err)
		}
		if acquired {
			var once sync.Once
			var releaseErr error
			return func() error {
				once.Do(func() {
					releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					var released bool
					if err := conn.QueryRowContext(releaseCtx, `SELECT pg_advisory_unlock($1)`, reviewMutationLockKey).Scan(&released); err != nil {
						releaseErr = fmt.Errorf("释放审核锁失败: %w", err)
					} else if !released {
						releaseErr = fmt.Errorf("释放审核锁失败: 当前连接未持有锁")
					}
					if err := conn.Close(); releaseErr == nil && err != nil {
						releaseErr = err
					}
				})
				return releaseErr
			}, nil
		}
		conn.Close()

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Count 返回题目总数。
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM questions`).Scan(&n)
	return n, err
}

// ListProfessions 返回题目表中出现过的全部非空专业值（数据库端去重）。
func (s *Store) ListProfessions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT profession FROM questions WHERE profession <> '' ORDER BY profession`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// ===== 单题命题运行记录 =====

const generationRunColumns = `id, owner_id, status, requested_count, question_ids, error, started_at, completed_at, updated_at, attempts, max_attempts, leased_until, request_json`

func scanGenerationRun(scanner interface{ Scan(dest ...any) error }) (*domain.GenerationRun, error) {
	var run domain.GenerationRun
	var questionIDs []string
	var completedAt sql.NullTime
	var leasedUntil sql.NullTime
	var requestJSON []byte
	if err := scanner.Scan(&run.ID, &run.OwnerID, &run.Status, &run.RequestedCount,
		pq.Array(&questionIDs), &run.Error, &run.StartedAt, &completedAt, &run.UpdatedAt,
		&run.Attempts, &run.MaxAttempts, &leasedUntil, &requestJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	run.QuestionIDs = questionIDs
	if completedAt.Valid {
		run.CompletedAt = &completedAt.Time
	}
	if leasedUntil.Valid {
		run.LeasedUntil = leasedUntil.Time
	}
	run.RequestJSON = requestJSON
	return &run, nil
}

func (s *Store) CreateGenerationRun(ctx context.Context, run domain.GenerationRun) (bool, error) {
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now()
	}
	if run.MaxAttempts <= 0 {
		run.MaxAttempts = 3
	}
	// question_ids 是 NOT NULL 列：nil 切片会被 pq 编码为 NULL 并触发约束错误，统一写入空数组。
	questionIDs := run.QuestionIDs
	if questionIDs == nil {
		questionIDs = []string{}
	}
	var requestJSON any
	if len(run.RequestJSON) > 0 {
		requestJSON = run.RequestJSON
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO generation_runs (id, owner_id, status, requested_count, question_ids, error, started_at, updated_at, max_attempts, request_json)
		VALUES ($1,$2,$3,$4,$5,'',$6,$6,$7,$8)
		ON CONFLICT (id) DO NOTHING
	`, run.ID, run.OwnerID, run.Status, run.RequestedCount, pq.Array(questionIDs), run.StartedAt, run.MaxAttempts, requestJSON)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	return affected > 0, err
}

// ClaimNextGenerationRun 抢占下一个可执行运行：pending 且到达可重试时间，或
// running 且租约过期（进程崩溃遗留）。抢占置 running、attempts+1 并续租，
// 语义与 AI 检查任务队列一致（FOR UPDATE SKIP LOCKED 防多 worker 重复执行）。
func (s *Store) ClaimNextGenerationRun(ctx context.Context, lease time.Duration) (*domain.GenerationRun, []byte, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE generation_runs SET status='running', attempts=attempts+1, leased_until=$1, updated_at=NOW()
		WHERE id = (
			SELECT id FROM generation_runs
			WHERE (status='pending' AND (leased_until IS NULL OR leased_until <= NOW()))
			   OR (status='running' AND leased_until <= NOW())
			ORDER BY started_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+generationRunColumns, time.Now().Add(lease))
	run, err := scanGenerationRun(row)
	if err != nil || run == nil {
		return nil, nil, err
	}
	return run, run.RequestJSON, nil
}

// RetryGenerationRun 记录一次可重试失败：执行次数未达上限则回 pending 并退避
// （返回 true），已达上限标记 failed 终态（返回 false）。
func (s *Store) RetryGenerationRun(ctx context.Context, id, errMsg string, backoff time.Duration) (bool, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE generation_runs SET
			status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
			leased_until = CASE WHEN attempts >= max_attempts THEN NULL ELSE $2::timestamptz END,
			error = $3,
			completed_at = CASE WHEN attempts >= max_attempts THEN NOW() ELSE completed_at END,
			updated_at = NOW()
		WHERE id=$1 AND status='running'
		RETURNING status
	`, id, time.Now().Add(backoff), errMsg)
	var status string
	if err := row.Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return status == domain.GenerationRunPending, nil
}

func (s *Store) GetGenerationRun(ctx context.Context, id string) (*domain.GenerationRun, error) {
	return scanGenerationRun(s.db.QueryRowContext(ctx, `SELECT `+generationRunColumns+` FROM generation_runs WHERE id=$1`, id))
}

func (s *Store) CompleteGenerationRun(ctx context.Context, id string, questionIDs []string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE generation_runs
		SET status='succeeded', question_ids=$2, error='', completed_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='running'
	`, id, pq.Array(questionIDs))
	return err
}

func (s *Store) FailGenerationRun(ctx context.Context, id, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE generation_runs
		SET status='failed', error=$2, completed_at=NOW(), updated_at=NOW()
		WHERE id=$1 AND status='running'
	`, id, message)
	return err
}

// ===== 题目 =====

func (s *Store) SaveQuestion(ctx context.Context, q domain.A2Question) error {
	if tx := reviewTransactionFromContext(ctx); tx != nil {
		return s.saveQuestion(ctx, tx, q)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.saveQuestion(ctx, tx, q); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) saveQuestion(ctx context.Context, executor sqlQueryExecer, q domain.A2Question) error {
	q.Status = domain.CanonicalLifecycleStatus(q.Status)
	if q.Version < 1 {
		return fmt.Errorf("%w: 题目 %s 的版本号必须从 1 开始", domain.ErrQuestionVersionConflict, q.ID)
	}
	existing, err := s.getQuestionForUpdate(ctx, executor, q.ID)
	if err != nil {
		return err
	}
	contentChanged := existing == nil
	if existing == nil {
		if q.Version != 1 {
			return fmt.Errorf("%w: 题目 %s 的初始版本必须为 1，实际为 %d", domain.ErrQuestionVersionConflict, q.ID, q.Version)
		}
		// 首次创建记录生成者（题目内容版本之外的身份字段，后续保存不覆盖）
		if strings.TrimSpace(q.CreatedBy) == "" {
			q.CreatedBy = storage.QuestionChangeFromContext(ctx).Actor
		}
		if strings.TrimSpace(q.CreatedBy) == "" {
			q.CreatedBy = "system"
		}
		if strings.TrimSpace(q.OwnerID) == "" {
			q.OwnerID = storage.QuestionChangeFromContext(ctx).OwnerID
		}
	} else {
		q.OwnerID = existing.OwnerID
		contentChanged = !domain.QuestionContentEqual(*existing, q)
		if contentChanged && (existing.Status == domain.StatusReviewing || existing.Status == domain.StatusConflict) {
			return fmt.Errorf("%w: 题目 %s 正在审核或等待决断，不能修改内容", domain.ErrQuestionVersionConflict, q.ID)
		}
		if contentChanged && q.Version != existing.Version+1 {
			return fmt.Errorf("%w: 题目 %s 内容已变化，必须从版本 %d 递增到 %d，实际为 %d", domain.ErrQuestionVersionConflict, q.ID, existing.Version, existing.Version+1, q.Version)
		}
		if !contentChanged && q.Version != existing.Version {
			return fmt.Errorf("%w: 题目 %s 内容未变化，版本号不能从 %d 变为 %d", domain.ErrQuestionVersionConflict, q.ID, existing.Version, q.Version)
		}
	}

	opts, _ := json.Marshal(q.Options)
	refs, _ := json.Marshal(q.SourceRefs)
	kps, _ := json.Marshal(q.KnowledgePoints)
	searchText := storage.BuildQuestionSearchText(q)

	_, err = executor.ExecContext(ctx, `
		INSERT INTO questions (id, clinical_stem, options, answer, explanation, source_refs, knowledge_points, difficulty, cognitive_level, exam_points, outline_code, profession, system_name, search_text, status, version, created_by, owner_id, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		ON CONFLICT (id) DO UPDATE SET
			clinical_stem=EXCLUDED.clinical_stem, options=EXCLUDED.options, answer=EXCLUDED.answer,
			explanation=EXCLUDED.explanation, source_refs=EXCLUDED.source_refs,
			knowledge_points=EXCLUDED.knowledge_points,
			difficulty=EXCLUDED.difficulty, cognitive_level=EXCLUDED.cognitive_level,
			exam_points=EXCLUDED.exam_points, outline_code=EXCLUDED.outline_code,
			profession=EXCLUDED.profession, system_name=EXCLUDED.system_name,
			search_text=EXCLUDED.search_text,
			status=EXCLUDED.status, version=EXCLUDED.version, updated_at=EXCLUDED.updated_at
	`, q.ID, q.ClinicalStem, opts, q.Answer, q.Explanation, refs, kps,
		q.Difficulty, q.CognitiveLevel, q.ExamPoints, q.OutlineCode, q.Profession, q.System, searchText,
		q.Status, q.Version, q.CreatedBy, q.OwnerID, q.CreatedAt, q.UpdatedAt)
	if err != nil {
		return err
	}
	// 更新题库归属（多对多）
	if err := s.replaceBankMembers(ctx, executor, q.ID, q.BankIDs); err != nil {
		return err
	}
	if contentChanged {
		if err := s.insertQuestionVersion(ctx, executor, q); err != nil {
			return err
		}
	}
	return nil
}

// questionSelectColumns 是题目查询的统一列清单（含多对多题库归属聚合）。
const questionSelectColumns = `q.id, q.clinical_stem, q.options, q.answer, q.explanation, q.source_refs, q.knowledge_points, q.difficulty, q.cognitive_level, q.exam_points, q.outline_code, q.profession, q.system_name,
	COALESCE(ARRAY(SELECT m.bank_id FROM question_bank_members m WHERE m.question_id = q.id ORDER BY m.bank_id), '{}') AS bank_ids,
	q.status, q.version, q.created_by, q.owner_id, q.created_at, q.updated_at`

func (s *Store) getQuestionForUpdate(ctx context.Context, executor sqlQueryExecer, id string) (*domain.A2Question, error) {
	row := executor.QueryRowContext(ctx, `
		SELECT `+questionSelectColumns+`
		FROM questions q WHERE q.id=$1 FOR UPDATE
	`, id)
	return scanQuestion(row)
}

func (s *Store) insertQuestionVersion(ctx context.Context, executor sqlExecer, q domain.A2Question) error {
	change := storage.QuestionChangeFromContext(ctx)
	if change.Actor == "" {
		change.Actor = "system"
	}
	if change.ChangeType == "" {
		if q.Version == 1 {
			change.ChangeType = "create"
		} else {
			change.ChangeType = "edit"
		}
	}
	if change.CreatedAt.IsZero() {
		change.CreatedAt = time.Now()
	}
	snapshot, err := json.Marshal(q)
	if err != nil {
		return fmt.Errorf("序列化题目版本失败: %w", err)
	}
	_, err = executor.ExecContext(ctx, `
		INSERT INTO question_versions (id, question_id, version, snapshot, actor, change_type, change_note, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, fmt.Sprintf("qv-%s-%d", q.ID, q.Version), q.ID, q.Version, snapshot, change.Actor, change.ChangeType, change.ChangeNote, change.CreatedAt)
	if err != nil {
		return fmt.Errorf("保存题目 %s 版本 %d 失败: %w", q.ID, q.Version, err)
	}
	return nil
}

// replaceBankMembers 重建题目-题库成员关系。
func (s *Store) replaceBankMembers(ctx context.Context, executor sqlExecer, questionID string, bankIDs []string) error {
	if _, err := executor.ExecContext(ctx, `DELETE FROM question_bank_members WHERE question_id=$1`, questionID); err != nil {
		return err
	}
	for _, b := range bankIDs {
		if b == "" {
			continue
		}
		if _, err := executor.ExecContext(ctx, `INSERT INTO question_bank_members (question_id, bank_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, questionID, b); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveQuestions(ctx context.Context, questions []domain.A2Question) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	count := 0
	for _, q := range questions {
		existing, err := s.getQuestionForUpdate(ctx, tx, q.ID)
		if err != nil {
			return count, err
		}
		if existing != nil {
			if domain.QuestionContentEqual(*existing, q) {
				count++
				continue
			}
			if existing.Status == domain.StatusReviewing || existing.Status == domain.StatusConflict {
				return count, fmt.Errorf("题目 %s 正在审核或等待决断，不能通过导入覆盖内容", q.ID)
			}
			q.Version = existing.Version + 1
			q.CreatedAt = existing.CreatedAt
			q.Status = domain.StatusAIDraft
			if len(q.BankIDs) == 0 {
				q.BankIDs = append([]string(nil), existing.BankIDs...)
			}
		}
		if q.Version < 1 {
			q.Version = 1
		}
		if q.CreatedAt.IsZero() {
			q.CreatedAt = time.Now()
		}
		q.UpdatedAt = time.Now()
		change := storage.QuestionChangeFromContext(ctx)
		if change.ChangeType == "" {
			change.ChangeType = "import"
		}
		if change.Actor == "" {
			change.Actor = "import"
		}
		questionCtx := storage.WithQuestionChange(ctx, change)
		if err := s.saveQuestion(questionCtx, tx, q); err != nil {
			return count, err
		}
		count++
	}
	return count, tx.Commit()
}

func (s *Store) GetQuestion(ctx context.Context, id string) (*domain.A2Question, error) {
	row := s.queryRowContext(ctx, `
		SELECT `+questionSelectColumns+`
		FROM questions q WHERE q.id=$1
	`, id)
	return scanQuestion(row)
}

func (s *Store) ListQuestionVersions(ctx context.Context, questionID string) ([]domain.QuestionVersion, error) {
	rows, err := s.queryContext(ctx, `
		SELECT question_id, version, snapshot, actor, change_type, change_note, created_at
		FROM question_versions WHERE question_id=$1 ORDER BY version DESC
	`, questionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []domain.QuestionVersion
	for rows.Next() {
		version, err := scanQuestionVersionRow(rows)
		if err != nil {
			return nil, err
		}
		versions = append(versions, *version)
	}
	return versions, rows.Err()
}

func (s *Store) GetQuestionVersion(ctx context.Context, questionID string, version int) (*domain.QuestionVersion, error) {
	row := s.queryRowContext(ctx, `
		SELECT question_id, version, snapshot, actor, change_type, change_note, created_at
		FROM question_versions WHERE question_id=$1 AND version=$2
	`, questionID, version)
	return scanQuestionVersion(row)
}

func (s *Store) DeleteQuestion(ctx context.Context, id string) error {
	return s.deleteQuestion(ctx, id)
}

// deleteQuestion 删除题目，关联的版本/审核任务/记录/AI 结果随外键级联清理。
// 通过 s.execContext 执行：处于事务上下文时并入调用方事务。
func (s *Store) deleteQuestion(ctx context.Context, id string) error {
	_, err := s.execContext(ctx, `DELETE FROM questions WHERE id=$1`, id)
	return err
}

// CreateQuestionShare 创建个人正式题目的一次性全局分享申请。
func (s *Store) CreateQuestionShare(ctx context.Context, request domain.QuestionShareRequest) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO question_share_requests (id, question_id, owner_id, status, created_at)
		VALUES ($1,$2,$3,$4,COALESCE($5,NOW()))
	`, request.ID, request.QuestionID, request.OwnerID, request.Status, request.CreatedAt)
	return err
}

func (s *Store) GetQuestionShareByQuestionID(ctx context.Context, questionID string) (*domain.QuestionShareRequest, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, question_id, owner_id, status, reviewed_by, review_note, created_at, reviewed_at
		FROM question_share_requests WHERE question_id=$1
	`, questionID)
	return scanQuestionShareRequest(row)
}

// ListQuestionShares 返回分享申请及题目内容。题目 JSON 显式移除 created_by/owner_id，
// 管理员审批页单独显示申请人，普通用户的申请列表不返回申请人字段。
func (s *Store) ListQuestionShares(ctx context.Context, status, ownerID string) ([]storage.QuestionShareItem, error) {
	where := []string{}
	args := []any{}
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if status != "" {
		where = append(where, "sr.status="+addArg(status))
	}
	// 迁移登记的历史题目用于恢复全局三层展示，不是新的分享申请；
	// 审批队列和申请历史不应把它们伪装成数千条待办申请。
	where = append(where, "sr.reviewed_by <> 'migration'")
	if ownerID != "" {
		where = append(where, "sr.owner_id="+addArg(ownerID))
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = "WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sr.id, sr.question_id, sr.owner_id, sr.status, sr.reviewed_by, sr.review_note,
			sr.created_at, sr.reviewed_at,
			(to_jsonb(q) - 'system_name' - 'created_by' - 'owner_id') || jsonb_build_object(
				'system', q.system_name,
				'bank_ids', ARRAY(SELECT m.bank_id FROM question_bank_members m WHERE m.question_id=q.id ORDER BY m.bank_id)
			) AS question_json,
			COALESCE(u.display_name,''), COALESCE(u.username,'')
		FROM question_share_requests sr
		JOIN questions q ON q.id=sr.question_id
		LEFT JOIN users u ON u.id=sr.owner_id
		`+whereSQL+`
		ORDER BY sr.created_at DESC, sr.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storage.QuestionShareItem
	for rows.Next() {
		var item storage.QuestionShareItem
		var reviewedAt sql.NullTime
		var questionJSON []byte
		if err := rows.Scan(
			&item.Request.ID, &item.Request.QuestionID, &item.Request.OwnerID, &item.Request.Status,
			&item.Request.ReviewedBy, &item.Request.ReviewNote, &item.Request.CreatedAt, &reviewedAt,
			&questionJSON, &item.OwnerName, &item.OwnerUsername,
		); err != nil {
			return nil, err
		}
		if reviewedAt.Valid {
			value := reviewedAt.Time
			item.Request.ReviewedAt = &value
		}
		if err := json.Unmarshal(questionJSON, &item.Question); err != nil {
			return nil, fmt.Errorf("解析分享申请题目失败: %w", err)
		}
		item.Question.OwnerID = item.Request.OwnerID
		item.Question.Status = domain.CanonicalLifecycleStatus(item.Question.Status)
		result = append(result, item)
	}
	return result, rows.Err()
}

// ReviewQuestionShare 审批分享申请。按行锁保证同一申请只能被成功处理一次。
func (s *Store) ReviewQuestionShare(ctx context.Context, id, reviewerID string, status domain.QuestionShareStatus, note string) (*domain.QuestionShareRequest, error) {
	if status != domain.QuestionShareApproved && status != domain.QuestionShareRejected {
		return nil, fmt.Errorf("无效的分享审批状态: %s", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var request domain.QuestionShareRequest
	var reviewedAt sql.NullTime
	var questionStatus domain.QuestionStatus
	err = tx.QueryRowContext(ctx, `
		SELECT sr.id, sr.question_id, sr.owner_id, sr.status, sr.reviewed_by, sr.review_note,
			sr.created_at, sr.reviewed_at, q.status
		FROM question_share_requests sr
		JOIN questions q ON q.id=sr.question_id
		WHERE sr.id=$1 FOR UPDATE
	`, id).Scan(&request.ID, &request.QuestionID, &request.OwnerID, &request.Status,
		&request.ReviewedBy, &request.ReviewNote, &request.CreatedAt, &reviewedAt, &questionStatus)
	if err == sql.ErrNoRows {
		return nil, storage.ErrQuestionShareNotFound
	}
	if err != nil {
		return nil, err
	}
	if request.Status != domain.QuestionSharePending {
		return nil, storage.ErrQuestionShareAlreadyReviewed
	}
	if questionStatus != domain.StatusPublished {
		return nil, storage.ErrQuestionShareNotFormal
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE question_share_requests
		SET status=$1, reviewed_by=$2, review_note=$3, reviewed_at=NOW()
		WHERE id=$4 AND status='pending'
	`, status, reviewerID, strings.TrimSpace(note), id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	request.Status = status
	request.ReviewedBy = reviewerID
	request.ReviewNote = strings.TrimSpace(note)
	now := time.Now()
	request.ReviewedAt = &now
	return &request, nil
}

func (s *Store) ListQuestions(ctx context.Context) ([]domain.A2Question, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+questionSelectColumns+`
		FROM questions q ORDER BY q.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestions(rows)
}

// escapeLike 转义 LIKE/ILIKE 通配符，保证用户输入按字面匹配。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// questionFilterWhere 把过滤条件拼成 WHERE 子句；所有值都走参数绑定。
func questionFilterWhere(f storage.QuestionFilter) (string, []any) {
	args := []any{}
	placeholder := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	clauses := []string{}
	if f.Status != "" {
		clauses = append(clauses, "q.status = "+placeholder(f.Status))
	}
	if f.OwnerID != "" {
		ownerClause := "q.owner_id = " + placeholder(f.OwnerID)
		if f.IncludeLegacyOwner {
			ownerClause = "(" + ownerClause + " OR q.owner_id='')"
		}
		clauses = append(clauses, ownerClause)
	}
	if f.ReviewerID != "" {
		// 我的审核记录：该用户在任何审核任务下提交过审核意见（含最终把关决断）的题目
		clauses = append(clauses, "EXISTS (SELECT 1 FROM review_records rr JOIN review_tasks rt ON rr.task_id=rt.id WHERE rt.question_id = q.id AND rr.expert_id = "+placeholder(f.ReviewerID)+")")
	}
	if len(f.GlobalStatuses) > 0 {
		statuses := make([]string, 0, len(f.GlobalStatuses))
		for _, status := range f.GlobalStatuses {
			if status == string(domain.QuestionSharePending) || status == string(domain.QuestionShareApproved) || status == string(domain.QuestionShareRejected) {
				statuses = append(statuses, status)
			}
		}
		if len(statuses) == 0 {
			clauses = append(clauses, "FALSE")
		} else {
			shareClause := "EXISTS (SELECT 1 FROM question_share_requests qs WHERE qs.question_id = q.id AND qs.status = ANY(" + placeholder(pq.Array(statuses)) + "))"
			if f.IncludeLegacyGlobal {
				legacyStatuses := []string{}
				for _, status := range statuses {
					switch status {
					case string(domain.QuestionSharePending):
						// 暂存态（ai_draft/auto_checked）对用户不可见，不进入全局待审核视图
						legacyStatuses = append(legacyStatuses, "'ai_reviewed'", "'reviewing'", "'conflict'", "'revision_required'")
					case string(domain.QuestionShareApproved):
						legacyStatuses = append(legacyStatuses, "'published'")
					case string(domain.QuestionShareRejected):
						legacyStatuses = append(legacyStatuses, "'rejected'", "'archived'")
					}
				}
				if len(legacyStatuses) > 0 {
					shareClause = "(" + shareClause + " OR (q.owner_id='' AND NOT EXISTS (SELECT 1 FROM question_share_requests qs_legacy WHERE qs_legacy.question_id=q.id) AND q.status IN (" + strings.Join(legacyStatuses, ",") + ")))"
				}
			}
			clauses = append(clauses, shareClause)
		}
	}
	if len(f.Tiers) > 0 {
		statuses := map[string]bool{}
		for _, tier := range f.Tiers {
			for _, s := range domain.TierStatuses(domain.QuestionTier(tier)) {
				statuses[string(s)] = true
			}
		}
		list := make([]string, 0, len(statuses))
		for s := range statuses {
			list = append(list, s)
		}
		clauses = append(clauses, "q.status = ANY("+placeholder(pq.Array(list))+")")
	}
	if f.Difficulty != "" {
		clauses = append(clauses, "q.difficulty = "+placeholder(f.Difficulty))
	}
	if f.DifficultyBand != "" {
		numericDifficulty := `(q.difficulty ~ '^[0-9]+(\.[0-9]+)?$')`
		switch f.DifficultyBand {
		case "easy":
			clauses = append(clauses, "(q.difficulty = 'easy' OR ("+numericDifficulty+" AND q.difficulty::numeric <= 0.60))")
		case "medium":
			clauses = append(clauses, "(q.difficulty = 'medium' OR ("+numericDifficulty+" AND q.difficulty::numeric > 0.60 AND q.difficulty::numeric <= 0.80))")
		case "hard":
			clauses = append(clauses, "(q.difficulty = 'hard' OR ("+numericDifficulty+" AND q.difficulty::numeric > 0.80))")
		}
	}
	if len(f.Professions) > 0 {
		clauses = append(clauses, "q.profession = ANY("+placeholder(pq.Array(f.Professions))+")")
	}
	if f.OutlineCode != "" {
		clauses = append(clauses, "q.outline_code LIKE "+placeholder(escapeLike(f.OutlineCode)+"%"))
	}
	if f.Keyword != "" {
		for _, token := range storage.SearchTokens(f.Keyword) {
			p := placeholder("%" + escapeLike(token) + "%")
			clauses = append(clauses, "q.search_text ILIKE "+p)
		}
	}
	if f.BankID != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM question_bank_members bm WHERE bm.question_id = q.id AND bm.bank_id = "+placeholder(f.BankID)+")")
	}
	if f.Unclassified {
		clauses = append(clauses, "NOT EXISTS (SELECT 1 FROM question_bank_members bu WHERE bu.question_id = q.id)")
	}
	if f.ClassifiableOnly {
		clauses = append(clauses, "q.status IN ('ai_draft','auto_checked','ai_reviewed','revision_required')")
	}
	if f.NoReviewTask {
		clauses = append(clauses, "NOT EXISTS (SELECT 1 FROM review_tasks nrt WHERE nrt.question_id = q.id)")
	}
	if f.ScopeRestricted {
		if len(f.BankScope) == 0 {
			clauses = append(clauses, "FALSE")
		} else {
			clauses = append(clauses, "EXISTS (SELECT 1 FROM question_bank_members bs WHERE bs.question_id = q.id AND bs.bank_id = ANY("+placeholder(pq.Array(f.BankScope))+"))")
		}
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// SearchQuestions 在数据库端完成过滤、计数和分页，避免整表载入内存。
func (s *Store) SearchQuestions(ctx context.Context, filter storage.QuestionFilter, page, pageSize int) ([]domain.A2Question, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 100
	}
	where, args := questionFilterWhere(filter)

	var total int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM questions q `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	pagedArgs := append(args, pageSize, (page-1)*pageSize)
	rows, err := s.queryContext(ctx, `
		SELECT `+questionSelectColumns+`
		FROM questions q `+where+`
		ORDER BY q.created_at DESC, q.id DESC
		LIMIT $`+fmt.Sprint(len(args)+1)+` OFFSET $`+fmt.Sprint(len(args)+2), pagedArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	questions, err := scanQuestions(rows)
	if err != nil {
		return nil, 0, err
	}
	return questions, total, nil
}

// 审核结果终态：archived（管理员归档删除）优先于一切任务状态——归档题已物理
// 淘汰，不得再按其历史任务显示“需修改”，更不得落入 pending 被算成“未提交审核”。
const reviewFinalStatusSQL = `CASE
	WHEN q.status = 'archived' THEN 'archived'
	WHEN q.status = 'published' THEN 'published'
	WHEN q.status = 'rejected' THEN 'rejected'
	WHEN lt.status = 'revision_required' THEN 'revision_required'
	WHEN lt.status = 'conflict' THEN 'conflict'
	WHEN q.status = 'reviewing' THEN 'reviewing'
	ELSE 'pending'
END`

const latestReviewTasksCTE = `WITH latest_tasks AS (
	SELECT DISTINCT ON (question_id) *
	FROM review_tasks
	ORDER BY question_id, created_at DESC
)`

func reviewResultWhere(filter storage.QuestionFilter, finalStatus string) (string, []any, error) {
	where, args := questionFilterWhere(filter)
	if finalStatus == "" {
		return where, args, nil
	}
	switch finalStatus {
	case "pending", "reviewing", "conflict", "revision_required", "rejected", "published", "archived":
	default:
		return "", nil, fmt.Errorf("无效的审核结果状态: %s", finalStatus)
	}
	condition := "(" + reviewFinalStatusSQL + ") = " + fmt.Sprintf("$%d", len(args)+1)
	args = append(args, finalStatus)
	if where == "" {
		return "WHERE " + condition, args, nil
	}
	return where + " AND " + condition, args, nil
}

// SearchReviewResults 在 PostgreSQL 内完成最新任务关联、权限过滤、状态计算、统计和分页。
// 避免审核记录页随题量增长把 questions/review_tasks 全表加载进 Go 内存。
func (s *Store) SearchReviewResults(ctx context.Context, query storage.ReviewResultQuery) (*storage.ReviewResultPage, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 500 {
		query.PageSize = 50
	}
	where, args, err := reviewResultWhere(query.Filter, query.FinalStatus)
	if err != nil {
		return nil, err
	}
	result := &storage.ReviewResultPage{Stats: map[string]int{}}
	if err := s.queryRowContext(ctx, latestReviewTasksCTE+`
		SELECT COUNT(*)
		FROM questions q LEFT JOIN latest_tasks lt ON lt.question_id=q.id `+where, args...).Scan(&result.Total); err != nil {
		return nil, err
	}

	statsWhere, statsArgs := questionFilterWhere(query.StatsFilter)
	rows, err := s.queryContext(ctx, latestReviewTasksCTE+`
		SELECT final_status, COUNT(*)
		FROM (
			SELECT `+reviewFinalStatusSQL+` AS final_status
			FROM questions q LEFT JOIN latest_tasks lt ON lt.question_id=q.id `+statsWhere+`
		) visible_results
		GROUP BY final_status`, statsArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return nil, err
		}
		result.Stats[status] = count
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	pagedArgs := append(append([]any(nil), args...), query.PageSize, (query.Page-1)*query.PageSize)
	rows, err = s.queryContext(ctx, latestReviewTasksCTE+`
		SELECT
			(to_jsonb(q) - 'system_name' - 'created_by' - 'owner_id') || jsonb_build_object(
				'system', q.system_name,
				'bank_ids', COALESCE(ARRAY(
					SELECT m.bank_id FROM question_bank_members m
					WHERE m.question_id=q.id ORDER BY m.bank_id
				), '{}')
			) AS question_json,
			CASE WHEN lt.id IS NULL THEN NULL ELSE to_jsonb(lt) END AS task_json,
			`+reviewFinalStatusSQL+` AS final_status
		FROM questions q LEFT JOIN latest_tasks lt ON lt.question_id=q.id `+where+`
		ORDER BY q.created_at DESC, q.id DESC
		LIMIT $`+fmt.Sprint(len(args)+1)+` OFFSET $`+fmt.Sprint(len(args)+2), pagedArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var questionJSON, taskJSON []byte
		var row storage.ReviewResultRow
		if err := rows.Scan(&questionJSON, &taskJSON, &row.FinalStatus); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(questionJSON, &row.Question); err != nil {
			return nil, fmt.Errorf("解析审核记录题目失败: %w", err)
		}
		row.Question.Status = domain.CanonicalLifecycleStatus(row.Question.Status)
		if len(taskJSON) > 0 && string(taskJSON) != "null" {
			var task domain.ReviewTask
			if err := json.Unmarshal(taskJSON, &task); err != nil {
				return nil, fmt.Errorf("解析审核任务失败: %w", err)
			}
			task.Status = domain.CanonicalLifecycleStatus(task.Status)
			row.Task = &task
		}
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}

// scanCounts 执行两列（key, count）的分组统计查询并累加到目标 map。
func (s *Store) scanCounts(ctx context.Context, query string, args []any, into map[string]int) error {
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return err
		}
		into[key] = n
	}
	return rows.Err()
}

// CountQuestionsByStatus 按状态统计题目数量（存储端 GROUP BY，避免整表载入）。
func (s *Store) CountQuestionsByStatus(ctx context.Context, filter storage.QuestionFilter) (map[string]int, error) {
	where, args := questionFilterWhere(filter)
	counts := map[string]int{}
	if err := s.scanCounts(ctx, `SELECT q.status, COUNT(*) FROM questions q `+where+` GROUP BY q.status`, args, counts); err != nil {
		return nil, err
	}
	return counts, nil
}

// AggregateQuestionStats 在存储端聚合题目统计分布。
func (s *Store) AggregateQuestionStats(ctx context.Context, filter storage.QuestionFilter, days int) (*storage.QuestionStatsAggregate, error) {
	if days <= 0 {
		days = 30
	}
	agg := &storage.QuestionStatsAggregate{
		ByStatus:     map[string]int{},
		ByDifficulty: map[string]int{},
		ByProfession: map[string]int{},
		ByDay:        map[string]int{},
		ByBank:       map[string]int{},
	}
	where, args := questionFilterWhere(filter)

	// 状态分布与总数
	if err := s.scanCounts(ctx, `SELECT q.status, COUNT(*) FROM questions q `+where+` GROUP BY q.status`, args, agg.ByStatus); err != nil {
		return nil, err
	}
	for _, n := range agg.ByStatus {
		agg.Total += n
	}
	// 难度分布
	if err := s.scanCounts(ctx, `SELECT q.difficulty, COUNT(*) FROM questions q `+where+` GROUP BY q.difficulty`, args, agg.ByDifficulty); err != nil {
		return nil, err
	}
	// 专业分布（按数量降序取前 12）
	rows, err := s.queryContext(ctx, `
		SELECT q.profession, COUNT(*) FROM questions q `+where+`
		GROUP BY q.profession ORDER BY COUNT(*) DESC LIMIT 12
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var profession string
		var n int
		if err := rows.Scan(&profession, &n); err != nil {
			return nil, err
		}
		agg.ByProfession[profession] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 每日新增趋势（近 days 天）
	dayWhere, dayArgs := where, args
	dayCond := fmt.Sprintf("q.created_at >= NOW() - make_interval(days => $%d::int)", len(dayArgs)+1)
	dayArgs = append(append([]any{}, dayArgs...), days)
	if dayWhere == "" {
		dayWhere = "WHERE " + dayCond
	} else {
		dayWhere += " AND " + dayCond
	}
	if err := s.scanCounts(ctx, `SELECT to_char(q.created_at, 'YYYY-MM-DD'), COUNT(*) FROM questions q `+dayWhere+` GROUP BY 1 ORDER BY 1`, dayArgs, agg.ByDay); err != nil {
		return nil, err
	}

	// 分类子题库分布（多对多按归属计）
	if err := s.scanCounts(ctx, `
		SELECT bm.bank_id, COUNT(*) FROM question_bank_members bm
		JOIN questions q ON q.id = bm.question_id `+where+`
		GROUP BY bm.bank_id
	`, args, agg.ByBank); err != nil {
		return nil, err
	}
	// 待归类（不属于任何分类子题库）
	uncWhere := where
	if uncWhere == "" {
		uncWhere = "WHERE NOT EXISTS (SELECT 1 FROM question_bank_members bm WHERE bm.question_id = q.id)"
	} else {
		uncWhere += " AND NOT EXISTS (SELECT 1 FROM question_bank_members bm WHERE bm.question_id = q.id)"
	}
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM questions q `+uncWhere, args...).Scan(&agg.Unclassified); err != nil {
		return nil, err
	}
	return agg, nil
}

// CoveredKnowledgePointIDs 返回过滤范围内题目引用的知识点 ID 去重列表。
func (s *Store) CoveredKnowledgePointIDs(ctx context.Context, filter storage.QuestionFilter) ([]string, error) {
	where, args := questionFilterWhere(filter)
	rows, err := s.queryContext(ctx, `
		SELECT DISTINCT kp->>'id'
		FROM questions q, jsonb_array_elements(q.knowledge_points) AS kp
		`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// ===== 专家 =====

// OwnerBankIDs 返回指定用户的题目所关联的分类子题库 ID 去重列表。
func (s *Store) OwnerBankIDs(ctx context.Context, ownerID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT m.bank_id
		FROM question_bank_members m
		JOIN questions q ON q.id = m.question_id
		WHERE q.owner_id = $1
		ORDER BY m.bank_id`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountGenerationRunsByStatus 按状态统计命题运行数量（存储端聚合）。
func (s *Store) CountGenerationRunsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.queryContext(ctx, `SELECT status, COUNT(*) FROM generation_runs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

// GetQuestionsByIDs 批量获取题目（返回仍存在的题目，按创建时间倒序）。
func (s *Store) GetQuestionsByIDs(ctx context.Context, ids []string) ([]domain.A2Question, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.queryContext(ctx, `SELECT `+questionSelectColumns+`
		FROM questions q WHERE q.id = ANY($1)
		ORDER BY q.created_at DESC, q.id DESC`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestions(rows)
}

func (s *Store) SaveExpert(ctx context.Context, e domain.Expert) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO experts (id, name, department, title, specialties, expert_types, contact, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, department=EXCLUDED.department, title=EXCLUDED.title,
			specialties=EXCLUDED.specialties, expert_types=EXCLUDED.expert_types,
			contact=EXCLUDED.contact, enabled=EXCLUDED.enabled
	`, e.ID, e.Name, e.Department, e.Title, pqArray(e.Specialties), pqArray(e.ExpertTypes), e.Contact, e.Enabled)
	return err
}

func (s *Store) GetExpert(ctx context.Context, id string) (*domain.Expert, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, department, title, specialties, expert_types, contact, enabled FROM experts WHERE id=$1`, id)
	return scanExpert(row)
}

func (s *Store) ListExperts(ctx context.Context) ([]domain.Expert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, department, title, specialties, expert_types, contact, enabled FROM experts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Expert
	for rows.Next() {
		e, err := scanExpertRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *Store) UpdateExpert(ctx context.Context, e domain.Expert) error {
	return s.SaveExpert(ctx, e)
}

func (s *Store) DeleteExpert(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM experts WHERE id=$1`, id)
	return err
}

// ===== 审核流程 =====

func (s *Store) SaveFlowConfig(ctx context.Context, f domain.ReviewFlowConfig) error {
	rounds, _ := json.Marshal(f.Rounds)
	_, err := s.execContext(ctx, `
		INSERT INTO review_flows (id, name, description, subject, bank_id, final_reviewer_ids, vote_rule, rounds, archived, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, description=EXCLUDED.description, subject=EXCLUDED.subject,
			bank_id=EXCLUDED.bank_id, final_reviewer_ids=EXCLUDED.final_reviewer_ids,
			vote_rule=EXCLUDED.vote_rule, rounds=EXCLUDED.rounds, archived=EXCLUDED.archived
	`, f.ID, f.Name, f.Description, f.Subject, f.BankID, pqArray(f.FinalReviewerIDs), f.VoteRule, rounds, f.Archived, f.CreatedAt)
	return err
}

func (s *Store) GetFlowConfig(ctx context.Context, id string) (*domain.ReviewFlowConfig, error) {
	row := s.queryRowContext(ctx, `SELECT id, name, description, subject, bank_id, final_reviewer_ids, vote_rule, rounds, archived, created_at FROM review_flows WHERE id=$1`, id)
	return scanFlow(row)
}

func (s *Store) ListFlowConfigs(ctx context.Context) ([]domain.ReviewFlowConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, subject, bank_id, final_reviewer_ids, vote_rule, rounds, archived, created_at FROM review_flows ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewFlowConfig
	for rows.Next() {
		f, err := scanFlowRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *f)
	}
	return result, rows.Err()
}

func (s *Store) DeleteFlowConfig(ctx context.Context, id string) error {
	_, err := s.execContext(ctx, `DELETE FROM review_flows WHERE id=$1`, id)
	return err
}

// ===== 审核任务 =====

func (s *Store) SaveTask(ctx context.Context, t domain.ReviewTask) error {
	t.Status = domain.CanonicalLifecycleStatus(t.Status)
	if t.QuestionVersion < 1 {
		return fmt.Errorf("%w: 审核任务 %s 必须绑定有效题目版本", domain.ErrQuestionVersionConflict, t.ID)
	}
	results, _ := json.Marshal(t.RoundResults)
	var finalDecision []byte
	if t.FinalDecision != nil {
		finalDecision, _ = json.Marshal(t.FinalDecision)
	} else {
		finalDecision = []byte("null")
	}
	_, err := s.execContext(ctx, `
		INSERT INTO review_tasks (id, question_id, flow_id, submission_bank_id, current_round, status, assigned_to, final_reviewer_ids, final_decision, question_prev_status, question_version, attempt, round_results, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET
			submission_bank_id=EXCLUDED.submission_bank_id,
			current_round=EXCLUDED.current_round, status=EXCLUDED.status,
			assigned_to=EXCLUDED.assigned_to, final_reviewer_ids=EXCLUDED.final_reviewer_ids,
			final_decision=EXCLUDED.final_decision,
			question_prev_status=EXCLUDED.question_prev_status,
			question_version=EXCLUDED.question_version,
			attempt=EXCLUDED.attempt,
			round_results=EXCLUDED.round_results, updated_at=EXCLUDED.updated_at
	`, t.ID, t.QuestionID, t.FlowID, t.SubmissionBankID, t.CurrentRound, t.Status, pqArray(t.AssignedTo), pqArray(t.FinalReviewerIDs), finalDecision, t.QuestionPrevStatus, t.QuestionVersion, attemptOrOne(t.Attempt), results, t.CreatedAt, t.UpdatedAt)
	return err
}

// attemptOrOne 兼容历史内存数据：批次号缺省视为第 1 批。
func attemptOrOne(attempt int) int {
	if attempt < 1 {
		return 1
	}
	return attempt
}

const taskColumns = `id, question_id, flow_id, submission_bank_id, current_round, status, assigned_to, final_reviewer_ids, final_decision, question_prev_status, question_version, attempt, round_results, created_at, updated_at`

func (s *Store) GetTask(ctx context.Context, id string) (*domain.ReviewTask, error) {
	row := s.queryRowContext(ctx, `SELECT `+taskColumns+` FROM review_tasks WHERE id=$1`, id)
	return scanTask(row)
}

func (s *Store) GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error) {
	row := s.queryRowContext(ctx, `SELECT `+taskColumns+` FROM review_tasks WHERE question_id=$1 ORDER BY created_at DESC LIMIT 1`, questionID)
	return scanTask(row)
}

func (s *Store) UpdateTask(ctx context.Context, t domain.ReviewTask) error {
	return s.SaveTask(ctx, t)
}

// DeleteTask 删除审核任务（级联删除其审核记录）。
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	_, err := s.execContext(ctx, `DELETE FROM review_tasks WHERE id=$1`, id)
	return err
}

func (s *Store) CountActiveTasksByFlow(ctx context.Context, flowID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM review_tasks WHERE flow_id=$1 AND status IN ('reviewing', 'revision_required', 'conflict')
	`, flowID).Scan(&count)
	return count, err
}

func (s *Store) CountTasksByFlow(ctx context.Context, flowID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM review_tasks WHERE flow_id=$1
	`, flowID).Scan(&count)
	return count, err
}

// ===== 审核记录 =====

// reviewRecordColumns 与 scanReviewRecord 保持一致。
const reviewRecordColumns = `id, task_id, question_id, round_number, attempt, expert_id, expert_name, conclusion, opinion, comment, created_at`

// scanReviewRecord 读取一条审核记录；comment 列为结构化评语 JSON（历史行可能是 NULL/'null'）。
func scanReviewRecord(scanner interface{ Scan(dest ...any) error }) (domain.ReviewRecord, error) {
	var r domain.ReviewRecord
	var commentJSON []byte
	if err := scanner.Scan(&r.ID, &r.TaskID, &r.QuestionID, &r.RoundNumber, &r.Attempt, &r.ExpertID, &r.ExpertName, &r.Conclusion, &r.Opinion, &commentJSON, &r.CreatedAt); err != nil {
		return r, err
	}
	if r.Attempt < 1 {
		r.Attempt = 1
	}
	if len(commentJSON) > 0 && string(commentJSON) != "null" {
		var c domain.ReviewComment
		if json.Unmarshal(commentJSON, &c) == nil && !c.IsEmpty() {
			r.Comment = &c
		}
	}
	return r, nil
}

func (s *Store) SaveRecord(ctx context.Context, r domain.ReviewRecord) error {
	var commentJSON []byte
	if r.Comment != nil {
		commentJSON, _ = json.Marshal(r.Comment)
	}
	_, err := s.execContext(ctx, `
		INSERT INTO review_records (id, task_id, question_id, round_number, attempt, expert_id, expert_name, conclusion, opinion, comment, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, r.ID, r.TaskID, r.QuestionID, r.RoundNumber, attemptOrOne(r.Attempt), r.ExpertID, r.ExpertName, r.Conclusion, r.Opinion, commentJSON, r.CreatedAt)
	return err
}

func (s *Store) ListRecordsByTaskID(ctx context.Context, taskID string) ([]domain.ReviewRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+reviewRecordColumns+` FROM review_records WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewRecord
	for rows.Next() {
		r, err := scanReviewRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ListAllTasks 列出全部审核任务（含历史，审核结果汇总用）。
// ListReviewTodoTasks 审核个人待办的 SQL 端预过滤：
// DedupByQuestion 时先按题目去重取最新任务（去重发生在全部任务上再应用状态
// 过滤，最新任务为终态时旧任务不复活，与内存实现语义一致），再按状态/
// 当前轮分配/把关人名单/题目出题人收敛。细粒度语义（本轮已投票等）由
// review 服务在 Go 侧原样判定。
func (s *Store) ListReviewTodoTasks(ctx context.Context, query storage.ReviewTodoQuery) ([]domain.ReviewTask, error) {
	if len(query.Statuses) == 0 {
		return nil, nil
	}
	// 关键语义：去重发生在全部任务上（先 DISTINCT ON 取每题最新任务），
	// 状态/分配/把关人过滤作用在去重结果上——最新任务为终态时旧任务不复活。
	args := []any{pq.Array(query.Statuses)}
	where := " WHERE latest.status = ANY($1)"
	n := 1
	if query.AssignedTo != "" {
		n++
		where += fmt.Sprintf(" AND $%d = ANY(latest.assigned_to)", n)
		args = append(args, query.AssignedTo)
	}
	if !query.FinalAny && query.FinalReviewer != "" {
		n++
		where += fmt.Sprintf(" AND (cardinality(latest.final_reviewer_ids) = 0 OR $%d = ANY(latest.final_reviewer_ids))", n)
		args = append(args, query.FinalReviewer)
	}
	innerOrder := " ORDER BY latest.created_at DESC, latest.id DESC"
	dedup := ""
	if query.DedupByQuestion {
		dedup = " DISTINCT ON (latest.question_id)"
		innerOrder = " ORDER BY latest.question_id, latest.created_at DESC, latest.id DESC"
	}
	rows, err := s.queryContext(ctx, `SELECT `+taskColumns+` FROM (
		SELECT`+dedup+` `+taskColumns+` FROM review_tasks latest`+innerOrder+`
	) latest`+where+` ORDER BY latest.updated_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewTask
	for rows.Next() {
		t, err := scanReviewTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *t)
	}
	return result, rows.Err()
}

// scanReviewTask 扫描一行审核任务（列清单见 taskColumns）。
func scanReviewTask(scanner interface{ Scan(dest ...any) error }) (*domain.ReviewTask, error) {
	var t domain.ReviewTask
	var assignedTo, finalReviewerIDs []string
	var resultsJSON, finalDecisionJSON []byte
	if err := scanner.Scan(&t.ID, &t.QuestionID, &t.FlowID, &t.SubmissionBankID, &t.CurrentRound, &t.Status, pqArrayScanner(&assignedTo), pqArrayScanner(&finalReviewerIDs), &finalDecisionJSON, &t.QuestionPrevStatus, &t.QuestionVersion, &t.Attempt, &resultsJSON, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.AssignedTo = assignedTo
	t.FinalReviewerIDs = finalReviewerIDs
	t.Status = domain.CanonicalLifecycleStatus(t.Status)
	json.Unmarshal(resultsJSON, &t.RoundResults)
	if len(finalDecisionJSON) > 0 && string(finalDecisionJSON) != "null" {
		var fd domain.ExpertReview
		if json.Unmarshal(finalDecisionJSON, &fd) == nil {
			t.FinalDecision = &fd
		}
	}
	return &t, nil
}

func (s *Store) ListAllTasks(ctx context.Context) ([]domain.ReviewTask, error) {
	rows, err := s.queryContext(ctx, `SELECT `+taskColumns+` FROM review_tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewTask
	for rows.Next() {
		t, err := scanReviewTask(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *t)
	}
	return result, rows.Err()
}

// ListRecordsByTaskIDs 批量列出多个任务的审核记录（审核结果汇总按页加载用）。
func (s *Store) ListRecordsByTaskIDs(ctx context.Context, taskIDs []string) ([]domain.ReviewRecord, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+reviewRecordColumns+` FROM review_records WHERE task_id = ANY($1) ORDER BY created_at DESC`, pq.Array(taskIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewRecord
	for rows.Next() {
		r, err := scanReviewRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ===== 操作日志 =====

func (s *Store) SaveLog(ctx context.Context, log domain.AuditLog) error {
	_, err := s.execContext(ctx, `
		INSERT INTO audit_logs (id, question_id, action, actor, detail, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, log.ID, log.QuestionID, log.Action, log.Actor, log.Detail, log.CreatedAt)
	return err
}

func (s *Store) ListLogs(ctx context.Context, limit int) ([]domain.AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, question_id, action, actor, detail, created_at FROM audit_logs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuditLogs(rows)
}

func (s *Store) ListLogsPage(ctx context.Context, limit, offset int) ([]domain.AuditLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, question_id, action, actor, detail, created_at FROM audit_logs ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuditLogs(rows)
}

func (s *Store) CountLogs(ctx context.Context) (int, error) {
	var n int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) ListLogsByQuestion(ctx context.Context, questionID string) ([]domain.AuditLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, question_id, action, actor, detail, created_at FROM audit_logs WHERE question_id=$1 ORDER BY created_at DESC`, questionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuditLogs(rows)
}

func (s *Store) ListLogsByActor(ctx context.Context, actor string) ([]domain.AuditLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, question_id, action, actor, detail, created_at FROM audit_logs WHERE actor=$1 ORDER BY created_at DESC`, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuditLogs(rows)
}

func (s *Store) SearchLogs(ctx context.Context, filter storage.AuditLogFilter) ([]domain.AuditLog, int, error) {
	where := []string{}
	args := []interface{}{}
	if filter.Action != "" {
		args = append(args, filter.Action)
		where = append(where, fmt.Sprintf("action=$%d", len(args)))
	}
	if filter.Actor != "" {
		args = append(args, filter.Actor)
		where = append(where, fmt.Sprintf("actor=$%d", len(args)))
	}
	if filter.QuestionID != "" {
		args = append(args, filter.QuestionID)
		where = append(where, fmt.Sprintf("question_id=$%d", len(args)))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.queryRowContext(ctx, "SELECT COUNT(*) FROM audit_logs"+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, question_id, action, actor, detail, created_at FROM audit_logs`+clause+fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	logs, err := scanAuditLogs(rows)
	if err != nil {
		return nil, 0, err
	}
	return logs, total, nil
}

func (s *Store) ListLogActions(ctx context.Context, actor string) ([]domain.AuditActionStat, error) {
	query := `SELECT action, COUNT(*) FROM audit_logs`
	args := []interface{}{}
	if actor != "" {
		query += ` WHERE actor=$1`
		args = append(args, actor)
	}
	query += ` GROUP BY action ORDER BY COUNT(*) DESC, action ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.AuditActionStat{}
	for rows.Next() {
		var stat domain.AuditActionStat
		if err := rows.Scan(&stat.Action, &stat.Count); err != nil {
			return nil, err
		}
		result = append(result, stat)
	}
	return result, rows.Err()
}

func scanAuditLogs(rows *sql.Rows) ([]domain.AuditLog, error) {
	var result []domain.AuditLog
	for rows.Next() {
		var l domain.AuditLog
		if err := rows.Scan(&l.ID, &l.QuestionID, &l.Action, &l.Actor, &l.Detail, &l.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	return result, rows.Err()
}

// ===== 工具函数 =====

func pqArray(arr []string) interface{} {
	if arr == nil {
		return "{}"
	}
	return fmt.Sprintf("{%s}", strings.Join(arr, ","))
}

func pqArrayScanner(dest *[]string) *arrayScanner {
	return &arrayScanner{dest: dest}
}

type arrayScanner struct {
	dest *[]string
}

func (s *arrayScanner) Scan(src interface{}) error {
	if src == nil {
		*s.dest = nil
		return nil
	}
	switch v := src.(type) {
	case []byte:
		str := string(v)
		if str == "{}" {
			*s.dest = []string{}
			return nil
		}
		str = strings.Trim(str, "{}")
		if str == "" {
			*s.dest = []string{}
			return nil
		}
		*s.dest = strings.Split(str, ",")
		return nil
	case string:
		if v == "{}" {
			*s.dest = []string{}
			return nil
		}
		v = strings.Trim(v, "{}")
		if v == "" {
			*s.dest = []string{}
			return nil
		}
		*s.dest = strings.Split(v, ",")
		return nil
	}
	return fmt.Errorf("unsupported array source: %T", src)
}

func scanQuestion(row *sql.Row) (*domain.A2Question, error) {
	var q domain.A2Question
	var optsJSON, refsJSON, kpsJSON []byte
	var bankIDs []string
	err := row.Scan(&q.ID, &q.ClinicalStem, &optsJSON, &q.Answer, &q.Explanation, &refsJSON, &kpsJSON,
		&q.Difficulty, &q.CognitiveLevel, &q.ExamPoints, &q.OutlineCode, &q.Profession, &q.System,
		pqArrayScanner(&bankIDs), &q.Status, &q.Version, &q.CreatedBy, &q.OwnerID, &q.CreatedAt, &q.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.BankIDs = bankIDs
	q.Status = domain.CanonicalLifecycleStatus(q.Status)
	json.Unmarshal(optsJSON, &q.Options)
	json.Unmarshal(refsJSON, &q.SourceRefs)
	json.Unmarshal(kpsJSON, &q.KnowledgePoints)
	return &q, nil
}

func scanQuestionShareRequest(row *sql.Row) (*domain.QuestionShareRequest, error) {
	var request domain.QuestionShareRequest
	var reviewedAt sql.NullTime
	err := row.Scan(&request.ID, &request.QuestionID, &request.OwnerID, &request.Status,
		&request.ReviewedBy, &request.ReviewNote, &request.CreatedAt, &reviewedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if reviewedAt.Valid {
		value := reviewedAt.Time
		request.ReviewedAt = &value
	}
	return &request, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanQuestionVersion(row rowScanner) (*domain.QuestionVersion, error) {
	var version domain.QuestionVersion
	var snapshot []byte
	err := row.Scan(&version.QuestionID, &version.Version, &snapshot, &version.Actor, &version.ChangeType, &version.ChangeNote, &version.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(snapshot, &version.Snapshot); err != nil {
		return nil, fmt.Errorf("解析题目版本快照失败: %w", err)
	}
	version.Snapshot.Status = domain.CanonicalLifecycleStatus(version.Snapshot.Status)
	return &version, nil
}

func scanQuestionVersionRow(rows *sql.Rows) (*domain.QuestionVersion, error) {
	return scanQuestionVersion(rows)
}

func scanQuestions(rows *sql.Rows) ([]domain.A2Question, error) {
	var result []domain.A2Question
	for rows.Next() {
		var q domain.A2Question
		var optsJSON, refsJSON, kpsJSON []byte
		var bankIDs []string
		if err := rows.Scan(&q.ID, &q.ClinicalStem, &optsJSON, &q.Answer, &q.Explanation, &refsJSON, &kpsJSON,
			&q.Difficulty, &q.CognitiveLevel, &q.ExamPoints, &q.OutlineCode, &q.Profession, &q.System,
			pqArrayScanner(&bankIDs), &q.Status, &q.Version, &q.CreatedBy, &q.OwnerID, &q.CreatedAt, &q.UpdatedAt); err != nil {
			return nil, err
		}
		q.BankIDs = bankIDs
		q.Status = domain.CanonicalLifecycleStatus(q.Status)
		json.Unmarshal(optsJSON, &q.Options)
		json.Unmarshal(refsJSON, &q.SourceRefs)
		json.Unmarshal(kpsJSON, &q.KnowledgePoints)
		result = append(result, q)
	}
	return result, rows.Err()
}

func scanExpert(row *sql.Row) (*domain.Expert, error) {
	var e domain.Expert
	var specialties, expertTypes []string
	err := row.Scan(&e.ID, &e.Name, &e.Department, &e.Title, pqArrayScanner(&specialties), pqArrayScanner(&expertTypes), &e.Contact, &e.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Specialties = specialties
	e.ExpertTypes = expertTypes
	return &e, nil
}

func scanExpertRow(rows *sql.Rows) (domain.Expert, error) {
	var e domain.Expert
	var specialties, expertTypes []string
	err := rows.Scan(&e.ID, &e.Name, &e.Department, &e.Title, pqArrayScanner(&specialties), pqArrayScanner(&expertTypes), &e.Contact, &e.Enabled)
	e.Specialties = specialties
	e.ExpertTypes = expertTypes
	return e, err
}

func scanTask(row *sql.Row) (*domain.ReviewTask, error) {
	var t domain.ReviewTask
	var assignedTo, finalReviewerIDs []string
	var resultsJSON, finalDecisionJSON []byte
	err := row.Scan(&t.ID, &t.QuestionID, &t.FlowID, &t.SubmissionBankID, &t.CurrentRound, &t.Status, pqArrayScanner(&assignedTo), pqArrayScanner(&finalReviewerIDs), &finalDecisionJSON, &t.QuestionPrevStatus, &t.QuestionVersion, &t.Attempt, &resultsJSON, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.AssignedTo = assignedTo
	t.FinalReviewerIDs = finalReviewerIDs
	t.Status = domain.CanonicalLifecycleStatus(t.Status)
	json.Unmarshal(resultsJSON, &t.RoundResults)
	if len(finalDecisionJSON) > 0 && string(finalDecisionJSON) != "null" {
		var fd domain.ExpertReview
		if json.Unmarshal(finalDecisionJSON, &fd) == nil {
			t.FinalDecision = &fd
		}
	}
	return &t, nil
}

// scanFlow 扫描单行审核流程配置。
func scanFlow(row *sql.Row) (*domain.ReviewFlowConfig, error) {
	var f domain.ReviewFlowConfig
	var finalReviewerIDs []string
	var roundsJSON []byte
	err := row.Scan(&f.ID, &f.Name, &f.Description, &f.Subject, &f.BankID, pqArrayScanner(&finalReviewerIDs), &f.VoteRule, &roundsJSON, &f.Archived, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	f.FinalReviewerIDs = finalReviewerIDs
	json.Unmarshal(roundsJSON, &f.Rounds)
	return &f, nil
}

// scanFlowRow 扫描多行结果集中的一行审核流程配置。
func scanFlowRow(rows *sql.Rows) (*domain.ReviewFlowConfig, error) {
	var f domain.ReviewFlowConfig
	var finalReviewerIDs []string
	var roundsJSON []byte
	err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.Subject, &f.BankID, pqArrayScanner(&finalReviewerIDs), &f.VoteRule, &roundsJSON, &f.Archived, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	f.FinalReviewerIDs = finalReviewerIDs
	json.Unmarshal(roundsJSON, &f.Rounds)
	return &f, nil
}

// ===== 批量任务 =====

func jsonOrEmptyArray(value string) string {
	if strings.TrimSpace(value) == "" {
		return "[]"
	}
	return value
}

func (s *Store) SaveBatchJob(ctx context.Context, job storage.BatchJobRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO batch_jobs (id, backend, backend_profile, model, job_name, status, total_count, completed, failed, output_file_id, points_json, owner_id, output_json, finished_at, created_at, updated_at)
		VALUES ($1,COALESCE(NULLIF($2,''),'local_single_api'),COALESCE(NULLIF($3,''),'local-default'),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,'')::timestamptz,NOW(),NOW())
		ON CONFLICT (id) DO UPDATE SET
			backend=COALESCE(NULLIF(EXCLUDED.backend,''),batch_jobs.backend),
			backend_profile=COALESCE(NULLIF(EXCLUDED.backend_profile,''),batch_jobs.backend_profile),
			model=COALESCE(NULLIF(EXCLUDED.model,''),batch_jobs.model),
			job_name=EXCLUDED.job_name, status=EXCLUDED.status,
			total_count=EXCLUDED.total_count, completed=EXCLUDED.completed, failed=EXCLUDED.failed,
			output_file_id=EXCLUDED.output_file_id, output_json=EXCLUDED.output_json,
			owner_id=COALESCE(NULLIF(EXCLUDED.owner_id,''), batch_jobs.owner_id), updated_at=NOW()
	`, job.ID, job.Backend, job.BackendProfile, job.Model, job.JobName, job.Status, job.TotalCount, job.Completed, job.Failed, job.OutputFileID, job.PointsJSON, job.OwnerID, jsonOrEmptyArray(job.OutputJSON), job.FinishedAt)
	return err
}

func (s *Store) UpdateBatchJob(ctx context.Context, job storage.BatchJobRecord) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batch_jobs SET
			backend=COALESCE(NULLIF($1,''),backend),
			backend_profile=COALESCE(NULLIF($2,''),backend_profile),
			model=COALESCE(NULLIF($3,''),model),
			status=$4, total_count=$5, completed=$6, failed=$7, output_file_id=$8, output_json=$9,
			finished_at=CASE
				WHEN $4 IN ('completed','complete','failed','cancelled','expired')
				THEN COALESCE(NULLIF($10,'')::timestamptz, NOW())
				ELSE NULL
			END,
			updated_at=NOW()
		WHERE id=$11
	`, job.Backend, job.BackendProfile, job.Model, job.Status, job.TotalCount, job.Completed, job.Failed, job.OutputFileID, jsonOrEmptyArray(job.OutputJSON), job.FinishedAt, job.ID)
	return err
}

func (s *Store) GetBatchJob(ctx context.Context, id string) (*storage.BatchJobRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, backend, backend_profile, model, job_name, status, total_count, completed, failed, output_file_id, points_json, owner_id, COALESCE(output_json::text, '[]'), COALESCE(imported_at::text, ''), COALESCE(import_result::text, ''), created_at, updated_at, COALESCE(finished_at::text, '') FROM batch_jobs WHERE id=$1`, id)
	var j storage.BatchJobRecord
	err := row.Scan(&j.ID, &j.Backend, &j.BackendProfile, &j.Model, &j.JobName, &j.Status, &j.TotalCount, &j.Completed, &j.Failed, &j.OutputFileID, &j.PointsJSON, &j.OwnerID, &j.OutputJSON, &j.ImportedAt, &j.ImportResult, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) ListBatchJobs(ctx context.Context, limit int) ([]storage.BatchJobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, backend, backend_profile, model, job_name, status, total_count, completed, failed, output_file_id, points_json, owner_id, COALESCE(output_json::text, '[]'), COALESCE(imported_at::text, ''), COALESCE(import_result::text, ''), created_at, updated_at, COALESCE(finished_at::text, '') FROM batch_jobs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storage.BatchJobRecord
	for rows.Next() {
		var j storage.BatchJobRecord
		if err := rows.Scan(&j.ID, &j.Backend, &j.BackendProfile, &j.Model, &j.JobName, &j.Status, &j.TotalCount, &j.Completed, &j.Failed, &j.OutputFileID, &j.PointsJSON, &j.OwnerID, &j.OutputJSON, &j.ImportedAt, &j.ImportResult, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

func (s *Store) SearchBatchJobs(ctx context.Context, name string, limit int) ([]storage.BatchJobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	clauses := []string{}
	args := []any{}
	for _, token := range storage.SearchTokens(name) {
		args = append(args, "%"+escapeLike(token)+"%")
		clauses = append(clauses, fmt.Sprintf("LOWER(job_name) LIKE $%d", len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id, backend, backend_profile, model, job_name, status, total_count, completed, failed, output_file_id, points_json, owner_id, COALESCE(output_json::text, '[]'), COALESCE(imported_at::text, ''), COALESCE(import_result::text, ''), created_at, updated_at, COALESCE(finished_at::text, '') FROM batch_jobs`+where+` ORDER BY created_at DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storage.BatchJobRecord
	for rows.Next() {
		var j storage.BatchJobRecord
		if err := rows.Scan(&j.ID, &j.Backend, &j.BackendProfile, &j.Model, &j.JobName, &j.Status, &j.TotalCount, &j.Completed, &j.Failed, &j.OutputFileID, &j.PointsJSON, &j.OwnerID, &j.OutputJSON, &j.ImportedAt, &j.ImportResult, &j.CreatedAt, &j.UpdatedAt, &j.FinishedAt); err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

// ClaimBatchJobImport 抢占式标记任务为已导入。依赖数据库行级更新原子性：
// 并发触发导入时只有一个调用能把 imported_at 从 NULL 更新为当前时间。
func (s *Store) ClaimBatchJobImport(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE batch_jobs SET imported_at=NOW(), updated_at=NOW() WHERE id=$1 AND imported_at IS NULL`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// SaveBatchJobImportResult 在导入完成后覆盖写入导入结果 JSON，供重复触发时重放。
func (s *Store) SaveBatchJobImportResult(ctx context.Context, id string, resultJSON string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batch_jobs SET import_result=$2::jsonb, imported_at=NOW(), updated_at=NOW() WHERE id=$1
	`, id, resultJSON)
	return err
}

// ReleaseBatchJobImport 释放导入标记，仅在尚未写入任何题目的前置失败后调用，
// 允许后续重试；已保存过导入结果的任务不会被释放。
func (s *Store) ReleaseBatchJobImport(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE batch_jobs SET imported_at=NULL WHERE id=$1 AND import_result IS NULL`, id)
	return err
}

// ===== AI 检查结果 =====

func (s *Store) SaveReviewResult(ctx context.Context, result domain.AIReviewResult) error {
	return s.saveReviewResult(ctx, result)
}

// saveReviewResult 保存一条 AI 检查结果；事务上下文中并入调用方事务。
func (s *Store) saveReviewResult(ctx context.Context, result domain.AIReviewResult) error {
	scoresJSON, _ := json.Marshal(result.Scores)
	issuesJSON, _ := json.Marshal(result.Issues)
	_, err := s.execContext(ctx, `
		INSERT INTO ai_review_results (id, question_id, question_version, verdict, scores, issues, suggestion, model, raw_response, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW())
	`, result.ID, result.QuestionID, result.QuestionVersion, result.Verdict, string(scoresJSON), string(issuesJSON),
		result.Suggestion, result.Model, result.RawResponse)
	return err
}

func (s *Store) GetLatestByQuestionID(ctx context.Context, questionID string) (*domain.AIReviewResult, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, question_id, question_version, verdict, scores, issues, suggestion, model, raw_response, created_at
		FROM ai_review_results WHERE question_id=$1 ORDER BY created_at DESC LIMIT 1
	`, questionID)
	var r domain.AIReviewResult
	var scoresJSON, issuesJSON string
	err := row.Scan(&r.ID, &r.QuestionID, &r.QuestionVersion, &r.Verdict, &scoresJSON, &issuesJSON,
		&r.Suggestion, &r.Model, &r.RawResponse, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(scoresJSON), &r.Scores)
	json.Unmarshal([]byte(issuesJSON), &r.Issues)
	return &r, nil
}

func (s *Store) ListByQuestionIDs(ctx context.Context, questionIDs []string) ([]domain.AIReviewResult, error) {
	if len(questionIDs) == 0 {
		return nil, nil
	}
	query := `SELECT DISTINCT ON (question_id) id, question_id, question_version, verdict, scores, issues, suggestion, model, raw_response, created_at
		FROM ai_review_results WHERE question_id = ANY($1) ORDER BY question_id, created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, pq.Array(questionIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []domain.AIReviewResult
	for rows.Next() {
		var r domain.AIReviewResult
		var scoresJSON, issuesJSON string
		if err := rows.Scan(&r.ID, &r.QuestionID, &r.QuestionVersion, &r.Verdict, &scoresJSON, &issuesJSON,
			&r.Suggestion, &r.Model, &r.RawResponse, &r.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(scoresJSON), &r.Scores)
		json.Unmarshal([]byte(issuesJSON), &r.Issues)
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) ListAll(ctx context.Context, limit int) ([]domain.AIReviewResult, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, question_id, question_version, verdict, scores, issues, suggestion, model, raw_response, created_at
		FROM ai_review_results ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []domain.AIReviewResult
	for rows.Next() {
		var r domain.AIReviewResult
		var scoresJSON, issuesJSON string
		if err := rows.Scan(&r.ID, &r.QuestionID, &r.QuestionVersion, &r.Verdict, &scoresJSON, &issuesJSON,
			&r.Suggestion, &r.Model, &r.RawResponse, &r.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(scoresJSON), &r.Scores)
		json.Unmarshal([]byte(issuesJSON), &r.Issues)
		results = append(results, r)
	}
	return results, rows.Err()
}

// ===== AI 检查任务队列 =====

const aiCheckTaskColumns = `id, question_id, question_version, status, attempts, max_attempts, last_error, leased_until, created_at, updated_at`

func scanAICheckTask(scanner interface{ Scan(dest ...any) error }) (domain.AICheckTask, error) {
	var t domain.AICheckTask
	// leased_until 在 pending（未入租约）和 succeeded/exhausted（回收）状态下为 NULL
	var leasedUntil sql.NullTime
	err := scanner.Scan(&t.ID, &t.QuestionID, &t.QuestionVersion, &t.Status, &t.Attempts, &t.MaxAttempts,
		&t.LastError, &leasedUntil, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	t.LeasedUntil = leasedUntil.Time
	return t, nil
}

func (s *Store) EnqueueCheckTask(ctx context.Context, task domain.AICheckTask) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO ai_check_tasks (id, question_id, question_version, status, attempts, max_attempts, last_error, leased_until, created_at, updated_at)
		SELECT $1, $2, $3, $4, 0, $5, '', NULL, NOW(), NOW()
		WHERE NOT EXISTS (SELECT 1 FROM ai_check_tasks WHERE question_id = $2)
	`, task.ID, task.QuestionID, task.QuestionVersion, task.Status, task.MaxAttempts)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (s *Store) ClaimNextCheckTask(ctx context.Context, lease time.Duration) (*domain.AICheckTask, error) {
	row := s.db.QueryRowContext(ctx, `
		UPDATE ai_check_tasks SET status='running', attempts=attempts+1, leased_until=$1, updated_at=NOW()
		WHERE id = (
			SELECT id FROM ai_check_tasks
			WHERE (status='pending' AND (leased_until IS NULL OR leased_until <= NOW()))
			   OR (status='running' AND leased_until <= NOW())
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING `+aiCheckTaskColumns, time.Now().Add(lease))
	task, err := scanAICheckTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) CompleteCheckTask(ctx context.Context, taskID string) error {
	return s.completeCheckTask(ctx, taskID)
}

// completeCheckTask 标记任务成功；事务上下文中并入调用方事务。
func (s *Store) completeCheckTask(ctx context.Context, taskID string) error {
	_, err := s.execContext(ctx, `
		UPDATE ai_check_tasks SET status='succeeded', leased_until=NULL, last_error='', updated_at=NOW()
		WHERE id=$1
	`, taskID)
	return err
}

func (s *Store) FailCheckTask(ctx context.Context, taskID string, errMsg string, backoff time.Duration) error {
	// $2 需显式标注 timestamptz：CASE 中与 NULL 混用时 PG 会把参数推断为 text
	_, err := s.db.ExecContext(ctx, `
		UPDATE ai_check_tasks SET
			status = CASE WHEN attempts >= max_attempts THEN 'exhausted' ELSE 'pending' END,
			leased_until = CASE WHEN attempts >= max_attempts THEN NULL ELSE $2::timestamptz END,
			last_error = $3,
			updated_at = NOW()
		WHERE id=$1
	`, taskID, time.Now().Add(backoff), errMsg)
	return err
}

func (s *Store) LatestCheckTasksByQuestionIDs(ctx context.Context, questionIDs []string) (map[string]domain.AICheckTask, error) {
	if len(questionIDs) == 0 {
		return map[string]domain.AICheckTask{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (question_id) `+aiCheckTaskColumns+`
		FROM ai_check_tasks WHERE question_id = ANY($1)
		ORDER BY question_id, created_at DESC, id DESC
	`, pq.Array(questionIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]domain.AICheckTask, len(questionIDs))
	for rows.Next() {
		t, err := scanAICheckTask(rows)
		if err != nil {
			return nil, err
		}
		out[t.QuestionID] = t
	}
	return out, rows.Err()
}

func (s *Store) CountCheckTasksByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM ai_check_tasks GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = count
	}
	return out, rows.Err()
}

// ===== AI 检查淘汰记录 =====

func (s *Store) SaveDiscardResult(ctx context.Context, discard domain.AICheckDiscard) error {
	return s.saveDiscardResult(ctx, discard)
}

// saveDiscardResult 保存一条淘汰档案；事务上下文中并入调用方事务。
// 淘汰档案无外键约束，题目删除后仍保留供生成页展示。
func (s *Store) saveDiscardResult(ctx context.Context, discard domain.AICheckDiscard) error {
	scoresJSON, _ := json.Marshal(discard.Scores)
	issuesJSON, _ := json.Marshal(discard.Issues)
	var questionJSON any
	if discard.Question != nil {
		payload, err := json.Marshal(discard.Question)
		if err != nil {
			return fmt.Errorf("序列化淘汰题目快照失败: %w", err)
		}
		questionJSON = string(payload)
	}
	_, err := s.execContext(ctx, `
		INSERT INTO ai_check_discards (id, question_id, verdict, scores, issues, suggestion, model, stem_summary, owner_id, question_json, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW())
	`, discard.ID, discard.QuestionID, discard.Verdict, string(scoresJSON), string(issuesJSON),
		discard.Suggestion, discard.Model, discard.StemSummary, discard.OwnerID, questionJSON)
	return err
}

// ApplyAICheckOutcome 在单个 PostgreSQL 事务中落库一次 AI 检查结论：
// 检查结果、题目状态推进或淘汰删除、检查任务完成要么全部提交、要么全部回滚。
// 任何一步失败都返回错误，调用方（aicheck 服务）据此重试，不会留下半成品状态。
func (s *Store) ApplyAICheckOutcome(ctx context.Context, outcome storage.AICheckOutcome) error {
	if tx := reviewTransactionFromContext(ctx); tx != nil {
		return s.applyAICheckOutcome(ctx, tx, outcome)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始 AI 检查事务失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	txCtx := context.WithValue(ctx, reviewTransactionContextKey{}, tx)
	if err := s.applyAICheckOutcome(txCtx, tx, outcome); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 AI 检查事务失败: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) applyAICheckOutcome(ctx context.Context, executor sqlQueryExecer, outcome storage.AICheckOutcome) error {
	if err := s.saveReviewResult(ctx, outcome.Result); err != nil {
		return fmt.Errorf("保存检查结果失败: %w", err)
	}
	if outcome.Question != nil {
		// 检查通过推进状态：内容不变、版本号不变，仅状态与更新时间变化
		if err := s.saveQuestion(ctx, executor, *outcome.Question); err != nil {
			return fmt.Errorf("更新题目状态失败: %w", err)
		}
	}
	if outcome.Discard != nil {
		if err := s.saveDiscardResult(ctx, *outcome.Discard); err != nil {
			return fmt.Errorf("保存淘汰档案失败: %w", err)
		}
	}
	if outcome.DeleteQuestionID != "" {
		// 级联清理版本/审核数据；刚写入的检查结果随外键级联删除，仅淘汰档案留档
		if err := s.deleteQuestion(ctx, outcome.DeleteQuestionID); err != nil {
			return fmt.Errorf("删除未通过检查的题目失败: %w", err)
		}
	}
	if outcome.CompleteTaskID != "" {
		if err := s.completeCheckTask(ctx, outcome.CompleteTaskID); err != nil {
			return fmt.Errorf("标记检查任务完成失败: %w", err)
		}
	}
	return nil
}

// CountReviewResultsByVerdict 按 verdict 统计 AI 检查结果数量（存储端聚合）。
func (s *Store) CountReviewResultsByVerdict(ctx context.Context) (map[string]int, error) {
	counts := map[string]int{}
	if err := s.scanCounts(ctx, `SELECT verdict, COUNT(*) FROM ai_review_results GROUP BY verdict`, nil, counts); err != nil {
		return nil, err
	}
	return counts, nil
}

// CountReviewResultsByVerdictForOwner 按题目归属人统计 AI 检查结果（JOIN questions 过滤）。
func (s *Store) CountReviewResultsByVerdictForOwner(ctx context.Context, ownerID string) (map[string]int, error) {
	counts := map[string]int{}
	if err := s.scanCounts(ctx, `
		SELECT r.verdict, COUNT(*) FROM ai_review_results r
		JOIN questions q ON q.id = r.question_id
		WHERE q.owner_id = $1 GROUP BY r.verdict`, []any{ownerID}, counts); err != nil {
		return nil, err
	}
	return counts, nil
}

// CountDiscardResults 统计 AI 检查淘汰留档总数。
func (s *Store) CountDiscardResults(ctx context.Context) (int, error) {
	var n int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM ai_check_discards`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountDiscardResultsForOwner 统计某归属人的 AI 检查淘汰留档数。
func (s *Store) CountDiscardResultsForOwner(ctx context.Context, ownerID string) (int, error) {
	var n int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM ai_check_discards WHERE owner_id = $1`, ownerID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// CountCheckTasksByStatusForOwner 按题目归属人统计 AI 检查任务状态数（JOIN questions 过滤）。
func (s *Store) CountCheckTasksByStatusForOwner(ctx context.Context, ownerID string) (map[string]int, error) {
	counts := map[string]int{}
	if err := s.scanCounts(ctx, `
		SELECT t.status, COUNT(*) FROM ai_check_tasks t
		JOIN questions q ON q.id = t.question_id
		WHERE q.owner_id = $1 GROUP BY t.status`, []any{ownerID}, counts); err != nil {
		return nil, err
	}
	return counts, nil
}

func (s *Store) ListDiscardResultsByQuestionIDs(ctx context.Context, questionIDs []string) (map[string]domain.AICheckDiscard, error) {
	if len(questionIDs) == 0 {
		return map[string]domain.AICheckDiscard{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT ON (question_id) id, question_id, verdict, scores, issues, suggestion, model, stem_summary, created_at, owner_id, question_json
		FROM ai_check_discards WHERE question_id = ANY($1)
		ORDER BY question_id, created_at DESC
	`, pq.Array(questionIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]domain.AICheckDiscard, len(questionIDs))
	for rows.Next() {
		var d domain.AICheckDiscard
		var scoresJSON, issuesJSON string
		var questionJSON []byte
		if err := rows.Scan(&d.ID, &d.QuestionID, &d.Verdict, &scoresJSON, &issuesJSON,
			&d.Suggestion, &d.Model, &d.StemSummary, &d.CreatedAt, &d.OwnerID, &questionJSON); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(scoresJSON), &d.Scores)
		json.Unmarshal([]byte(issuesJSON), &d.Issues)
		if len(questionJSON) > 0 {
			var q domain.A2Question
			if err := json.Unmarshal(questionJSON, &q); err == nil && q.ID != "" {
				d.Question = &q
			}
		}
		out[d.QuestionID] = d
	}
	return out, rows.Err()
}

// ===== 角色模板 =====

func (s *Store) SaveRole(ctx context.Context, r domain.Role) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO roles (id, name, description, permissions, is_builtin, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, description=EXCLUDED.description,
			permissions=EXCLUDED.permissions, is_builtin=EXCLUDED.is_builtin, updated_at=EXCLUDED.updated_at
	`, r.ID, r.Name, r.Description, pqArray(r.Permissions), r.IsBuiltin, r.CreatedAt, r.UpdatedAt)
	return err
}

func (s *Store) GetRole(ctx context.Context, id string) (*domain.Role, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, permissions, is_builtin, created_at, updated_at FROM roles WHERE id=$1`, id)
	var r domain.Role
	var perms []string
	err := row.Scan(&r.ID, &r.Name, &r.Description, pqArrayScanner(&perms), &r.IsBuiltin, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Permissions = perms
	return &r, nil
}

func (s *Store) ListRoles(ctx context.Context) ([]domain.Role, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, permissions, is_builtin, created_at, updated_at FROM roles ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.Role
	for rows.Next() {
		var r domain.Role
		var perms []string
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, pqArrayScanner(&perms), &r.IsBuiltin, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Permissions = perms
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) DeleteRole(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE id=$1`, id)
	return err
}

// ===== 题库 =====

func (s *Store) SaveBank(ctx context.Context, b domain.QuestionBank) error {
	_, err := s.execContext(ctx, `
		INSERT INTO question_banks (id, name, description, professions, created_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, description=EXCLUDED.description, professions=EXCLUDED.professions
	`, b.ID, b.Name, b.Description, pqArray(b.Professions), b.CreatedAt)
	return err
}

func (s *Store) GetBank(ctx context.Context, id string) (*domain.QuestionBank, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, professions, created_at FROM question_banks WHERE id=$1`, id)
	var b domain.QuestionBank
	var professions []string
	err := row.Scan(&b.ID, &b.Name, &b.Description, pqArrayScanner(&professions), &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.Professions = professions
	return &b, nil
}

func (s *Store) ListBanks(ctx context.Context) ([]domain.QuestionBank, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, professions, created_at FROM question_banks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.QuestionBank
	for rows.Next() {
		var b domain.QuestionBank
		var professions []string
		if err := rows.Scan(&b.ID, &b.Name, &b.Description, pqArrayScanner(&professions), &b.CreatedAt); err != nil {
			return nil, err
		}
		b.Professions = professions
		result = append(result, b)
	}
	return result, rows.Err()
}

func (s *Store) DeleteBank(ctx context.Context, id string) error {
	_, err := s.execContext(ctx, `DELETE FROM question_banks WHERE id=$1`, id)
	return err
}

// AddQuestionsToBank 批量把题目加入题库（高效 SQL 插入成员关系，跳过已在库的）。
func (s *Store) AddQuestionsToBank(ctx context.Context, questionIDs []string, bankID string) (int, error) {
	if len(questionIDs) == 0 {
		return 0, nil
	}
	// 校验题库存在
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM question_banks WHERE id=$1)`, bankID).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, fmt.Errorf("题库 %s 不存在", bankID)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO question_bank_members (question_id, bank_id)
		SELECT q.id, $2 FROM questions q WHERE q.id = ANY($1::text[])
		ON CONFLICT DO NOTHING
	`, pq.Array(questionIDs), bankID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
