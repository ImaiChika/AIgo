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
func (s *Store) InitSchema(schemaSQL string) error {
	_, err := s.db.Exec(schemaSQL)
	return err
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

	_, err := s.db.ExecContext(ctx, `
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
	return err
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
				explanation=EXCLUDED.explanation, status=EXCLUDED.status, updated_at=EXCLUDED.updated_at
		`, q.ID, q.ClinicalStem, opts, q.Answer, q.Explanation, refs, kps, media,
			q.Difficulty, q.CognitiveLevel, q.ExamPoints, q.OutlineCode, q.Profession, q.System,
			q.Status, q.Version, q.CreatedAt, q.UpdatedAt)
		if err != nil {
			return count, err
		}
		count++
	}
	return count, tx.Commit()
}

func (s *Store) GetQuestion(ctx context.Context, id string) (*domain.A2Question, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, clinical_stem, options, answer, explanation, source_refs, knowledge_points, media_refs, difficulty, cognitive_level, exam_points, outline_code, profession, system_name, status, version, created_at, updated_at
		FROM questions WHERE id=$1
	`, id)
	return scanQuestion(row)
}

func (s *Store) DeleteQuestion(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM questions WHERE id=$1`, id)
	return err
}

func (s *Store) ListQuestions(ctx context.Context) ([]domain.A2Question, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, clinical_stem, options, answer, explanation, source_refs, knowledge_points, media_refs, difficulty, cognitive_level, exam_points, outline_code, profession, system_name, status, version, created_at, updated_at
		FROM questions ORDER BY created_at DESC
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
		INSERT INTO review_flows (id, name, description, subject, rounds, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (id) DO UPDATE SET
			name=EXCLUDED.name, description=EXCLUDED.description, subject=EXCLUDED.subject, rounds=EXCLUDED.rounds
	`, f.ID, f.Name, f.Description, f.Subject, rounds, f.CreatedAt)
	return err
}

func (s *Store) GetFlowConfig(ctx context.Context, id string) (*domain.ReviewFlowConfig, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, description, subject, rounds, created_at FROM review_flows WHERE id=$1`, id)
	var f domain.ReviewFlowConfig
	var roundsJSON []byte
	err := row.Scan(&f.ID, &f.Name, &f.Description, &f.Subject, &roundsJSON, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(roundsJSON, &f.Rounds)
	return &f, nil
}

func (s *Store) ListFlowConfigs(ctx context.Context) ([]domain.ReviewFlowConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, description, subject, rounds, created_at FROM review_flows ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.ReviewFlowConfig
	for rows.Next() {
		var f domain.ReviewFlowConfig
		var roundsJSON []byte
		if err := rows.Scan(&f.ID, &f.Name, &f.Description, &f.Subject, &roundsJSON, &f.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(roundsJSON, &f.Rounds)
		result = append(result, f)
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_tasks (id, question_id, flow_id, current_round, status, assigned_to, round_results, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			current_round=EXCLUDED.current_round, status=EXCLUDED.status,
			assigned_to=EXCLUDED.assigned_to, round_results=EXCLUDED.round_results, updated_at=EXCLUDED.updated_at
	`, t.ID, t.QuestionID, t.FlowID, t.CurrentRound, t.Status, pqArray(t.AssignedTo), results, t.CreatedAt, t.UpdatedAt)
	return err
}

func (s *Store) GetTask(ctx context.Context, id string) (*domain.ReviewTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, question_id, flow_id, current_round, status, assigned_to, round_results, created_at, updated_at FROM review_tasks WHERE id=$1`, id)
	return scanTask(row)
}

func (s *Store) GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, question_id, flow_id, current_round, status, assigned_to, round_results, created_at, updated_at FROM review_tasks WHERE question_id=$1 ORDER BY created_at DESC LIMIT 1`, questionID)
	return scanTask(row)
}

func (s *Store) UpdateTask(ctx context.Context, t domain.ReviewTask) error {
	return s.SaveTask(ctx, t)
}

func (s *Store) CountActiveTasksByFlow(ctx context.Context, flowID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM review_tasks WHERE flow_id=$1 AND status IN ('reviewing', 'revision_required')
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
	err := row.Scan(&q.ID, &q.ClinicalStem, &optsJSON, &q.Answer, &q.Explanation, &refsJSON, &kpsJSON, &mediaJSON,
		&q.Difficulty, &q.CognitiveLevel, &q.ExamPoints, &q.OutlineCode, &q.Profession, &q.System,
		&q.Status, &q.Version, &q.CreatedAt, &q.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
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
		if err := rows.Scan(&q.ID, &q.ClinicalStem, &optsJSON, &q.Answer, &q.Explanation, &refsJSON, &kpsJSON, &mediaJSON,
			&q.Difficulty, &q.CognitiveLevel, &q.ExamPoints, &q.OutlineCode, &q.Profession, &q.System,
			&q.Status, &q.Version, &q.CreatedAt, &q.UpdatedAt); err != nil {
			return nil, err
		}
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
	var assignedTo []string
	var resultsJSON []byte
	err := row.Scan(&t.ID, &t.QuestionID, &t.FlowID, &t.CurrentRound, &t.Status, pqArrayScanner(&assignedTo), &resultsJSON, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.AssignedTo = assignedTo
	json.Unmarshal(resultsJSON, &t.RoundResults)
	return &t, nil
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
		INSERT INTO ai_review_results (id, question_id, verdict, scores, issues, suggestion, model, raw_response, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NOW())
	`, result.ID, result.QuestionID, result.Verdict, string(scoresJSON), string(issuesJSON),
		result.Suggestion, result.Model, result.RawResponse)
	return err
}

func (s *Store) GetLatestByQuestionID(ctx context.Context, questionID string) (*domain.AIReviewResult, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, question_id, verdict, scores, issues, suggestion, model, raw_response, created_at
		FROM ai_review_results WHERE question_id=$1 ORDER BY created_at DESC LIMIT 1
	`, questionID)
	var r domain.AIReviewResult
	var scoresJSON, issuesJSON string
	err := row.Scan(&r.ID, &r.QuestionID, &r.Verdict, &scoresJSON, &issuesJSON,
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
	query := `SELECT DISTINCT ON (question_id) id, question_id, verdict, scores, issues, suggestion, model, raw_response, created_at
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
		if err := rows.Scan(&r.ID, &r.QuestionID, &r.Verdict, &scoresJSON, &issuesJSON,
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
		SELECT id, question_id, verdict, scores, issues, suggestion, model, raw_response, created_at
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
		if err := rows.Scan(&r.ID, &r.QuestionID, &r.Verdict, &scoresJSON, &issuesJSON,
			&r.Suggestion, &r.Model, &r.RawResponse, &r.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(scoresJSON), &r.Scores)
		json.Unmarshal([]byte(issuesJSON), &r.Issues)
		results = append(results, r)
	}
	return results, rows.Err()
}

