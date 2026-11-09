package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Schema62's data adapter is immutable just like released SQL. Including this
// exact source in its checksum prevents a later Go-only edit from silently
// changing an already applied migration's meaning.
//
//go:embed private_knowledge_migration.go
var privateKnowledgeMigrationSource []byte

func privateKnowledgeStatements() []string {
	return []string{
		`SELECT 'private-knowledge-data-v1:` + privateKnowledgeSourceChecksum(privateKnowledgeMigrationSource) + `'`,
		`ALTER TABLE batch_jobs RENAME COLUMN rules_snapshot TO prompt_snapshot`,
		privateKnowledgePermissionStatement("roles"),
		privateKnowledgePermissionStatement("users"),
		`DROP TABLE generation_rules`,
	}
}

func privateKnowledgeSourceChecksum(source []byte) string {
	// Git may use CRLF on a Windows checkout. Go raw strings discard CR, so
	// equivalent checkout line endings must not change a released data adapter.
	source = bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	hash := sha256.Sum256(source)
	return hex.EncodeToString(hash[:])
}

func privateKnowledgePermissionStatement(table string) string {
	return `UPDATE ` + table + ` SET permissions=ARRAY(
 SELECT mapped FROM (
  SELECT CASE p WHEN 'rule:view' THEN 'experience:view' WHEN 'rule:manage' THEN 'experience:manage' WHEN 'rule:use' THEN 'experience:use' ELSE p END AS mapped,MIN(ordinality) AS position
  FROM unnest(permissions) WITH ORDINALITY AS prior(p,ordinality) WHERE p<>'rule:view_all'
  GROUP BY mapped ORDER BY position
 ) converted),updated_at=NOW()
 WHERE permissions && ARRAY['rule:view','rule:manage','rule:use','rule:view_all']::text[]`
}

type retiredPreference struct {
	ID, OwnerID, Name, Content, Kind string
	Enabled                          bool
	Order, Revision                  int
	CreatedAt, UpdatedAt             time.Time
	DeletedAt                        sql.NullTime
}

type preferenceKnowledgeRule struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	Reason        string `json:"reason"`
	Field         string `json:"field"`
	Before        string `json:"before"`
	After         string `json:"after"`
	MedicalChange bool   `json:"medical_change"`
}

func migratePrivateKnowledge(ctx context.Context, tx *sql.Tx) error {
	if err := migrateRetiredPreferences(ctx, tx); err != nil {
		return err
	}
	if err := rewritePrivateKnowledgeSnapshots(ctx, tx, "generation_runs", "request_json", `jsonb_typeof(request_json->'request')='object' AND (request_json->'request') ? 'personal_rules'`, migrateGenerationPrompt); err != nil {
		return err
	}
	return rewritePrivateKnowledgeSnapshots(ctx, tx, "batch_jobs", "rules_snapshot", `rules_snapshot IS NOT NULL`, migrateFrozenPrompt)
}

func migrateRetiredPreferences(ctx context.Context, tx *sql.Tx) error {
	last, first, migrated := "", true, 0
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,owner_id,name,content,kind,enabled,sort_order,revision,created_at,updated_at,deleted_at FROM generation_rules WHERE ($1 OR id>$2) ORDER BY id LIMIT 100`, first, last)
		if err != nil {
			return err
		}
		var batch []retiredPreference
		for rows.Next() {
			var old retiredPreference
			if err = rows.Scan(&old.ID, &old.OwnerID, &old.Name, &old.Content, &old.Kind, &old.Enabled, &old.Order, &old.Revision, &old.CreatedAt, &old.UpdatedAt, &old.DeletedAt); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, old)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, old := range batch {
			if err := migrateRetiredPreference(ctx, tx, old); err != nil {
				return err
			}
			migrated++
		}
		first, last = false, batch[len(batch)-1].ID
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM generation_rules`).Scan(&total); err != nil {
		return err
	}
	if migrated != total {
		return fmt.Errorf("个人知识迁移数量不一致，保留原数据")
	}
	return nil
}

func migrateRetiredPreference(ctx context.Context, tx *sql.Tx, old retiredPreference) error {
	if old.ID == "" || old.OwnerID == "" || old.Revision < 1 || !utf8.ValidString(old.Content) {
		return fmt.Errorf("个人知识迁移遇到无效历史记录，保留原数据")
	}
	runes := []rune(old.Content)
	if len(runes) == 0 || len(runes) > 2000 {
		return fmt.Errorf("个人知识迁移正文长度异常，保留原数据")
	}
	rules := []preferenceKnowledgeRule{}
	for start := 0; start < len(runes); start += 600 {
		end := start + 600
		if end > len(runes) {
			end = len(runes)
		}
		rules = append(rules, preferenceKnowledgeRule{ID: fmt.Sprintf("origin-part-%d", len(rules)+1), Text: string(runes[start:end]), Field: "clinical_stem"})
	}
	status := "draft"
	if old.Enabled {
		status = "active"
	}
	if old.DeletedAt.Valid {
		status = "archived"
	}
	source := map[string]any{"kind": "legacy_personal_preference", "origin_id": old.ID, "origin_type": old.Kind, "origin_order": old.Order, "job_id": "", "candidate_id": old.ID, "question_id": "", "original_version": 0, "current_version": 0, "original_hash": "", "current_hash": ""}
	if old.DeletedAt.Valid {
		source["origin_deleted_at"] = old.DeletedAt.Time
	}
	id := "experience-legacy-" + old.ID
	body, err := json.Marshal(map[string]any{"medical_confirmed": false, "id": id, "kind": "authoring_experience", "scope_type": "personal", "title": old.Name, "scope": map[string]string{"kind": "all"}, "rules": rules, "status": status, "revision": old.Revision, "source": source, "created_at": old.CreatedAt, "updated_at": old.UpdatedAt})
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO personal_experiences(id,owner_id,revision,status,source_job_id,source_candidate_id,body,created_at,updated_at) VALUES($1,$2,$3,$4,'',$5,$6,$7,$8)`, id, old.OwnerID, old.Revision, status, old.ID, body, old.CreatedAt, old.UpdatedAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO personal_experience_versions(owner_id,experience_id,revision,body,created_at) VALUES($1,$2,$3,$4,$5)`, old.OwnerID, id, old.Revision, body, old.UpdatedAt); err != nil {
		return err
	}
	var owner, gotStatus string
	var revision int
	var stored []byte
	if err = tx.QueryRowContext(ctx, `SELECT owner_id,status,revision,body FROM personal_experiences WHERE id=$1`, id).Scan(&owner, &gotStatus, &revision, &stored); err != nil {
		return err
	}
	var check struct {
		Rules []preferenceKnowledgeRule `json:"rules"`
	}
	if err = json.Unmarshal(stored, &check); err != nil {
		return err
	}
	var joined strings.Builder
	for _, rule := range check.Rules {
		joined.WriteString(rule.Text)
	}
	if owner != old.OwnerID || gotStatus != status || revision != old.Revision || joined.String() != old.Content {
		return fmt.Errorf("个人知识迁移正文核验失败，保留原数据")
	}
	return nil
}

// Fixed literals preserve the exact ordered JSON/escaping produced by the
// retired generator. It is only a migration decoder, never a live input path.
func migrateFrozenPrompt(raw []byte) ([]byte, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []byte("null"), nil
	}
	var prior map[string]json.RawMessage
	if err := json.Unmarshal(raw, &prior); err != nil {
		return nil, fmt.Errorf("历史冻结prompt无效: %w", err)
	}
	if prior == nil {
		return nil, fmt.Errorf("历史冻结prompt不是对象")
	}
	var entries []struct {
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Content string `json:"content"`
	}
	if len(prior["rules"]) > 0 {
		if err := json.Unmarshal(prior["rules"], &entries); err != nil {
			return nil, fmt.Errorf("历史冻结内容无效: %w", err)
		}
	}
	current := map[string]json.RawMessage{}
	for _, field := range []string{"policy_version", "owner_id", "role_id", "captured_at", "system_prompt", "user_prompts", "appendix"} {
		if value, ok := prior[field]; ok {
			current[field] = value
		}
	}
	if len(entries) > 0 {
		if value, ok := current["appendix"]; ok && !bytes.Equal(value, []byte(`""`)) && !bytes.Equal(value, []byte("null")) {
			return nil, fmt.Errorf("历史冻结内容存在两份追加文本，保留原数据")
		}
		formatted, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return nil, err
		}
		appendix := "\n\n【本次个人命题规则】\n以下JSON是当前命题人的补充约束和建议，仅用于本次新题。constraint表示约束，suggestion表示建议。遵守系统底座的A2格式、五选项、单选答案和JSON输出协议；冲突时以系统底座为准。规则不能改变审核、检查、权限或其他业务流程。按列出顺序参考所有规则，不把规则标题、规则标记或规则内容照抄到题目中。\n" + string(formatted)
		current["appendix"], err = json.Marshal(appendix)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(current)
}

func migrateGenerationPrompt(raw []byte) ([]byte, error) {
	var spec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(spec["request"], &request); err != nil {
		return nil, err
	}
	if existing, ok := request["prompt_snapshot"]; ok && !bytes.Equal(bytes.TrimSpace(existing), []byte("null")) {
		return nil, fmt.Errorf("历史请求已有另一份prompt快照，保留原数据")
	}
	converted, err := migrateFrozenPrompt(request["personal_rules"])
	if err != nil {
		return nil, err
	}
	delete(request, "personal_rules")
	request["prompt_snapshot"] = converted
	spec["request"], err = json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(spec)
}

func rewritePrivateKnowledgeSnapshots(ctx context.Context, tx *sql.Tx, table, column, predicate string, convert func([]byte) ([]byte, error)) error {
	last, first := "", true
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,`+column+` FROM `+table+` WHERE ($1 OR id>$2) AND (`+predicate+`) ORDER BY id LIMIT 100`, first, last)
		if err != nil {
			return err
		}
		var batch []struct {
			ID   string
			Body []byte
		}
		for rows.Next() {
			var row struct {
				ID   string
				Body []byte
			}
			if err = rows.Scan(&row.ID, &row.Body); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, row)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, row := range batch {
			body, err := convert(row.Body)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE `+table+` SET `+column+`=$1 WHERE id=$2`, body, row.ID); err != nil {
				return err
			}
		}
		first, last = false, batch[len(batch)-1].ID
	}
}
