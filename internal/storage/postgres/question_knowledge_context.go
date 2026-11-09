package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func questionKnowledgeContextMigrationStatements() []string {
	return []string{
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS generation_workflow_version TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS question_generation_context (question_id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,workflow_version TEXT NOT NULL,body JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),CHECK(body->>'workflow_version'=workflow_version))`,
		`CREATE INDEX IF NOT EXISTS question_generation_context_owner ON question_generation_context(owner_id,created_at DESC)`,
	}
}
func (s *Store) saveQuestionKnowledgeContext(ctx context.Context, executor sqlExecer, q domain.A2Question) error {
	frozen := q.GenerationContext
	if !knowledgebase.SupportsWorkflowVersion(frozen.WorkflowVersion) {
		return fmt.Errorf("未知知识工作流版本")
	}
	if e := knowledgebase.ValidateInitialQuestionContext(&q, frozen); e != nil {
		return e
	}
	raw, e := json.Marshal(frozen)
	if e != nil {
		return e
	}
	_, e = executor.ExecContext(ctx, `INSERT INTO question_generation_context(question_id,owner_id,workflow_version,body) VALUES($1,$2,$3,$4)`, q.ID, q.OwnerID, frozen.WorkflowVersion, raw)
	return e
}
func (s *Store) GetQuestionGenerationContext(ctx context.Context, id string) (*domain.QuestionGenerationContext, error) {
	var raw []byte
	e := s.queryRowContext(ctx, `SELECT body FROM question_generation_context WHERE question_id=$1`, id).Scan(&raw)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var frozen domain.QuestionGenerationContext
	e = json.Unmarshal(raw, &frozen)
	return &frozen, e
}
