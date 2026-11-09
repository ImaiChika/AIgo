package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"aigo/internal/domain"
)

type KnowledgeImportExpectation struct{ DocumentHash, StateHash string }
type knowledgeImportExpectationKey struct{}

func WithKnowledgeImportExpectation(ctx context.Context, expected KnowledgeImportExpectation) context.Context {
	return context.WithValue(ctx, knowledgeImportExpectationKey{}, expected)
}

func KnowledgeImportExpected(ctx context.Context) (KnowledgeImportExpectation, bool) {
	expected, ok := ctx.Value(knowledgeImportExpectationKey{}).(KnowledgeImportExpectation)
	return expected, ok
}

// KnowledgeStateHash binds a preview to the full effective syllabus, including
// content and revision. Writers compare it while holding the syllabus lock.
func KnowledgeStateHash(points []domain.KnowledgePoint) string {
	items := append([]domain.KnowledgePoint{}, points...)
	for i := range items {
		items[i].VersionName, items[i].VersionYear = "", 0
		if len(items[i].Keywords) == 0 {
			items[i].Keywords = nil
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	raw, _ := json.Marshal(items)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
