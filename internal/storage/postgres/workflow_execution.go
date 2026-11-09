package postgres

import (
	"aigo/internal/workflowtrace"
	"context"
	"encoding/json"
	"fmt"
)

func workflowExecutionMigrationStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS workflow_execution_records (
 id TEXT PRIMARY KEY,operation TEXT NOT NULL,phase TEXT NOT NULL CHECK(phase IN ('submission','generation')),
 attempt INTEGER NOT NULL CHECK(attempt>=0),started_at TIMESTAMPTZ NOT NULL,
 record JSONB NOT NULL CHECK(jsonb_typeof(record)='object'))`,
		`CREATE INDEX IF NOT EXISTS idx_workflow_execution_operation ON workflow_execution_records(operation,phase,attempt,started_at,id)`,
	}
}

// Independent, best-effort diagnostics must not join the business transaction.
func (s *Store) SaveWorkflowExecution(ctx context.Context, record workflowtrace.Record) error {
	if record.ID == "" || record.Operation == "" || record.StartedAt.IsZero() {
		return fmt.Errorf("invalid workflow evidence")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_execution_records(id,operation,phase,attempt,started_at,record) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO NOTHING`, record.ID, record.Operation, record.Phase, record.Attempt, record.StartedAt, string(raw))
	return err
}

var _ workflowtrace.Recorder = (*Store)(nil)
