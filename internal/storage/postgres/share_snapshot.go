package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

const shareRequestColumns = `id,question_id,owner_id,status,reviewed_by,review_note,created_at,reviewed_at,question_version,library_revision,revision,question_snapshot`

func (s *Store) validateShareSource(ctx context.Context, q *domain.A2Question, request domain.QuestionShareRequest) error {
	if q == nil {
		return storage.ErrQuestionShareNotFound
	}
	if request.OwnerID == "" || q.OwnerID != request.OwnerID {
		return storage.ErrQuestionShareNotOwner
	}
	if (request.ExpectedVersion != 0 && request.ExpectedVersion != q.Version) ||
		(request.ExpectedLibraryRevision != nil && *request.ExpectedLibraryRevision != q.LibraryRevision) ||
		(request.ExpectedReviewRevision != nil && *request.ExpectedReviewRevision != q.ReviewRevision) {
		return storage.ErrQuestionShareVersionConflict
	}
	if q.LibraryState != domain.LibraryActive || q.ConfirmedVersion != q.Version || (q.Status != domain.StatusPublished && q.Status != domain.StatusAIReviewed) {
		return storage.ErrQuestionShareNotFormal
	}
	active, err := s.HasActiveReviewTask(ctx, q.ID)
	if err != nil {
		return err
	}
	if active {
		return storage.ErrQuestionShareNotFormal
	}
	if q.Status == domain.StatusPublished && q.ConfirmationSource == domain.ConfirmSourceLegacy {
		return nil
	}
	pass, err := s.HasInitialCheckPass(ctx, q.ID)
	if err != nil {
		return err
	}
	if !pass {
		return storage.ErrQuestionShareNotFormal
	}
	return nil
}

func shareSnapshot(q domain.A2Question) domain.A2Question {
	q.GenerationContext = nil
	q.CreatedBy = ""
	q.ConfirmedBy = ""
	q.DeletedBy = ""
	return q
}

func (s *Store) createQuestionShareSnapshot(ctx context.Context, request domain.QuestionShareRequest) error {
	q, err := s.GetQuestion(ctx, request.QuestionID)
	if err != nil {
		return err
	}
	if err = s.validateShareSource(ctx, q, request); err != nil {
		return err
	}
	snapshot := shareSnapshot(*q)
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	createdAt := request.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err = s.execContext(ctx, `INSERT INTO question_share_requests(id,question_id,owner_id,status,created_at,question_version,library_revision,revision,question_snapshot,snapshot_search_text) VALUES($1,$2,$3,'pending',$4,$5,$6,1,$7,$8)`, request.ID, q.ID, request.OwnerID, createdAt, q.Version, q.LibraryRevision, raw, storage.BuildQuestionSearchText(snapshot))
	return err
}

// GetQuestionShareSnapshot returns the immutable submitted version. Sharing
// status projects the global collection tier, never an expert-review assertion.
func (s *Store) GetQuestionShareSnapshot(ctx context.Context, id string) (*domain.A2Question, *domain.QuestionShareRequest, error) {
	request, err := s.GetQuestionShareByQuestionID(ctx, id)
	if err != nil || request == nil {
		return nil, request, err
	}
	if request.Snapshot == nil || request.QuestionVersion < 1 {
		return nil, request, storage.ErrQuestionShareSnapshotUnavailable
	}
	q := projectShareSnapshot(*request)
	return &q, request, nil
}
func projectShareSnapshot(request domain.QuestionShareRequest) domain.A2Question {
	q := shareSnapshot(*request.Snapshot)
	q.ID = request.QuestionID
	q.OwnerID = request.OwnerID
	q.Version = request.QuestionVersion
	q.LibraryState = domain.LibraryActive
	q.ConfirmedVersion = request.QuestionVersion
	q.DeletedReason = ""
	q.DeletedFromState = ""
	switch request.Status {
	case domain.QuestionShareApproved:
		q.Status = domain.StatusPublished
	case domain.QuestionShareRejected:
		q.Status = domain.StatusRejected
	default:
		q.Status = domain.StatusAIReviewed
	}
	return q
}

func (s *Store) listQuestionShareSnapshots(ctx context.Context, status, ownerID string) ([]storage.QuestionShareItem, error) {
	rows, err := s.queryContext(ctx, `SELECT `+shareRequestColumns+` FROM question_share_requests WHERE reviewed_by<>'migration' AND ($1='' OR status=$1) AND ($2='' OR owner_id=$2) ORDER BY created_at DESC,id DESC`, status, ownerID)
	if err != nil {
		return nil, err
	}
	requests := []domain.QuestionShareRequest{}
	for rows.Next() {
		request, e := scanQuestionShareRequest(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		requests = append(requests, *request)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := make([]storage.QuestionShareItem, 0, len(requests))
	for _, request := range requests {
		item := storage.QuestionShareItem{Request: request}
		if request.Snapshot != nil && request.QuestionVersion > 0 {
			item.Question = projectShareSnapshot(request)
		} else {
			item.Question = domain.A2Question{ID: request.QuestionID, OwnerID: request.OwnerID, Version: request.QuestionVersion}
		}
		e := s.queryRowContext(ctx, `SELECT COALESCE(display_name,''),COALESCE(username,'') FROM users WHERE id=$1`, request.OwnerID).Scan(&item.OwnerName, &item.OwnerUsername)
		if e != nil && e != sql.ErrNoRows {
			return nil, e
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) ReviewQuestionShareVersion(ctx context.Context, id, reviewerID string, status domain.QuestionShareStatus, note string, expectedRevision int64) (*domain.QuestionShareRequest, error) {
	if status != domain.QuestionShareApproved && status != domain.QuestionShareRejected {
		return nil, fmt.Errorf("无效的分享审批状态: %s", status)
	}
	var questionID string
	err := s.queryRowContext(ctx, `SELECT question_id FROM question_share_requests WHERE id=$1`, id).Scan(&questionID)
	if err == sql.ErrNoRows {
		return nil, storage.ErrQuestionShareNotFound
	}
	if err != nil {
		return nil, err
	}
	var result *domain.QuestionShareRequest
	err = s.WithLibraryTransaction(ctx, []string{questionID}, func(txctx context.Context) error {
		request, e := scanQuestionShareRequest(s.queryRowContext(txctx, `SELECT `+shareRequestColumns+` FROM question_share_requests WHERE id=$1 FOR UPDATE`, id))
		if e != nil {
			return e
		}
		if request == nil {
			return storage.ErrQuestionShareNotFound
		}
		if expectedRevision > 0 && request.Revision != expectedRevision {
			return storage.ErrQuestionShareVersionConflict
		}
		if request.Status != domain.QuestionSharePending {
			return storage.ErrQuestionShareAlreadyReviewed
		}
		if request.Snapshot == nil || request.QuestionVersion < 1 {
			return storage.ErrQuestionShareSnapshotUnavailable
		}
		q, e := s.GetQuestion(txctx, questionID)
		if e != nil {
			return e
		}
		if q == nil || q.Version != request.QuestionVersion || q.LibraryRevision != request.LibraryRevision || q.ReviewRevision != request.Snapshot.ReviewRevision {
			return storage.ErrQuestionShareVersionConflict
		}
		if e = s.validateShareSource(txctx, q, *request); e != nil {
			return e
		}
		e = s.queryRowContext(txctx, `UPDATE question_share_requests SET status=$2,reviewed_by=$3,review_note=$4,reviewed_at=NOW(),revision=revision+1 WHERE id=$1 RETURNING reviewed_at,revision`, id, status, reviewerID, strings.TrimSpace(note)).Scan(&request.ReviewedAt, &request.Revision)
		if e != nil {
			return e
		}
		request.Status = status
		request.ReviewedBy = reviewerID
		request.ReviewNote = strings.TrimSpace(note)
		result = request
		return nil
	})
	return result, err
}
