package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
	"strings"
)

func (s *Store) CollectionReceipt(ctx context.Context, uid, key, kind, hash string) (json.RawMessage, error) {
	if reviewTransactionFromContext(ctx) == nil {
		return nil, knowledgebase.ErrDenied
	}
	if _, e := s.execContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, uid+":"+kind+":"+key); e != nil {
		return nil, e
	}
	var old string
	var result []byte
	e := s.queryRowContext(ctx, `SELECT request_hash,result FROM knowledge_collection_operations WHERE owner_id=$1 AND key=$2 AND kind=$3`, uid, key, kind).Scan(&old, &result)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if old != hash {
		return nil, knowledgebase.ErrConflict
	}
	return result, nil
}
func (s *Store) FinishCollectionReceipt(ctx context.Context, uid, key, kind, hash string, value any) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	_, e = s.execContext(ctx, `INSERT INTO knowledge_collection_operations(owner_id,key,kind,request_hash,result) VALUES($1,$2,$3,$4,$5)`, uid, key, kind, hash, raw)
	return e
}
func privateKnowledgeWhere(uid string, f knowledgebase.Filter) (string, []any) {
	args := []any{uid}
	where := "c.owner_id=$1"
	if f.Status != "" {
		args = append(args, f.Status)
		where += fmt.Sprintf(" AND c.status=$%d", len(args))
	}
	if f.Query != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Query)+"%")
		where += fmt.Sprintf(" AND c.body->>'title' ILIKE $%d", len(args))
	}
	return where, args
}
func (s *Store) ListPrivateKnowledge(ctx context.Context, uid string, f knowledgebase.Filter) ([]domain.PrivateKnowledgeCard, int, error) {
	where, args := privateKnowledgeWhere(uid, f)
	var total int
	if e := s.queryRowContext(ctx, "SELECT count(*) FROM personal_knowledge_cards c WHERE "+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	args = append(args, f.Limit, f.Offset)
	rows, e := s.queryContext(ctx, fmt.Sprintf(`SELECT c.body,EXISTS(SELECT 1 FROM knowledge_card_choices x WHERE x.user_id=c.owner_id AND x.scope='private' AND x.card_id=c.id) FROM personal_knowledge_cards c WHERE %s ORDER BY c.updated_at DESC,c.id LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	cards := []domain.PrivateKnowledgeCard{}
	for rows.Next() {
		var raw []byte
		var selected bool
		if e = rows.Scan(&raw, &selected); e != nil {
			return nil, 0, e
		}
		var card domain.PrivateKnowledgeCard
		if e = json.Unmarshal(raw, &card); e != nil {
			return nil, 0, e
		}
		card.OwnerID = uid
		card.Selected = selected
		cards = append(cards, card)
	}
	return cards, total, rows.Err()
}
func (s *Store) GetPrivateKnowledge(ctx context.Context, uid, id string, lock bool) (*domain.PrivateKnowledgeCard, error) {
	query := `SELECT body FROM personal_knowledge_cards WHERE owner_id=$1 AND id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	var raw []byte
	e := s.queryRowContext(ctx, query, uid, id).Scan(&raw)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var card domain.PrivateKnowledgeCard
	e = json.Unmarshal(raw, &card)
	card.OwnerID = uid
	return &card, e
}
func (s *Store) PrivateKnowledgeHistory(ctx context.Context, uid, id string) ([]domain.PrivateKnowledgeCard, error) {
	rows, e := s.queryContext(ctx, `SELECT v.body FROM personal_knowledge_versions v JOIN personal_knowledge_cards c ON c.id=v.card_id WHERE c.owner_id=$1 AND c.id=$2 ORDER BY v.revision DESC LIMIT 100`, uid, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	cards := []domain.PrivateKnowledgeCard{}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		var card domain.PrivateKnowledgeCard
		if e = json.Unmarshal(raw, &card); e != nil {
			return nil, e
		}
		card.OwnerID = uid
		cards = append(cards, card)
	}
	return cards, rows.Err()
}
func (s *Store) WritePrivateKnowledge(ctx context.Context, card domain.PrivateKnowledgeCard, expected int) error {
	if reviewTransactionFromContext(ctx) == nil {
		return knowledgebase.ErrDenied
	}
	raw, e := json.Marshal(card)
	if e != nil {
		return e
	}
	var result sql.Result
	if expected == 0 {
		result, e = s.execContext(ctx, `INSERT INTO personal_knowledge_cards(id,owner_id,revision,status,body,updated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, card.ID, card.OwnerID, card.Revision, card.Status, raw, card.UpdatedAt)
	} else {
		result, e = s.execContext(ctx, `UPDATE personal_knowledge_cards SET revision=$3,status=$4,body=$5,updated_at=$6 WHERE id=$1 AND owner_id=$2 AND revision=$7`, card.ID, card.OwnerID, card.Revision, card.Status, raw, card.UpdatedAt, expected)
	}
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return knowledgebase.ErrConflict
	}
	_, e = s.execContext(ctx, `INSERT INTO personal_knowledge_versions(card_id,revision,body) VALUES($1,$2,$3)`, card.ID, card.Revision, raw)
	if e != nil {
		return e
	}
	return s.collectionAudit(ctx, card.OwnerID, "personal_knowledge_"+card.Status, fmt.Sprintf("card=%s revision=%d", card.ID, card.Revision))
}
func (s *Store) collectionAudit(ctx context.Context, uid, action, detail string) error {
	var name string
	if e := s.queryRowContext(ctx, `SELECT username FROM users WHERE id=$1`, uid).Scan(&name); e != nil {
		return e
	}
	_, e := s.execContext(ctx, `INSERT INTO audit_logs(id,action,actor,detail) VALUES('kcol-'||md5(random()::text||clock_timestamp()::text),$1,$2,$3)`, action, name, detail)
	return e
}
func (s *Store) SetKnowledgeChoice(ctx context.Context, uid, scope, id string, selected bool) error {
	if selected {
		var count int
		var exists bool
		if e := s.queryRowContext(ctx, `SELECT count(*),COALESCE(bool_or(scope=$2 AND card_id=$3),false) FROM knowledge_card_choices WHERE user_id=$1`, uid, scope, id).Scan(&count, &exists); e != nil {
			return e
		}
		if count >= 100 && !exists {
			return fmt.Errorf("%w: 最多勾选100张资料，请先取消部分选择", knowledgebase.ErrInvalid)
		}
		_, e := s.execContext(ctx, `INSERT INTO knowledge_card_choices(user_id,scope,card_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, uid, scope, id)
		return e
	}
	_, e := s.execContext(ctx, `DELETE FROM knowledge_card_choices WHERE user_id=$1 AND scope=$2 AND card_id=$3`, uid, scope, id)
	return e
}
func (s *Store) KnowledgeChoices(ctx context.Context, uid string) ([]domain.KnowledgeCardRef, error) {
	rows, e := s.queryContext(ctx, `SELECT c.id,c.revision FROM knowledge_cards c JOIN knowledge_card_choices x ON x.card_id=c.id AND x.scope='shared' AND x.user_id=$1 ORDER BY c.id`, uid)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	refs := []domain.KnowledgeCardRef{}
	for rows.Next() {
		var r domain.KnowledgeCardRef
		if e = rows.Scan(&r.ID, &r.Revision); e != nil {
			return nil, e
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}
func (s *Store) LoadChosenKnowledgeCards(ctx context.Context, uid string, ids []string, private, shared bool) (map[string]knowledgebase.WorkflowCandidates, error) {
	result := map[string]knowledgebase.WorkflowCandidates{}
	if !private && !shared {
		return result, nil
	}
	var anyChoice bool
	if e := s.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_card_choices WHERE user_id=$1 AND (($2 AND scope='private') OR ($3 AND scope='shared')))`, uid, private, shared).Scan(&anyChoice); e != nil {
		return nil, e
	}
	if !anyChoice {
		return result, nil
	}
	// Constant JSON predicates retain the GIN index selectivity. Keep one bounded
	// statement per group instead of one SQL round trip for every selected point.
	unique := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) > knowledgebase.WorkflowLookupBatchSize {
		return nil, knowledgebase.ErrInvalid
	}
	if len(unique) == 0 {
		return result, nil
	}
	args := []any{uid, shared, private}
	branches := make([]string, 0, len(unique))
	for _, id := range unique {
		raw, _ := json.Marshal([]map[string]string{{"id": id}})
		args = append(args, id, string(raw))
		idArg, jsonArg := len(args)-1, len(args)
		result[id] = knowledgebase.WorkflowCandidates{Cards: []domain.KnowledgeCard{}}
		branches = append(branches, fmt.Sprintf(`(SELECT $%d::text AS point_id,body,count(*) OVER() AS total,updated_at,id FROM (
   SELECT c.body,c.updated_at,c.id FROM personal_knowledge_cards c JOIN knowledge_card_choices x ON x.card_id=c.id AND x.scope='private' AND x.user_id=$1 WHERE $3 AND c.owner_id=$1 AND c.status='active' AND c.body->'knowledge_points' @> $%d::jsonb
   UNION ALL
   SELECT c.body,c.updated_at,c.id FROM knowledge_cards c JOIN knowledge_card_choices x ON x.card_id=c.id AND x.scope='shared' AND x.user_id=$1 WHERE $2 AND c.status='active' AND COALESCE(c.body->>'origin_submission_id','')<>'' AND c.body->'knowledge_points' @> $%d::jsonb
  ) chosen ORDER BY updated_at DESC,id LIMIT 8)`, idArg, jsonArg, jsonArg))
	}
	rows, e := s.queryContext(ctx, "SELECT point_id,body,total FROM ("+strings.Join(branches, " UNION ALL ")+") selected_points ORDER BY point_id,updated_at DESC,id", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var body []byte
		var total int
		if e = rows.Scan(&id, &body, &total); e != nil {
			return nil, e
		}
		var card domain.KnowledgeCard
		if e = json.Unmarshal(body, &card); e != nil {
			return nil, e
		}
		entry := result[id]
		entry.Total = total
		entry.Cards = append(entry.Cards, card)
		result[id] = entry
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	return result, nil
}

func (s *Store) SubmitKnowledgeCard(ctx context.Context, item domain.KnowledgeSubmission) (domain.KnowledgeSubmission, error) {
	if e := s.queryRowContext(ctx, `SELECT COALESCE(NULLIF(display_name,''),username) FROM users WHERE id=$1`, item.SubmitterID).Scan(&item.SubmitterName); e != nil {
		return item, e
	}
	raw, e := json.Marshal(item)
	if e != nil {
		return item, e
	}
	var result []byte
	e = s.queryRowContext(ctx, `INSERT INTO knowledge_submissions(id,submitter_id,private_card_id,private_revision,status,body,created_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(private_card_id,private_revision) DO UPDATE SET private_revision=EXCLUDED.private_revision RETURNING body`, item.ID, item.SubmitterID, item.PrivateCardID, item.PrivateRevision, item.Status, raw, item.CreatedAt).Scan(&result)
	if e != nil {
		return item, e
	}
	e = json.Unmarshal(result, &item)
	if e != nil {
		return item, e
	}
	e = s.collectionAudit(ctx, item.SubmitterID, "knowledge_submit", "submission="+item.ID)
	return item, e
}
func (s *Store) ListKnowledgeSubmissions(ctx context.Context, uid string, review bool, f knowledgebase.Filter) ([]domain.KnowledgeSubmission, int, error) {
	args := []any{uid}
	where := "submitter_id=$1"
	if review {
		where = "submitter_id<>$1"
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where += fmt.Sprintf(" AND status=$%d", len(args))
	}
	var total int
	if e := s.queryRowContext(ctx, `SELECT count(*) FROM knowledge_submissions WHERE `+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	args = append(args, f.Limit, f.Offset)
	rows, e := s.queryContext(ctx, fmt.Sprintf(`SELECT body FROM knowledge_submissions WHERE %s ORDER BY created_at DESC,id LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	items := []domain.KnowledgeSubmission{}
	for rows.Next() {
		var raw []byte
		if e = rows.Scan(&raw); e != nil {
			return nil, 0, e
		}
		var item domain.KnowledgeSubmission
		if e = json.Unmarshal(raw, &item); e != nil {
			return nil, 0, e
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}
func (s *Store) GetKnowledgeSubmission(ctx context.Context, id string) (*domain.KnowledgeSubmission, error) {
	var raw []byte
	e := s.queryRowContext(ctx, `SELECT body FROM knowledge_submissions WHERE id=$1 FOR UPDATE`, id).Scan(&raw)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var item domain.KnowledgeSubmission
	e = json.Unmarshal(raw, &item)
	return &item, e
}
func (s *Store) DecideKnowledgeSubmission(ctx context.Context, item domain.KnowledgeSubmission) error {
	raw, e := json.Marshal(item)
	if e != nil {
		return e
	}
	result, e := s.execContext(ctx, `UPDATE knowledge_submissions SET status=$2,body=$3 WHERE id=$1 AND status='pending'`, item.ID, item.Status, raw)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return knowledgebase.ErrConflict
	}
	return s.collectionAudit(ctx, item.ReviewedBy, "knowledge_review_"+item.Status, "submission="+item.ID+" shared_card="+item.SharedCardID)
}

var _ knowledgebase.CollectionStore = (*Store)(nil)

func (s *Store) FindKnowledgeCandidate(ctx context.Context, uid, job, candidate string) (string, error) {
	var id string
	e := s.queryRowContext(ctx, `SELECT id FROM personal_knowledge_cards WHERE owner_id=$1 AND body->'origin'->>'kind'='learning' AND body->'origin'->>'job_id'=$2 AND body->'origin'->>'candidate_id'=$3`, uid, job, candidate).Scan(&id)
	if e == sql.ErrNoRows {
		return "", nil
	}
	return id, e
}

func (s *Store) HasPrivateKnowledgeChoice(ctx context.Context, uid string) (bool, error) {
	var selected bool
	e := s.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM personal_knowledge_cards c JOIN knowledge_card_choices x ON x.card_id=c.id AND x.scope='private' AND x.user_id=$1 WHERE c.owner_id=$1 AND c.status='active')`, uid).Scan(&selected)
	return selected, e
}

// Lock only the parent identity, never return somebody else's private body.
// Both submission and approval lock parents before submission rows, preventing
// duplicate publications and inverse-order deadlocks with a repeated submit.
func (s *Store) LockKnowledgeSubmissionSources(ctx context.Context, ids []string) error {
	rows, e := s.queryContext(ctx, `SELECT c.id FROM personal_knowledge_cards c WHERE c.id IN (SELECT private_card_id FROM knowledge_submissions WHERE id=ANY($1::text[])) ORDER BY c.id FOR UPDATE`, pq.Array(ids))
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return e
		}
	}
	return rows.Err()
}
func (s *Store) FindApprovedKnowledgeSubmission(ctx context.Context, id string) (*domain.KnowledgeSubmission, error) {
	var raw []byte
	e := s.queryRowContext(ctx, `SELECT body FROM knowledge_submissions WHERE private_card_id=$1 AND status='approved' ORDER BY private_revision DESC LIMIT 1`, id).Scan(&raw)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var item domain.KnowledgeSubmission
	e = json.Unmarshal(raw, &item)
	return &item, e
}
