package postgres

import (
	"bytes"
	"context"
	"testing"
)

func TestLegacyRAGRetirementPreservesDataAndRunsOnce(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, e := New(dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	applyMigrationPrefix(t, ctx, s, configuredMigrations(baselineSchemaSQL)[:48])
	if _, e = s.db.Exec(`INSERT INTO rag_documents(id,metadata) VALUES('old-doc','{"title":"old original"}');INSERT INTO rag_versions(id,document_id,metadata,pages,hash)VALUES('old-version','old-doc','{}','[{"text":"original text"}]','old-hash');INSERT INTO rag_chunks(id,version_id,document_id,page,text,profile,embedding,metadata)VALUES('old-chunk','old-version','old-doc',1,'original chunk','legacy','[1,2]','{}');INSERT INTO rag_snapshots(key,stage,payload)VALUES('old-snapshot','generation','{"used":"old-doc"}');INSERT INTO rag_files(hash,filename,data)VALUES('old-hash','original.pdf',decode('000102ff','hex'));INSERT INTO batch_jobs(id,rag_scope)VALUES('old-job','{"versions":["old-version"]}');INSERT INTO roles(id,name,permissions)VALUES('legacy-role','legacy',ARRAY['rag:view','rag:manage','rag:control','question:view']);INSERT INTO users(id,username,password_hash,permissions)VALUES('legacy-user','legacy-user','unused',ARRAY['rag:view','kb:view'])`); e != nil {
		t.Fatal(e)
	}
	applied, e := s.Migrate(ctx)
	if e != nil || len(applied) != int(LatestSchemaVersion-48) {
		t.Fatal(applied, e)
	}
	for _, table := range []string{"rag_documents", "rag_versions", "rag_chunks", "rag_snapshots", "rag_files", "rag_settings"} {
		var active bool
		if e = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name=$1)`, table).Scan(&active); e != nil || active {
			t.Fatal(table, active, e)
		}
		var n int
		if e = s.db.QueryRow(`SELECT count(*) FROM legacy_` + table).Scan(&n); e != nil || n != 1 {
			t.Fatal(table, n, e)
		}
	}
	var original []byte
	if e = s.db.QueryRow(`SELECT data FROM legacy_rag_files WHERE hash='old-hash'`).Scan(&original); e != nil || !bytes.Equal(original, []byte{0, 1, 2, 255}) {
		t.Fatal("file changed", original, e)
	}
	var text string
	if e = s.db.QueryRow(`SELECT scope->'versions'->>0 FROM legacy_rag_batch_scopes WHERE job_id='old-job'`).Scan(&text); e != nil || text != "old-version" {
		t.Fatal(text, e)
	}
	var legacyGrants int
	if e = s.db.QueryRow(`SELECT count(*) FROM roles r,unnest(r.permissions)p WHERE p LIKE 'rag:%'`).Scan(&legacyGrants); e != nil || legacyGrants != 0 {
		t.Fatal(legacyGrants, e)
	}
	again, e := s.Migrate(ctx)
	if e != nil || len(again) != 0 {
		t.Fatal("migration not idempotent", again, e)
	}
}

func TestHistoricalRAGV39ChecksumUnchanged(t *testing.T) {
	info := migrationInfo(configuredMigrations(baselineSchemaSQL)[38])
	if info.Checksum != "f2a46ccbb7ed840dc0b09ca511583f1f7bbe0d0f5f094a89ef90b548d2c2d097" {
		t.Fatal("released v39 checksum changed", info)
	}
}
