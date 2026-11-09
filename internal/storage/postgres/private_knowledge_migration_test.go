//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
)

func privateKnowledge61Store(t *testing.T) *Store {
	t.Helper()
	_, dsn, cleanup := migrationTestSchema(t)
	t.Cleanup(cleanup)
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:61])
	if _, err = s.db.ExecContext(t.Context(), `INSERT INTO roles(id,name,permissions) VALUES
 ('old-role','old-role',ARRAY['rule:view','rule:manage','rule:use','rule:view_all','learning:use','learning:use','question:generate']),
 ('dormant-role','dormant-role',ARRAY['rule:use','question:generate']),
 ('revoked-role','revoked-role',ARRAY['question:generate']);
 INSERT INTO users(id,username,password_hash,role,roles,permissions)VALUES
 ('preference-owner','preference-owner','unused','old-role',ARRAY['old-role','dormant-role'],ARRAY['rule:view','rule:manage','rule:use']),
 ('preference-other','preference-other','unused','dormant-role',ARRAY['dormant-role'],ARRAY['rule:use','question:generate'])`); err != nil {
		t.Fatal(err)
	}
	return s
}

func privateKnowledgeJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func releasedFrozenPreferenceFixture() map[string]any {
	return map[string]any{"policy_version": "a2-clinical-20261024-v5", "owner_id": "preference-owner", "role_id": "old-role", "captured_at": "2026-10-24T08:00:00Z", "system_prompt": "旧系统底座", "user_prompts": map[string]string{"frozen-key": "旧用户底座"}, "rules": []map[string]any{
		{"name": "旧表达 <tag> & \u2028", "kind": "constraint", "content": "冻结中文 \"引号\"\\路径\n第二行 😀 \u2029", "owner_id": "MUST-NOT-REACH-MODEL", "revision": 123},
		{"name": "次序二", "kind": "suggestion", "content": "第二条冻结要求"},
	}}
}

func TestPrivateKnowledgeMigrationPreservesPrivatePreferencesAndFrozenJobs(t *testing.T) {
	s := privateKnowledge61Store(t)
	ctx := t.Context()
	created := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2026, 10, 24, 8, 9, 10, 0, time.UTC)
	content := strings.Repeat("汉😀", 1000)
	for _, c := range []struct {
		id, owner, name, text, kind string
		enabled                     bool
		order, revision             int
		deleted                     any
	}{
		{"old-long", "preference-owner", "原2000字偏好", content, "constraint", true, 2, 7, nil},
		{"old-draft", "preference-owner", "未启用偏好", "保留换行\n第二行，原文不裁剪。", "suggestion", false, 0, 3, nil},
		{"old-deleted", "preference-other", "已删除偏好", "不能把旧删除记录当作启用条目。", "constraint", true, 1, 4, updated},
	} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO generation_rules(id,owner_id,name,content,kind,enabled,sort_order,revision,created_at,updated_at,deleted_at)VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, c.id, c.owner, c.name, c.text, c.kind, c.enabled, c.order, c.revision, created, updated, c.deleted); err != nil {
			t.Fatal(err)
		}
	}
	// Deleted history can exceed the former 100-live-item UI limit. Exercise the
	// migration's bounded page boundary without losing archived records.
	for i := 0; i < 121; i++ {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO generation_rules(id,owner_id,name,content,kind,enabled,revision,deleted_at)VALUES($1,'preference-other','旧删除历史',$1,'suggestion',FALSE,2,NOW())`, fmt.Sprintf("history-%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	const priorCard = `{"id":"pre-existing-c5","title":"原C5卡","status":"draft","revision":1,"kind":"authoring_experience","scope_type":"personal","rules":[],"source":{"kind":"learning"}}`
	if _, err := s.db.ExecContext(ctx, `INSERT INTO personal_experiences(id,owner_id,revision,status,source_job_id,source_candidate_id,body)VALUES('pre-existing-c5','preference-owner',1,'draft','prior-paid-job','prior-candidate',$1)`, priorCard); err != nil {
		t.Fatal(err)
	}
	var priorBody string
	if err := s.db.QueryRowContext(ctx, `SELECT body::text FROM personal_experiences WHERE id='pre-existing-c5'`).Scan(&priorBody); err != nil {
		t.Fatal(err)
	}
	frozen := releasedFrozenPreferenceFixture()
	spec := map[string]any{"request": map[string]any{"count": 1, "personal_rules": frozen, "subject": "旧任务专业"}, "actor": "旧作者", "owner_id": "preference-owner"}
	for _, status := range []string{"pending", "running", "succeeded", "failed"} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO generation_runs(id,owner_id,status,requested_count,request_json)VALUES($1,'preference-owner',$2,1,$3)`, "frozen-"+status, status, privateKnowledgeJSON(t, spec)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO generation_runs(id,owner_id,status,requested_count,request_json)VALUES('without-frozen','preference-owner','pending',1,'{"request":{"count":1}}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO batch_jobs(id,owner_id,status,rules_snapshot,request_hash)VALUES('old-batch','preference-owner','in_progress',$1,'EXISTING-RECEIPT-UNCHANGED')`, privateKnowledgeJSON(t, frozen)); err != nil {
		t.Fatal(err)
	}
	before := map[int64]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int64
		var hash string
		if err = rows.Scan(&v, &hash); err != nil {
			t.Fatal(err)
		}
		before[v] = hash
	}
	rows.Close()
	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != int(LatestSchemaVersion-61) || applied[0].Version != 62 {
		t.Fatalf("expected forward upgrade through immutable Schema62, got %+v", applied)
	}
	var oldTable sql.NullString
	if err = s.db.QueryRowContext(ctx, `SELECT to_regclass('generation_rules')::text`).Scan(&oldTable); err != nil || oldTable.Valid {
		t.Fatal("retired runtime table retained", oldTable, err)
	}
	var legacyCount, versions, jobs int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM personal_experiences WHERE body->'source'->>'kind'='legacy_personal_preference'`).Scan(&legacyCount); err != nil || legacyCount != 124 {
		t.Fatal(legacyCount, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM personal_experience_versions`).Scan(&versions); err != nil || versions != 124 {
		t.Fatal("fabricated or lost version history", versions, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM learning_analysis_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("migration fabricated paid learning jobs", jobs, err)
	}
	var owner, status string
	var revision int
	var body []byte
	var gotCreated, gotUpdated time.Time
	if err = s.db.QueryRowContext(ctx, `SELECT owner_id,status,revision,body,created_at,updated_at FROM personal_experiences WHERE id='experience-legacy-old-long'`).Scan(&owner, &status, &revision, &body, &gotCreated, &gotUpdated); err != nil {
		t.Fatal(err)
	}
	var card struct {
		Title  string `json:"title"`
		Rules  []struct{ ID, Text, Field string }
		Source struct {
			Kind, OriginID, OriginType, JobID, QuestionID string
			OriginOrder                                   int
		}
	}
	// Decode source with the public snake_case keys independently of domain DTOs.
	var rawCard map[string]json.RawMessage
	if err = json.Unmarshal(body, &rawCard); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(body, &card); err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if err = json.Unmarshal(rawCard["source"], &source); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	seen := map[string]bool{}
	for _, part := range card.Rules {
		if part.ID == "" || seen[part.ID] || part.Field != "clinical_stem" || utf8.RuneCountInString(part.Text) > 600 {
			t.Fatal("invalid split", part)
		}
		seen[part.ID] = true
		joined.WriteString(part.Text)
	}
	if joined.String() != content || len(card.Rules) != 4 || owner != "preference-owner" || status != "active" || revision != 7 || !created.Equal(gotCreated) || !updated.Equal(gotUpdated) || card.Title != "原2000字偏好" {
		t.Fatal("current content or metadata changed")
	}
	if source["kind"] != "legacy_personal_preference" || source["origin_id"] != "old-long" || source["origin_type"] != "constraint" || source["origin_order"] != float64(2) || source["job_id"] != "" || source["question_id"] != "" {
		t.Fatal("false source provenance", source)
	}
	var deleted string
	if err = s.db.QueryRowContext(ctx, `SELECT status FROM personal_experiences WHERE id='experience-legacy-old-deleted'`).Scan(&deleted); err != nil || deleted != "archived" {
		t.Fatal(deleted, err)
	}
	var deletedAt string
	if err = s.db.QueryRowContext(ctx, `SELECT body->'source'->>'origin_deleted_at' FROM personal_experiences WHERE id='experience-legacy-old-deleted'`).Scan(&deletedAt); err != nil || deletedAt == "" {
		t.Fatal("deleted provenance lost", err)
	}
	var priorAfter string
	if err = s.db.QueryRowContext(ctx, `SELECT body::text FROM personal_experiences WHERE id='pre-existing-c5'`).Scan(&priorAfter); err != nil || priorAfter != priorBody {
		t.Fatal("existing C5 card changed", err)
	}
	for table, want := range map[string][]string{"roles": {"experience:view", "experience:manage", "experience:use", "learning:use", "question:generate", "kb:view", "kb:use", "kb:submit"}, "users": {"experience:view", "experience:manage", "experience:use"}} {
		id := "old-role"
		if table == "users" {
			id = "preference-owner"
		}
		var permissions []string
		if err = s.db.QueryRowContext(ctx, `SELECT permissions FROM `+table+` WHERE id=$1`, id).Scan(pq.Array(&permissions)); err != nil || !reflect.DeepEqual(permissions, want) {
			t.Fatal(table, permissions, err)
		}
	}
	// Schema63 now explicitly grants these authoring templates manual knowledge;
	// the legacy mapping must still never create paid learning capabilities.
	var dormant []string
	if err = s.db.QueryRowContext(ctx, `SELECT permissions FROM roles WHERE id='dormant-role'`).Scan(pq.Array(&dormant)); err != nil || !reflect.DeepEqual(dormant, []string{"experience:use", "question:generate", "experience:view", "experience:manage", "kb:view", "kb:use", "kb:submit"}) || slices.Contains(dormant, "learning:use") {
		t.Fatal("Schema63 manual grant changed unrelated paid learning rights", dormant, err)
	}
	var revoked []string
	if err = s.db.QueryRowContext(ctx, `SELECT permissions FROM roles WHERE id='revoked-role'`).Scan(pq.Array(&revoked)); err != nil || !reflect.DeepEqual(revoked, []string{"question:generate", "experience:view", "experience:manage", "experience:use", "kb:view", "kb:use", "kb:submit"}) {
		t.Fatal("Schema63 explicit teacher grant differs", revoked, err)
	}
	for _, state := range []string{"pending", "running", "succeeded", "failed"} {
		var raw []byte
		var afterStatus string
		if err = s.db.QueryRowContext(ctx, `SELECT request_json,status FROM generation_runs WHERE id=$1`, "frozen-"+state).Scan(&raw, &afterStatus); err != nil {
			t.Fatal(err)
		}
		var spec map[string]json.RawMessage
		json.Unmarshal(raw, &spec)
		var req map[string]json.RawMessage
		json.Unmarshal(spec["request"], &req)
		if _, old := req["personal_rules"]; old || afterStatus != state {
			t.Fatal("old snapshot or state changed", string(raw))
		}
		assertPrivateKnowledgePrompt(t, req["prompt_snapshot"])
	}
	var batchPrompt []byte
	var receipt string
	if err = s.db.QueryRowContext(ctx, `SELECT prompt_snapshot,request_hash FROM batch_jobs WHERE id='old-batch'`).Scan(&batchPrompt, &receipt); err != nil || receipt != "EXISTING-RECEIPT-UNCHANGED" {
		t.Fatal(err, receipt)
	}
	assertPrivateKnowledgePrompt(t, batchPrompt)
	for v, want := range before {
		var got string
		if err = s.db.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version=$1`, v).Scan(&got); err != nil || got != want {
			t.Fatal("released checksum changed", v, err)
		}
	}
	if again, err := s.Migrate(ctx); err != nil || len(again) != 0 {
		t.Fatal("data migration not idempotent", again, err)
	}
}

func assertPrivateKnowledgePrompt(t *testing.T, raw []byte) {
	t.Helper()
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot["rules"]; exists {
		t.Fatal("retired typed payload remained")
	}
	var appendix, system string
	var prompts map[string]string
	json.Unmarshal(snapshot["appendix"], &appendix)
	json.Unmarshal(snapshot["system_prompt"], &system)
	json.Unmarshal(snapshot["user_prompts"], &prompts)
	if appendix != releasedPreferenceAppendix || system != "旧系统底座" || prompts["frozen-key"] != "旧用户底座" || strings.Contains(appendix, "MUST-NOT-REACH-MODEL") {
		t.Fatal("frozen prompt changed", appendix)
	}
}

func TestPrivateKnowledgeMigrationFailureRollsBackAllData(t *testing.T) {
	for _, failure := range []string{"corrupt_snapshot", "destination_collision"} {
		t.Run(failure, func(t *testing.T) {
			s := privateKnowledge61Store(t)
			ctx := t.Context()
			if _, err := s.db.ExecContext(ctx, `INSERT INTO generation_rules(id,owner_id,name,content,kind)VALUES('rollback','preference-owner','不应丢','未迁移内容','constraint')`); err != nil {
				t.Fatal(err)
			}
			if failure == "corrupt_snapshot" {
				if _, err := s.db.ExecContext(ctx, `INSERT INTO batch_jobs(id,rules_snapshot)VALUES('corrupt','{"rules":"not-an-array"}')`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.ExecContext(ctx, `INSERT INTO personal_experiences(id,owner_id,revision,status,source_job_id,source_candidate_id,body)VALUES('experience-legacy-rollback','preference-owner',1,'draft','','collision','{}')`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Migrate(ctx); err == nil {
				t.Fatal("invalid immutable data should abort migration")
			}
			var current, n int
			if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&current); err != nil || current != 61 {
				t.Fatal("bad migration was recorded", current, err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM generation_rules WHERE id='rollback'`).Scan(&n); err != nil || n != 1 {
				t.Fatal("old data lost", n, err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM personal_experience_versions`).Scan(&n); err != nil || n != 0 {
				t.Fatal("partial history leaked", n, err)
			}
			var permissions []string
			if err := s.db.QueryRowContext(ctx, `SELECT permissions FROM roles WHERE id='old-role'`).Scan(pq.Array(&permissions)); err != nil || !slices.Contains(permissions, "rule:view") {
				t.Fatal("partial permission migration", permissions, err)
			}
		})
	}
}

func TestPrivateKnowledgeAppliedAdapterMarkerRejectsTampering(t *testing.T) {
	s := privateKnowledge61Store(t)
	if _, err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	changed := configuredMigrations(baselineSchemaSQL)
	last := len(changed) - 1
	changed[last].Statements = append([]string(nil), changed[last].Statements...)
	changed[last].Statements[0] += " -- altered adapter"
	if _, err := s.migrate(context.Background(), changed); err == nil || !strings.Contains(err.Error(), "校验和") {
		t.Fatal("changed adapter marker was accepted", err)
	}
}
