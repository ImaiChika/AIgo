package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"errors"
	"testing"
	"time"
)

func TestKnowledgeVersionMigrationCRUDAndAtomicImports(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	// Start at the old schema with a real legacy point, then migrate in place.
	definitions := configuredMigrations(baselineSchemaSQL)
	if _, err := s.db.ExecContext(ctx, baselineSchemaSQL); err != nil {
		t.Fatal(err)
	}
	for _, m := range definitions[:3] {
		for _, stmt := range m.Statements {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
		info := migrationInfo(m)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum) VALUES ($1,$2,$3)`, info.Version, info.Name, info.Checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO knowledge_points(id,topic,outline_code) VALUES ('001','原有知识点','001')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := s.GetPoint(ctx, "001")
	if err != nil || old == nil || old.VersionID != domain.LegacyKnowledgeVersion || old.Topic != "原有知识点" {
		t.Fatalf("migration lost legacy: %+v %v", old, err)
	}
	for _, v := range []domain.KnowledgeVersion{{ID: "v2025", Name: "2025", Year: 2025, CreatedAt: time.Now()}, {ID: "v2026", Name: "2026", Year: 2026, CreatedAt: time.Now()}} {
		if err := s.CreateKnowledgeVersion(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	a := domain.KnowledgePoint{ID: "a", VersionID: "v2025", Category: "临床综合", Subject: "呼吸系统", Topic: "旧考点", OutlineCode: "001"}
	b := a
	b.ID = "b"
	b.VersionID = "v2026"
	b.Topic = "新考点"
	if err := s.UpdatePoint(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePoint(ctx, b); err != nil {
		t.Fatal(err)
	}
	a2, err := s.GetPoint(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	a2.Keywords = []string{"新增关键词"}
	if err := s.UpdatePoint(ctx, *a2); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdatePoint(ctx, *a2); !errors.Is(err, storage.ErrKnowledgeConflict) {
		t.Fatalf("stale edit accepted: %v", err)
	}
	b.Topic = "导入新内容"
	_, updated, _, err := s.SaveVersionPoints(ctx, "v2026", []domain.KnowledgePoint{b}, false)
	if err != nil || updated != 1 {
		t.Fatalf("import update: %d %v", updated, err)
	}
	// Attempt to overwrite another version midway through an import. All writes roll back.
	c := b
	c.ID = "c"
	c.OutlineCode = "002"
	bad := b
	bad.ID = "a"
	bad.OutlineCode = "003"
	if _, _, _, err := s.SaveVersionPoints(ctx, "v2026", []domain.KnowledgePoint{c, bad}, true); !errors.Is(err, storage.ErrKnowledgeConflict) {
		t.Fatalf("cross-version overwrite allowed: %v", err)
	}
	if p, err := s.GetPoint(ctx, "c"); err != nil || p != nil {
		t.Fatal("failed import partially committed")
	}
	if _, _, _, err := s.SaveVersionPoints(ctx, "v2026", []domain.KnowledgePoint{c}, true); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetPoint(ctx, "b"); p != nil {
		t.Fatal("replacement did not soft delete absent row")
	}
	if p, _ := s.GetPoint(ctx, "a"); p == nil || p.Topic != "旧考点" {
		t.Fatal("replacement affected another version")
	}
	if err := s.PublishKnowledgeVersion(ctx, "v2026"); err != nil {
		t.Fatal(err)
	}
	vs, err := s.ListKnowledgeVersions(ctx)
	if err != nil || vs[0].ID != "v2026" || vs[0].PointCount != 1 {
		t.Fatalf("versions: %+v %v", vs, err)
	}
	if err := s.DeletePoint(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	if err := s.db.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM knowledge_points WHERE id='c'`).Scan(&deleted); err != nil || !deleted {
		t.Fatal("delete removed archival row")
	}
}
