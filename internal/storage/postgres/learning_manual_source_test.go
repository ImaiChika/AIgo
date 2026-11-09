//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestManualKnowledgeCannotForgeLearningOrMigratedSource(t *testing.T) {
	s, _ := learningStoreFixture(t)
	for _, tc := range []struct {
		name   string
		source domain.ExperienceSource
		want   error
	}{
		{"manual_with_job", domain.ExperienceSource{Kind: "manual", JobID: "forged-job", CandidateID: "manual:key", OriginRequestHash: strings.Repeat("a", 64)}, storage.ErrLearningInvalid},
		{"manual_with_question", domain.ExperienceSource{Kind: "manual", QuestionID: "question", CandidateID: "manual:key", OriginRequestHash: strings.Repeat("a", 64)}, storage.ErrLearningInvalid},
		{"manual_with_version", domain.ExperienceSource{Kind: "manual", OriginalVersion: 1, CandidateID: "manual:key", OriginRequestHash: strings.Repeat("a", 64)}, storage.ErrLearningInvalid},
		{"manual_missing_hash", domain.ExperienceSource{Kind: "manual", CandidateID: "manual:key"}, storage.ErrLearningInvalid},
		{"manual_bad_candidate", domain.ExperienceSource{Kind: "manual", CandidateID: "not-manual", OriginRequestHash: strings.Repeat("a", 64)}, storage.ErrLearningInvalid},
		{"manual_forged_origin", domain.ExperienceSource{Kind: "manual", CandidateID: "manual:key", OriginID: "old-preference", OriginRequestHash: strings.Repeat("a", 64)}, storage.ErrLearningInvalid},
		{"migrated_source_not_HTTP_creation", domain.ExperienceSource{Kind: "legacy_personal_preference", CandidateID: "old-entry"}, storage.ErrLearningInvalid},
		{"unproven_learning", domain.ExperienceSource{Kind: "learning", JobID: "missing", CandidateID: "c1"}, storage.ErrLearningConflict},
		{"old_unproven_source", domain.ExperienceSource{JobID: "missing", CandidateID: "c1"}, storage.ErrLearningConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
				return s.SaveAuthoringExperience(ctx, domain.AuthoringExperience{ID: tc.name, OwnerID: "owner", Kind: "authoring_experience", ScopeType: "personal", Title: "来源守卫", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "r", Text: "保留病例线索", Field: "clinical_stem"}}, Status: "draft", Revision: 1, Source: tc.source}, 0)
			})
			if !errors.Is(err, tc.want) {
				t.Fatal("forged source accepted", err)
			}
		})
	}
	card := domain.AuthoringExperience{ID: "valid-manual", OwnerID: "owner", Kind: "authoring_experience", ScopeType: "personal", Title: "合法手工知识", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "r", Text: "按就诊顺序书写", Field: "clinical_stem"}}, Status: "draft", Revision: 1, Source: domain.ExperienceSource{Kind: "manual", CandidateID: "manual:valid", OriginRequestHash: strings.Repeat("a", 64)}}
	if err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error { return s.SaveAuthoringExperience(ctx, card, 0) }); err != nil {
		t.Fatal("valid manual creation still needs a paid job", err)
	}
	card.Source.OriginRequestHash = strings.Repeat("b", 64)
	card.Revision = 2
	if err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error { return s.SaveAuthoringExperience(ctx, card, 1) }); !errors.Is(err, storage.ErrLearningConflict) {
		t.Fatal("manual original create hash was mutable", err)
	}
}
