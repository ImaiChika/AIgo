package postgres

import (
	"aigo/internal/domain"
	"context"
	"github.com/lib/pq"
)

func (s *Store) SearchAuthoringExperiences(ctx context.Context, owner, query, status string, limit, offset int) ([]domain.AuthoringExperience, int, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where := ` FROM personal_experiences WHERE owner_id=$1 AND ($2='' OR strpos(lower(COALESCE(body->>'title','')),lower($2))>0) AND ($3='' OR status=$3)`
	var total int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*)`+where, owner, query, status).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.queryContext(ctx, `SELECT body,owner_id`+where+` ORDER BY updated_at DESC,id LIMIT $4 OFFSET $5`, owner, query, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []domain.AuthoringExperience{}
	for rows.Next() {
		item, err := scanAuthoringExperience(rows)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, *item)
	}
	return result, total, rows.Err()
}

func (s *Store) MatchAuthoringExperiences(ctx context.Context, owner string, points, professions []string, limit int, allowedOrigins ...string) ([]domain.AuthoringExperience, int, error) {
	if err := learningOwner(ctx, owner); err != nil {
		return nil, 0, err
	}
	if len(points) == 0 {
		return []domain.AuthoringExperience{}, 0, nil
	}
	if limit < 1 || limit > 200 {
		limit = 200
	}
	where := ` FROM personal_experiences WHERE owner_id=$1 AND status='active'
 AND ($4::text[] IS NULL OR body#>>'{source,kind}'=ANY($4::text[]))
 AND (body->>'medical_confirmed'='true' OR NOT EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(body->'rules')='array' THEN body->'rules' ELSE '[]'::jsonb END) r WHERE r->>'medical_change'='true'))
 AND (body#>>'{scope,kind}'='all' OR (body#>>'{scope,kind}'='point' AND COALESCE(body#>'{scope,point_ids}','[]'::jsonb) ?| $2::text[]) OR (body#>>'{scope,kind}'='profession' AND btrim(body#>>'{scope,profession}')=ANY($3::text[])))`
	var total int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*)`+where, owner, pq.Array(points), pq.Array(professions), pq.Array(allowedOrigins)).Scan(&total); err != nil {
		return nil, 0, err
	}
	// Prioritize the migrated enabled preference before LIMIT; doing this only
	// in the in-memory freeze pass would lose it beyond 200 newer point cards.
	rows, err := s.queryContext(ctx, `SELECT body,owner_id`+where+` ORDER BY CASE WHEN body#>>'{source,kind}'='legacy_personal_preference' THEN 1 ELSE 0 END DESC,CASE body#>>'{scope,kind}' WHEN 'point' THEN 3 WHEN 'profession' THEN 2 ELSE 1 END DESC,updated_at DESC,id LIMIT $5`, owner, pq.Array(points), pq.Array(professions), pq.Array(allowedOrigins), limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := []domain.AuthoringExperience{}
	for rows.Next() {
		item, err := scanAuthoringExperience(rows)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, *item)
	}
	return result, total, rows.Err()
}
