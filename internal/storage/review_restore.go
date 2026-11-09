package storage

import (
	"aigo/internal/domain"
	"context"
)

// ReviewRestoreStore 的写方法只在审核锁保护的同一业务事务内执行。
type ReviewRestoreStore interface {
	SearchReviewRestoreCandidates(context.Context, domain.ReviewRestoreFilter, int, int, bool) ([]domain.ReviewRestoreCandidate, int, error)
	GetReviewRestoreOperation(context.Context, string, string, string) (*domain.ReviewRestoreResult, error)
	ApplyReviewRestore(context.Context, domain.ReviewRestoreCommand) (*domain.ReviewRestoreResult, error)
}

type ReviewRestoreOptionsStore interface {
	ReviewRestoreOptions(context.Context) (*domain.ReviewRestoreOptions, error)
}
