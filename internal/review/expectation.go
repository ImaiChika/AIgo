package review

import (
	"aigo/internal/domain"
	"context"
)

type submissionExpectationKey struct{}

// WithSubmissionExpectation 绑定页面读取到的题目版本和审核修订号，不能采用执行时的最新值替代。
func WithSubmissionExpectation(ctx context.Context, e domain.ReviewExpectation) context.Context {
	return context.WithValue(ctx, submissionExpectationKey{}, e)
}

func checkSubmissionExpectation(ctx context.Context, q *domain.A2Question) error {
	e, _ := ctx.Value(submissionExpectationKey{}).(domain.ReviewExpectation)
	if e.QuestionID != "" && e.QuestionID != q.ID {
		return domain.ErrReviewConflict
	}
	if e.ReviewRevision != q.ReviewRevision || (e.Version > 0 && e.Version != q.Version) {
		return domain.ErrReviewConflict
	}
	return nil
}
