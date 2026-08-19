package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"

	"github.com/lib/pq"
)

// Store PostgreSQL 存储实现。
type Store struct {
	db *sql.DB
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

// InitSchema 初始化数据库表结构。
// 先执行建表语句（CREATE TABLE IF NOT EXISTS），再执行幂等迁移，
// 保证已有部署的旧库也能补充新增列和约束。
func (s *Store) InitSchema(schemaSQL string) error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return err
	}
	return s.runMigrations()
}

// runMigrations 幂等迁移：对已存在的旧表补充新增列/约束。
// 每条迁移都必须可重复执行（IF NOT EXISTS / IF EXISTS 判断）。
func (s *Store) runMigrations() error {
	migrations := []string{
		// ai_review_results 增加 question_version 列（AI 检查结果版本绑定）
		`ALTER TABLE ai_review_results ADD COLUMN IF NOT EXISTS question_version INT NOT NULL DEFAULT 0`,
		// 删除题目时级联清理关联数据
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
		// 题库分库
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS bank_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_questions_bank ON questions(bank_id)`,
		`ALTER TABLE question_banks ADD COLUMN IF NOT EXISTS professions TEXT[] DEFAULT '{}'`,
		// 题目-题库多对多（一道题可属于多个题库）：
		// 先把旧单库归属迁入成员表，再删除旧列
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
		// 用户权限体系
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS permissions TEXT[] DEFAULT '{}'`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS bank_ids TEXT[] DEFAULT '{}'`,
		// 审核流程：适用题库 + 最终把关人
		`ALTER TABLE review_flows ADD COLUMN IF NOT EXISTS bank_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE review_flows ADD COLUMN IF NOT EXISTS final_reviewer_ids TEXT[] DEFAULT '{}'`,
		`ALTER TABLE review_flows ADD COLUMN IF NOT EXISTS vote_rule TEXT NOT NULL DEFAULT ''`,
		// 审核任务：最终把关人快照
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS final_reviewer_ids TEXT[] DEFAULT '{}'`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS final_decision JSONB DEFAULT 'null'`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS question_prev_status TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return fmt.Errorf("数据库迁移失败: %w\nSQL: %s", err, m)
		}
	}
	return nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	return s.db.Close()
}

// DB 返回底层数据库连接。
func (s *Store) DB() *sql.DB {
	return s.db
}

// ===== 知识点 =====

func (s *Store) SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO knowledge_points (id, category, subject, unit, sub_item, topic, outline_code, outline_ref, keywords)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			category=EXCLUDED.category, subject=EXCLUDED.subject, unit=EXCLUDED.unit,
			sub_item=EXCLUDED.sub_item, topic=EXCLUDED.topic, outline_code=EXCLUDED.outline_code,
			outline_ref=EXCLUDED.outline_ref, keywords=EXCLUDED.keywords
	`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	count := 0
	for _, p := range points {
		_, err := stmt.ExecContext(ctx, p.ID, p.Category, p.Subject, p.Unit, p.SubItem, p.Topic, p.OutlineCode, p.OutlineCode, pqArray(p.Keywords))
		if err != nil {
			return count, err
		}
		count++
	}
	return count, tx.Commit()
}

func (s *Store) GetPoint(ctx context.Context, id string) (*domain.KnowledgePoint, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, category, subject, unit, sub_item, topic, outline_code, keywords FROM knowledge_points WHERE id=$1`, id)
	return scanPoint(row)
}

func (s *Store) ListPoints(ctx context.Context) ([]domain.KnowledgePoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, category, subject, unit, sub_item, topic, outline_code, keywords FROM knowledge_points ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoints(rows)
}

func (s *Store) SearchPoints(ctx context.Context, keyword string) ([]domain.KnowledgePoint, error) {
	kw := "%" + strings.ToLower(keyword) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, category, subject, unit, sub_item, topic, outline_code, keywords
		FROM knowledge_points
		WHERE LOWER(topic) LIKE $1 OR LOWER(unit) LIKE $1 OR LOWER(sub_item) LIKE $1
		   OR LOWER(outline_code) LIKE $1 OR LOWER(subject) LIKE $1
		ORDER BY id
	`, kw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoints(rows)
}

func (s *Store) ListBySubject(ctx context.Context, subject string) ([]domain.KnowledgePoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, category, subject, unit, sub_item, topic, outline_code, keywords FROM knowledge_points WHERE subject=$1 ORDER BY id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoints(rows)
}

func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM questions`).Scan(&n)
	return n, err
}

// KPCount 知识点数量。
func (s *Store) KPCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_points`).Scan(&n)
	return n, err
}

func (s *Store) DeletePoint(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM knowledge_points WHERE id=$1`, id)
	return err
}

// ===== 题目 =====

func (s *Store) SaveQuestion(ctx context.Context, q domain.A2Question) error {
	opts, _ := json.Marshal(q.Options)
	refs, _ := json.Marshal(q.SourceRefs)
	kps, _ := json.Marshal(q.KnowledgePoints)
	media, _ := json.Marshal(q.MediaRefs)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO questions (id, clinical_stem, options, answer, explanation, source_refs, knowledge_points, media_refs, difficulty, cognitive_level, exam_points, outline_code, profession, system_name, status, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (id) DO UPDATE SET
			clinical_stem=EXCLUDED.clinical_stem, options=EXCLUDED.options, answer=EXCLUDED.answer,
			explanation=EXCLUDED.explanation, source_refs=EXCLUDED.source_refs,
			knowledge_points=EXCLUDED.knowledge_points, media_refs=EXCLUDED.media_refs,
			difficulty=EXCLUDED.difficulty, cognitive_level=EXCLUDED.cognitive_level,
			exam_points=EXCLUDED.exam_points, outline_code=EXCLUDED.outline_code,
			profession=EXCLUDED.profession, system_name=EXCLUDED.system_name,
			status=EXCLUDED.status, version=EXCLUDED.version, updated_at=EXCLUDED.updated_at
	`, q.ID, q.ClinicalStem, opts, q.Answer, q.Explanation, refs, kps, media,
		q.Difficulty, q.CognitiveLevel, q.ExamPoints, q.OutlineCode, q.Profession, q.System,
		q.Status, q.Version, q.CreatedAt, q.UpdatedAt)
	if err != nil {
		return err
	}
	// 更新题库归属（多对多）
	if err := s.replaceBankMembers(ctx, tx, q.ID, q.BankIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// replaceBankMembers 重建题目-题库成员关系。
func (s *Store) replaceBankMembers(ctx context.Context, tx *sql.Tx, questionID string, bankIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM question_bank_members WHERE question_id=$1`, questionID); err != nil {
		return err
	}
	for _, b := range bankIDs {
		if b == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO question_bank_members (question_id, bank_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, questionID, b); err != nil {
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
		opts, _ := json.Marshal(q.Options)
		refs, _ := json.Marshal(q.SourceRefs)
		kps, _ := json.Marshal(q.KnowledgePoints)
		media, _ := json.Marshal(q.MediaRefs)

		_, err := tx.ExecContext(ctx, `
			INSERT INTO questions (id, clinical_stem, options, answer, explanation, source_refs, knowledge_points, media_refs, difficulty, cognitive_level, exam_points, outline_code, profession, system_name, status, version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT (id) DO UPDATE SET
				clinical_stem=EXCLUDED.clinical_stem, options=EXCLUDED.options, answer=EXCLUDED.answer,
				explanation=EXCLUDED.explanation,
				status=EXCLUDED.status, updated_at=EXCLUDED.updated_at
		`, q.ID, q.ClinicalStem, opts, q.Answer, q.Explanation, refs, kps, media,
			q.Difficulty, q.CognitiveLevel, q.ExamPoints, q.OutlineCode, q.Profession, q.System,
			q.Status, q.Version, q.CreatedAt, q.UpdatedAt)
		if err != nil {
			return count, err
		}
		if err := s.replaceBankMembers(ctx, tx, q.ID, q.BankIDs); err != nil {
			return count, err
		}
		count++
	}
	return count, tx.Commit()
}

func (s *Store) GetQuestion(ctx context.Context, id string) (*domain.A2Question, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT q.id, q.clinical_stem, q.options, q.answer, q.explanation, q.source_refs, q.knowledge_points, q.media_refs, q.difficulty, q.cognitive_level, q.exam_points, q.outline_code, q.profession, q.system_name,
			COALESCE(ARRAY(SELECT m.bank_id FROM question_bank_members m WHERE m.question_id = q.id ORDER BY m.bank_id), '{}') AS bank_ids,
			q.status, q.version, q.created_at, q.updated_at
		FROM questions q WHERE q.id=$1
	`, id)
	return scanQuestion(row)
}

func (s *Store) DeleteQuestion(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM questions WHERE id=$1`, id)
	return err
}

func (s *Store) ListQuestions(ctx context.Context) ([]domain.A2Question, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT q.id, q.clinical_stem, q.options, q.answer, q.explanation, q.source_refs, q.knowledge_points, q.media_refs, q.difficulty, q.cognitive_level, q.exam_points, q.outline_code, q.profession, q.system_name,
			COALESCE(ARRAY(SELECT m.bank_id FROM question_bank_members m WHERE m.question_id = q.id ORDER BY m.bank_id), '{}') AS bank_ids,
			q.status, q.version, q.created_at, q.updated_at
		FROM questions q ORDER BY q.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanQuestions(rows)
}

// ===== 专家 =====

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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_flows (id, name, description, subject, bank_id, final_reviewer_ids, vote_rule, rounds, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, description=EXCLUDED.description, subject=EXCLUDED.subject,
			bank_id=EXCLUDED.bank_id, final_reviewer_ids=EXCLUDED.final_reviewer_ids,
			vote_rule=EXCLUDED.vote_rule, rounds=EXCLUDED.rounds
	`, f.ID, f.Name, f.Description, f.Subject, f.BankID, pqArray(f.FinalReviewerIDs), f.VoteRule, rounds, f.CreatedAt)
	return err
}

func (s *Store) GetFlowConfig(ctx context.Context, id string) (*domain.ReviewFlowConfig, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, subject, bank_id, final_reviewer_ids, vote_rule, rounds, created_at FROM review_flows WHERE id=$1`, id)
	return scanFlow(row)
}

func (s *Store) ListFlowConfigs(ctx context.Context) ([]domain.ReviewFlowConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, subject, bank_id, final_reviewer_ids, vote_rule, rounds, created_at FROM review_flows ORDER BY created_at`)
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM review_flows WHERE id=$1`, id)
	return err
}

// ===== 审核任务 =====

func (s *Store) SaveTask(ctx context.Context, t domain.ReviewTask) error {
	results, _ := json.Marshal(t.RoundResults)
	var finalDecision []byte
	if t.FinalDecision != nil {
		finalDecision, _ = json.Marshal(t.FinalDecision)
	} else {
		finalDecision = []byte("null")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_tasks (id, question_id, flow_id, current_round, status, assigned_to, final_reviewer_ids, final_decision, question_prev_status, round_results, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
			current_round=EXCLUDED.current_round, status=EXCLUDED.status,
			assigned_to=EXCLUDED.assigned_to, final_reviewer_ids=EXCLUDED.final_reviewer_ids,
			final_decision=EXCLUDED.final_decision,
			question_prev_status=EXCLUDED.question_prev_status,
			round_results=EXCLUDED.round_results, updated_at=EXCLUDED.updated_at
	`, t.ID, t.QuestionID, t.FlowID, t.CurrentRound, t.Status, pqArray(t.AssignedTo), pqArray(t.FinalReviewerIDs), finalDecision, t.QuestionPrevStatus, results, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) GetTask(ctx context.Context, id string) (*domain.ReviewTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, question_id, flow_id, current_round, status, assigned_to, final_reviewer_ids, final_decision, question_prev_status, round_results, created_at, updated_at FROM review_tasks WHERE id=$1`, id)
	return scanTask(row)
}

func (s *Store) GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, question_id, flow_id, current_round, status, assigned_to, final_reviewer_ids, final_decision, question_prev_status, round_results, created_at, updated_at FROM review_tasks WHERE question_id=$1 ORDER BY created_at DESC LIMIT 1`, questionID)
	return scanTask(row)
}

func (s *Store) UpdateTask(ctx context.Context, t domain.ReviewTask) error {
	return s.SaveTask(ctx, t)
}

// DeleteTask 删除审核任务（级联删除其审核记录）。
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM review_tasks WHERE id=$1`, id)
	return err
}

func (s *Store) CountActiveTasksByFlow(ctx context.Context, flowID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM review_tasks WHERE flow_id=$1 AND status IN ('reviewing', 'revision_required')
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

func (s *Store) SaveRecord(ctx context.Context, r domain.ReviewRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_records (id, task_id, question_id, round_number, expert_id, conclusion, opinion, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`, r.ID, r.TaskID, r.QuestionID, r.RoundNumber, r.ExpertID, r.Conclusion, r.Opinion, r.CreatedAt)
	return err
}

func (s *Store) ListRecordsByTaskID(ctx context.Context, taskID string) ([]domain.ReviewRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, question_id, round_number, expert_id, conclusion, opinion, created_at FROM review_records WHERE task_id=$1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewRecord
	for rows.Next() {
		var r domain.ReviewRecord
		if err := rows.Scan(&r.ID, &r.TaskID, &r.QuestionID, &r.RoundNumber, &r.ExpertID, &r.Conclusion, &r.Opinion, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ListAllTasks 列出全部审核任务（含历史，审核结果汇总用）。
func (s *Store) ListAllTasks(ctx context.Context) ([]domain.ReviewTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, question_id, flow_id, current_round, status, assigned_to, final_reviewer_ids, final_decision, question_prev_status, round_results, created_at, updated_at FROM review_tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewTask
	for rows.Next() {
		var t domain.ReviewTask
		var assignedTo, finalReviewerIDs []string
		var resultsJSON, finalDecisionJSON []byte
		if err := rows.Scan(&t.ID, &t.QuestionID, &t.FlowID, &t.CurrentRound, &t.Status, pqArrayScanner(&assignedTo), pqArrayScanner(&finalReviewerIDs), &finalDecisionJSON, &t.QuestionPrevStatus, &resultsJSON, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		t.AssignedTo = assignedTo
		t.FinalReviewerIDs = finalReviewerIDs
		json.Unmarshal(resultsJSON, &t.RoundResults)
		if len(finalDecisionJSON) > 0 && string(finalDecisionJSON) != "null" {
			var fd domain.ExpertReview
			if json.Unmarshal(finalDecisionJSON, &fd) == nil {
				t.FinalDecision = &fd
			}
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// ListAllRecords 列出全部审核记录（审核结果汇总用）。
func (s *Store) ListAllRecords(ctx context.Context) ([]domain.ReviewRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, question_id, round_number, expert_id, conclusion, opinion, created_at FROM review_records ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewRecord
	for rows.Next() {
		var r domain.ReviewRecord
		if err := rows.Scan(&r.ID, &r.TaskID, &r.QuestionID, &r.RoundNumber, &r.ExpertID, &r.Conclusion, &r.Opinion, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// ===== 操作日志 =====

func (s *Store) SaveLog(ctx context.Context, log domain.AuditLog) error {
	_, err := s.db.ExecContext(ctx, `
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

// ===== 图片提示词 =====

func (s *Store) SavePrompt(ctx context.Context, p domain.ImagePrompt) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO image_prompts (id, question_id, purpose, image_type, subject, must_include, must_exclude, style, knowledge_point, review_focus, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE SET
			purpose=EXCLUDED.purpose, image_type=EXCLUDED.image_type, subject=EXCLUDED.subject,
			must_include=EXCLUDED.must_include, must_exclude=EXCLUDED.must_exclude,
			style=EXCLUDED.style, knowledge_point=EXCLUDED.knowledge_point, review_focus=EXCLUDED.review_focus
	`, p.ID, p.QuestionID, p.Purpose, p.ImageType, p.Subject, pqArray(p.MustInclude), pqArray(p.MustExclude), p.Style, p.KnowledgePoint, p.ReviewFocus, p.CreatedAt)
	return err
}

func (s *Store) GetPrompt(ctx context.Context, id string) (*domain.ImagePrompt, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, question_id, purpose, image_type, subject, must_include, must_exclude, style, knowledge_point, review_focus, created_at FROM image_prompts WHERE id=$1`, id)
	return scanImagePrompt(row)
}

func (s *Store) GetPromptByQuestionID(ctx context.Context, questionID string) (*domain.ImagePrompt, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, question_id, purpose, image_type, subject, must_include, must_exclude, style, knowledge_point, review_focus, created_at FROM image_prompts WHERE question_id=$1 ORDER BY created_at DESC LIMIT 1`, questionID)
	return scanImagePrompt(row)
}

// ===== 候选图 =====

func (s *Store) SaveImage(ctx context.Context, img domain.GeneratedImage) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO generated_images (id, prompt_id, question_id, image_path, model_name, model_version, status, review_note, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			status=EXCLUDED.status, review_note=EXCLUDED.review_note
	`, img.ID, img.PromptID, img.QuestionID, img.ImagePath, img.ModelName, img.ModelVersion, img.Status, img.ReviewNote, img.CreatedAt)
	return err
}

func (s *Store) GetImage(ctx context.Context, id string) (*domain.GeneratedImage, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, prompt_id, question_id, image_path, model_name, model_version, status, review_note, created_at FROM generated_images WHERE id=$1`, id)
	return scanImage(row)
}

func (s *Store) ListImagesByQuestionID(ctx context.Context, questionID string) ([]domain.GeneratedImage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, prompt_id, question_id, image_path, model_name, model_version, status, review_note, created_at FROM generated_images WHERE question_id=$1 ORDER BY created_at`, questionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanImages(rows)
}

func (s *Store) ListImagesByPromptID(ctx context.Context, promptID string) ([]domain.GeneratedImage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, prompt_id, question_id, image_path, model_name, model_version, status, review_note, created_at FROM generated_images WHERE prompt_id=$1 ORDER BY created_at`, promptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanImages(rows)
}

func (s *Store) UpdateImageStatus(ctx context.Context, id string, status domain.ImageStatus, note string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE generated_images SET status=$1, review_note=$2 WHERE id=$3`, status, note, id)
	return err
}

// ===== 图片审核记录 =====

func (s *Store) SaveReviewRecord(ctx context.Context, r domain.ImageReviewRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO image_review_records (id, image_id, expert_id, conclusion, opinion, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, r.ID, r.ImageID, r.ExpertID, r.Conclusion, r.Opinion, r.CreatedAt)
	return err
}

func (s *Store) ListReviewRecordsByImageID(ctx context.Context, imageID string) ([]domain.ImageReviewRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, image_id, expert_id, conclusion, opinion, created_at FROM image_review_records WHERE image_id=$1 ORDER BY created_at`, imageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ImageReviewRecord
	for rows.Next() {
		var r domain.ImageReviewRecord
		if err := rows.Scan(&r.ID, &r.ImageID, &r.ExpertID, &r.Conclusion, &r.Opinion, &r.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, r)
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

func scanPoint(row *sql.Row) (*domain.KnowledgePoint, error) {
	var p domain.KnowledgePoint
	var keywords []string
	err := row.Scan(&p.ID, &p.Category, &p.Subject, &p.Unit, &p.SubItem, &p.Topic, &p.OutlineCode, pqArrayScanner(&keywords))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Keywords = keywords
	return &p, nil
}

func scanPoints(rows *sql.Rows) ([]domain.KnowledgePoint, error) {
	var result []domain.KnowledgePoint
	for rows.Next() {
		var p domain.KnowledgePoint
		var keywords []string
		if err := rows.Scan(&p.ID, &p.Category, &p.Subject, &p.Unit, &p.SubItem, &p.Topic, &p.OutlineCode, pqArrayScanner(&keywords)); err != nil {
			return nil, err
		}
		p.Keywords = keywords
		result = append(result, p)
	}
	return result, rows.Err()
}

func scanQuestion(row *sql.Row) (*domain.A2Question, error) {
	var q domain.A2Question
	var optsJSON, refsJSON, kpsJSON, mediaJSON []byte
	var bankIDs []string
	err := row.Scan(&q.ID, &q.ClinicalStem, &optsJSON, &q.Answer, &q.Explanation, &refsJSON, &kpsJSON, &mediaJSON,
		&q.Difficulty, &q.CognitiveLevel, &q.ExamPoints, &q.OutlineCode, &q.Profession, &q.System,
		pqArrayScanner(&bankIDs), &q.Status, &q.Version, &q.CreatedAt, &q.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	q.BankIDs = bankIDs
	json.Unmarshal(optsJSON, &q.Options)
	json.Unmarshal(refsJSON, &q.SourceRefs)
	json.Unmarshal(kpsJSON, &q.KnowledgePoints)
	json.Unmarshal(mediaJSON, &q.MediaRefs)
	return &q, nil
}

func scanQuestions(rows *sql.Rows) ([]domain.A2Question, error) {
	var result []domain.A2Question
	for rows.Next() {
		var q domain.A2Question
		var optsJSON, refsJSON, kpsJSON, mediaJSON []byte
		var bankIDs []string
		if err := rows.Scan(&q.ID, &q.ClinicalStem, &optsJSON, &q.Answer, &q.Explanation, &refsJSON, &kpsJSON, &mediaJSON,
			&q.Difficulty, &q.CognitiveLevel, &q.ExamPoints, &q.OutlineCode, &q.Profession, &q.System,
			pqArrayScanner(&bankIDs), &q.Status, &q.Version, &q.CreatedAt, &q.UpdatedAt); err != nil {
			return nil, err
		}
		q.BankIDs = bankIDs
		json.Unmarshal(optsJSON, &q.Options)
		json.Unmarshal(refsJSON, &q.SourceRefs)
		json.Unmarshal(kpsJSON, &q.KnowledgePoints)
		json.Unmarshal(mediaJSON, &q.MediaRefs)
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
	err := row.Scan(&t.ID, &t.QuestionID, &t.FlowID, &t.CurrentRound, &t.Status, pqArrayScanner(&assignedTo), pqArrayScanner(&finalReviewerIDs), &finalDecisionJSON, &t.QuestionPrevStatus, &resultsJSON, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.AssignedTo = assignedTo
	t.FinalReviewerIDs = finalReviewerIDs
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
	err := row.Scan(&f.ID, &f.Name, &f.Description, &f.Subject, &f.BankID, pqArrayScanner(&finalReviewerIDs), &f.VoteRule, &roundsJSON, &f.CreatedAt)
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
	err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.Subject, &f.BankID, pqArrayScanner(&finalReviewerIDs), &f.VoteRule, &roundsJSON, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	f.FinalReviewerIDs = finalReviewerIDs
	json.Unmarshal(roundsJSON, &f.Rounds)
	return &f, nil
}

func scanImagePrompt(row *sql.Row) (*domain.ImagePrompt, error) {
	var p domain.ImagePrompt
	var include, exclude []string
	err := row.Scan(&p.ID, &p.QuestionID, &p.Purpose, &p.ImageType, &p.Subject, pqArrayScanner(&include), pqArrayScanner(&exclude), &p.Style, &p.KnowledgePoint, &p.ReviewFocus, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.MustInclude = include
	p.MustExclude = exclude
	return &p, nil
}

func scanImage(row *sql.Row) (*domain.GeneratedImage, error) {
	var img domain.GeneratedImage
	err := row.Scan(&img.ID, &img.PromptID, &img.QuestionID, &img.ImagePath, &img.ModelName, &img.ModelVersion, &img.Status, &img.ReviewNote, &img.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &img, nil
}

func scanImages(rows *sql.Rows) ([]domain.GeneratedImage, error) {
	var result []domain.GeneratedImage
	for rows.Next() {
		var img domain.GeneratedImage
		if err := rows.Scan(&img.ID, &img.PromptID, &img.QuestionID, &img.ImagePath, &img.ModelName, &img.ModelVersion, &img.Status, &img.ReviewNote, &img.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, img)
	}
	return result, rows.Err()
}

// ===== 批量任务 =====

func (s *Store) SaveBatchJob(ctx context.Context, job storage.BatchJobRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO batch_jobs (id, job_name, status, total_count, completed, failed, output_file_id, points_json, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NOW(),NOW())
		ON CONFLICT (id) DO UPDATE SET
			job_name=EXCLUDED.job_name, status=EXCLUDED.status,
			total_count=EXCLUDED.total_count, completed=EXCLUDED.completed, failed=EXCLUDED.failed,
			output_file_id=EXCLUDED.output_file_id, updated_at=NOW()
	`, job.ID, job.JobName, job.Status, job.TotalCount, job.Completed, job.Failed, job.OutputFileID, job.PointsJSON)
	return err
}

func (s *Store) UpdateBatchJob(ctx context.Context, job storage.BatchJobRecord) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batch_jobs SET status=$1, total_count=$2, completed=$3, failed=$4, output_file_id=$5, updated_at=NOW()
		WHERE id=$6
	`, job.Status, job.TotalCount, job.Completed, job.Failed, job.OutputFileID, job.ID)
	return err
}

func (s *Store) GetBatchJob(ctx context.Context, id string) (*storage.BatchJobRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, job_name, status, total_count, completed, failed, output_file_id, points_json, created_at, updated_at FROM batch_jobs WHERE id=$1`, id)
	var j storage.BatchJobRecord
	err := row.Scan(&j.ID, &j.JobName, &j.Status, &j.TotalCount, &j.Completed, &j.Failed, &j.OutputFileID, &j.PointsJSON, &j.CreatedAt, &j.UpdatedAt)
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
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_name, status, total_count, completed, failed, output_file_id, points_json, created_at, updated_at FROM batch_jobs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storage.BatchJobRecord
	for rows.Next() {
		var j storage.BatchJobRecord
		if err := rows.Scan(&j.ID, &j.JobName, &j.Status, &j.TotalCount, &j.Completed, &j.Failed, &j.OutputFileID, &j.PointsJSON, &j.CreatedAt, &j.UpdatedAt); err != nil {
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
	kw := "%" + strings.ToLower(name) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT id, job_name, status, total_count, completed, failed, output_file_id, points_json, created_at, updated_at FROM batch_jobs WHERE LOWER(job_name) LIKE $1 ORDER BY created_at DESC LIMIT $2`, kw, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []storage.BatchJobRecord
	for rows.Next() {
		var j storage.BatchJobRecord
		if err := rows.Scan(&j.ID, &j.JobName, &j.Status, &j.TotalCount, &j.Completed, &j.Failed, &j.OutputFileID, &j.PointsJSON, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, j)
	}
	return result, rows.Err()
}

// ===== AI 检查结果 =====

func (s *Store) SaveReviewResult(ctx context.Context, result domain.AIReviewResult) error {
	scoresJSON, _ := json.Marshal(result.Scores)
	issuesJSON, _ := json.Marshal(result.Issues)
	_, err := s.db.ExecContext(ctx, `
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
	_, err := s.db.ExecContext(ctx, `
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
	_, err := s.db.ExecContext(ctx, `DELETE FROM question_banks WHERE id=$1`, id)
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
