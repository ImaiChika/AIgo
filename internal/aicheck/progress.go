// progress.go 提供检查进度的题目粒度查询，供前端分段进度与概况统计使用。
package aicheck

import (
	"context"
	"fmt"
	"time"

	"aigo/internal/domain"
)

// CheckProgress 单题的检查进度快照。
type CheckProgress struct {
	QuestionID       string     `json:"question_id"`
	QuestionStatus   string     `json:"question_status"`
	QuestionVer      int        `json:"question_version"`
	TaskStatus       string     `json:"task_status"` // "" = 从未入队
	Attempts         int        `json:"attempts"`
	MaxAttempts      int        `json:"max_attempts"`
	LastError        string     `json:"last_error,omitempty"`
	Verdict          string     `json:"verdict,omitempty"` // 最新检查结论 pass / issues_found / reject
	ResultStale      bool       `json:"result_stale"`      // 题目版本新于检查结果版本
	CheckStartedAt   *time.Time `json:"check_started_at,omitempty"`
	CheckCompletedAt *time.Time `json:"check_completed_at,omitempty"`
	// Discarded=true 表示题目首次检查未通过、已自动淘汰删除；原因见 Verdict/Issues/Suggestion/StemSummary
	Discarded   bool                 `json:"discarded,omitempty"`
	StemSummary string               `json:"stem_summary,omitempty"`
	Issues      []domain.ReviewIssue `json:"issues,omitempty"`
	Suggestion  string               `json:"suggestion,omitempty"`
	// 淘汰详情补充：四维评分、检查模型与完整题目快照（旧留档无快照时为空）
	Scores  *domain.ReviewScores `json:"scores,omitempty"`
	Model   string               `json:"model,omitempty"`
	Question *domain.A2Question  `json:"question,omitempty"`
	// DiscardOwnerID 淘汰题归属人，供接口层做越权过滤，不外发
	DiscardOwnerID string `json:"-"`
}

// ProgressByQuestionIDs 汇总多题的检查进度。
// 已淘汰（检查不通过自动删除）的题目不在题目库中，从淘汰记录中还原原因。
// counts 返回粗粒度统计：pending/running/exhausted/no_task（任务维度）、
// passed/issues/discarded（结论维度），供"检查中 x/N"等进度展示使用。
func (s *Service) ProgressByQuestionIDs(ctx context.Context, questionIDs []string) ([]CheckProgress, map[string]int, error) {
	counts := map[string]int{}
	if len(questionIDs) == 0 {
		return nil, counts, nil
	}

	tasks := map[string]domain.AICheckTask{}
	if s.taskStore != nil {
		var err error
		tasks, err = s.taskStore.LatestCheckTasksByQuestionIDs(ctx, questionIDs)
		if err != nil {
			return nil, nil, fmt.Errorf("查询检查任务失败: %w", err)
		}
	}
	results := map[string]domain.AIReviewResult{}
	if len(tasks) > 0 {
		ids := make([]string, 0, len(tasks))
		for _, t := range tasks {
			ids = append(ids, t.QuestionID)
		}
		list, err := s.aiReviewStore.ListByQuestionIDs(ctx, ids)
		if err != nil {
			return nil, nil, fmt.Errorf("查询检查结果失败: %w", err)
		}
		for _, r := range list {
			results[r.QuestionID] = r
		}
	}
	discards, err := s.aiReviewStore.ListDiscardResultsByQuestionIDs(ctx, questionIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("查询淘汰记录失败: %w", err)
	}

	items := make([]CheckProgress, 0, len(questionIDs))
	for _, id := range questionIDs {
		item := CheckProgress{QuestionID: id, MaxAttempts: s.maxAttemptsOrDefault()}
		if q, err := s.questionStore.GetQuestion(ctx, id); err == nil && q != nil {
			item.QuestionStatus = string(q.Status)
			item.QuestionVer = q.Version
			// 暂存态（检查未完成）不暴露题干预览：待检内容对用户不可见；
			// 通过后（ai_reviewed+）才下发摘要；淘汰题走下方 discard 分支的留档摘要。
			if !domain.IsStagingStatus(q.Status) {
				item.StemSummary = stemSummary(q.ClinicalStem, 60)
			}
		}
		if task, ok := tasks[id]; ok {
			item.TaskStatus = task.Status
			item.Attempts = task.Attempts
			item.MaxAttempts = task.MaxAttempts
			item.LastError = task.LastError
			startedAt := task.CreatedAt
			item.CheckStartedAt = &startedAt
			if task.Status == domain.AICheckTaskSucceeded || task.Status == domain.AICheckTaskExhausted {
				completedAt := task.UpdatedAt
				item.CheckCompletedAt = &completedAt
			}
		}
		if r, ok := results[id]; ok {
			item.Verdict = r.Verdict
			item.ResultStale = r.QuestionVersion < item.QuestionVer
		}
		if discard, ok := discards[id]; ok {
			item.Discarded = true
			item.Verdict = discard.Verdict
			item.StemSummary = discard.StemSummary
			item.Issues = discard.Issues
			item.Suggestion = discard.Suggestion
			item.Scores = &discard.Scores
			item.Model = discard.Model
			item.Question = discard.Question
			item.DiscardOwnerID = discard.OwnerID
			completedAt := discard.CreatedAt
			item.CheckCompletedAt = &completedAt
		}
		items = append(items, item)

		if item.Discarded {
			counts["discarded"]++
			continue
		}
		switch item.TaskStatus {
		case domain.AICheckTaskPending:
			counts["pending"]++
		case domain.AICheckTaskRunning:
			counts["running"]++
		case domain.AICheckTaskExhausted:
			counts["exhausted"]++
		}
		switch item.Verdict {
		case "pass":
			counts["passed"]++
		case "issues_found", "reject":
			counts["issues"]++
		}
	}
	return items, counts, nil
}

// TaskSummary 返回全量检查任务的状态计数（概况统计用）。
func (s *Service) TaskSummary(ctx context.Context) (map[string]int, error) {
	if s.taskStore == nil {
		return map[string]int{}, nil
	}
	return s.taskStore.CountCheckTasksByStatus(ctx)
}

// VerdictSummary 按结论统计全部 AI 检查结果（数据统计页用）。
func (s *Service) VerdictSummary(ctx context.Context) (map[string]int, error) {
	if s.aiReviewStore == nil {
		return map[string]int{}, nil
	}
	return s.aiReviewStore.CountReviewResultsByVerdict(ctx)
}

// DiscardCount 返回 AI 检查淘汰留档总数（数据统计页用）。
func (s *Service) DiscardCount(ctx context.Context) (int, error) {
	if s.aiReviewStore == nil {
		return 0, nil
	}
	return s.aiReviewStore.CountDiscardResults(ctx)
}
