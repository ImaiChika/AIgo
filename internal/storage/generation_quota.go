package storage

import (
	"context"
	"errors"
	"fmt"
)

// GenerationQuota 控制提交时的任务准入；所有数值是全站配置，修改后立即生效。
// GlobalPendingQuestions 统计尚未完成的生成单元与首次 AI 检查任务，
// 同一道单题生成运行收尾和首检短暂交叠时只计算一次。
type GenerationQuota struct {
	SingleMaxQuestions     int `json:"single_max_questions"`
	BatchMaxQuestions      int `json:"batch_max_questions"`
	SingleActivePerUser    int `json:"single_active_per_user"`
	BatchActivePerUser     int `json:"batch_active_per_user"`
	GlobalPendingQuestions int `json:"global_pending_questions"`
}

func DefaultGenerationQuota() GenerationQuota {
	return GenerationQuota{SingleMaxQuestions: 20, BatchMaxQuestions: 100,
		SingleActivePerUser: 2, BatchActivePerUser: 1, GlobalPendingQuestions: 500}
}

func (q GenerationQuota) Validate() error {
	checks := []struct {
		name            string
		value, min, max int
	}{
		{"单题每次题数", q.SingleMaxQuestions, 1, 20},
		{"批量每任务题数", q.BatchMaxQuestions, 1, 500},
		{"每人未完成单题任务数", q.SingleActivePerUser, 1, 10},
		{"每人活跃批量任务数", q.BatchActivePerUser, 1, 5},
		{"全站未完成出题/检查量", q.GlobalPendingQuestions, 20, 5000},
	}
	for _, c := range checks {
		if c.value < c.min || c.value > c.max {
			return fmt.Errorf("%s应在 %d–%d 之间", c.name, c.min, c.max)
		}
	}
	if q.GlobalPendingQuestions < q.SingleMaxQuestions || q.GlobalPendingQuestions < q.BatchMaxQuestions {
		return errors.New("全站未完成出题/检查量不能小于单题或批量的单次上限")
	}
	return nil
}

type GenerationQuotaUsage struct {
	ActiveSingleRuns             int `json:"active_single_runs"`
	ActiveBatchJobs              int `json:"active_batch_jobs"`
	ActiveAICheckTasks           int `json:"active_ai_check_tasks"`
	ActiveBatchImports           int `json:"active_batch_imports"`
	SinglePendingQuestions       int `json:"single_pending_questions"`
	BatchPendingQuestions        int `json:"batch_pending_questions"`
	BatchImportReservedQuestions int `json:"batch_import_reserved_questions"`
	OverlapQuestions             int `json:"overlap_questions"`
	PendingQuestionUnits         int `json:"pending_question_units"`
}

type GenerationQuotaStore interface {
	GetGenerationQuota(ctx context.Context) (GenerationQuota, GenerationQuotaUsage, error)
	SaveGenerationQuota(ctx context.Context, quota GenerationQuota) error
}

var ErrGenerationQuotaExceeded = errors.New("生成任务配额已满")
var ErrBatchJobAlreadyActive = errors.New("批量任务已在执行中")

// BatchJobRetryAdmitter 原子地将终态批量任务重新纳入配额并设为执行中。
type BatchJobRetryAdmitter interface {
	ReactivateBatchJob(ctx context.Context, job BatchJobRecord) error
}

type GenerationQuotaError struct{ Message string }

func (e *GenerationQuotaError) Error() string        { return e.Message }
func (e *GenerationQuotaError) Is(target error) bool { return target == ErrGenerationQuotaExceeded }
