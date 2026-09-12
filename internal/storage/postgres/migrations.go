package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// baselineSchemaSQL 是版本 1 的不可变数据库基线。后续结构变化必须新增迁移，
// 不能修改已在生产库记录过校验和的历史迁移。
//
//go:embed schema.sql
var baselineSchemaSQL string

const (
	// LatestSchemaVersion 是当前程序能够使用的最新数据库版本。
	LatestSchemaVersion int64 = 24
	// migrationLockKey 在同一 PostgreSQL 数据库内串行化所有 AIgo Schema 迁移。
	migrationLockKey int64 = 0x4149474f5f4d4947 // "AIGO_MIG"
)

const migrationTableSQL = `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`

// historicalMigrationChecksums 记录已经发布过的迁移 checksum。17 号迁移曾在
// 个人题库功能的预发布构建中落库，后续仅补充了等价的兼容执行逻辑；接受该
// 已落库 checksum，才能在不篡改迁移台账的前提下继续应用 18 号迁移。
var historicalMigrationChecksums = map[int64]map[string]struct{}{
	17: {
		"f69592a847835f245b1e66788af0640b80434f698c852ee9ef65c892e702ab6c": {},
	},
	19: {
		// 本地部署已应用的 19 号迁移与当前定义 SQL 等价，仅因历史注释/格式
		// 调整产生不同 checksum；保留兼容值，避免上线新增配置表时阻断升级。
		"78cdf3447f47409c16188b4bd5bb739f32890ecc7582b0354709033ab1cc2651": {},
	},
}

// MigrationInfo 是可安全展示的迁移元数据。
type MigrationInfo struct {
	Version  int64
	Name     string
	Checksum string
}

// MigrationStatus 描述数据库当前版本和待应用版本。
type MigrationStatus struct {
	Initialized    bool
	CurrentVersion int64
	LatestVersion  int64
	Pending        []MigrationInfo
}

type migration struct {
	Version    int64
	Name       string
	Statements []string
}

type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func configuredMigrations(schemaSQL string) []migration {
	return []migration{
		{
			Version:    1,
			Name:       "baseline_schema",
			Statements: []string{schemaSQL},
		},
		{
			Version:    2,
			Name:       "legacy_schema_compatibility",
			Statements: legacyCompatibilityStatements(),
		},
		{
			Version: 3,
			Name:    "authentication_rate_limits",
			Statements: []string{
				`CREATE TABLE IF NOT EXISTS auth_login_limits (
					scope TEXT NOT NULL,
					key_hash TEXT NOT NULL,
					failure_count INT NOT NULL DEFAULT 0,
					window_started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					blocked_until TIMESTAMPTZ,
					updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
					PRIMARY KEY (scope, key_hash)
				)`,
				`CREATE INDEX IF NOT EXISTS idx_auth_login_limits_updated ON auth_login_limits(updated_at)`,
			},
		},
		{Version: 4, Name: "knowledge_syllabus_versions", Statements: knowledgeVersionStatements()},
		{Version: 5, Name: "knowledge_version_deletion", Statements: knowledgeVersionDeletionStatements()},
		{Version: 6, Name: "review_structured_comments", Statements: []string{
			`ALTER TABLE review_records ADD COLUMN IF NOT EXISTS expert_name TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE review_records ADD COLUMN IF NOT EXISTS comment JSONB DEFAULT 'null'`,
		}},
		{Version: 7, Name: "review_submission_attempt", Statements: []string{
			`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS attempt INT NOT NULL DEFAULT 1`,
			`ALTER TABLE review_records ADD COLUMN IF NOT EXISTS attempt INT NOT NULL DEFAULT 1`,
		}},
		{Version: 8, Name: "query_performance_indexes", Statements: queryPerformanceIndexStatements()},
		{Version: 9, Name: "ai_check_task_queue", Statements: []string{
			`CREATE TABLE IF NOT EXISTS ai_check_tasks (
				id TEXT PRIMARY KEY,
				question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
				question_version INT NOT NULL DEFAULT 0,
				status TEXT NOT NULL DEFAULT 'pending',
				attempts INT NOT NULL DEFAULT 0,
				max_attempts INT NOT NULL DEFAULT 3,
				last_error TEXT NOT NULL DEFAULT '',
				leased_until TIMESTAMPTZ,
				created_at TIMESTAMPTZ DEFAULT NOW(),
				updated_at TIMESTAMPTZ DEFAULT NOW()
			)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_check_tasks_claim ON ai_check_tasks(status, leased_until, created_at)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_check_tasks_question ON ai_check_tasks(question_id)`,
		}},
		{Version: 10, Name: "question_created_by_and_ai_check_discards", Statements: []string{
			`ALTER TABLE questions ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT ''`,
			`CREATE TABLE IF NOT EXISTS ai_check_discards (
				id TEXT PRIMARY KEY,
				question_id TEXT NOT NULL,
				verdict TEXT NOT NULL DEFAULT 'reject',
				scores JSONB NOT NULL DEFAULT '{}',
				issues JSONB NOT NULL DEFAULT '[]',
				suggestion TEXT NOT NULL DEFAULT '',
				model TEXT NOT NULL DEFAULT '',
				stem_summary TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ DEFAULT NOW()
			)`,
			`CREATE INDEX IF NOT EXISTS idx_ai_check_discards_question ON ai_check_discards(question_id)`,
		}},
		{Version: 11, Name: "batch_import_idempotency", Statements: []string{
			// 导入幂等：批量任务结果只允许入库一次。imported_at 非空表示已导入；
			// import_result 保存上次导入结果 JSON，重复触发时重放而不重新入库。
			`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS imported_at TIMESTAMPTZ`,
			`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS import_result JSONB`,
			// 本功能上线前已完成的历史任务按原手动流程处理，统一回填为已导入，
			// 避免自动导入把历史任务的结果重复写进题库。
			`UPDATE batch_jobs SET imported_at = COALESCE(updated_at, NOW()) WHERE status IN ('completed','complete') AND imported_at IS NULL`,
		}},
		{Version: 12, Name: "review_submission_bank_snapshot", Statements: []string{
			`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS submission_bank_id TEXT NOT NULL DEFAULT ''`,
			// 历史任务仅在题目恰好属于一个子题库时安全回填；多归属任务保留空值并阻止自动改派。
			`UPDATE review_tasks rt
				 SET submission_bank_id = COALESCE((
					 SELECT MIN(m.bank_id) FROM question_bank_members m
					 WHERE m.question_id = rt.question_id
					 GROUP BY m.question_id HAVING COUNT(*) = 1
				 ), '')
				 WHERE submission_bank_id = ''`,
			`CREATE INDEX IF NOT EXISTS idx_review_tasks_submission_bank ON review_tasks(submission_bank_id)`,
		}},
		{Version: 13, Name: "review_results_query_index", Statements: []string{
			`CREATE INDEX IF NOT EXISTS idx_review_tasks_question_created ON review_tasks(question_id, created_at DESC)`,
		}},
		{Version: 14, Name: "question_unified_search_text", Statements: []string{
			`ALTER TABLE questions ADD COLUMN IF NOT EXISTS search_text TEXT NOT NULL DEFAULT ''`,
			`UPDATE questions SET search_text = LOWER(CONCAT_WS(' ',
				id, clinical_stem, options::text, answer, explanation, source_refs::text,
				knowledge_points::text, media_refs::text, difficulty, cognitive_level,
				exam_points, outline_code, profession, system_name
			))`,
			`DO $$
			DECLARE
				ext_schema text;
			BEGIN
				IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
					SELECT extnamespace::regnamespace::text INTO ext_schema FROM pg_extension WHERE extname = 'pg_trgm';
					EXECUTE format('CREATE INDEX IF NOT EXISTS idx_questions_search_text_trgm ON questions USING gin (search_text %I.gin_trgm_ops)', ext_schema);
				END IF;
			END $$`,
		}},
		{Version: 15, Name: "remove_question_images", Statements: []string{
			// 题目继续保留；只清理图片引用、候选图及其审核数据。
			`UPDATE question_versions
			 SET snapshot = snapshot - 'media_refs'
			 WHERE snapshot ? 'media_refs'`,
			`UPDATE questions SET search_text = LOWER(CONCAT_WS(' ',
				id, clinical_stem, options::text, answer, explanation, source_refs::text,
				knowledge_points::text, difficulty, cognitive_level,
				exam_points, outline_code, profession, system_name
			))`,
			`UPDATE roles SET permissions = array_remove(array_remove(permissions, 'image:generate'), 'image:review')`,
			`UPDATE users SET permissions = array_remove(array_remove(permissions, 'image:generate'), 'image:review')`,
			`DROP TABLE IF EXISTS image_review_records`,
			`DROP TABLE IF EXISTS generated_images`,
			`DROP TABLE IF EXISTS image_prompts`,
			`ALTER TABLE questions DROP COLUMN IF EXISTS media_refs`,
		}},
		{Version: 16, Name: "single_super_admin_role", Statements: []string{
			// 历史库若已有多个超级管理员，优先保留用户名 admin，否则保留最早创建者，
			// 再由唯一索引从数据库层面保证后续任何写入都不能产生第二个超级管理员。
			`UPDATE users
			 SET role='admin'
			 WHERE role='super_admin'
			   AND id <> COALESCE(
					(SELECT id FROM users WHERE username='admin' ORDER BY created_at, id LIMIT 1),
					(SELECT id FROM users WHERE role='super_admin' ORDER BY created_at, id LIMIT 1)
			   )`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_single_super_admin ON users(role) WHERE role='super_admin'`,
		}},
		{Version: 17, Name: "personal_question_ownership_and_global_shares", Statements: []string{
			`ALTER TABLE questions ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT ''`,
			`UPDATE questions q
			 SET owner_id=u.id
			 FROM users u
			 WHERE q.owner_id='' AND q.created_by<>'' AND u.username=q.created_by`,
			`CREATE INDEX IF NOT EXISTS idx_questions_owner_status ON questions(owner_id, status, created_at DESC)`,
			`CREATE TABLE IF NOT EXISTS question_share_requests (
				id TEXT PRIMARY KEY,
				question_id TEXT NOT NULL UNIQUE REFERENCES questions(id) ON DELETE CASCADE,
				owner_id TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','rejected')),
				reviewed_by TEXT NOT NULL DEFAULT '',
				review_note TEXT NOT NULL DEFAULT '',
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				reviewed_at TIMESTAMPTZ
			)`,
			`CREATE INDEX IF NOT EXISTS idx_question_share_status_created ON question_share_requests(status, created_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_question_share_owner_created ON question_share_requests(owner_id, created_at DESC)`,
			// 迁移前已有题目属于原来的全局题库：按生命周期状态登记到全局三层，
			// 避免启用个人题库后历史待审核/淘汰题从管理员视图消失。
			`INSERT INTO question_share_requests (id, question_id, owner_id, status, reviewed_by, review_note, created_at, reviewed_at)
			 SELECT 'legacy-share-'||q.id, q.id, q.owner_id,
				CASE WHEN q.status='published' THEN 'approved'
					 WHEN q.status IN ('rejected','archived') THEN 'rejected'
					 ELSE 'pending' END,
				'migration', '历史题目纳入全局题库', COALESCE(q.created_at, NOW()),
				CASE WHEN q.status IN ('published','rejected','archived') THEN NOW() ELSE NULL END
			 FROM questions q
			 WHERE q.owner_id=''
				ON CONFLICT (question_id) DO NOTHING`,
		}},
		{Version: 18, Name: "personal_batch_job_ownership", Statements: []string{
			`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT ''`,
			`CREATE INDEX IF NOT EXISTS idx_batch_jobs_owner_created ON batch_jobs(owner_id, created_at DESC)`,
		}},
		{Version: 19, Name: "restore_legacy_global_question_tiers", Statements: []string{
			// 17 号迁移的预发布版本只登记了旧正式题；为已经应用过该版本的数据库
			// 补登记仍无个人归属的历史待审核/淘汰题。带 owner_id 的新个人题不受影响。
			`INSERT INTO question_share_requests (id, question_id, owner_id, status, reviewed_by, review_note, created_at, reviewed_at)
			 SELECT 'legacy-share-'||q.id, q.id, q.owner_id,
				CASE WHEN q.status='published' THEN 'approved'
					 WHEN q.status IN ('rejected','archived') THEN 'rejected'
					 ELSE 'pending' END,
				'migration', '历史题目纳入全局题库', COALESCE(q.created_at, NOW()),
				CASE WHEN q.status IN ('published','rejected','archived') THEN NOW() ELSE NULL END
			 FROM questions q
			 WHERE q.owner_id=''
				 ON CONFLICT (question_id) DO NOTHING`,
		}},
		{Version: 20, Name: "ai_provider_configs", Statements: []string{
			`CREATE TABLE IF NOT EXISTS ai_provider_configs (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				deployment TEXT NOT NULL DEFAULT 'cloud' CHECK (deployment IN ('cloud','local')),
				base_url TEXT NOT NULL,
				api_key_ciphertext TEXT NOT NULL DEFAULT '',
				generation_model TEXT NOT NULL,
				check_model TEXT NOT NULL,
				active BOOLEAN NOT NULL DEFAULT FALSE,
				source TEXT NOT NULL DEFAULT 'manual',
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_provider_configs_single_active ON ai_provider_configs(active) WHERE active = TRUE`,
			`CREATE INDEX IF NOT EXISTS idx_ai_provider_configs_updated ON ai_provider_configs(updated_at DESC)`,
		}},
		{Version: 21, Name: "ai_provider_batch_config", Statements: []string{
			`ALTER TABLE ai_provider_configs ADD COLUMN IF NOT EXISTS batch_api_key_ciphertext TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_provider_configs ADD COLUMN IF NOT EXISTS batch_base_url TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE ai_provider_configs ADD COLUMN IF NOT EXISTS batch_model TEXT NOT NULL DEFAULT ''`,
		}},
		{Version: 22, Name: "separate_question_and_batch_permissions", Statements: []string{
			// 试题生成与批量推理是两个独立权限。清理旧版本内置角色中
			// 曾默认继承的批量权限；用户后续明确配置的直接权限不受影响。
			`UPDATE roles
			 SET permissions = array_remove(permissions, 'batch:run')
			 WHERE is_builtin = TRUE AND id IN ('admin', 'expert', 'teacher')`,
		}},
		{Version: 23, Name: "generation_run_recovery", Statements: []string{
			`CREATE TABLE IF NOT EXISTS generation_runs (
				id TEXT PRIMARY KEY,
				owner_id TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed')),
				requested_count INT NOT NULL DEFAULT 1,
				question_ids TEXT[] NOT NULL DEFAULT '{}',
				error TEXT NOT NULL DEFAULT '',
				started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				completed_at TIMESTAMPTZ,
				updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			)`,
			`CREATE INDEX IF NOT EXISTS idx_generation_runs_owner_started ON generation_runs(owner_id, started_at DESC)`,
		}},
		{Version: 24, Name: "generation_run_persistent_queue", Statements: []string{
			// 单题生成升级为持久化任务：请求快照随提交落库，worker 抢占执行，
			// 租约到期可由新进程接管，失败按次数上限退避重试。
			`ALTER TABLE generation_runs
				ADD COLUMN IF NOT EXISTS request_json JSONB,
				ADD COLUMN IF NOT EXISTS attempts INT NOT NULL DEFAULT 0,
				ADD COLUMN IF NOT EXISTS max_attempts INT NOT NULL DEFAULT 3,
				ADD COLUMN IF NOT EXISTS leased_until TIMESTAMPTZ`,
			// v23 的内联 CHECK 约束被自动命名为 generation_runs_status_check，放宽加入 pending
			`DO $$
			 BEGIN
				ALTER TABLE generation_runs DROP CONSTRAINT generation_runs_status_check;
			 EXCEPTION WHEN undefined_object THEN NULL;
			 END $$;`,
			`ALTER TABLE generation_runs ADD CONSTRAINT generation_runs_status_check CHECK (status IN ('pending','running','succeeded','failed'))`,
			`CREATE INDEX IF NOT EXISTS idx_generation_runs_status_lease ON generation_runs(status, leased_until)`,
		}},
	}
}

// queryPerformanceIndexStatements 为列表/搜索的高频过滤与排序补齐索引。
// pg_trgm 可能因数据库权限不可用（托管库常见），缺失时跳过三元组索引，
// 关键词搜索退化为顺序扫描，其余等值/前缀索引不受影响。
func queryPerformanceIndexStatements() []string {
	return []string{
		// 题目专业筛选（等值）
		`CREATE INDEX IF NOT EXISTS idx_questions_profession ON questions(profession)`,
		// 列表默认排序 ORDER BY created_at DESC
		`CREATE INDEX IF NOT EXISTS idx_questions_created_at ON questions(created_at DESC)`,
		// 状态筛选 + 按时间排序的组合查询
		`CREATE INDEX IF NOT EXISTS idx_questions_status_created ON questions(status, created_at DESC)`,
		// 审核任务按流程统计进行中数量（flow_id + status 组合）
		`CREATE INDEX IF NOT EXISTS idx_review_tasks_flow_status ON review_tasks(flow_id, status)`,
		// 批量任务列表 ORDER BY created_at DESC LIMIT
		`CREATE INDEX IF NOT EXISTS idx_batch_jobs_created_at ON batch_jobs(created_at DESC)`,
		// 题干模糊搜索的三元组索引（可选）。扩展固定安装到 public，
		// 操作符类按扩展实际所在 schema 动态限定，兼容测试用的独立 search_path。
		`DO $$
		BEGIN
			CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public;
		EXCEPTION WHEN OTHERS THEN
			RAISE NOTICE 'pg_trgm extension unavailable, skipping trigram index';
		END $$`,
		`DO $$
		DECLARE
			ext_schema text;
		BEGIN
			IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
				SELECT extnamespace::regnamespace::text INTO ext_schema FROM pg_extension WHERE extname = 'pg_trgm';
				EXECUTE format('CREATE INDEX IF NOT EXISTS idx_questions_stem_trgm ON questions USING gin (clinical_stem %I.gin_trgm_ops)', ext_schema);
			END IF;
		END $$`,
	}
}

func migrationInfo(m migration) MigrationInfo {
	payload := fmt.Sprintf("%d\n%s\n%s", m.Version, m.Name, strings.Join(m.Statements, "\n-- AIgo migration statement --\n"))
	sum := sha256.Sum256([]byte(payload))
	return MigrationInfo{Version: m.Version, Name: m.Name, Checksum: hex.EncodeToString(sum[:])}
}

// Migrate 将数据库按顺序迁移到当前版本。每个版本使用独立事务，失败时该版本
// 的结构、数据和版本记录会一起回滚；数据库级锁保证多个进程不会重复执行。
func (s *Store) Migrate(ctx context.Context) ([]MigrationInfo, error) {
	return s.migrate(ctx, configuredMigrations(baselineSchemaSQL))
}

// InitSchema 保留给现有调用方和集成测试；实际执行已统一进入版本化迁移器。
func (s *Store) InitSchema(schemaSQL string) error {
	if strings.TrimSpace(schemaSQL) == "" {
		schemaSQL = baselineSchemaSQL
	}
	_, err := s.migrate(context.Background(), configuredMigrations(schemaSQL))
	return err
}

func (s *Store) migrate(ctx context.Context, migrations []migration) (applied []MigrationInfo, err error) {
	if err := validateMigrationDefinitions(migrations); err != nil {
		return nil, err
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取数据库迁移连接失败: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return nil, fmt.Errorf("获取数据库迁移锁失败: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, migrationLockKey)
	}()

	if _, err := conn.ExecContext(ctx, migrationTableSQL); err != nil {
		return nil, fmt.Errorf("创建数据库迁移记录表失败: %w", err)
	}

	known, current, err := readAndValidateAppliedMigrations(ctx, conn, migrations)
	if err != nil {
		return nil, err
	}
	for _, m := range migrations {
		if _, ok := known[m.Version]; ok {
			continue
		}
		if m.Version != current+1 {
			return nil, fmt.Errorf("数据库迁移版本不连续: 当前版本 %d，下一迁移版本 %d", current, m.Version)
		}

		info := migrationInfo(m)
		statements, err := migrationStatementsForExecution(ctx, conn, m)
		if err != nil {
			return nil, fmt.Errorf("检查数据库迁移 %d (%s) 兼容状态失败: %w", m.Version, m.Name, err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("开始数据库迁移 %d (%s) 失败: %w", m.Version, m.Name, err)
		}
		committed := false
		for _, statement := range statements {
			if _, err = tx.ExecContext(ctx, statement); err != nil {
				break
			}
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO schema_migrations (version, name, checksum)
				VALUES ($1, $2, $3)
			`, info.Version, info.Name, info.Checksum)
		}
		if err == nil {
			err = tx.Commit()
			committed = err == nil
		}
		if !committed {
			_ = tx.Rollback()
			return nil, fmt.Errorf("数据库迁移 %d (%s) 失败并已回滚: %w", m.Version, m.Name, err)
		}
		applied = append(applied, info)
		known[m.Version] = info
		current = m.Version
	}
	return applied, nil
}

// migrationStatementsForExecution 保持历史迁移定义和 checksum 不变，同时兼容
// “先迁移到版本 15 删除图片表，再因故重放早期迁移”的维护场景。版本 2 的
// 图片约束在新库中已经没有目标表，题目版本回填也不能再读取已删除的 media_refs 列。
// 仅调整本次执行的语句列表，记录的迁移信息仍来自原始定义。
func migrationStatementsForExecution(ctx context.Context, conn *sql.Conn, m migration) ([]string, error) {
	if m.Version != 2 && m.Version != 14 && m.Version != 15 {
		return m.Statements, nil
	}
	imageTablesPresent := true
	if m.Version == 2 {
		var imageTable sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT to_regclass('image_prompts')::text`).Scan(&imageTable); err != nil {
			return nil, err
		}
		imageTablesPresent = imageTable.Valid && imageTable.String != ""
	}
	var mediaColumnPresent bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='questions' AND column_name='media_refs'
	)`).Scan(&mediaColumnPresent); err != nil {
		return nil, err
	}
	if imageTablesPresent && mediaColumnPresent {
		return m.Statements, nil
	}
	statements := make([]string, 0, len(m.Statements))
	for _, statement := range m.Statements {
		if !imageTablesPresent && (strings.Contains(statement, "image_prompts") ||
			strings.Contains(statement, "generated_images") ||
			strings.Contains(statement, "image_review_records")) {
			continue
		}
		if !mediaColumnPresent {
			statement = strings.ReplaceAll(statement, "'media_refs', q.media_refs,", "")
			statement = strings.ReplaceAll(statement, "media_refs::text,", "")
		}
		statements = append(statements, statement)
	}
	return statements, nil
}

// GetMigrationStatus 只读取迁移状态，不创建或修改任何数据库对象。
func (s *Store) GetMigrationStatus(ctx context.Context) (MigrationStatus, error) {
	migrations := configuredMigrations(baselineSchemaSQL)
	status := MigrationStatus{LatestVersion: LatestSchemaVersion}

	var tableName sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT to_regclass('schema_migrations')::text`).Scan(&tableName); err != nil {
		return status, fmt.Errorf("读取数据库迁移状态失败: %w", err)
	}
	if !tableName.Valid || tableName.String == "" {
		for _, m := range migrations {
			status.Pending = append(status.Pending, migrationInfo(m))
		}
		return status, nil
	}
	status.Initialized = true

	applied, current, err := readAndValidateAppliedMigrations(ctx, s.db, migrations)
	if err != nil {
		return status, err
	}
	status.CurrentVersion = current
	for _, m := range migrations {
		if _, ok := applied[m.Version]; !ok {
			status.Pending = append(status.Pending, migrationInfo(m))
		}
	}
	return status, nil
}

// CheckSchemaVersion 保证应用不会在未迁移、过旧、过新或校验和异常的库上运行。
func (s *Store) CheckSchemaVersion(ctx context.Context) error {
	status, err := s.GetMigrationStatus(ctx)
	if err != nil {
		return err
	}
	if !status.Initialized || status.CurrentVersion != status.LatestVersion || len(status.Pending) != 0 {
		return fmt.Errorf("数据库 Schema 版本未就绪: 当前 %d，程序要求 %d", status.CurrentVersion, status.LatestVersion)
	}
	return nil
}

func validateMigrationDefinitions(migrations []migration) error {
	if len(migrations) == 0 {
		return fmt.Errorf("未配置数据库迁移")
	}
	for i, m := range migrations {
		expected := int64(i + 1)
		if m.Version != expected {
			return fmt.Errorf("数据库迁移定义必须从 1 连续递增: 期望 %d，实际 %d", expected, m.Version)
		}
		if strings.TrimSpace(m.Name) == "" || len(m.Statements) == 0 {
			return fmt.Errorf("数据库迁移 %d 缺少名称或 SQL", m.Version)
		}
	}
	if migrations[len(migrations)-1].Version != LatestSchemaVersion {
		return fmt.Errorf("最新数据库迁移版本 %d 与程序声明 %d 不一致", migrations[len(migrations)-1].Version, LatestSchemaVersion)
	}
	return nil
}

func readAndValidateAppliedMigrations(ctx context.Context, queryer rowsQuerier, migrations []migration) (map[int64]MigrationInfo, int64, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, 0, fmt.Errorf("读取数据库迁移记录失败: %w", err)
	}
	defer rows.Close()

	expected := make(map[int64]MigrationInfo, len(migrations))
	for _, m := range migrations {
		expected[m.Version] = migrationInfo(m)
	}
	applied := make(map[int64]MigrationInfo, len(migrations))
	var current int64
	for rows.Next() {
		var actual MigrationInfo
		if err := rows.Scan(&actual.Version, &actual.Name, &actual.Checksum); err != nil {
			return nil, 0, fmt.Errorf("解析数据库迁移记录失败: %w", err)
		}
		want, ok := expected[actual.Version]
		if !ok {
			return nil, 0, fmt.Errorf("数据库版本 %d 高于或不属于当前程序，拒绝继续", actual.Version)
		}
		_, historicalChecksumAccepted := historicalMigrationChecksums[actual.Version][actual.Checksum]
		if actual.Name != want.Name || (actual.Checksum != want.Checksum && !historicalChecksumAccepted) {
			return nil, 0, fmt.Errorf("数据库迁移 %d 的名称或校验和与程序不一致，拒绝继续", actual.Version)
		}
		if actual.Version != current+1 {
			return nil, 0, fmt.Errorf("数据库迁移记录不连续: 版本 %d 之后出现版本 %d", current, actual.Version)
		}
		applied[actual.Version] = actual
		current = actual.Version
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("读取数据库迁移记录失败: %w", err)
	}
	return applied, current, nil
}

func legacyCompatibilityStatements() []string {
	return []string{
		`ALTER TABLE ai_review_results ADD COLUMN IF NOT EXISTS question_version INT NOT NULL DEFAULT 0`,
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS backend TEXT NOT NULL DEFAULT 'dashscope'`,
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS backend_profile TEXT NOT NULL DEFAULT 'dashscope-default'`,
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT ''`,
		`DO $$ BEGIN
			ALTER TABLE review_tasks DROP CONSTRAINT IF EXISTS review_tasks_question_id_fkey;
			ALTER TABLE review_tasks ADD CONSTRAINT review_tasks_question_id_fkey
				FOREIGN KEY (question_id) REFERENCES questions(id) ON DELETE CASCADE;
		END $$`,
		`DO $$ BEGIN
			ALTER TABLE review_records DROP CONSTRAINT IF EXISTS review_records_task_id_fkey;
			ALTER TABLE review_records ADD CONSTRAINT review_records_task_id_fkey
				FOREIGN KEY (task_id) REFERENCES review_tasks(id) ON DELETE CASCADE;
			ALTER TABLE review_records DROP CONSTRAINT IF EXISTS review_records_question_id_fkey;
			ALTER TABLE review_records ADD CONSTRAINT review_records_question_id_fkey
				FOREIGN KEY (question_id) REFERENCES questions(id) ON DELETE CASCADE;
		END $$`,
		`DO $$ BEGIN
			ALTER TABLE image_prompts DROP CONSTRAINT IF EXISTS image_prompts_question_id_fkey;
			ALTER TABLE image_prompts ADD CONSTRAINT image_prompts_question_id_fkey
				FOREIGN KEY (question_id) REFERENCES questions(id) ON DELETE CASCADE;
		END $$`,
		`DO $$ BEGIN
			ALTER TABLE generated_images DROP CONSTRAINT IF EXISTS generated_images_prompt_id_fkey;
			ALTER TABLE generated_images ADD CONSTRAINT generated_images_prompt_id_fkey
				FOREIGN KEY (prompt_id) REFERENCES image_prompts(id) ON DELETE CASCADE;
			ALTER TABLE generated_images DROP CONSTRAINT IF EXISTS generated_images_question_id_fkey;
			ALTER TABLE generated_images ADD CONSTRAINT generated_images_question_id_fkey
				FOREIGN KEY (question_id) REFERENCES questions(id) ON DELETE CASCADE;
		END $$`,
		`DO $$ BEGIN
			ALTER TABLE image_review_records DROP CONSTRAINT IF EXISTS image_review_records_image_id_fkey;
			ALTER TABLE image_review_records ADD CONSTRAINT image_review_records_image_id_fkey
				FOREIGN KEY (image_id) REFERENCES generated_images(id) ON DELETE CASCADE;
		END $$`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS bank_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_questions_bank ON questions(bank_id)`,
		`ALTER TABLE question_banks ADD COLUMN IF NOT EXISTS professions TEXT[] DEFAULT '{}'`,
		`DO $$ BEGIN
			CREATE TABLE IF NOT EXISTS question_bank_members (
				question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
				bank_id TEXT NOT NULL REFERENCES question_banks(id) ON DELETE CASCADE,
				PRIMARY KEY (question_id, bank_id)
			);
		END $$`,
		`INSERT INTO question_bank_members (question_id, bank_id)
			SELECT id, bank_id FROM questions WHERE bank_id != ''
			ON CONFLICT DO NOTHING`,
		`ALTER TABLE questions DROP COLUMN IF EXISTS bank_id`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS permissions TEXT[] DEFAULT '{}'`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS bank_ids TEXT[] DEFAULT '{}'`,
		`ALTER TABLE review_flows ADD COLUMN IF NOT EXISTS bank_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE review_flows ADD COLUMN IF NOT EXISTS final_reviewer_ids TEXT[] DEFAULT '{}'`,
		`ALTER TABLE review_flows ADD COLUMN IF NOT EXISTS vote_rule TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS final_reviewer_ids TEXT[] DEFAULT '{}'`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS final_decision JSONB DEFAULT 'null'`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS question_prev_status TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS question_versions (
			id TEXT PRIMARY KEY,
			question_id TEXT NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
			version INT NOT NULL,
			snapshot JSONB NOT NULL,
			actor TEXT NOT NULL DEFAULT 'system',
			change_type TEXT NOT NULL DEFAULT 'create',
			change_note TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (question_id, version)
		)`,
		`ALTER TABLE question_versions ADD COLUMN IF NOT EXISTS actor TEXT NOT NULL DEFAULT 'system'`,
		`ALTER TABLE question_versions ADD COLUMN IF NOT EXISTS change_type TEXT NOT NULL DEFAULT 'create'`,
		`ALTER TABLE question_versions ADD COLUMN IF NOT EXISTS change_note TEXT NOT NULL DEFAULT ''`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema=current_schema() AND table_name='question_versions'
					AND column_name='snapshot' AND data_type='text'
			) THEN
				ALTER TABLE question_versions ALTER COLUMN snapshot TYPE JSONB USING snapshot::jsonb;
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema=current_schema() AND table_name='question_versions' AND column_name='changed_by'
			) THEN
				UPDATE question_versions SET actor=COALESCE(NULLIF(changed_by,''),'migration')
				WHERE actor='' OR actor='system';
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid='question_versions'::regclass AND contype='u'
					AND conname='question_versions_question_id_version_key'
			) THEN
				ALTER TABLE question_versions ADD CONSTRAINT question_versions_question_id_version_key UNIQUE (question_id, version);
			END IF;
		END $$`,
		`DO $$ BEGIN
			ALTER TABLE question_versions DROP CONSTRAINT IF EXISTS question_versions_question_id_fkey;
			ALTER TABLE question_versions ADD CONSTRAINT question_versions_question_id_fkey
				FOREIGN KEY (question_id) REFERENCES questions(id) ON DELETE CASCADE;
		END $$`,
		`CREATE INDEX IF NOT EXISTS idx_question_versions_question ON question_versions(question_id, version DESC)`,
		`INSERT INTO question_versions (id, question_id, version, snapshot, actor, change_type, change_note, created_at)
			SELECT 'qv-'||q.id||'-'||GREATEST(q.version, 1), q.id, GREATEST(q.version, 1), jsonb_build_object(
				'id', q.id,
				'clinical_stem', q.clinical_stem,
				'options', q.options,
				'answer', q.answer,
				'explanation', q.explanation,
				'source_refs', q.source_refs,
				'knowledge_points', q.knowledge_points,
				'media_refs', q.media_refs,
				'difficulty', q.difficulty,
				'cognitive_level', q.cognitive_level,
				'exam_points', q.exam_points,
				'outline_code', q.outline_code,
				'profession', q.profession,
				'system', q.system_name,
				'bank_ids', COALESCE((SELECT jsonb_agg(m.bank_id ORDER BY m.bank_id) FROM question_bank_members m WHERE m.question_id=q.id), '[]'::jsonb),
				'status', q.status,
				'version', GREATEST(q.version, 1),
				'created_at', q.created_at,
				'updated_at', q.updated_at
			), 'migration', 'backfill', '上线版本快照时补录当前内容', q.updated_at
			FROM questions q
			ON CONFLICT (question_id, version) DO NOTHING`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS question_version INT NOT NULL DEFAULT 0`,
		`UPDATE review_tasks rt SET question_version=q.version
			FROM questions q WHERE rt.question_id=q.id AND rt.question_version=0`,
		`ALTER TABLE review_tasks ALTER COLUMN question_version SET DEFAULT 1`,
		`UPDATE questions SET status='published' WHERE status='approved'`,
		`UPDATE review_tasks SET status='published' WHERE status='approved'`,
		`UPDATE question_versions
			SET snapshot=jsonb_set(snapshot, '{status}', '"published"'::jsonb, true)
			WHERE snapshot->>'status'='approved'`,
		`DO $$ BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid='questions'::regclass AND conname='questions_no_legacy_approved'
			) THEN
				ALTER TABLE questions ADD CONSTRAINT questions_no_legacy_approved CHECK (status <> 'approved');
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid='review_tasks'::regclass AND conname='review_tasks_no_legacy_approved'
			) THEN
				ALTER TABLE review_tasks ADD CONSTRAINT review_tasks_no_legacy_approved CHECK (status <> 'approved');
			END IF;
		END $$`,
	}
}
