package knowledgebase

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"aigo/internal/domain"
)

const ImportFormat = "aigo-knowledge-import-v1"
const MaxImportBytes = 2 << 20
const MaxImportCards = 50

type ImportCard struct {
	Kind       string                  `json:"kind"`
	Title      string                  `json:"title"`
	PointCodes []string                `json:"knowledge_point_codes"`
	Content    domain.KnowledgeContent `json:"content"`
	Source     string                  `json:"source"`
	Note       string                  `json:"note"`
}
type ImportDocument struct {
	Format string       `json:"format"`
	Cards  []ImportCard `json:"cards"`
}
type ImportRequest struct {
	VersionID string `json:"version_id"`
	Document  string `json:"document"`
	Token     string `json:"preview_token,omitempty"`
}
type ImportRow struct {
	Number  int                   `json:"number"`
	Card    *domain.KnowledgeCard `json:"card,omitempty"`
	Error   string                `json:"error,omitempty"`
	Warning string                `json:"warning,omitempty"`
}
type ImportPreview struct {
	Rows      []ImportRow `json:"rows"`
	Valid     bool        `json:"valid"`
	Token     string      `json:"preview_token,omitempty"`
	ExpiresAt time.Time   `json:"expires_at,omitempty"`
}
type ImportedCard struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Revision int    `json:"revision"`
}
type ImportResult struct {
	Saved int            `json:"saved"`
	Cards []ImportedCard `json:"cards"`
}
type importClaims struct {
	User, Role, Version, DocumentHash, RowsHash, Nonce string
	ExpiresAt                                          int64
}

func importHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func parseImport(raw string) (ImportDocument, error) {
	var doc ImportDocument
	if len(raw) > MaxImportBytes || !json.Valid([]byte(raw)) {
		return doc, fmt.Errorf("%w: 文件须为不超过2 MiB的有效JSON", ErrInvalid)
	}
	if err := uniqueImportFields(raw); err != nil {
		return doc, err
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&doc); err != nil {
		return doc, fmt.Errorf("%w: 导入结构错误：%s", ErrInvalid, err)
	}
	if doc.Format != ImportFormat || len(doc.Cards) == 0 || len(doc.Cards) > MaxImportCards {
		return doc, fmt.Errorf("%w: 格式须为%s，且每批1—50张卡", ErrInvalid, ImportFormat)
	}
	return doc, nil
}

// A file is reviewed before confirmation: duplicate/case-folded fields must not
// let two readers interpret a different card or state from the same document.
func uniqueImportFields(raw string) error {
	d := json.NewDecoder(strings.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 12 {
			return fmt.Errorf("%w: 导入JSON嵌套过深", ErrInvalid)
		}
		t, err := d.Token()
		if err != nil {
			return ErrInvalid
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				t, err = d.Token()
				if err != nil {
					return ErrInvalid
				}
				name, ok := t.(string)
				if !ok {
					return ErrInvalid
				}
				for _, ch := range name {
					if ch > 127 {
						return fmt.Errorf("%w: JSON字段名须使用模板中的英文名称", ErrInvalid)
					}
				}
				name = strings.ToLower(name)
				if seen[name] {
					return fmt.Errorf("%w: JSON存在重复字段", ErrInvalid)
				}
				seen[name] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			count := 0
			for d.More() {
				count++
				if count > MaxImportCards {
					return fmt.Errorf("%w: 导入数组最多50项，请拆分资料", ErrInvalid)
				}
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	return walk(0)
}

func (s *Service) prepareImport(ctx context.Context, req ImportRequest) (*ImportPreview, error) {
	doc, err := parseImport(req.Document)
	if err != nil {
		return nil, err
	}
	if req.VersionID == "" {
		return nil, fmt.Errorf("%w: 请选择大纲版本", ErrInvalid)
	}
	preview := &ImportPreview{Rows: []ImportRow{}, Valid: true}
	seen := map[string]bool{}
	for i, input := range doc.Cards {
		row := ImportRow{Number: i + 1}
		card := domain.KnowledgeCard{Kind: input.Kind, Title: strings.TrimSpace(input.Title), Source: strings.TrimSpace(input.Source), Note: input.Note, Status: "draft", Content: input.Content}
		if input.Kind == "authoring_guideline" {
			if len(input.PointCodes) != 0 {
				row.Error = "通用命题规范不绑定知识点"
			}
		} else if len(input.PointCodes) == 0 || len(input.PointCodes) > 20 {
			row.Error = "每卡须关联1—20个知识点代码"
		} else {
			card.KnowledgePoints, err = s.Store.ResolveKnowledgeImportPoints(ctx, req.VersionID, input.PointCodes)
			if err != nil {
				if !isImportInputError(err) {
					return nil, err
				}
				row.Error = err.Error()
			}
		}
		if row.Error == "" {
			if card.Content.Expression != nil {
				row.Error = "表达要求请在个人知识卡维护后提交共享"
			}
		}
		if row.Error == "" {
			if err = Validate(card, false); err != nil {
				row.Error = err.Error()
			}
		}
		raw, _ := json.Marshal(card)
		key := importHash(raw)
		if row.Error == "" && seen[key] {
			row.Error = "本文件包含重复知识卡，请移除重复条目"
		}
		seen[key] = true
		if row.Error == "" {
			row.Card = &card
			if err = Validate(card, true); err != nil {
				row.Warning = "可保存草稿；启用前需补充：" + err.Error()
			}
		} else {
			preview.Valid = false
		}
		preview.Rows = append(preview.Rows, row)
	}
	return preview, nil
}

func isImportInputError(err error) bool { return errors.Is(err, ErrInvalid) }

func (s *Service) PreviewImport(ctx context.Context, uid, role string, req ImportRequest, key []byte) (out *ImportPreview, err error) {
	err = s.Access(ctx, uid, role, func(ctx context.Context, caps Capabilities) error {
		if !caps.Manage {
			return ErrDenied
		}
		var e error
		out, e = s.prepareImport(ctx, req)
		if e != nil || !out.Valid {
			return e
		}
		if len(key) < 16 {
			return fmt.Errorf("导入确认密钥不可用")
		}
		nonce := make([]byte, 16)
		if _, e = rand.Read(nonce); e != nil {
			return e
		}
		raw, _ := json.Marshal(out.Rows)
		out.ExpiresAt = time.Now().UTC().Add(30 * time.Minute)
		claims := importClaims{uid, role, req.VersionID, importHash([]byte(req.Document)), importHash(raw), hex.EncodeToString(nonce), out.ExpiresAt.Unix()}
		body, _ := json.Marshal(claims)
		mac := hmac.New(sha256.New, key)
		mac.Write(body)
		out.Token = base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		return nil
	})
	if err == nil && out != nil {
		slog.Info("knowledge_import.previewed", "rows", len(out.Rows), "valid", out.Valid, "version_id", req.VersionID)
	}
	return
}

func (s *Service) ConfirmImport(ctx context.Context, uid, role string, req ImportRequest, key []byte) (out *ImportResult, err error) {
	var receiptID string
	var replayed bool
	err = s.Access(ctx, uid, role, func(ctx context.Context, caps Capabilities) error {
		if !caps.Manage {
			return ErrDenied
		}
		if len(req.Token) > 4000 || len(key) < 16 {
			return ErrConflict
		}
		parts := strings.Split(req.Token, ".")
		if len(parts) != 2 {
			return ErrConflict
		}
		body, e := base64.RawURLEncoding.DecodeString(parts[0])
		if e != nil {
			return ErrConflict
		}
		sig, e := base64.RawURLEncoding.DecodeString(parts[1])
		if e != nil {
			return ErrConflict
		}
		mac := hmac.New(sha256.New, key)
		mac.Write(body)
		var claims importClaims
		if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(body, &claims) != nil || claims.User != uid || claims.Role != role || claims.Version != req.VersionID || claims.DocumentHash != importHash([]byte(req.Document)) {
			return ErrConflict
		}
		id := importHash([]byte(req.Token))
		receiptID = id
		out, e = s.Store.BeginKnowledgeImport(ctx, id, uid, role, claims.DocumentHash)
		replayed = out != nil
		if e != nil || out != nil {
			return e
		}
		if time.Now().Unix() > claims.ExpiresAt {
			return fmt.Errorf("%w: 预览已过期，请重新预览", ErrConflict)
		}
		preview, e := s.prepareImport(ctx, req)
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(preview.Rows)
		if !preview.Valid || !bytes.Equal([]byte(importHash(raw)), []byte(claims.RowsHash)) {
			return fmt.Errorf("%w: 资料或知识点已变化，请重新预览", ErrConflict)
		}
		out = &ImportResult{Cards: []ImportedCard{}}
		for _, row := range preview.Rows {
			card, e := s.Save(ctx, uid, role, "", *row.Card)
			if e != nil {
				return e
			}
			out.Cards = append(out.Cards, ImportedCard{card.ID, card.Title, card.Kind, card.Status, card.Revision})
		}
		out.Saved = len(out.Cards)
		return s.Store.FinishKnowledgeImport(ctx, id, *out)
	})
	if err == nil && out != nil {
		slog.Info("knowledge_import.confirmed", "receipt_id", receiptID, "saved", out.Saved, "replayed", replayed)
	}
	return
}
