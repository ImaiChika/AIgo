package postgres

// These are one-time, explicitly requested grants to existing teaching
// templates. Startup must never restore a later revocation. Keep the literal
// permission set independent of evolving application defaults.
func teacherKnowledgeStatements() []string {
	return []string{`WITH eligible AS (
 SELECT id, md5(array_to_string(ARRAY(SELECT DISTINCT p FROM unnest(permissions) p ORDER BY p), ',')) AS fingerprint,
 ARRAY(SELECT p FROM unnest(permissions || ARRAY['experience:view','experience:manage']::text[] ||
   CASE WHEN permissions && ARRAY['question:generate','batch:run']::text[] THEN ARRAY['experience:use']::text[] ELSE ARRAY[]::text[] END) WITH ORDINALITY AS added(p,position) GROUP BY p ORDER BY MIN(position)) AS grants
 FROM roles
 WHERE id IN ('teacher','expert') OR (id NOT IN ('super_admin','admin') AND permissions && ARRAY['question:generate','batch:run','review:do']::text[])
), updated AS (
 UPDATE roles r SET permissions=e.grants, updated_at=NOW() FROM eligible e WHERE r.id=e.id AND NOT (r.permissions @> e.grants) RETURNING r.id
)
INSERT INTO role_capability_migrations(migration_key,role_id,before_fingerprint)
 SELECT 'teacher-personal-knowledge-v1',e.id,e.fingerprint FROM eligible e JOIN updated u ON u.id=e.id`}
}
