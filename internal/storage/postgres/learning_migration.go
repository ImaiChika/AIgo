package postgres

import "strings"

// Schema61 is forward-only. Personal experiences never enter shared KB tables.
func learningStatements() []string {
	statements := []string{
		`CREATE TABLE learning_analysis_jobs(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,role_id TEXT NOT NULL,request_key TEXT NOT NULL,request_hash TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('pending','running','succeeded','failed')),attempts INT NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 2),max_attempts INT NOT NULL DEFAULT 2 CHECK(max_attempts BETWEEN 1 AND 2),revision INT NOT NULL DEFAULT 1 CHECK(revision>0),lease_token TEXT NOT NULL DEFAULT '',leased_until TIMESTAMPTZ,first_started_at TIMESTAMPTZ,deadline_at TIMESTAMPTZ,next_attempt_at TIMESTAMPTZ,completed_at TIMESTAMPTZ,body JSONB NOT NULL CHECK(jsonb_typeof(body)='object'),created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),UNIQUE(owner_id,request_key))`,
		`CREATE INDEX learning_jobs_owner ON learning_analysis_jobs(owner_id,created_at DESC,id)`,
		`CREATE INDEX learning_jobs_claim ON learning_analysis_jobs(status,next_attempt_at,created_at,id)`,
		`CREATE UNIQUE INDEX learning_one_running ON learning_analysis_jobs((status)) WHERE status='running'`,
		`CREATE TABLE personal_experiences(id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,kind TEXT NOT NULL DEFAULT 'authoring_experience' CHECK(kind='authoring_experience'),scope_type TEXT NOT NULL DEFAULT 'personal' CHECK(scope_type='personal'),revision INT NOT NULL CHECK(revision>0),status TEXT NOT NULL CHECK(status IN ('draft','active','archived')),source_job_id TEXT NOT NULL,source_candidate_id TEXT NOT NULL,body JSONB NOT NULL CHECK(jsonb_typeof(body)='object'),created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),UNIQUE(owner_id,source_job_id,source_candidate_id),UNIQUE(owner_id,id))`,
		`CREATE INDEX personal_experiences_owner ON personal_experiences(owner_id,status,updated_at DESC,id)`,
		`CREATE TABLE personal_experience_versions(owner_id TEXT NOT NULL,experience_id TEXT NOT NULL,revision INT NOT NULL CHECK(revision>0),body JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(experience_id,revision),FOREIGN KEY(owner_id,experience_id) REFERENCES personal_experiences(owner_id,id))`,
		`CREATE TABLE learning_import_operations(owner_id TEXT NOT NULL,import_key TEXT NOT NULL,request_hash TEXT NOT NULL,job_id TEXT NOT NULL,experience_ids JSONB NOT NULL CHECK(jsonb_typeof(experience_ids)='array'),created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(owner_id,import_key))`,
		`CREATE TABLE role_capability_migrations(migration_key TEXT NOT NULL,role_id TEXT NOT NULL,before_fingerprint TEXT NOT NULL,applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(migration_key,role_id))`,
		`ALTER TABLE model_call_records DROP CONSTRAINT model_call_records_purpose_check`,
		`ALTER TABLE model_call_records ADD CONSTRAINT model_call_records_purpose_check CHECK(purpose IN ('generation','ai_check','learning_analysis'))`,
		`ALTER TABLE batch_jobs ADD COLUMN experience_snapshot TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE question_experience_contexts(question_id TEXT PRIMARY KEY REFERENCES questions(id) ON DELETE CASCADE,owner_id TEXT NOT NULL,snapshot JSONB NOT NULL CHECK(jsonb_typeof(snapshot)='object'),created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
	}
	// Literal C4 fingerprints are immutable; never derive historical migration
	// input from AllPermissions, which will evolve in later releases.
	teacher := []string{"question:view", "question:edit", "question:confirm", "question:generate", "rule:view", "rule:manage", "rule:use", "batch:run", "question:view_formal", "question:view_eliminated", "question:download", "question:share", "review:submit", "stats:view"}
	admin := []string{"question:view", "question:edit", "question:confirm", "question:delete", "question:generate", "batch:run", "rule:view", "rule:manage", "rule:use", "rule:view_all", "kb:view", "kb:manage", "kb:use", "question:download", "question:share", "question:view_global", "question:share_review", "question:view_formal", "question:delete_formal", "question:view_eliminated", "review:do", "review:submit", "review:final", "review:restore", "review:view_results", "stats:view", "user:manage", "bank:manage", "flow:manage", "expert:manage", "audit:view", "knowledge:manage"}
	for _, entry := range []struct {
		id          string
		permissions []string
	}{{"teacher", teacher}, {"admin", admin}} {
		quoted := []string{}
		for _, permission := range entry.permissions {
			quoted = append(quoted, "'"+permission+"'")
		}
		fingerprint := "ARRAY[" + strings.Join(quoted, ",") + "]::text[]"
		statements = append(statements, `WITH eligible AS(SELECT id,md5(array_to_string(ARRAY(SELECT DISTINCT p FROM unnest(permissions)p ORDER BY p),',')) AS fingerprint FROM roles WHERE id='`+entry.id+`' AND is_builtin=TRUE AND permissions <@ `+fingerprint+` AND permissions @> `+fingerprint+` AND (updated_at <= (SELECT applied_at FROM schema_migrations WHERE version=60) OR created_at=updated_at) AND NOT EXISTS(SELECT 1 FROM role_capability_migrations m WHERE m.role_id=roles.id AND m.migration_key='c5-learning-v1')),updated AS(UPDATE roles r SET permissions=r.permissions||ARRAY['learning:analyze','learning:save','learning:use'],updated_at=NOW() FROM eligible e WHERE r.id=e.id RETURNING r.id) INSERT INTO role_capability_migrations(migration_key,role_id,before_fingerprint) SELECT 'c5-learning-v1',e.id,e.fingerprint FROM eligible e JOIN updated u ON u.id=e.id`)
	}
	return statements
}
