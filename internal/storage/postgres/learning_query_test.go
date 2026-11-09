//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"errors"
	"testing"
)

func TestLearningQueryPagesDraftsAndMatchesCanonicalProfessionalAlias(t *testing.T) {
	s, _ := learningStoreFixture(t)
	job := successfulLearningFixture(t, s)
	first := experienceFixture(job, "query-profession", "c1")
	first.Status = "active"
	first.Scope = domain.ExperienceScope{Kind: "profession", Profession: " 消化 "}
	second := experienceFixture(job, "query-draft", "c2")
	second.Title = "草稿中的表达经验"
	err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
		if e := s.SaveAuthoringExperience(ctx, first, 0); e != nil {
			return e
		}
		if e := s.SaveAuthoringExperience(ctx, second, 0); e != nil {
			return e
		}
		rows, total, e := s.SearchAuthoringExperiences(ctx, "owner", "草稿", "draft", 1, 0)
		if e != nil || total != 1 || len(rows) != 1 || rows[0].ID != second.ID {
			t.Fatal(rows, total, e)
		}
		rows, total, e = s.SearchAuthoringExperiences(ctx, "owner", "", "", 1, 1)
		if e != nil || total != 2 || len(rows) != 1 {
			t.Fatal("pagination must retain full total", rows, total, e)
		}
		rows, total, e = s.MatchAuthoringExperiences(ctx, "owner", []string{"digestive-point"}, []string{"三、消化系统", "消化系统", "消化"}, 200)
		if e != nil || total != 1 || len(rows) != 1 || rows[0].ID != first.ID {
			t.Fatal("profession alias did not reach SQL selection", rows, total, e)
		}
		if _, _, e = s.SearchAuthoringExperiences(ctx, "other", "", "", 1, 0); !errors.Is(e, storage.ErrLearningDenied) {
			t.Fatal("owner boundary bypassed", e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
