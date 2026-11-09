package domain

import (
	"errors"
	"time"
)

var ErrReviewConflict = errors.New("审核状态已变化，请刷新后重试")
var ErrReviewTaskNotFound = errors.New("审核任务已不存在，可能已被还原，请刷新列表")
var ErrReviewRestoreForbidden = errors.New("无审核还原权限")
var ErrReviewRestoreUnavailable = errors.New("审核还原存储未配置")

const MaxReviewRestoreQuestions = 1000

// ReviewRestoreTarget 是用户确认的审核快照，与内容版本分开防止状态往返后的旧写入。
type ReviewRestoreTarget struct {
	QuestionID     string    `json:"question_id"`
	TaskID         string    `json:"task_id"`
	Version        int       `json:"version"`
	ReviewRevision int64     `json:"review_revision"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ReviewRestoreCandidate struct {
	Question        A2Question        `json:"question"`
	Task            ReviewTask        `json:"task"`
	Flow            ReviewFlowConfig  `json:"flow"`
	CreatorUsername string            `json:"creator_username"`
	CreatorName     string            `json:"creator_name"`
	Eligible        bool              `json:"eligible"`
	Reason          string            `json:"reason,omitempty"`
	RecordCount     int               `json:"record_count"`
	TaskCount       int               `json:"task_count"`
	History         []ReviewTask      `json:"history,omitempty"`
	Records         []ReviewRecord    `json:"records,omitempty"`
	People          map[string]string `json:"people,omitempty"`
}

// 仅返回在途任务所涉及的流程/作者；不要求额外的用户或流程管理权限。
type ReviewRestoreOption struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}
type ReviewRestoreOptions struct {
	Flows  []ReviewRestoreOption `json:"flows"`
	Owners []ReviewRestoreOption `json:"owners"`
}

func (c ReviewRestoreCandidate) Target() ReviewRestoreTarget {
	return ReviewRestoreTarget{QuestionID: c.Question.ID, TaskID: c.Task.ID, Version: c.Question.Version, ReviewRevision: c.Question.ReviewRevision, UpdatedAt: c.Task.UpdatedAt}
}

type ReviewRestoreFilter struct {
	EligibleOnly bool     `json:"-"`
	FlowID       string   `json:"flow_id,omitempty"`
	OwnerID      string   `json:"owner_id,omitempty"`
	Keyword      string   `json:"keyword,omitempty"`
	Status       string   `json:"status,omitempty"`
	TaskIDs      []string `json:"task_ids,omitempty"`
}

type ReviewRestoreResult struct {
	OperationID    string   `json:"operation_id"`
	QuestionIDs    []string `json:"question_ids"`
	Restored       int      `json:"restored"`
	DeletedTasks   int      `json:"deleted_tasks"`
	DeletedRecords int      `json:"deleted_records"`
	DeletedLogs    int      `json:"deleted_logs"`
	Replayed       bool     `json:"replayed,omitempty"`
}

type ReviewRestoreCommand struct {
	OperationID string
	ActorID     string
	ActorName   string
	Reason      string
	RequestHash string
	Targets     []ReviewRestoreTarget
}

// ReviewExpectation 只读并发条件，不是要求服务端写入的新修订值。
type ReviewExpectation struct {
	QuestionID     string `json:"question_id,omitempty"`
	Version        int    `json:"expected_version"`
	ReviewRevision int64  `json:"expected_review_revision"`
	PreviousTaskID string `json:"previous_task_id,omitempty"`
}
