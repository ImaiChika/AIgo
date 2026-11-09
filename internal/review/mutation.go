package review

import (
	"aigo/internal/storage"
	"context"
)

type reviewMutationKey struct{}
type reviewMutationScope struct {
	service     *Service
	transaction bool
}

// WithAssignmentLock 串行化审核资格、流程与任务的变更，不强制把认证存储混入审核事务。
func (s *Service) WithAssignmentLock(ctx context.Context, fn func(context.Context) error) (err error) {
	if scope, _ := ctx.Value(reviewMutationKey{}).(reviewMutationScope); scope.service == s {
		return fn(ctx)
	}
	release, err := s.reviewStore.AcquireReviewMutationLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if e := release(); err == nil {
			err = e
		}
	}()
	return fn(context.WithValue(ctx, reviewMutationKey{}, reviewMutationScope{service: s}))
}

// WithReviewMutation 允许API把人工审核操作和对应审计写入同一锁/事务，嵌套服务复用边界。
func (s *Service) WithReviewMutation(ctx context.Context, fn func(context.Context) error) error {
	return s.withReviewMutation(ctx, fn)
}

func (s *Service) withReviewMutation(ctx context.Context, fn func(context.Context) error) error {
	if scope, _ := ctx.Value(reviewMutationKey{}).(reviewMutationScope); scope.service == s && scope.transaction {
		return fn(ctx)
	}
	return s.WithAssignmentLock(ctx, func(locked context.Context) error {
		return s.reviewStore.WithReviewTransaction(locked, s.questionStore, func(txctx context.Context) error {
			return fn(context.WithValue(txctx, reviewMutationKey{}, reviewMutationScope{service: s, transaction: true}))
		})
	})
}

// WithQuestionMutation 同时保护旧编辑请求、送审、还原与版本写入。
func (s *Service) WithQuestionMutation(ctx context.Context, fn func(context.Context) error) error {
	if scope, _ := ctx.Value(reviewMutationKey{}).(reviewMutationScope); scope.service == s && scope.transaction {
		return fn(ctx)
	}
	return s.WithAssignmentLock(ctx, func(locked context.Context) error {
		if store, ok := s.questionStore.(storage.TransactionStore); ok {
			return store.WithTransaction(locked, func(txctx context.Context) error {
				return fn(context.WithValue(txctx, reviewMutationKey{}, reviewMutationScope{service: s, transaction: true}))
			})
		}
		return fn(locked)
	})
}
