package postgres

import (
	"context"
	"fmt"
)

func learningConcurrencyStatements() []string {
	return []string{
		`ALTER TABLE generation_quota ADD COLUMN IF NOT EXISTS learning_concurrency INTEGER NOT NULL DEFAULT 1 CHECK(learning_concurrency BETWEEN 1 AND 8)`,
		`DROP INDEX IF EXISTS learning_one_running`,
		`CREATE INDEX IF NOT EXISTS learning_running_capacity ON learning_analysis_jobs(status) WHERE status='running'`,
	}
}
func (s *Store) LearningConcurrencyLimit(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT learning_concurrency FROM generation_quota WHERE id=1`).Scan(&n)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > 8 {
		return 0, fmt.Errorf("invalid saved learning concurrency")
	}
	return n, nil
}
