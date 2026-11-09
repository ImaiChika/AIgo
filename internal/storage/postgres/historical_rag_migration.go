package postgres

// Historical v39 definition. Do not edit released statements.
func ragMigrationStatements() []string {
	return []string{
		`CREATE TABLE rag_documents(id TEXT PRIMARY KEY, metadata JSONB NOT NULL, revision INT NOT NULL DEFAULT 1, enabled BOOLEAN NOT NULL DEFAULT FALSE, archived BOOLEAN NOT NULL DEFAULT FALSE, active_version TEXT NOT NULL DEFAULT '', updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`CREATE TABLE rag_versions(id TEXT PRIMARY KEY, document_id TEXT NOT NULL REFERENCES rag_documents(id), metadata JSONB NOT NULL, pages JSONB NOT NULL, hash TEXT NOT NULL, original_hash TEXT NOT NULL DEFAULT '', filename TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','indexing','ready','failed')), profile TEXT NOT NULL DEFAULT '', chunk_count INT NOT NULL DEFAULT 0, attempts INT NOT NULL DEFAULT 0, lease_until TIMESTAMPTZ, error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`CREATE INDEX rag_versions_work ON rag_versions(state,lease_until)`,
		`CREATE TABLE rag_chunks(id TEXT PRIMARY KEY, version_id TEXT NOT NULL REFERENCES rag_versions(id), document_id TEXT NOT NULL REFERENCES rag_documents(id), page INT NOT NULL, text TEXT NOT NULL, profile TEXT NOT NULL, embedding JSONB NOT NULL, metadata JSONB NOT NULL)`,
		`CREATE INDEX rag_chunks_version ON rag_chunks(version_id)`,
		`CREATE TABLE rag_snapshots(key TEXT PRIMARY KEY, stage TEXT NOT NULL, payload JSONB NOT NULL, decision JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`ALTER TABLE batch_jobs ADD COLUMN rag_scope JSONB`,
		`UPDATE roles SET permissions=ARRAY(SELECT DISTINCT p FROM unnest(permissions || ARRAY['rag:view','rag:manage']) p) WHERE id IN ('super_admin','admin')`,
	}
}
