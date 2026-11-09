package postgres

func knowledgeVersionDeletionStatements() []string {
	return []string{
		`ALTER TABLE knowledge_versions ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`ALTER TABLE knowledge_versions DROP CONSTRAINT IF EXISTS knowledge_versions_name_key`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_knowledge_versions_active_name ON knowledge_versions(name) WHERE deleted_at IS NULL`,
	}
}
