package knowledge

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSyllabusIsolationDefaultAndQuestionSnapshots(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewMemoryKnowledgeStore()
	svc := NewService(store)
	old, err := svc.CreateVersion(ctx, "2025 临床", 2025, "")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := svc.CreateVersion(ctx, "2026 临床", 2026, "")
	if err != nil {
		t.Fatal(err)
	}
	makePoint := func(v string, topic string) *domain.KnowledgePoint {
		t.Helper()
		p, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: v, Category: "临床综合", Subject: "呼吸系统", Unit: "肺部感染", SubItem: "肺炎", Topic: topic, OutlineCode: "110.1"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := makePoint(old.ID, "旧考点")
	b := makePoint(newer.ID, "新考点")
	if a.ID == b.ID {
		t.Fatal("same code across years reused identity")
	}
	if err := svc.PublishVersion(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListAll(ctx)
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("draft replaced default: %+v %v", got, err)
	}
	if _, err := svc.ResolveForGeneration(ctx, newer.ID, b.ID, ""); err == nil {
		t.Fatal("draft used for generation")
	}
	if err := svc.PublishVersion(ctx, newer.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Search(ctx, "")
	if len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("latest version not default: %+v", got)
	}
	if _, err := svc.ResolveForGeneration(ctx, old.ID, b.ID, ""); err == nil {
		t.Fatal("cross-version id accepted")
	}
	if p, err := svc.ResolveForGeneration(ctx, old.ID, "", "110.1"); err != nil || p.ID != a.ID {
		t.Fatalf("old version no longer usable: %+v %v", p, err)
	}
	snapshot := *a
	a.Topic = "旧考点修订"
	a.Keywords = []string{"关键词"}
	changed, err := svc.UpdatePoint(ctx, a.ID, *a)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision != 2 {
		t.Fatal("edit did not increment revision")
	}
	if _, err := svc.UpdatePoint(ctx, a.ID, *a); !errors.Is(err, storage.ErrKnowledgeConflict) {
		t.Fatalf("lost-update protection: %v", err)
	}
	if err := svc.DeletePoint(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if snapshot.Topic != "旧考点" || snapshot.VersionID != old.ID {
		t.Fatal("snapshot changed")
	}
	if _, err := svc.ResolveForGeneration(ctx, old.ID, a.ID, ""); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("deleted selection accepted: %v", err)
	}
	got, _ = svc.ListAll(ctx)
	if len(got) != 1 || got[0].ID != b.ID {
		t.Fatal("deleting old point affected latest")
	}
	keys := domain.ExistingKnowledgeKeys([]domain.A2Question{{OutlineCode: "110.1", KnowledgePoints: []domain.KnowledgePoint{snapshot}}})
	if keys[domain.KnowledgePointKey(*b)] {
		t.Fatal("skip existing crosses syllabus versions")
	}
}
func TestImportFilesAtomicReplacementAndTree(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testutil.NewMemoryKnowledgeStore())
	v, _ := svc.CreateVersion(ctx, "2026", 2026, "")
	dir := t.TempDir()
	csv := func(name, rows string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("分类,专业/系统,单元,细目,要点,大纲代码,关键词\n"+rows), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := csv("a.csv", "临床综合,呼吸系统,肺部感染,肺炎,考点A,001,\n临床综合,呼吸系统,肺部感染,肺炎,考点B,002,\n")
	second := csv("b.csv", "临床综合,呼吸系统,胸膜疾病,胸腔积液,考点C,003,\n")
	n, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{first, second}, false)
	if err != nil || n != 3 {
		t.Fatalf("import: %d %v", n, err)
	}
	_, _, dupes, err := svc.ImportDocuments(ctx, v.ID, []string{first, second}, false)
	if err != nil || dupes != 3 {
		t.Fatalf("repeat: %d %v", dupes, err)
	}
	tree, err := svc.Tree(ctx, v.ID)
	if err != nil || len(tree) != 1 || tree[0].Count != 3 || len(tree[0].Children[0].Children) != 2 {
		t.Fatalf("tree: %+v %v", tree, err)
	}
	branch, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID, Path: []string{"临床综合", "呼吸系统", "肺部感染"}, Keyword: "考点"})
	if len(branch) != 2 {
		t.Fatalf("parent subtree: %+v", branch)
	}
	multi, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID, Keyword: "呼吸，考点C"})
	if len(multi) != 1 || multi[0].OutlineCode != "003" {
		t.Fatalf("multi-keyword AND search: %+v", multi)
	}
	bad := csv("bad.csv", "临床综合,呼吸系统,肺部感染,肺炎,,004,\n")
	if _, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{second, bad}, true); err == nil {
		t.Fatal("invalid batch accepted")
	}
	all, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if len(all) != 3 {
		t.Fatal("failed replace was partially committed")
	}
	conflict := csv("conflict.csv", "临床综合,呼吸系统,肺部感染,肺炎,另一个考点,001,\n")
	if _, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{first, conflict}, false); err == nil {
		t.Fatal("conflicting files silently overwrite")
	}
	if _, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{second}, true); err != nil {
		t.Fatal(err)
	}
	all, _ = svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if len(all) != 1 || all[0].OutlineCode != "003" {
		t.Fatalf("replace did not remove absent points: %+v", all)
	}
}
func TestManualCodeChangeThenImportDoesNotOverwriteDifferentCode(t *testing.T) {
	ctx := context.Background()
	svc := NewService(testutil.NewMemoryKnowledgeStore())
	v, _ := svc.CreateVersion(ctx, "2026", 2026, "")
	path := filepath.Join(t.TempDir(), "outline.csv")
	os.WriteFile(path, []byte("分类,专业/系统,要点,大纲代码\n临床综合,呼吸系统,旧代码内容,001\n"), 0600)
	if _, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{path}, false); err != nil {
		t.Fatal(err)
	}
	all, _ := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	p := all[0]
	p.OutlineCode = "002"
	if _, err := svc.UpdatePoint(ctx, p.ID, p); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.ImportDocuments(ctx, v.ID, []string{path}, false); err != nil {
		t.Fatal(err)
	}
	all, _ = svc.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID})
	if len(all) != 2 {
		t.Fatalf("import overwrote manually changed code: %+v", all)
	}
}

func TestDeleteVersionFallsBackWithoutRevivingDrafts(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewMemoryKnowledgeStore()
	svc := NewService(store)
	old, _ := svc.CreateVersion(ctx, "2025 临床", 2025, "")
	latest, _ := svc.CreateVersion(ctx, "2026 临床", 2026, "")
	draft, _ := svc.CreateVersion(ctx, "2027 整理中", 2027, "")
	var snapshot domain.KnowledgePoint
	for _, v := range []*domain.KnowledgeVersion{old, latest, draft} {
		p, err := svc.CreatePoint(ctx, domain.KnowledgePoint{VersionID: v.ID, Category: "临床综合", Subject: "呼吸系统", Topic: "考点", OutlineCode: "001"})
		if err != nil {
			t.Fatal(err)
		}
		if v.ID == latest.ID {
			snapshot = *p
		}
		if v != draft {
			if err := svc.PublishVersion(ctx, v.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count, err := svc.DeleteVersion(ctx, latest.ID); err != nil || count != 1 {
		t.Fatalf("delete: %d %v", count, err)
	}
	if v, err := svc.ResolveVersion(ctx, ""); err != nil || v.ID != old.ID {
		t.Fatalf("wrong fallback: %+v %v", v, err)
	}
	if _, err := svc.SearchFiltered(ctx, KPSearchOptions{VersionID: latest.ID}); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatal("deleted version was silently replaced")
	}
	if p, _ := svc.GetByID(ctx, snapshot.ID); p != nil {
		t.Fatal("deleted version still exposes point")
	}
	if _, err := svc.ResolveForGeneration(ctx, latest.ID, snapshot.ID, ""); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("deleted selection accepted: %v", err)
	}
	if _, err := svc.CreatePoint(ctx, snapshot); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("create revived version: %v", err)
	}
	if err := store.UpdatePoint(ctx, snapshot); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("stale update revived version: %v", err)
	}
	if _, _, _, err := store.SaveVersionPoints(ctx, latest.ID, []domain.KnowledgePoint{snapshot}, false); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("import revived version: %v", err)
	}
	if err := svc.PublishVersion(ctx, latest.ID); !errors.Is(err, storage.ErrKnowledgeNotFound) {
		t.Fatalf("publish revived version: %v", err)
	}
	if _, err := svc.CreateVersion(ctx, latest.Name, latest.Year, ""); err != nil {
		t.Fatalf("deleted name cannot be reused: %v", err)
	}
	svc.DeleteVersion(ctx, old.ID)
	svc.DeleteVersion(ctx, domain.LegacyKnowledgeVersion)
	if _, err := svc.ResolveVersion(ctx, ""); !errors.Is(err, storage.ErrKnowledgeNoDefault) {
		t.Fatalf("draft promoted implicitly: %v", err)
	}
	points, err := svc.ListAll(ctx)
	if err != nil || len(points) != 0 {
		t.Fatalf("empty default must be a normal empty list: %+v %v", points, err)
	}
	if count, err := svc.Count(ctx); count != 0 || err != nil {
		t.Fatalf("empty statistics: %d %v", count, err)
	}
	if snapshot.VersionID != latest.ID || snapshot.VersionName != latest.Name || snapshot.Topic != "考点" {
		t.Fatal("historical snapshot changed")
	}
}
