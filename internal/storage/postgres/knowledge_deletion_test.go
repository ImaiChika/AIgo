package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestKnowledgeVersionDeleteAtomicAndConcurrent(t *testing.T) {
	_, dsn, cleanup := migrationTestSchema(t)
	defer cleanup()
	s, err := New(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	version := domain.KnowledgeVersion{ID: "delete-version", Name: "2026 年大纲", Year: 2026, CreatedAt: time.Now()}
	if err := s.CreateKnowledgeVersion(ctx, version); err != nil {
		t.Fatal(err)
	}
	point := domain.KnowledgePoint{ID: "delete-point", VersionID: version.ID, Category: "临床综合", Subject: "呼吸系统", Topic: "删除前内容", OutlineCode: "001"}
	if err := s.UpdatePoint(ctx, point); err != nil {
		t.Fatal(err)
	}
	// Inject a failure after the point update, proving the deletion is one transaction.
	if _, err := s.db.Exec(`CREATE FUNCTION fail_syllabus_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.deleted_at IS NOT NULL THEN RAISE EXCEPTION 'delete failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_syllabus_delete BEFORE UPDATE ON knowledge_versions FOR EACH ROW EXECUTE FUNCTION fail_syllabus_delete()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteKnowledgeVersion(ctx, version.ID); err == nil {
		t.Fatal("expected injected failure")
	}
	if p, _ := s.GetPoint(ctx, point.ID); p == nil || p.Revision != 1 {
		t.Fatal("failed deletion partially hid points")
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_syllabus_delete ON knowledge_versions; DROP FUNCTION fail_syllabus_delete()`); err != nil {
		t.Fatal(err)
	}
	// Deletion and import use the same cross-process lock, so an import cannot resurrect it.
	newer := point
	newer.ID = "new-point"
	newer.OutlineCode = "002"
	var wg sync.WaitGroup
	wg.Add(2)
	var deleteErr, importErr error
	go func() { defer wg.Done(); _, deleteErr = s.DeleteKnowledgeVersion(ctx, version.ID) }()
	go func() {
		defer wg.Done()
		_, _, _, importErr = s.SaveVersionPoints(ctx, version.ID, []domain.KnowledgePoint{newer}, false)
	}()
	wg.Wait()
	if deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if importErr != nil && !errors.Is(importErr, storage.ErrKnowledgeNotFound) {
		t.Fatal(importErr)
	}
	active, err := s.ListVersionPoints(ctx, version.ID)
	if err != nil || len(active) != 0 {
		t.Fatalf("points survived: %+v %v", active, err)
	}
	if err := s.UpdatePoint(ctx, point); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("stale insert revived version: %v", err)
	}
	if err := s.PublishKnowledgeVersion(ctx, version.ID); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("activation revived version: %v", err)
	}
	var original string
	if err := s.db.QueryRow(`SELECT topic FROM knowledge_points WHERE id=$1`, point.ID).Scan(&original); err != nil || original != point.Topic {
		t.Fatal("historical row erased")
	}
	if _, err := s.DeleteKnowledgeVersion(ctx, version.ID); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("repeat delete: %v", err)
	}
	version.ID = "recreated-version"
	if err := s.CreateKnowledgeVersion(ctx, version); err != nil {
		t.Fatalf("name not reusable: %v", err)
	}
}
