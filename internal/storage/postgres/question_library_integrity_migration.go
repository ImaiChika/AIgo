package postgres

// Schema59 is forward-only. Historical mappings in Schema58 are never rerun.
// Unknown historical confirmation actors/times remain unknown, not fabricated.
func questionLibraryIntegrityStatements() []string {
	return []string{
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS confirmed_by TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS confirmed_at TIMESTAMPTZ`,
		`ALTER TABLE questions ADD COLUMN IF NOT EXISTS deleted_from_state TEXT NOT NULL DEFAULT '' CHECK(deleted_from_state IN ('','unfiled','active','deleted'))`,
		// C3 initially left old review completion disconnected from library state.
		// Repair only durable, current-version completed reviews with initial-check
		// evidence. A label alone, an older task, or an unresolved task never suffices.
		`WITH completed AS (
		 SELECT q.id,q.status,t.updated_at,
		 COALESCE(NULLIF(t.final_decision->>'expert_id',''),
		   (SELECT NULLIF(r.expert_id,'') FROM review_records r WHERE r.task_id=t.id
		    AND r.conclusion=CASE WHEN q.status='published' THEN 'approved' ELSE 'rejected' END
		    ORDER BY r.created_at DESC,r.id DESC LIMIT 1),'legacy_review') AS actor
		 FROM questions q JOIN LATERAL
		  (SELECT * FROM review_tasks rt WHERE rt.question_id=q.id ORDER BY rt.created_at DESC,rt.id DESC LIMIT 1) t ON TRUE
		 WHERE q.library_state='unfiled' AND q.status IN ('published','rejected')
		 AND t.status=q.status AND t.question_version=q.version AND q.confirmed_version<>q.version
		 AND q.deleted_reason='' AND q.deleted_by=''
		 AND NOT EXISTS(SELECT 1 FROM review_tasks pending WHERE pending.question_id=q.id AND pending.status NOT IN ('published','approved','rejected','archived'))
		 AND EXISTS(SELECT 1 FROM ai_review_results ar WHERE ar.question_id=q.id AND ar.verdict='pass'
		  AND ar.question_version IN (0,1) AND ar.question_version<=q.version
		  AND (NOT EXISTS(SELECT 1 FROM ai_check_tasks act WHERE act.question_id=q.id)
		   OR EXISTS(SELECT 1 FROM ai_check_tasks act WHERE act.question_id=q.id AND act.status='succeeded'
		     AND act.attempts>0 AND act.question_version=1 AND act.question_version=ar.question_version)))
		 ) UPDATE questions q SET
		 library_state=CASE WHEN completed.status='published' THEN 'active' ELSE 'deleted' END,
		 confirmed_version=CASE WHEN completed.status='published' THEN q.version ELSE q.confirmed_version END,
		 confirmation_source=CASE WHEN completed.status='published' THEN 'review' ELSE q.confirmation_source END,
		 confirmed_by=CASE WHEN completed.status='published' THEN completed.actor ELSE q.confirmed_by END,
		 confirmed_at=CASE WHEN completed.status='published' THEN completed.updated_at ELSE q.confirmed_at END,
		 deleted_reason=CASE WHEN completed.status='rejected' THEN 'review_reject' ELSE '' END,
		 deleted_by=CASE WHEN completed.status='rejected' THEN completed.actor ELSE '' END,
		 deleted_from_state=CASE WHEN completed.status='rejected' THEN 'unfiled' ELSE '' END,
		 library_revision=q.library_revision+1 FROM completed WHERE q.id=completed.id`,
		// Only untouched pre-C3 defaults qualify. A post-Schema58 edit may be
		// an explicit revocation even when it recreates the old permission set.
		`UPDATE roles SET permissions=array_append(permissions,'question:confirm'),updated_at=NOW()
 WHERE id='teacher' AND is_builtin=TRUE
 AND updated_at < (SELECT applied_at FROM schema_migrations WHERE version=58)
 AND permissions <@ ARRAY['question:view','question:edit','question:generate','rule:view','rule:manage','rule:use','batch:run','question:view_formal','question:view_eliminated','question:download','question:share','review:submit','stats:view']::text[] AND permissions @> ARRAY['question:view','question:edit','question:generate','rule:view','rule:manage','rule:use','batch:run','question:view_formal','question:view_eliminated','question:download','question:share','review:submit','stats:view']::text[]`,
		// Only untouched pre-C3 defaults qualify. A post-Schema58 edit may be
		// an explicit revocation even when it recreates the old permission set.
		`UPDATE roles SET permissions=array_append(permissions,'question:confirm'),updated_at=NOW()
 WHERE id='admin' AND is_builtin=TRUE
 AND updated_at < (SELECT applied_at FROM schema_migrations WHERE version=58)
 AND permissions <@ ARRAY['question:view','question:edit','question:delete','question:generate','batch:run','rule:view','rule:manage','rule:use','rule:view_all','kb:view','kb:manage','kb:use','question:download','question:share','question:view_global','question:share_review','question:view_formal','question:delete_formal','question:view_eliminated','review:do','review:submit','review:final','review:restore','review:view_results','stats:view','user:manage','bank:manage','flow:manage','expert:manage','audit:view','knowledge:manage']::text[] AND permissions @> ARRAY['question:view','question:edit','question:delete','question:generate','batch:run','rule:view','rule:manage','rule:use','rule:view_all','kb:view','kb:manage','kb:use','question:download','question:share','question:view_global','question:share_review','question:view_formal','question:delete_formal','question:view_eliminated','review:do','review:submit','review:final','review:restore','review:view_results','stats:view','user:manage','bank:manage','flow:manage','expert:manage','audit:view','knowledge:manage']::text[]`,
	}
}
