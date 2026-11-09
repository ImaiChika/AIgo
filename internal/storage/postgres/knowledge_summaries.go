package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"encoding/json"
	"fmt"
)

// jsonb_to_record detoasts the body into only the fields required for a list.
// No complete body/content/note/origin object crosses the DB driver boundary.
const summaryRecord = ` CROSS JOIN LATERAL jsonb_to_record(c.body) AS meta(title text,kind text,source text,content jsonb,knowledge_points jsonb,origin_submission_id text,updated_at text) `

func boundedPreviewSQL(expr string, n int) string {
	return fmt.Sprintf(`left(COALESCE(%s,''),%d)||CASE WHEN char_length(COALESCE(%s,''))>%d THEN '…' ELSE '' END`, expr, n, expr, n)
}
func summaryProjection(selected string) string {
	excerpt := `COALESCE(NULLIF(meta.content#>>'{expression,instructions}',''),NULLIF(meta.content#>>'{expression,explanation_note}',''),NULLIF(meta.content->>'custom_text',''),NULLIF(meta.content#>>'{facts,0,text}',''),meta.content#>>'{reference,stem}','')`
	return `jsonb_build_object('is_summary',true,'id',c.id,'revision',c.revision,'kind',meta.kind,'has_expression',COALESCE(jsonb_typeof(meta.content->'expression')='object',false),'title',meta.title,'status',c.status,'updated_at',COALESCE(meta.updated_at,'0001-01-01T00:00:00Z'),'origin_submission_id',meta.origin_submission_id,'selected',` + selected + `,'excerpt',` + boundedPreviewSQL(excerpt, 160) + `,'source_excerpt',` + boundedPreviewSQL("meta.source", 200) + `,'knowledge_points',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p->>'id','version_id',p->>'version_id','version_name',p->>'version_name','version_year',p->'version_year','topic',p->>'topic') ORDER BY ord) FROM jsonb_array_elements(CASE WHEN jsonb_typeof(meta.knowledge_points)='array' THEN meta.knowledge_points ELSE '[]'::jsonb END) WITH ORDINALITY AS point(p,ord)),'[]'::jsonb))`
}
func (s *Store) ListKnowledgeSummaries(ctx context.Context, f knowledgebase.Filter) ([]domain.KnowledgeCardSummary, int, error) {
	where, args := knowledgeCardsWhere(f)
	return s.readKnowledgeSummaries(ctx, "knowledge_cards", where, args, f, "NULL")
}
func (s *Store) ListPrivateKnowledgeSummaries(ctx context.Context, uid string, f knowledgebase.Filter) ([]domain.KnowledgeCardSummary, int, error) {
	where, args := privateKnowledgeWhere(uid, f)
	selected := `EXISTS(SELECT 1 FROM knowledge_card_choices x WHERE x.user_id=c.owner_id AND x.scope='private' AND x.card_id=c.id)`
	return s.readKnowledgeSummaries(ctx, "personal_knowledge_cards", where, args, f, selected)
}
func (s *Store) readKnowledgeSummaries(ctx context.Context, table, where string, args []any, f knowledgebase.Filter, selected string) ([]domain.KnowledgeCardSummary, int, error) {
	// table and expressions are internal constants; all external filters are bound.
	var total int
	if e := s.queryRowContext(ctx, "SELECT count(*) FROM "+table+" c WHERE "+where, args...).Scan(&total); e != nil {
		return nil, 0, e
	}
	args = append(args, f.Limit, f.Offset)
	query := fmt.Sprintf(`SELECT %s FROM %s c %s WHERE %s ORDER BY c.updated_at DESC,c.id LIMIT $%d OFFSET $%d`, summaryProjection(selected), table, summaryRecord, where, len(args)-1, len(args))
	rows, e := s.queryContext(ctx, query, args...)
	if e != nil {
		return nil, 0, e
	}
	defer rows.Close()
	items := []domain.KnowledgeCardSummary{}
	for rows.Next() {
		var raw []byte
		var item domain.KnowledgeCardSummary
		if e = rows.Scan(&raw); e != nil {
			return nil, 0, e
		}
		if e = json.Unmarshal(raw, &item); e != nil {
			return nil, 0, e
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}
