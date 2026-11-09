// Package questionlib centralizes the transactional personal-library lifecycle.
package questionlib

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

const MaxConfirmBatch = 100

var (
	ErrNotOwner        = errors.New("仅题目归属人本人可以执行该操作")
	ErrLibraryConflict = errors.New("题目已被其他操作修改，请刷新后重试")
	ErrBatchTooLarge   = fmt.Errorf("单次最多处理 %d 道题，请分批操作", MaxConfirmBatch)
	ErrInvalidRequest  = errors.New("题库操作请求不完整")
	ErrUnavailable     = errors.New("题库事务或持久证据存储不可用")
)

// Every mutation binds the saved content and both independent concurrency axes.
// Pointers distinguish the valid initial review revision 0 from an omitted guard.
type ConfirmItem struct {
	QuestionID              string `json:"question_id"`
	ExpectedVersion         int    `json:"expected_version"`
	ExpectedLibraryRevision *int64 `json:"expected_library_revision"`
	ExpectedReviewRevision  *int64 `json:"expected_review_revision"`
}

type Store interface {
	GetQuestion(context.Context, string) (*domain.A2Question, error)
	SaveQuestion(context.Context, domain.A2Question) error
}

type TaskChecker interface {
	ActiveTaskForQuestion(context.Context, string) (bool, error)
}

type initialCheckStore interface {
	HasInitialCheckPass(context.Context, string) (bool, error)
}
type shareStore interface {
	GetQuestionShareByQuestionID(context.Context, string) (*domain.QuestionShareRequest, error)
}

type Service struct {
	store Store
	tasks TaskChecker
}

func NewService(store Store, tasks TaskChecker) *Service { return &Service{store: store, tasks: tasks} }

// Confirm confirms precisely the versions the author inspected. All guards and
// writes run under the same sorted question-row locks; any error rolls back all.
func (s *Service) Confirm(ctx context.Context, owner string, items []ConfirmItem, now time.Time) (int, error) {
	return s.mutate(ctx, owner, items, now, "confirm")
}

func (s *Service) Delete(ctx context.Context, owner string, items []ConfirmItem, now time.Time) (int, error) {
	return s.mutate(ctx, owner, items, now, "delete")
}

func (s *Service) Restore(ctx context.Context, owner string, items []ConfirmItem, now time.Time) (int, error) {
	return s.mutate(ctx, owner, items, now, "restore")
}

func ValidateItems(items []ConfirmItem) error {
	if len(items) == 0 {
		return fmt.Errorf("%w：请选择题目", ErrInvalidRequest)
	}
	if len(items) > MaxConfirmBatch {
		return ErrBatchTooLarge
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.QuestionID) == "" || item.QuestionID != strings.TrimSpace(item.QuestionID) {
			return fmt.Errorf("%w：缺少有效题目 ID", ErrInvalidRequest)
		}
		if seen[item.QuestionID] {
			return fmt.Errorf("%w：题目 %s 重复出现", ErrInvalidRequest, item.QuestionID)
		}
		seen[item.QuestionID] = true
		if item.ExpectedVersion <= 0 || item.ExpectedLibraryRevision == nil || item.ExpectedReviewRevision == nil || *item.ExpectedLibraryRevision < 0 || *item.ExpectedReviewRevision < 0 {
			return fmt.Errorf("%w：题目 %s 需提供内容版本、题库修订及审核修订", ErrInvalidRequest, item.QuestionID)
		}
	}
	return nil
}

func (s *Service) mutate(ctx context.Context, owner string, items []ConfirmItem, now time.Time, operation string) (int, error) {
	if err := ValidateItems(items); err != nil {
		return 0, err
	}
	tx, ok := s.store.(storage.QuestionLibraryTransactionStore)
	if !ok || s.tasks == nil {
		return 0, ErrUnavailable
	}
	evidence, ok := s.store.(initialCheckStore)
	if !ok {
		return 0, ErrUnavailable
	}
	shares, ok := s.store.(shareStore)
	if !ok {
		return 0, ErrUnavailable
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.QuestionID
	}
	if now.IsZero() {
		now = time.Now()
	}
	changed := 0
	err := tx.WithLibraryTransaction(ctx, ids, func(txCtx context.Context) error {
		for _, item := range items {
			q, err := s.store.GetQuestion(txCtx, item.QuestionID)
			if err != nil {
				return err
			}
			if q == nil {
				return fmt.Errorf("题目 %s 不存在", item.QuestionID)
			}
			if q.OwnerID != owner {
				return fmt.Errorf("%w（题目 %s）", ErrNotOwner, item.QuestionID)
			}
			// Ownership is checked before any conflict detail is returned.
			if q.Version != item.ExpectedVersion || q.ReviewRevision != *item.ExpectedReviewRevision {
				return conflict(q.ID)
			}
			revision := *item.ExpectedLibraryRevision
			replay := false
			switch operation {
			case "confirm":
				replay = q.LibraryState == domain.LibraryActive && q.ConfirmedVersion == q.Version && q.ConfirmationSource == domain.ConfirmSourceSelf
			case "delete":
				replay = q.LibraryState == domain.LibraryDeleted && q.DeletedReason == domain.DeleteReasonOwnerDelete
			case "restore":
				replay = q.LibraryState == domain.LibraryUnfiled && q.ConfirmedVersion == 0 && q.DeletedReason == ""
			}
			if q.LibraryRevision != revision && !(replay && q.LibraryRevision == revision+1) {
				return conflict(q.ID)
			}
			active, err := s.tasks.ActiveTaskForQuestion(txCtx, q.ID)
			if err != nil {
				return fmt.Errorf("查询持久审核任务失败: %w", err)
			}
			if active {
				return fmt.Errorf("%w：题目 %s 已进入人工审核流程", ErrLibraryConflict, q.ID)
			}
			share, err := shares.GetQuestionShareByQuestionID(txCtx, q.ID)
			if err != nil {
				return fmt.Errorf("查询分享状态失败: %w", err)
			}
			if share != nil && (share.Status == domain.QuestionSharePending || share.Status == domain.QuestionShareApproved && operation != "confirm") {
				return fmt.Errorf("%w：题目 %s 正在分享审批或已获批共享", ErrLibraryConflict, q.ID)
			}
			if share != nil && share.Status == domain.QuestionShareApproved && operation == "confirm" {
				snapshots, ok := s.store.(storage.QuestionShareSnapshotStore)
				if !ok {
					return ErrUnavailable
				}
				snapshot, _, err := snapshots.GetQuestionShareSnapshot(txCtx, q.ID)
				if err != nil {
					return err
				}
				if snapshot == nil {
					return ErrUnavailable
				}
			}
			if replay {
				if operation == "confirm" {
					if err := q.SelfConfirmable(); err != nil {
						return err
					}
					if err := ValidateAdmission(txCtx, evidence, q); err != nil {
						return err
					}
					changed++
				}
				continue
			}
			switch operation {
			case "confirm":
				if err := q.SelfConfirmable(); err != nil {
					return err
				}
				if err := ValidateAdmission(txCtx, evidence, q); err != nil {
					return err
				}
				q.LibraryState = domain.LibraryActive
				q.ConfirmedVersion = q.Version
				q.ConfirmationSource = domain.ConfirmSourceSelf
				q.ConfirmedBy = owner
				confirmedAt := now
				q.ConfirmedAt = &confirmedAt
				q.DeletedReason = ""
				q.DeletedBy = ""
				q.DeletedFromState = ""
			case "delete":
				switch domain.CanonicalLifecycleStatus(q.Status) {
				case domain.StatusAIReviewed, domain.StatusPublished:
				default:
					return fmt.Errorf("题目 %s 首检未完成、流程中或终态锁定，不能删除", q.ID)
				}
				if q.LibraryState == domain.LibraryDeleted {
					return fmt.Errorf("题目 %s 为终态删除留档", q.ID)
				}
				q.DeletedFromState = q.LibraryState
				q.LibraryState = domain.LibraryDeleted
				q.DeletedReason = domain.DeleteReasonOwnerDelete
				q.DeletedBy = owner
			case "restore":
				if q.LibraryState != domain.LibraryDeleted || !domain.RestoreAllowed(q.DeletedReason) {
					return fmt.Errorf("题目 %s 为驳回/归档终态留档，不能恢复", q.ID)
				}
				switch domain.CanonicalLifecycleStatus(q.Status) {
				case domain.StatusAIReviewed, domain.StatusPublished:
				default:
					return fmt.Errorf("题目 %s 为流程或终态锁定，不能恢复", q.ID)
				}
				passed, err := evidence.HasInitialCheckPass(txCtx, q.ID)
				if err != nil {
					return err
				}
				if !passed {
					return fmt.Errorf("题目 %s 缺少初始 AI 首检通过依据", q.ID)
				}
				q.LibraryState = domain.LibraryUnfiled
				q.ConfirmedVersion = 0
				q.ConfirmationSource = ""
				q.ConfirmedBy = ""
				q.ConfirmedAt = nil
				q.DeletedReason = ""
				q.DeletedBy = ""
				q.DeletedFromState = ""
				q.Status = domain.StatusAIReviewed
			}
			expected := q.LibraryRevision
			q.LibraryRevision++
			q.UpdatedAt = now
			if err := s.store.SaveQuestion(storage.WithLibraryRevision(txCtx, expected), *q); err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}
func conflict(id string) error { return fmt.Errorf("%w：题目 %s", ErrLibraryConflict, id) }
