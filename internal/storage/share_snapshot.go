package storage

import (
	"context"
	"errors"

	"aigo/internal/domain"
)

var ErrQuestionShareVersionConflict = errors.New("分享源版本或确认状态已变化，请刷新后重新选择")
var ErrQuestionShareSnapshotUnavailable = errors.New("历史分享缺少可核验的固定版本，不能读取个人最新修订替代")

type QuestionShareSnapshotStore interface {
	GetQuestionShareSnapshot(context.Context, string) (*domain.A2Question, *domain.QuestionShareRequest, error)
}
type VersionedQuestionShareReviewStore interface {
	ReviewQuestionShareVersion(context.Context, string, string, domain.QuestionShareStatus, string, int64) (*domain.QuestionShareRequest, error)
}
type VersionedBulkQuestionShareStore interface {
	PreviewQuestionShareItems(context.Context, QuestionFilter, int) ([]domain.QuestionShareExpectation, int, error)
	CreateQuestionShareItems(context.Context, string, []domain.QuestionShareExpectation) ([]string, error)
}
