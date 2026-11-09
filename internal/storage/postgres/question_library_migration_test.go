//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"context"
	"testing"
	"time"
)

// TestSchema58BackfillsPersonalLibrary 按 §12.1 验证历史状态映射：
// published→active(legacy)、rejected→deleted(legacy_rejected)、archived→
// deleted(legacy_archive)、ai_reviewed/reviewing→unfiled；幂等重放不改归属。
func TestSchema58BackfillsPersonalLibrary(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	migrations := configuredMigrations(baselineSchemaSQL)
	if len(migrations) != int(LatestSchemaVersion) || migrations[57].Version != 58 {
		t.Fatalf("迁移清单异常: len=%d", len(migrations))
	}
	applyMigrationPrefix(t, ctx, s, migrations[:57])

	seed := []struct {
		id     string
		status domain.QuestionStatus
	}{
		{"lib-published", domain.StatusPublished},
		{"lib-reviewed", domain.StatusAIReviewed},
		{"lib-reviewing", domain.StatusReviewing},
		{"lib-rejected", domain.StatusRejected},
		{"lib-archived", domain.StatusArchived},
		{"lib-draft", domain.StatusAIDraft},
	}
	for _, item := range seed {
		q := domain.A2Question{
			ID: item.id, OwnerID: "legacy-owner", CreatedBy: "legacy-owner",
			ClinicalStem: "患者，男，40岁。历史题。该患者最可能的诊断是",
			Options:      []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}, {Label: "E", Text: "戊"}},
			Answer:       "A", Status: item.status, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO questions (id, clinical_stem, options, answer, status, version, created_by, owner_id, created_at, updated_at) VALUES ($1,$2,'[]'::jsonb,'A',$3,1,'legacy-owner','legacy-owner',NOW(),NOW())`, q.ID, q.ClinicalStem, string(item.status)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	expect := map[string]struct {
		state  domain.LibraryState
		source string
		reason string
	}{
		"lib-published": {domain.LibraryActive, domain.ConfirmSourceLegacy, ""},
		"lib-reviewed":  {domain.LibraryUnfiled, "", ""},
		"lib-reviewing": {domain.LibraryUnfiled, "", ""},
		"lib-rejected":  {domain.LibraryDeleted, "", domain.DeleteReasonLegacyRejected},
		"lib-archived":  {domain.LibraryDeleted, "", domain.DeleteReasonLegacyArchive},
		"lib-draft":     {domain.LibraryUnfiled, "", ""},
	}
	for id, want := range expect {
		q, err := s.GetQuestion(ctx, id)
		if err != nil || q == nil {
			t.Fatalf("%s: %v", id, err)
		}
		if q.LibraryState != want.state || q.ConfirmationSource != want.source || q.DeletedReason != want.reason {
			t.Fatalf("%s 映射不符: state=%s source=%s reason=%s", id, q.LibraryState, q.ConfirmationSource, q.DeletedReason)
		}
	}
	published, _ := s.GetQuestion(ctx, "lib-published")
	if published.ConfirmedVersion != published.Version || !published.PersonallyExportable() {
		t.Fatalf("legacy published 应可导出: %+v", published)
	}
	// 幂等：重放迁移不改变归属（修订号稳定）。
	rejected, _ := s.GetQuestion(ctx, "lib-rejected")
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetQuestion(ctx, "lib-rejected")
	if again.LibraryRevision != rejected.LibraryRevision || again.LibraryState != domain.LibraryDeleted {
		t.Fatal("迁移重放不得改写归属")
	}
}
