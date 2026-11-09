package postgres

import (
	"aigo/internal/llm"
	"context"
	"encoding/json"
	"fmt"
)

func modelCallMigrationStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS model_call_records (
 id TEXT PRIMARY KEY, operation TEXT NOT NULL, purpose TEXT NOT NULL CHECK(purpose IN ('generation','ai_check')),
 started_at TIMESTAMPTZ NOT NULL, record JSONB NOT NULL CHECK(jsonb_typeof(record)='object')
 )`,
		`CREATE INDEX IF NOT EXISTS idx_model_call_operation ON model_call_records(operation,started_at,id)`,
	}
}

// Diagnostic evidence is independent of question transactions and has no public
// read route. It cannot make a generated/reviewed question visible or invisible.
func (s *Store) SaveModelCall(ctx context.Context, record llm.ModelCallRecord) error {
	if record.ID == "" || record.Stage != "model_call" || record.StartedAt.IsZero() {
		return fmt.Errorf("invalid model-call evidence")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO model_call_records(id,operation,purpose,started_at,record) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, record.ID, record.Operation, record.Purpose, record.StartedAt, string(raw))
	return err
}

var _ llm.ModelCallRecorder = (*Store)(nil)
