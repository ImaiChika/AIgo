package questionlib

import (
	"context"
	"fmt"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// ValidateAdmission is the shared content and durable-first-check gate for
// personal confirmation and optional review completion. Identity and workflow
// authority remain with the calling operation.
func ValidateAdmission(ctx context.Context, evidence storage.InitialCheckEvidenceStore, q *domain.A2Question) error {
	if evidence == nil || q == nil {
		return ErrUnavailable
	}
	// The durable pass belongs to the original AI generation. Teachers may
	// revise its wording freely; confirmation only checks usable structure.
	if err := q.Validate(); err != nil {
		return err
	}
	passed, err := evidence.HasInitialCheckPass(ctx, q.ID)
	if err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("题目 %s 缺少初始 AI 首检通过依据", q.ID)
	}
	return nil
}
