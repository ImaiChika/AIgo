package storage

import (
	"aigo/internal/domain"
	"context"
	"errors"
)

var ErrPreferenceDenied = errors.New("无权操作此题的个人需求反馈")
var ErrPreferenceConflict = errors.New("题目版本或反馈已变化，请刷新后重新核对")
var ErrPreferenceReadOnly = errors.New("当前题目不处于AI检查通过的可反馈阶段")
var ErrPreferenceInvalid = errors.New("个人需求反馈内容无效")

type PreferenceFeedbackStore interface {
	GetPreferenceFeedback(context.Context, string, string, string) (*domain.PreferenceFeedback, int, bool, error)
	SavePreferenceFeedback(context.Context, string, string, string, domain.PreferenceFeedback) (*domain.PreferenceFeedback, error)
}

type PreferenceSummaryStore interface {
	PreferenceSummary(context.Context, string, string) (domain.PreferenceSummary, error)
}
