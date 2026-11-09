package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

func knowledgeImportMigrationStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS knowledge_import_receipts (id TEXT PRIMARY KEY,owner_id TEXT NOT NULL,role_id TEXT NOT NULL,input_digest TEXT NOT NULL,result JSONB NOT NULL DEFAULT '{}',created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`}
}

func (s *Store) ResolveKnowledgeImportPoints(ctx context.Context, version string, codes []string) ([]domain.KnowledgePoint, error) {
	if reviewTransactionFromContext(ctx) == nil {
		return nil, knowledgebase.ErrDenied
	}
	if _, err := s.execContext(ctx, `SELECT pg_advisory_xact_lock_shared(4704385982817583184)`); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	points := []domain.KnowledgePoint{}
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" || len(code) > 160 || seen[code] {
			return nil, fmt.Errorf("%w: 知识点代码为空、重复或过长", knowledgebase.ErrInvalid)
		}
		seen[code] = true
		p, err := scanKP(s.queryRowContext(ctx, `SELECT `+kpColumns+kpFrom+`WHERE p.version_id=$1 AND p.outline_code=$2 AND p.deleted_at IS NULL FOR SHARE OF p`, version, code))
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("%w: 所选大纲中未找到知识点代码 %s", knowledgebase.ErrInvalid, code)
		}
		points = append(points, *p)
	}
	return points, nil
}

func (s *Store) BeginKnowledgeImport(ctx context.Context, id, uid, role, digest string) (*knowledgebase.ImportResult, error) {
	if reviewTransactionFromContext(ctx) == nil {
		return nil, knowledgebase.ErrDenied
	}
	result, err := s.execContext(ctx, `INSERT INTO knowledge_import_receipts(id,owner_id,role_id,input_digest) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, id, uid, role, digest)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 1 {
		return nil, nil
	}
	var owner, identity, hash string
	var raw []byte
	if err = s.queryRowContext(ctx, `SELECT owner_id,role_id,input_digest,result FROM knowledge_import_receipts WHERE id=$1`, id).Scan(&owner, &identity, &hash, &raw); err != nil {
		return nil, err
	}
	if owner != uid || identity != role || hash != digest {
		return nil, knowledgebase.ErrConflict
	}
	var replay knowledgebase.ImportResult
	if err = json.Unmarshal(raw, &replay); err != nil {
		return nil, err
	}
	if replay.Saved < 1 || len(replay.Cards) != replay.Saved {
		return nil, fmt.Errorf("导入回执不完整")
	}
	return &replay, nil
}
func (s *Store) FinishKnowledgeImport(ctx context.Context, id string, result knowledgebase.ImportResult) error {
	if reviewTransactionFromContext(ctx) == nil {
		return knowledgebase.ErrDenied
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	res, err := s.execContext(ctx, `UPDATE knowledge_import_receipts SET result=$2 WHERE id=$1 AND result='{}'::jsonb`, id, raw)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return knowledgebase.ErrConflict
	}
	return nil
}
