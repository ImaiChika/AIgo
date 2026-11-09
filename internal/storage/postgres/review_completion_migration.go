package postgres

// Appended by Schema60. Existing tasks/flows retain the legacy final decision;
// merely upgrading never publishes a waiting conflict task.
func reviewCompletionStatements() []string {
	return []string{
		`ALTER TABLE review_flows ADD COLUMN IF NOT EXISTS completion_mode TEXT NOT NULL DEFAULT 'external_final' CHECK(completion_mode IN ('on_last_round','external_final'))`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS completion_mode TEXT NOT NULL DEFAULT 'external_final' CHECK(completion_mode IN ('on_last_round','external_final'))`,
		`ALTER TABLE review_tasks ADD COLUMN IF NOT EXISTS library_before_submit JSONB`,
	}
}
