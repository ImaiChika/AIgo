package postgres

import "fmt"

// Retire runtime tables without destroying original files or published history.
// Archives are never read by the knowledge-card service or generation workers.
func retireRAGMigrationStatements() []string {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS legacy_rag_batch_scopes (job_id TEXT PRIMARY KEY,scope JSONB NOT NULL)`,
		`DO $$ BEGIN IF EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='batch_jobs' AND column_name='rag_scope') THEN
 INSERT INTO legacy_rag_batch_scopes(job_id,scope) SELECT id,rag_scope FROM batch_jobs WHERE rag_scope IS NOT NULL ON CONFLICT DO NOTHING;
 ALTER TABLE batch_jobs DROP COLUMN rag_scope; END IF; END $$`,
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS workflow_snapshot JSONB`,
		`UPDATE roles SET permissions=ARRAY(SELECT p FROM unnest(permissions) p WHERE p NOT IN ('rag:view','rag:manage','rag:control'))`,
		`UPDATE users SET permissions=ARRAY(SELECT p FROM unnest(permissions) p WHERE p NOT IN ('rag:view','rag:manage','rag:control'))`,
	}
	for _, table := range []string{"rag_documents", "rag_versions", "rag_chunks", "rag_snapshots", "rag_files", "rag_settings"} {
		statements = append(statements, fmt.Sprintf(`DO $$ BEGIN IF EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='%s') THEN ALTER TABLE %s RENAME TO legacy_%s; END IF; END $$`, table, table, table))
	}
	return statements
}
