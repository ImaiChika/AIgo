package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/lib/pq"
)

func knowledgeCardMigrationStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS knowledge_cards (id TEXT PRIMARY KEY, revision INT NOT NULL CHECK(revision>0),kind TEXT NOT NULL CHECK(kind IN ('medical','exam_reference','custom')),status TEXT NOT NULL CHECK(status IN ('draft','active','archived')),body JSONB NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),CHECK(body->>'id'=id AND (body->>'revision')::int=revision AND body->>'kind'=kind AND body->>'status'=status))`,
		`CREATE TABLE IF NOT EXISTS knowledge_card_versions (card_id TEXT NOT NULL REFERENCES knowledge_cards(id),revision INT NOT NULL CHECK(revision>0),body JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(card_id,revision))`,
		`CREATE INDEX IF NOT EXISTS knowledge_cards_state ON knowledge_cards(status,kind,updated_at DESC,id)`,
		`CREATE INDEX IF NOT EXISTS knowledge_cards_points ON knowledge_cards USING gin ((body->'knowledge_points') jsonb_path_ops)`,
		`UPDATE roles SET permissions=ARRAY(SELECT DISTINCT p FROM unnest(permissions || ARRAY['kb:view','kb:manage','kb:use']) p) WHERE id IN ('super_admin','admin')`,
	}
}
func (s *Store) WithKnowledgeBaseAccess(ctx context.Context, uid, role string, fn func(context.Context, []string) error) error {
	err := s.WithAuthoringAccess(ctx, uid, role, func(ctx context.Context, p []string) error {
		tx, _ := ctx.Value(authoringTxKey{}).(*sql.Tx)
		if tx == nil {
			return knowledgebase.ErrDenied
		}
		return fn(context.WithValue(ctx, reviewTransactionContextKey{}, tx), p)
	})
	if errors.Is(err, storage.ErrAuthoringDenied) {
		return knowledgebase.ErrDenied
	}
	return err
}
func knowledgeCardsWhere(f knowledgebase.Filter) (string, []any) {
	args := []any{}
	clauses := []string{"TRUE"}
	if f.BaseOnly {
		clauses = append(clauses, "COALESCE(c.body->>'origin_submission_id','')=''")
	}
	add := func(sql string, v any) {
		args = append(args, v)
		clauses = append(clauses, fmt.Sprintf(sql, len(args)))
	}
	if f.Query != "" {
		q := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Query)
		add("c.body->>'title' ILIKE $%d", "%"+q+"%")
	}
	if f.Kind != "" {
		add("c.kind=$%d", f.Kind)
	}
	if f.Status != "" {
		add("c.status=$%d", f.Status)
	}
	if f.KnowledgePointID != "" {
		raw, _ := json.Marshal([]map[string]string{{"id": f.KnowledgePointID}})
		add("c.body->'knowledge_points' @> $%d::jsonb", string(raw))
	}
	return strings.Join(clauses, " AND "), args
}
func (s *Store) ListKnowledgeCards(ctx context.Context, f knowledgebase.Filter) ([]domain.KnowledgeCard, int, error) {
	where, args := knowledgeCardsWhere(f)
	var count int
	if e := s.queryRowContext(ctx, "SELECT count(*) FROM knowledge_cards c WHERE "+where, args...).Scan(&count); e != nil {
		return nil, 0, e
	}
	args = append(args, f.Limit, f.Offset)
	rows, e := s.queryContext(ctx, fmt.Sprintf("SELECT c.body FROM knowledge_cards c WHERE %s ORDER BY c.updated_at DESC,c.id LIMIT $%d OFFSET $%d", where, len(args)-1, len(args)), args...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	items := []domain.KnowledgeCard{}
	for rows.Next() {
		var raw []byte
		var c domain.KnowledgeCard
		if e = rows.Scan(&raw); e != nil {
			return nil, 0, e
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			return nil, 0, e
		}
		items = append(items, c)
	}
	return items, count, rows.Err()
}
func (s *Store) GetKnowledgeCard(ctx context.Context, id string) (*domain.KnowledgeCard, error) {
	var raw []byte
	e := s.queryRowContext(ctx, `SELECT body FROM knowledge_cards WHERE id=$1`, id).Scan(&raw)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var c domain.KnowledgeCard
	e = json.Unmarshal(raw, &c)
	return &c, e
}
func (s *Store) KnowledgeCardHistory(ctx context.Context, id string) ([]domain.KnowledgeCard, error) {
	rows, e := s.queryContext(ctx, `SELECT body FROM knowledge_card_versions WHERE card_id=$1 ORDER BY revision DESC LIMIT 100`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []domain.KnowledgeCard{}
	for rows.Next() {
		var raw []byte
		var c domain.KnowledgeCard
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &c); e != nil {
			return nil, e
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s *Store) WriteKnowledgeCard(ctx context.Context, c domain.KnowledgeCard, expected int) error {
	if reviewTransactionFromContext(ctx) == nil {
		return errors.New("knowledge card writes require an access transaction")
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return e
	}
	var res sql.Result
	if expected == 0 {
		res, e = s.execContext(ctx, `INSERT INTO knowledge_cards(id,revision,kind,status,body,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, c.ID, c.Revision, c.Kind, c.Status, raw, c.UpdatedAt)
	} else {
		res, e = s.execContext(ctx, `UPDATE knowledge_cards SET revision=$2,kind=$3,status=$4,body=$5,updated_at=$6 WHERE id=$1 AND revision=$7`, c.ID, c.Revision, c.Kind, c.Status, raw, c.UpdatedAt, expected)
	}
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return knowledgebase.ErrConflict
	}
	_, e = s.execContext(ctx, `INSERT INTO knowledge_card_versions(card_id,revision,body) VALUES($1,$2,$3)`, c.ID, c.Revision, raw)
	if e != nil {
		return e
	}
	var actor string
	if e = s.queryRowContext(ctx, `SELECT username FROM users WHERE id=$1`, c.UpdatedBy).Scan(&actor); e != nil {
		return e
	}
	return s.SaveLog(ctx, domain.AuditLog{ID: fmt.Sprintf("kb-%s-%d", c.ID, c.Revision), Action: "knowledge_card_" + c.Status, Actor: actor, Detail: fmt.Sprintf("card=%s revision=%d kind=%s", c.ID, c.Revision, c.Kind), CreatedAt: c.UpdatedAt})
}
func (s *Store) ResolveKnowledgeCardPoints(ctx context.Context, points []domain.KnowledgePoint) ([]domain.KnowledgePoint, error) {
	// Share the syllabus writer lock, so whole-version deletion and point edits
	// cannot race validation or acquire rows in the opposite order.
	if _, err := s.execContext(ctx, `SELECT pg_advisory_xact_lock_shared(4704385982817583184)`); err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return []domain.KnowledgePoint{}, nil
	}
	// A single point is the common generation path. Retain its small queries;
	// the batched planner/lookup only pays off when resolving multiple points.
	if len(points) == 1 {
		p := points[0]
		var version string
		err := s.queryRowContext(ctx, `SELECT id FROM knowledge_versions WHERE id=$1 AND deleted_at IS NULL FOR KEY SHARE`, p.VersionID).Scan(&version)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: 大纲版本已删除，请重新选择", knowledgebase.ErrInvalid)
		}
		if err != nil {
			return nil, err
		}
		kp, err := scanKP(s.queryRowContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.id=$1 AND p.deleted_at IS NULL FOR SHARE OF p`, p.ID))
		if err != nil {
			return nil, err
		}
		if kp == nil || kp.VersionID != p.VersionID {
			return nil, fmt.Errorf("%w: 知识点已删除或版本不匹配", knowledgebase.ErrInvalid)
		}
		return []domain.KnowledgePoint{*kp}, nil
	}
	versions, ids := []string{}, []string{}
	seenVersions, seenIDs := map[string]bool{}, map[string]bool{}
	for _, p := range points {
		if !seenVersions[p.VersionID] {
			seenVersions[p.VersionID] = true
			versions = append(versions, p.VersionID)
		}
		if !seenIDs[p.ID] {
			seenIDs[p.ID] = true
			ids = append(ids, p.ID)
		}
	}
	// Stable lock ordering and bounded round trips; preserve caller order and
	// duplicates only after validating every requested ID/version pair.
	sort.Strings(versions)
	sort.Strings(ids)
	rows, err := s.queryContext(ctx, `SELECT id FROM knowledge_versions WHERE id=ANY($1::text[]) AND deleted_at IS NULL ORDER BY id FOR KEY SHARE`, pq.Array(versions))
	if err != nil {
		return nil, err
	}
	validVersions := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		validVersions[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.queryContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.id=ANY($1::text[]) AND p.deleted_at IS NULL ORDER BY p.id FOR SHARE OF p`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	current := make(map[string]domain.KnowledgePoint, len(ids))
	for rows.Next() {
		p, e := scanKP(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		current[p.ID] = *p
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]domain.KnowledgePoint, 0, len(points))
	for _, p := range points {
		if !validVersions[p.VersionID] {
			return nil, fmt.Errorf("%w: 大纲版本已删除，请重新选择", knowledgebase.ErrInvalid)
		}
		kp, ok := current[p.ID]
		if !ok || kp.VersionID != p.VersionID {
			return nil, fmt.Errorf("%w: 知识点已删除或版本不匹配", knowledgebase.ErrInvalid)
		}
		result = append(result, kp)
	}
	return result, nil
}

// LoadKnowledgeWorkflowCandidates combines round trips, not knowledge scope.
// Counts and each kind's top 4/2/2 use the same indexed predicate and ordering as
// ListKnowledgeCards; no unlimited card bodies or cross-request cache are read.
func (s *Store) LoadKnowledgeWorkflowCandidates(ctx context.Context, ids []string) (map[string]knowledgebase.WorkflowCandidates, error) {
	if reviewTransactionFromContext(ctx) == nil {
		return nil, errors.New("knowledge preparation requires an access transaction")
	}
	if len(ids) == 0 || len(ids) > knowledgebase.WorkflowLookupBatchSize {
		return nil, errors.New("knowledge candidate batch exceeds its bound")
	}
	unique := make([]string, 0, len(ids))
	seenIDs := map[string]bool{}
	for _, id := range ids {
		if !seenIDs[id] {
			seenIDs[id] = true
			unique = append(unique, id)
		}
	}
	query, args := knowledgeWorkflowCandidatesQuery(unique)
	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]knowledgebase.WorkflowCandidates, len(ids))
	seen := map[[2]string]bool{}
	for rows.Next() {
		var id, kind string
		var total int
		var raw []byte
		if err = rows.Scan(&id, &kind, &total, &raw); err != nil {
			return nil, err
		}
		entry := result[id]
		key := [2]string{id, kind}
		if !seen[key] {
			entry.Total += total
			seen[key] = true
		}
		if len(raw) > 0 {
			var card domain.KnowledgeCard
			if err = json.Unmarshal(raw, &card); err != nil {
				return nil, err
			}
			entry.Cards = append(entry.Cards, card)
		}
		result[id] = entry
	}
	return result, rows.Err()
}

// Keep each point's JSON predicate as a bound constant in its own branch. A
// lateral expression built from outer point/kind variables hides their selectivity
// from PostgreSQL and can scan every unrelated card for every point/kind.
func knowledgeWorkflowCandidatesQuery(ids []string) (string, []any) {
	branches := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)*2)
	for i, id := range ids {
		raw, _ := json.Marshal([]map[string]string{{"id": id}})
		args = append(args, id, string(raw))
		idArg, jsonArg := len(args)-1, len(args)
		for pos, kind := range []struct {
			name  string
			limit int
		}{{"medical", 4}, {"exam_reference", 2}, {"custom", 2}} {
			branches = append(branches, fmt.Sprintf(`
SELECT $%d::text AS point_id,'%s'::text AS kind,totals.total,c.body,%d AS point_order,%d AS kind_order,c.updated_at,c.id
FROM (
 SELECT count(*) AS total FROM knowledge_cards
 WHERE status='active' AND COALESCE(body->>'origin_submission_id','')='' AND kind='%s' AND body->'knowledge_points' @> $%d::jsonb
) totals
LEFT JOIN LATERAL (
 SELECT body,updated_at,id FROM knowledge_cards
 WHERE status='active' AND COALESCE(body->>'origin_submission_id','')='' AND kind='%s' AND body->'knowledge_points' @> $%d::jsonb
 ORDER BY updated_at DESC,id LIMIT %d
) c ON true`, idArg, kind.name, i, pos, kind.name, jsonArg, kind.name, jsonArg, kind.limit))
		}

	}
	return "SELECT point_id,kind,total,body FROM (" + strings.Join(branches, " UNION ALL ") + ") candidates ORDER BY point_order,kind_order,updated_at DESC,id", args
}
