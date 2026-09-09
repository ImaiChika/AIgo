package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"github.com/lib/pq"
)

const kpColumns = `p.id,p.category,p.subject,p.unit,p.sub_item,p.topic,p.outline_code,p.keywords,p.version_id,v.name,v.year,p.revision`
const kpFrom = ` FROM knowledge_points p JOIN knowledge_versions v ON v.id=p.version_id AND v.deleted_at IS NULL `

func (s *Store) ListKnowledgeVersions(ctx context.Context) ([]domain.KnowledgeVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.id,v.name,v.year,v.description,v.status,v.created_at,v.published_at,COUNT(p.id)
 FROM knowledge_versions v LEFT JOIN knowledge_points p ON p.version_id=v.id AND p.deleted_at IS NULL
 WHERE v.deleted_at IS NULL GROUP BY v.id ORDER BY v.year DESC,v.created_at DESC,v.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.KnowledgeVersion{}
	for rows.Next() {
		var v domain.KnowledgeVersion
		if err := rows.Scan(&v.ID, &v.Name, &v.Year, &v.Description, &v.Status, &v.CreatedAt, &v.PublishedAt, &v.PointCount); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
func knowledgeError(err error) error {
	var pg *pq.Error
	if errors.As(err, &pg) && pg.Code == "23505" {
		return storage.ErrKnowledgeConflict
	}
	if errors.As(err, &pg) && pg.Code == "23503" {
		return storage.ErrKnowledgeNotFound
	}
	return err
}
func (s *Store) CreateKnowledgeVersion(ctx context.Context, v domain.KnowledgeVersion) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO knowledge_versions (id,name,year,description,status,created_at) VALUES ($1,$2,$3,$4,'draft',$5)`, v.ID, v.Name, v.Year, v.Description, v.CreatedAt)
	return knowledgeError(err)
}

// One transaction lock serializes imports, activation and edits, including across
// server processes. A full import is either entirely visible or entirely rolled back.
func (s *Store) knowledgeTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4704385982817583184)`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func (s *Store) PublishKnowledgeVersion(ctx context.Context, id string) error {
	tx, err := s.knowledgeTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireKnowledgeVersion(ctx, tx, id); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_points WHERE version_id=$1 AND deleted_at IS NULL`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("版本没有知识点，请先导入或新增")
	}
	_, err = tx.ExecContext(ctx, `UPDATE knowledge_versions SET status='published',published_at=COALESCE(published_at,NOW()) WHERE id=$1`, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func pointFingerprint(p domain.KnowledgePoint) string {
	// Include keywords: updating only keywords is still an actual edit.
	b, _ := json.Marshal([]any{p.Category, p.Subject, p.Unit, p.SubItem, p.Topic, p.OutlineCode, strings.Join(p.Keywords, "\x00")})
	return string(b)
}
func scanKP(row interface{ Scan(...any) error }) (*domain.KnowledgePoint, error) {
	var p domain.KnowledgePoint
	err := row.Scan(&p.ID, &p.Category, &p.Subject, &p.Unit, &p.SubItem, &p.Topic, &p.OutlineCode, pqArrayScanner(&p.Keywords), &p.VersionID, &p.VersionName, &p.VersionYear, &p.Revision)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
func scanPoints(rows *sql.Rows) ([]domain.KnowledgePoint, error) {
	result := []domain.KnowledgePoint{}
	for rows.Next() {
		p, err := scanKP(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *p)
	}
	return result, rows.Err()
}
func (s *Store) GetPoint(ctx context.Context, id string) (*domain.KnowledgePoint, error) {
	return scanKP(s.db.QueryRowContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.id=$1 AND p.deleted_at IS NULL`, id))
}
func (s *Store) ListPoints(ctx context.Context) ([]domain.KnowledgePoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.deleted_at IS NULL ORDER BY p.outline_code,p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoints(rows)
}
func (s *Store) SearchPoints(ctx context.Context, keyword string) ([]domain.KnowledgePoint, error) {
	clauses := []string{"p.deleted_at IS NULL"}
	args := []any{}
	for _, token := range storage.SearchTokens(keyword) {
		args = append(args, "%"+escapeLike(token)+"%")
		clauses = append(clauses, fmt.Sprintf("LOWER(CONCAT_WS(' ',p.category,p.topic,p.unit,p.sub_item,p.subject,p.outline_code,array_to_string(p.keywords,' '))) LIKE $%d", len(args)))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE `+strings.Join(clauses, " AND ")+` ORDER BY p.outline_code,p.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoints(rows)
}
func (s *Store) ListBySubject(ctx context.Context, subject string) ([]domain.KnowledgePoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.deleted_at IS NULL AND p.subject=$1 ORDER BY p.outline_code,p.id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoints(rows)
}
func (s *Store) KPCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_points WHERE deleted_at IS NULL`).Scan(&n)
	return n, err
}
func (s *Store) SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, int, int, error) {
	version := domain.LegacyKnowledgeVersion
	if len(points) > 0 && points[0].VersionID != "" {
		version = points[0].VersionID
	}
	return s.SaveVersionPoints(ctx, version, points, false)
}
func (s *Store) SaveVersionPoints(ctx context.Context, version string, points []domain.KnowledgePoint, replace bool) (int, int, int, error) {
	tx, err := s.knowledgeTx(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_versions WHERE id=$1 AND deleted_at IS NULL)`, version).Scan(&exists); err != nil {
		return 0, 0, 0, err
	}
	if !exists {
		return 0, 0, 0, storage.ErrKnowledgeNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+kpColumns+`,p.deleted_at IS NOT NULL`+kpFrom+`WHERE p.version_id=$1`, version)
	if err != nil {
		return 0, 0, 0, err
	}
	byID := map[string]domain.KnowledgePoint{}
	byCode := map[string]string{}
	deleted := map[string]bool{}
	for rows.Next() {
		var p domain.KnowledgePoint
		var gone bool
		err = rows.Scan(&p.ID, &p.Category, &p.Subject, &p.Unit, &p.SubItem, &p.Topic, &p.OutlineCode, pqArrayScanner(&p.Keywords), &p.VersionID, &p.VersionName, &p.VersionYear, &p.Revision, &gone)
		if err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		byID[p.ID] = p
		deleted[p.ID] = gone
		if p.OutlineCode != "" && !gone {
			byCode[p.OutlineCode] = p.ID
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, 0, err
	}
	inserted, updated, duplicated := 0, 0, 0
	ids := []string{}
	for _, p := range points {
		if p.VersionID != "" && p.VersionID != version {
			return 0, 0, 0, storage.ErrKnowledgeConflict
		}
		p.VersionID = version
		if id := byCode[p.OutlineCode]; id != "" {
			p.ID = id
		}
		if p.ID == "" {
			p.ID = domain.ScopedKnowledgeID(version, p.OutlineCode)
		}
		ids = append(ids, p.ID)
		old, ok := byID[p.ID]
		if ok && !deleted[p.ID] && pointFingerprint(old) == pointFingerprint(p) {
			duplicated++
			continue
		}
		if ok && !deleted[p.ID] {
			updated++
		} else {
			inserted++
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO knowledge_points (id,version_id,category,subject,unit,sub_item,topic,outline_code,outline_ref,keywords)
   VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,$9) ON CONFLICT (id) DO UPDATE SET
   category=EXCLUDED.category,subject=EXCLUDED.subject,unit=EXCLUDED.unit,sub_item=EXCLUDED.sub_item,topic=EXCLUDED.topic,
   outline_code=EXCLUDED.outline_code,outline_ref=EXCLUDED.outline_ref,keywords=EXCLUDED.keywords,
   revision=knowledge_points.revision+1,deleted_at=NULL,updated_at=NOW() WHERE knowledge_points.version_id=EXCLUDED.version_id`,
			p.ID, version, p.Category, p.Subject, p.Unit, p.SubItem, p.Topic, p.OutlineCode, pqArray(p.Keywords))
		if e != nil {
			return 0, 0, 0, knowledgeError(e)
		}
		n, e := res.RowsAffected()
		if e != nil {
			return 0, 0, 0, e
		}
		if n == 0 {
			return 0, 0, 0, storage.ErrKnowledgeConflict
		}
		byID[p.ID] = p
		deleted[p.ID] = false
		if p.OutlineCode != "" {
			byCode[p.OutlineCode] = p.ID
		}
	}
	if replace {
		if len(ids) == 0 {
			return 0, 0, 0, fmt.Errorf("不能用空文档替换大纲")
		}
		_, err = tx.ExecContext(ctx, `UPDATE knowledge_points SET deleted_at=NOW(),updated_at=NOW(),revision=revision+1 WHERE version_id=$1 AND deleted_at IS NULL AND NOT(id=ANY($2))`, version, pqArray(ids))
		if err != nil {
			return 0, 0, 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return inserted, updated, duplicated, nil
}

// Revision zero is insert-only; positive revisions use optimistic concurrency.
func (s *Store) UpdatePoint(ctx context.Context, p domain.KnowledgePoint) error {
	tx, err := s.knowledgeTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := requireKnowledgeVersion(ctx, tx, p.VersionID); err != nil {
		return err
	}
	args := []any{p.ID, p.VersionID, p.Category, p.Subject, p.Unit, p.SubItem, p.Topic, p.OutlineCode, pqArray(p.Keywords)}
	if p.Revision == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_points (id,version_id,category,subject,unit,sub_item,topic,outline_code,outline_ref,keywords) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,$9)`, args...)
	} else {
		args = append(args, p.Revision)
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE knowledge_points SET category=$3,subject=$4,unit=$5,sub_item=$6,topic=$7,outline_code=$8,outline_ref=$8,keywords=$9,revision=revision+1,updated_at=NOW() WHERE id=$1 AND version_id=$2 AND revision=$10 AND deleted_at IS NULL`, args...)
		if err == nil {
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				return storage.ErrKnowledgeConflict
			}
		}
	}
	if err != nil {
		return knowledgeError(err)
	}
	return tx.Commit()
}
func (s *Store) DeletePoint(ctx context.Context, id string) error {
	tx, err := s.knowledgeTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE knowledge_points SET deleted_at=NOW(),updated_at=NOW(),revision=revision+1 WHERE id=$1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return storage.ErrKnowledgeNotFound
	}
	return tx.Commit()
}

func (s *Store) ListVersionPoints(ctx context.Context, version string) ([]domain.KnowledgePoint, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.version_id=$1 AND p.deleted_at IS NULL ORDER BY p.outline_code,p.id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoints(rows)
}

func requireKnowledgeVersion(ctx context.Context, tx *sql.Tx, id string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_versions WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return storage.ErrKnowledgeNotFound
	}
	return nil
}

// DeleteKnowledgeVersion removes the entire syllabus atomically from active use.
// Historical rows and the independent question/job snapshots remain available for audit.
func (s *Store) DeleteKnowledgeVersion(ctx context.Context, id string) (int, error) {
	tx, err := s.knowledgeTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := requireKnowledgeVersion(ctx, tx, id); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE knowledge_points SET deleted_at=NOW(),updated_at=NOW(),revision=revision+1 WHERE version_id=$1 AND deleted_at IS NULL`, id)
	if err != nil {
		return 0, err
	}
	count, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_versions SET deleted_at=NOW() WHERE id=$1`, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(count), nil
}
