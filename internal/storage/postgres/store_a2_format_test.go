//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestSecondWaveMigrationPreservesLegacyAndPersistsFormat(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	store, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	definitions := configuredMigrations(baselineSchemaSQL)
	applyMigrationPrefix(t, ctx, store, definitions[:35])
	if _, err := store.db.ExecContext(ctx, `INSERT INTO questions(id,clinical_stem,options,answer,status,version) VALUES ('legacy-format','男，45岁。腹痛1天。查体：腹软。该患者最可能的诊断是','[{"label":"A","text":"甲"},{"label":"B","text":"乙"},{"label":"C","text":"丙"},{"label":"D","text":"丁"}]','A','ai_reviewed',1)`); err != nil {
		t.Fatal(err)
	}
	fingerprint := func() string {
		var hash string
		if err := store.db.QueryRowContext(ctx, `SELECT md5((to_jsonb(q)-'format_version'-'review_revision'-'generation_workflow_version'-'library_state'-'confirmed_version'-'confirmation_source'-'library_revision'-'deleted_reason'-'deleted_by'-'confirmed_by'-'confirmed_at'-'deleted_from_state')::text) FROM questions q WHERE id='legacy-format'`).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	before := fingerprint()
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if fingerprint() != before {
		t.Fatal("格式迁移不得修改历史题内容、状态或版本")
	}
	legacy, err := store.GetQuestion(ctx, "legacy-format")
	if err != nil || legacy == nil || legacy.FormatVersion != "" || legacy.WorkflowVersion != "" {
		t.Fatalf("历史规则应保留空值: %+v %v", legacy, err)
	}
	if err := legacy.ValidateForReview(); err != nil {
		t.Fatalf("历史四选项题应保持兼容: %v", err)
	}
	q := domain.A2Question{
		ID: "round2-format", FormatVersion: domain.A2FormatSecondWave,
		ClinicalStem: "患者，男，45岁。腹痛1天。该患者最可能的诊断是",
		Options:      []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}},
		Answer:       "A", Status: domain.StatusAIReviewed, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetQuestion(ctx, q.ID)
	if err != nil || loaded == nil || loaded.FormatVersion != q.FormatVersion {
		t.Fatalf("格式标记没有持久化: %+v %v", loaded, err)
	}
	listed, err := store.ListQuestions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed {
		if item.ID == q.ID {
			found = item.FormatVersion == q.FormatVersion
		}
	}
	if !found {
		t.Fatal("列表查询丢失格式标记")
	}
	snapshot, err := store.GetQuestionVersion(ctx, q.ID, 1)
	if err != nil || snapshot == nil || snapshot.Snapshot.FormatVersion != q.FormatVersion {
		t.Fatalf("题目版本快照丢失格式标记: %+v %v", snapshot, err)
	}
	loaded.FormatVersion = ""
	if err := store.SaveQuestion(ctx, *loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.GetQuestion(ctx, q.ID)
	if err != nil || loaded.FormatVersion != q.FormatVersion {
		t.Fatal("编辑不能清空格式标记降级校验")
	}
}
