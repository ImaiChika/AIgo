package postgres

// Schema58 为题目增加个人题库归属（C3）：library_state/confirmed_version/
// confirmation_source/library_revision/deleted_reason/deleted_by，并按
// 客户需求 §12.1 回填历史状态。迁移幂等：仅更新仍为默认值的历史行，
// 重复执行不覆盖已确认/删除的归属。
func questionLibraryStatements() []string {
	return []string{
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS library_state TEXT NOT NULL DEFAULT 'unfiled'
			CHECK (library_state IN ('unfiled','active','deleted'))`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS confirmed_version INT NOT NULL DEFAULT 0`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS confirmation_source TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS library_revision INT NOT NULL DEFAULT 1`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS deleted_reason TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS deleted_by TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_questions_owner_library ON questions(owner_id, library_state) WHERE owner_id <> ''`,
		// 历史回填：published → active（确认版本=当前版本，legacy）；
		// rejected/archived → deleted（区分原因，终态保留）。
		`UPDATE questions SET library_state='active', confirmed_version=version, confirmation_source='legacy'
		WHERE status='published' AND library_state='unfiled' AND confirmed_version=0`,
		`UPDATE questions SET library_state='deleted', deleted_reason='legacy_rejected', deleted_by='legacy'
		WHERE status='rejected' AND library_state='unfiled'`,
		`UPDATE questions SET library_state='deleted', deleted_reason='legacy_archive', deleted_by='legacy'
		WHERE status='archived' AND library_state='unfiled'`,
		// 新生成的行兜底初始化归属修订号。
		`UPDATE questions SET library_revision=1 WHERE library_revision=0`,
	}
}
