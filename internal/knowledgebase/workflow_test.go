package knowledgebase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"aigo/internal/domain"
)

type workflowReadProbe struct {
	Store
	denied, missing, empty bool
	err                    error
	batchSizes             []int
	singleLookups          int
}

func (s *workflowReadProbe) WithKnowledgeBaseAccess(ctx context.Context, _ string, _ string, fn func(context.Context, []string) error) error {
	if s.denied {
		return fn(ctx, nil)
	}
	return fn(ctx, []string{domain.PermKBView, domain.PermKBUse})
}
func (s *workflowReadProbe) ResolveKnowledgeCardPoints(_ context.Context, points []domain.KnowledgePoint) ([]domain.KnowledgePoint, error) {
	return points, nil
}
func (s *workflowReadProbe) LoadKnowledgeWorkflowCandidates(_ context.Context, ids []string) (map[string]WorkflowCandidates, error) {
	s.batchSizes = append(s.batchSizes, len(ids))
	if s.err != nil {
		return nil, s.err
	}
	result := map[string]WorkflowCandidates{}
	for _, id := range ids {
		if s.missing {
			continue
		}
		if s.empty {
			result[id] = WorkflowCandidates{}
			continue
		}
		card := validMedical()
		card.ID = "card-" + id
		card.Status = "active"
		card.Revision = 1
		card.KnowledgePoints = []domain.KnowledgePoint{{ID: id, VersionID: "version", Revision: 1}}
		result[id] = WorkflowCandidates{Cards: []domain.KnowledgeCard{card}, Total: 1}
	}
	return result, nil
}

func (s *workflowReadProbe) ListKnowledgeCards(ctx context.Context, f Filter) ([]domain.KnowledgeCard, int, error) {
	s.singleLookups++
	if s.empty {
		return nil, 0, nil
	}
	if f.Kind != "medical" {
		return nil, 0, nil
	}
	card := validMedical()
	card.ID = "card-" + f.KnowledgePointID
	card.Status = "active"
	card.Revision = 1
	card.KnowledgePoints = []domain.KnowledgePoint{{ID: f.KnowledgePointID, VersionID: "version", Revision: 1}}
	return []domain.KnowledgeCard{card}, 1, nil
}
func TestWorkflowPreparationBoundsCandidatesAndFailsClosed(t *testing.T) {
	points := []domain.KnowledgePoint{}
	for i := 0; i < 17; i++ {
		points = append(points, domain.KnowledgePoint{ID: fmt.Sprintf("p%d", i), VersionID: "version", Revision: 1})
	}
	probe := &workflowReadProbe{}
	svc := Service{Store: probe}
	var frozen *domain.KnowledgeWorkflowSnapshot
	err := svc.WithPreparedSnapshot(t.Context(), "user", "role", "knowledge", append(points, points[0]), func(_ context.Context, snapshot *domain.KnowledgeWorkflowSnapshot) error {
		frozen = snapshot
		return nil
	})
	if err != nil || len(frozen.Bundles) != 17 || len(probe.batchSizes) != 2 || probe.singleLookups != 3 {
		t.Fatal("unbounded or duplicated preparation", err, probe.batchSizes)
	}
	for _, size := range probe.batchSizes {
		if size > WorkflowLookupBatchSize {
			t.Fatal("unbounded candidate material", size)
		}
	}
	for _, tc := range []struct {
		name  string
		probe workflowReadProbe
		want  error
	}{
		{"revoked", workflowReadProbe{denied: true}, ErrDenied},
		{"canceled", workflowReadProbe{err: context.Canceled}, context.Canceled},
		{"incomplete loader", workflowReadProbe{missing: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := Service{Store: &tc.probe}
			err := svc.WithPreparedSnapshot(t.Context(), "user", "role", "knowledge", points[:2], func(context.Context, *domain.KnowledgeWorkflowSnapshot) error {
				t.Fatal("invalid preparation reached submission")
				return nil
			})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatal(err)
			}
			if tc.probe.denied && len(tc.probe.batchSizes) > 0 {
				t.Fatal("revoked user read candidates")
			}
		})
	}
}

func TestNewKnowledgePresentationIsStandardAndFrozen(t *testing.T) {
	probe := &workflowReadProbe{}
	svc := Service{Store: probe}
	points := []domain.KnowledgePoint{{ID: "point", VersionID: "version", Revision: 1}}
	choice := &domain.PresentationRequirements{SectionOrder: domain.ClinicalSections()}
	var frozen *domain.KnowledgeWorkflowSnapshot
	if err := svc.WithPreparedPresentation(t.Context(), "user", "role", "knowledge", points, choice, func(_ context.Context, s *domain.KnowledgeWorkflowSnapshot) error { frozen = s; return nil }); err != nil {
		t.Fatal(err)
	}
	choice.SectionOrder[0] = "bad"
	if frozen.Version != CurrentClinicalWorkflowVersion || frozen.Presentation.Validate() != nil {
		t.Fatal("caller changed a frozen requirement")
	}
	if err := svc.WithPreparedSnapshot(t.Context(), "user", "role", "knowledge", points, func(_ context.Context, s *domain.KnowledgeWorkflowSnapshot) error {
		if s.Version != CurrentClinicalWorkflowVersion || !domain.IsStandardPresentation(s.Presentation) {
			t.Fatal("new task did not freeze current standard")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.WithPreparedPresentation(t.Context(), "user", "role", "direct", points, frozen.Presentation, func(context.Context, *domain.KnowledgeWorkflowSnapshot) error {
		t.Fatal("direct accepted a knowledge-only option")
		return nil
	}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestOptionalWorkflowKeepsEmptyPointAndLegacySnapshotStrict(t *testing.T) {
	probe := &workflowReadProbe{empty: true}
	svc := Service{Store: probe}
	point := domain.KnowledgePoint{ID: "no-card", VersionID: "version", Revision: 1}
	var frozen *domain.KnowledgeWorkflowSnapshot
	if err := svc.WithPreparedSnapshot(t.Context(), "user", "role", "knowledge", []domain.KnowledgePoint{point}, func(_ context.Context, s *domain.KnowledgeWorkflowSnapshot) error { frozen = s; return nil }); err != nil {
		t.Fatal("missing cards blocked generation", err)
	}
	if len(frozen.Bundles) != 1 {
		t.Fatal("unmatched point not frozen")
	}
	if b, err := BundleFor(frozen, point.ID); err != nil || len(b.Cards) != 0 {
		t.Fatal("empty lookup not usable", err)
	}
	if _, err := BundleFor(frozen, "unknown-point"); err == nil {
		t.Fatal("missing frozen point silently accepted")
	}
	legacy := &domain.KnowledgeWorkflowSnapshot{Version: CurrentClinicalWorkflowVersion, Presentation: domain.StandardPresentation(), Bundles: frozen.Bundles}
	if _, err := BundleFor(legacy, point.ID); err == nil {
		t.Fatal("old snapshot lost its required knowledge contract")
	}
}
