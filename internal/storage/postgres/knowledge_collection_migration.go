package postgres

func knowledgeCollectionStatements() []string {
	return []string{
		`CREATE UNIQUE INDEX knowledge_card_submission_source ON knowledge_cards((body->>'origin_submission_id')) WHERE COALESCE(body->>'origin_submission_id','')<>''`,
		`CREATE TABLE personal_knowledge_cards (id TEXT PRIMARY KEY,owner_id TEXT NOT NULL REFERENCES users(id),revision INT NOT NULL CHECK(revision>0),status TEXT NOT NULL CHECK(status IN ('draft','active','archived')),body JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL, CHECK(body->>'id'=id AND (body->>'revision')::int=revision AND body->>'status'=status))`,
		`CREATE INDEX personal_knowledge_owner ON personal_knowledge_cards(owner_id,status,updated_at DESC,id)`,
		`CREATE UNIQUE INDEX personal_knowledge_learning_source ON personal_knowledge_cards(owner_id,(body->'origin'->>'job_id'),(body->'origin'->>'candidate_id')) WHERE body->'origin'->>'kind'='learning'`,
		`CREATE INDEX personal_knowledge_points ON personal_knowledge_cards USING gin ((body->'knowledge_points') jsonb_path_ops)`,
		`CREATE TABLE personal_knowledge_versions (card_id TEXT NOT NULL REFERENCES personal_knowledge_cards(id),revision INT NOT NULL,body JSONB NOT NULL,PRIMARY KEY(card_id,revision))`,
		`CREATE TABLE knowledge_card_choices (user_id TEXT NOT NULL REFERENCES users(id),scope TEXT NOT NULL CHECK(scope IN ('private','shared')),card_id TEXT NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(user_id,scope,card_id))`,
		`CREATE TABLE knowledge_collection_operations (owner_id TEXT NOT NULL REFERENCES users(id),key TEXT NOT NULL,kind TEXT NOT NULL,request_hash TEXT NOT NULL,result JSONB NOT NULL,PRIMARY KEY(owner_id,key,kind))`,
		`CREATE TABLE knowledge_submissions (id TEXT PRIMARY KEY,submitter_id TEXT NOT NULL REFERENCES users(id),private_card_id TEXT NOT NULL REFERENCES personal_knowledge_cards(id),private_revision INT NOT NULL,status TEXT NOT NULL CHECK(status IN ('pending','approved','rejected')),body JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL, CHECK(body->>'id'=id AND body->>'status'=status), UNIQUE(private_card_id,private_revision))`,
		`CREATE INDEX knowledge_submissions_queue ON knowledge_submissions(status,created_at,id)`,
		`CREATE INDEX knowledge_submissions_owner ON knowledge_submissions(submitter_id,created_at DESC,id)`,
		`UPDATE roles SET permissions=ARRAY(SELECT p FROM unnest(permissions || ARRAY['kb:view','kb:use','kb:submit']::text[]) WITH ORDINALITY AS added(p,pos) GROUP BY p ORDER BY MIN(pos)),updated_at=NOW() WHERE id IN ('teacher','expert') OR (id NOT IN ('super_admin','admin') AND permissions && ARRAY['question:generate','batch:run','review:do']::text[])`,
		`UPDATE roles SET permissions=ARRAY(SELECT p FROM unnest(permissions || ARRAY['kb:submit','kb:review']::text[]) WITH ORDINALITY AS added(p,pos) GROUP BY p ORDER BY MIN(pos)),updated_at=NOW() WHERE id IN ('super_admin','admin')`,
	}
}
