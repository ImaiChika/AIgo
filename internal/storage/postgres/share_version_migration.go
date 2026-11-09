package postgres

// C4 snapshots are a separate sharing authority. Personal revision/deletion or
// later optional-review outcomes never rewrite an already approved snapshot.
func sharedVersionStatements() []string {
	return []string{
		`ALTER TABLE question_share_requests ADD COLUMN IF NOT EXISTS question_version INT NOT NULL DEFAULT 0`,
		`ALTER TABLE question_share_requests ADD COLUMN IF NOT EXISTS library_revision BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE question_share_requests ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1`,
		`ALTER TABLE question_share_requests ADD COLUMN IF NOT EXISTS question_snapshot JSONB`,
		`ALTER TABLE question_share_requests ADD COLUMN IF NOT EXISTS snapshot_search_text TEXT NOT NULL DEFAULT ''`,
		`UPDATE question_share_requests SET revision=2 WHERE status IN ('approved','rejected') AND revision=1`,
		// Existing pending/approved sources were edit-locked before this migration.
		`UPDATE question_share_requests sr SET question_version=q.version,library_revision=q.library_revision,
 question_snapshot=(to_jsonb(q)-'created_by'-'confirmed_by'-'deleted_by'-'search_text'-'system_name'-'generation_workflow_version')||jsonb_build_object('system',q.system_name,'workflow_version',q.generation_workflow_version,'bank_ids',COALESCE((SELECT jsonb_agg(m.bank_id ORDER BY m.bank_id) FROM question_bank_members m WHERE m.question_id=q.id),'[]'::jsonb))
 FROM questions q WHERE sr.question_id=q.id AND sr.status IN ('pending','approved') AND sr.question_snapshot IS NULL AND sr.question_version=0`,
		// A rejected share may have a newer private revision. Recover only a version
		// which existed at submission; missing historical evidence stays unavailable.
		`WITH proven AS(SELECT sr.id,v.version,v.snapshot FROM question_share_requests sr JOIN LATERAL(SELECT version,snapshot FROM question_versions v WHERE v.question_id=sr.question_id AND v.created_at<=sr.created_at AND COALESCE(v.snapshot->>'clinical_stem','')<>'' ORDER BY v.created_at DESC,v.version DESC LIMIT 1)v ON TRUE WHERE sr.status='rejected' AND sr.question_snapshot IS NULL AND sr.question_version=0)
 UPDATE question_share_requests sr SET question_version=p.version,question_snapshot=(p.snapshot-'created_by'-'confirmed_by'-'deleted_by')||jsonb_build_object('owner_id',sr.owner_id,'status','published','library_state','active','confirmed_version',p.version,'confirmation_source','legacy') FROM proven p WHERE sr.id=p.id`,
		`UPDATE question_share_requests SET snapshot_search_text=concat_ws(' ',question_snapshot->>'id',question_snapshot->>'clinical_stem',question_snapshot->>'explanation',question_snapshot->>'outline_code',question_snapshot->>'profession',question_snapshot->>'system',(question_snapshot->'options')::text,(question_snapshot->'knowledge_points')::text) WHERE question_snapshot IS NOT NULL AND snapshot_search_text=''`,
		`CREATE INDEX IF NOT EXISTS idx_question_shares_fixed_status ON question_share_requests(status,question_version) WHERE question_snapshot IS NOT NULL`,
	}
}
