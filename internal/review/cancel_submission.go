package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/questionlib"
	"aigo/internal/storage"
)

var ErrReviewCancelNotOwner = errors.New("只能取消本人题目的审核")

// CancelSubmissionRequest binds an author's cancellation to the displayed task
// and all three question revisions. OwnerID is supplied by the authenticated API.
type CancelSubmissionRequest struct {
	TaskID                  string `json:"task_id"`
	OwnerID                 string `json:"-"`
	ExpectedVersion         int    `json:"expected_version"`
	ExpectedLibraryRevision int64  `json:"expected_library_revision"`
	ExpectedReviewRevision  int64  `json:"expected_review_revision"`
}

func untouchedSubmission(task *domain.ReviewTask) bool {
	if task == nil || task.Status != domain.StatusReviewing || task.CurrentRound != 1 || task.FinalDecision != nil {
		return false
	}
	for _, round := range task.RoundResults {
		if len(round.Reviews) > 0 || round.ApprovedCount != 0 || round.RejectedCount != 0 || round.RevisionCount != 0 || round.Passed {
			return false
		}
	}
	return true
}

// CanCancelSubmission exposes an author capability without revealing isolated
// vote counts or opinions. Mutations still recheck it with all three revisions.
func (s *Service) CanCancelSubmission(ctx context.Context, task *domain.ReviewTask, ownerID string) (bool, error) {
	if ownerID == "" || !untouchedSubmission(task) {
		return false, nil
	}
	q, err := s.questionStore.GetQuestion(ctx, task.QuestionID)
	if err != nil {
		return false, err
	}
	if q == nil || q.OwnerID != ownerID || q.Status != domain.StatusReviewing || q.LibraryState == domain.LibraryDeleted || task.QuestionVersion != q.Version {
		return false, nil
	}
	records, err := s.reviewStore.ListRecordsByTaskID(ctx, task.ID)
	if err != nil || len(records) != 0 {
		return false, err
	}
	latest, err := s.reviewStore.GetTaskByQuestionID(ctx, q.ID)
	if err != nil || latest == nil || latest.ID != task.ID {
		return false, err
	}
	if shares, ok := s.questionStore.(storage.QuestionShareStore); ok {
		share, err := shares.GetQuestionShareByQuestionID(ctx, q.ID)
		if err != nil {
			return false, err
		}
		if share != nil && share.Status == domain.QuestionSharePending {
			return false, nil
		}
	}
	return true, nil
}

// CancelSubmission archives an untouched first-round submission. It preserves
// history and can restore the exact personal confirmation suspended at submit.
func (s *Service) CancelSubmission(ctx context.Context, req CancelSubmissionRequest) error {
	if strings.TrimSpace(req.TaskID) == "" || strings.TrimSpace(req.OwnerID) == "" || req.ExpectedVersion < 1 || req.ExpectedLibraryRevision < 1 || req.ExpectedReviewRevision < 0 {
		return fmt.Errorf("%w: 取消审核需要任务及完整版本信息", domain.ErrReviewBadRequest)
	}
	return s.withReviewMutation(ctx, func(txctx context.Context) error {
		task, err := s.reviewStore.GetTask(txctx, req.TaskID)
		if err != nil {
			return err
		}
		if task == nil {
			return domain.ErrReviewTaskNotFound
		}
		q, err := s.questionStore.GetQuestion(txctx, task.QuestionID)
		if err != nil {
			return err
		}
		if q == nil {
			return domain.ErrReviewTaskNotFound
		}
		if q.OwnerID != req.OwnerID {
			return ErrReviewCancelNotOwner
		}
		if q.Version != req.ExpectedVersion || q.LibraryRevision != req.ExpectedLibraryRevision || q.ReviewRevision != req.ExpectedReviewRevision || task.QuestionVersion != q.Version {
			return domain.ErrReviewConflict
		}
		if q.LibraryState == domain.LibraryDeleted || q.Status != domain.StatusReviewing || !untouchedSubmission(task) {
			return fmt.Errorf("%w: 只能取消首轮尚无投票的审核", domain.ErrReviewConflict)
		}
		latest, err := s.reviewStore.GetTaskByQuestionID(txctx, q.ID)
		if err != nil {
			return err
		}
		if latest == nil || latest.ID != task.ID {
			return domain.ErrReviewConflict
		}
		records, err := s.reviewStore.ListRecordsByTaskID(txctx, task.ID)
		if err != nil {
			return err
		}
		if len(records) != 0 {
			return fmt.Errorf("%w: 已有审核记录，请联系管理员处理", domain.ErrReviewConflict)
		}
		if shares, ok := s.questionStore.(storage.QuestionShareStore); ok {
			share, err := shares.GetQuestionShareByQuestionID(txctx, q.ID)
			if err != nil {
				return err
			}
			if share != nil && share.Status == domain.QuestionSharePending {
				return fmt.Errorf("%w: 分享申请正在审批", domain.ErrReviewConflict)
			}
		}
		task.Status = domain.StatusArchived
		task.UpdatedAt = time.Now()
		if err := s.reviewStore.UpdateTask(txctx, *task); err != nil {
			return err
		}
		if err := s.ensureNoOtherActiveReview(txctx, q.ID); err != nil {
			return err
		}
		q.Status = domain.StatusAIReviewed
		// Historical optional tasks could predate the mandatory initial check.
		// Cancellation must not promote their lifecycle into a passed label.
		if previous := domain.CanonicalLifecycleStatus(task.QuestionPrevStatus); previous == domain.StatusAIDraft || previous == domain.StatusAutoChecked {
			q.Status = previous
		}
		q.LibraryState = domain.LibraryUnfiled
		q.ConfirmedVersion, q.ConfirmationSource, q.ConfirmedBy, q.ConfirmedAt = 0, "", "", nil
		before := task.LibraryBeforeSubmit
		if before != nil && before.QuestionVersion == q.Version && before.State == domain.LibraryActive && before.ConfirmedVersion == q.Version && (before.ConfirmationSource == domain.ConfirmSourceSelf || before.ConfirmationSource == domain.ConfirmSourceReview) {
			if s.RequireAICheck {
				evidence, ok := s.questionStore.(storage.InitialCheckEvidenceStore)
				if !ok {
					return fmt.Errorf("无法核验原始首检通过依据")
				}
				if err := questionlib.ValidateAdmission(txctx, evidence, q); err != nil {
					return err
				}
			}
			before = before.Clone()
			q.LibraryState, q.ConfirmedVersion, q.ConfirmationSource = before.State, before.ConfirmedVersion, before.ConfirmationSource
			q.ConfirmedBy, q.ConfirmedAt = before.ConfirmedBy, before.ConfirmedAt
		}
		previousRevision := q.LibraryRevision
		q.LibraryRevision++
		q.UpdatedAt = time.Now()
		change := storage.QuestionChangeFromContext(txctx)
		change.Actor, change.ChangeType = req.OwnerID, "review_cancel"
		return s.questionStore.SaveQuestion(storage.WithQuestionChange(storage.WithLibraryRevision(txctx, previousRevision), change), *q)
	})
}

func (s *Service) ensureNoOtherActiveReview(ctx context.Context, questionID string) error {
	if store, ok := s.questionStore.(interface {
		HasActiveReviewTask(context.Context, string) (bool, error)
	}); ok {
		active, err := store.HasActiveReviewTask(ctx, questionID)
		if err != nil {
			return err
		}
		if active {
			return domain.ErrReviewConflict
		}
		return nil
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.QuestionID == questionID && taskInFlight(task.Status) {
			return domain.ErrReviewConflict
		}
	}
	return nil
}
