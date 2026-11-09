package knowledgebase

import (
	"context"
	"fmt"
	"testing"

	"aigo/internal/domain"
)

type coverageReadProbe struct {
	workflowReadProbe
	stale bool
}

func (s *coverageReadProbe) LoadKnowledgeWorkflowCandidates(ctx context.Context, ids []string) (map[string]WorkflowCandidates, error) {
	if len(ids) > WorkflowLookupBatchSize {
		return nil, fmt.Errorf("candidate batch exceeds its bound")
	}
	result, err := s.workflowReadProbe.LoadKnowledgeWorkflowCandidates(ctx, ids)
	if s.stale {
		for id, entry := range result {
			for i := range entry.Cards {
				entry.Cards[i].KnowledgePoints[0].Revision++
			}
			result[id] = entry
		}
	}
	return result, err
}
func TestCardCoverageUsesBoundedQueriesAndCurrentMappings(t *testing.T) {
	points := make([]domain.KnowledgePoint, 1002)
	for i := range points {
		points[i] = domain.KnowledgePoint{ID: fmt.Sprintf("p%d", i), VersionID: "version", Revision: 1}
	}
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%v", stale), func(t *testing.T) {
			probe := &coverageReadProbe{stale: stale}
			svc := Service{Store: probe}
			missing, checked, err := svc.CardCoverage(t.Context(), "user", "role", append(points, points[0]))
			if err != nil || checked != 1000 {
				t.Fatalf("coverage checked=%d error=%v", checked, err)
			}
			want := 0
			if stale {
				want = 1000
			}
			if len(missing) != want {
				t.Fatalf("missing=%d want=%d", len(missing), want)
			}
			if len(probe.batchSizes) != (1000+WorkflowLookupBatchSize-1)/WorkflowLookupBatchSize {
				t.Fatalf("lookup count=%d", len(probe.batchSizes))
			}
			for _, size := range probe.batchSizes {
				if size > WorkflowLookupBatchSize {
					t.Fatal("unbounded query", size)
				}
			}
		})
	}
}
