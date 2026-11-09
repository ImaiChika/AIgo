package testutil

import (
	"errors"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestKnowledgeScopeGuardsMatchPostgres(t *testing.T) {
	for _, entry := range []string{"create", "batch", "edit"} {
		t.Run(entry, func(t *testing.T) {
			s := NewMemoryKnowledgeStore()
			legacy := domain.KnowledgePoint{ID: "legacy-basic", VersionID: domain.LegacyKnowledgeVersion, Category: "基础医学", Subject: "生理", OutlineCode: "old", Topic: "历史内容", Revision: 2}
			s.points = []domain.KnowledgePoint{legacy}
			s.index[legacy.ID] = 0
			s.deleted[legacy.ID] = true
			incoming := domain.KnowledgePoint{ID: "fresh-id", VersionID: legacy.VersionID, Category: "临床综合", Subject: "呼吸系统", OutlineCode: "old", Topic: "换标签"}
			var err error
			if entry == "batch" {
				_, _, _, err = s.SaveVersionPoints(t.Context(), legacy.VersionID, []domain.KnowledgePoint{incoming}, false)
			} else {
				if entry == "edit" {
					incoming.ID = legacy.ID
					incoming.Revision = 2
				}
				err = s.UpdatePoint(t.Context(), incoming)
			}
			if !errors.Is(err, storage.ErrKnowledgeConflict) {
				t.Fatalf("removed code reentered via %s: %v", entry, err)
			}
			if len(s.points) != 1 || s.points[0].Topic != legacy.Topic || !s.deleted[legacy.ID] {
				t.Fatal("rejected write changed historical point")
			}
		})
	}
	for _, category := range []string{"基础医学", "临床综合"} {
		t.Run("incoming/"+category, func(t *testing.T) {
			s := NewMemoryKnowledgeStore()
			p := domain.KnowledgePoint{ID: "new", VersionID: domain.LegacyKnowledgeVersion, Category: category, Subject: "药理", Topic: "药物考点"}
			if err := s.UpdatePoint(t.Context(), p); !errors.Is(err, storage.ErrKnowledgeConflict) {
				t.Fatalf("non-clinical write accepted: %v", err)
			}
		})
	}
}
