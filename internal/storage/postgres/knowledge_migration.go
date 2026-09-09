package postgres

// Only add migrations; the baseline and previously applied checksums stay intact.
func knowledgeVersionStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS knowledge_versions (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, year INT NOT NULL CHECK (year = 0 OR year BETWEEN 1900 AND 2200),
			description TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published')),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), published_at TIMESTAMPTZ
		)`,
		`INSERT INTO knowledge_versions (id,name,year,status,published_at) VALUES ('legacy','历史大纲（年份待确认）',0,'published',NOW()) ON CONFLICT(id) DO NOTHING`,
		`ALTER TABLE knowledge_points ADD COLUMN IF NOT EXISTS version_id TEXT NOT NULL DEFAULT 'legacy' REFERENCES knowledge_versions(id),
			ADD COLUMN IF NOT EXISTS revision INT NOT NULL DEFAULT 1, ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ,
			ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_kp_version_code ON knowledge_points(version_id,outline_code) WHERE outline_code <> '' AND deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_kp_version_tree ON knowledge_points(version_id,category,subject,unit,sub_item) WHERE deleted_at IS NULL`,
	}
}
