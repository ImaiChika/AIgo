package knowledgebase

import (
	"aigo/internal/domain"
	"context"
)

type snapshotKey struct{}

func WithSnapshot(ctx context.Context, s *domain.KnowledgeWorkflowSnapshot) context.Context {
	return context.WithValue(ctx, snapshotKey{}, s)
}
func SnapshotFromContext(ctx context.Context) *domain.KnowledgeWorkflowSnapshot {
	s, _ := ctx.Value(snapshotKey{}).(*domain.KnowledgeWorkflowSnapshot)
	return s
}

type planOffsetKey struct{}

func WithPlanOffset(ctx context.Context, offset int) context.Context {
	return context.WithValue(ctx, planOffsetKey{}, offset)
}
func PlanOffsetFromContext(ctx context.Context) int {
	v, _ := ctx.Value(planOffsetKey{}).(int)
	return v
}
