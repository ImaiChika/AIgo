package review

import (
	"context"
	"sort"
	"time"

	"aigo/internal/domain"
)

type ReviewHistoryItem struct {
	TaskID          string                `json:"task_id"`
	PreviousTaskID  string                `json:"previous_task_id,omitempty"`
	FlowID          string                `json:"flow_id"`
	FlowName        string                `json:"flow_name"`
	Attempt         int                   `json:"attempt"`
	QuestionVersion int                   `json:"question_version"`
	Status          domain.QuestionStatus `json:"status"`
	Superseded      bool                  `json:"superseded"`
	CreatedAt       time.Time             `json:"created_at"`
	Records         []domain.ReviewRecord `json:"records"`
}

type questionReviewTasks interface {
	ListQuestionReviewTasks(context.Context, string) ([]domain.ReviewTask, error)
}

// QuestionReviewHistory 保留跨流程的退修原因，进行中只向作者开放需修改意见及本人记录。
// 调用方仍须先按题库/任务权限授权题目访问，full只用于既有管理员/把关授权。
func (s *Service) QuestionReviewHistory(ctx context.Context, qid, viewer string, full bool) ([]ReviewHistoryItem, error) {
	q, err := s.questionStore.GetQuestion(ctx, qid)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, domain.ErrReviewTaskNotFound
	}
	var tasks []domain.ReviewTask
	if store, ok := s.reviewStore.(questionReviewTasks); ok {
		tasks, err = store.ListQuestionReviewTasks(ctx, qid)
	} else {
		all, e := s.reviewStore.ListAllTasks(ctx)
		err = e
		for _, t := range all {
			if t.QuestionID == qid {
				tasks = append(tasks, t)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	ids := []string{}
	superseded := map[string]bool{}
	for _, t := range tasks {
		ids = append(ids, t.ID)
		if t.PreviousTaskID != "" {
			superseded[t.PreviousTaskID] = true
		}
	}
	records, err := s.reviewStore.ListRecordsByTaskIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	public := q.Status == domain.StatusPublished || q.Status == domain.StatusRejected || q.Status == domain.StatusArchived
	byTask := map[string][]domain.ReviewRecord{}
	for _, r := range records {
		if full || public || r.ExpertID == viewer || (q.OwnerID == viewer && r.Conclusion == domain.StatusRevisionRequired) {
			byTask[r.TaskID] = append(byTask[r.TaskID], r)
		}
	}
	out := []ReviewHistoryItem{}
	for _, t := range tasks {
		flow, err := s.reviewStore.GetFlowConfig(ctx, t.FlowID)
		if err != nil {
			return nil, err
		}
		name := t.FlowID
		if flow != nil {
			name = flow.Name
		}
		rr := byTask[t.ID]
		if rr == nil {
			rr = []domain.ReviewRecord{}
		}
		out = append(out, ReviewHistoryItem{TaskID: t.ID, PreviousTaskID: t.PreviousTaskID, FlowID: t.FlowID, FlowName: name, Attempt: attemptOrOne(t.Attempt), QuestionVersion: t.QuestionVersion, Status: t.Status, Superseded: superseded[t.ID], CreatedAt: t.CreatedAt, Records: rr})
	}
	return out, nil
}
