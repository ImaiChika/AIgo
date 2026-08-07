// Package storage 定义数据存储接口。
// 生产实现为 PostgreSQL (internal/storage/postgres)。
// 测试实现为内存 (internal/storage/testutil)。
package storage

import (
	"context"

	"aigo/internal/domain"
)

// QuestionStore 题目存储接口。
type QuestionStore interface {
	SaveQuestion(ctx context.Context, question domain.A2Question) error
	SaveQuestions(ctx context.Context, questions []domain.A2Question) (int, error)
	ListQuestions(ctx context.Context) ([]domain.A2Question, error)
	GetQuestion(ctx context.Context, id string) (*domain.A2Question, error)
	DeleteQuestion(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
}

// ExpertStore 专家存储接口。
type ExpertStore interface {
	SaveExpert(ctx context.Context, expert domain.Expert) error
	GetExpert(ctx context.Context, id string) (*domain.Expert, error)
	ListExperts(ctx context.Context) ([]domain.Expert, error)
	UpdateExpert(ctx context.Context, expert domain.Expert) error
	DeleteExpert(ctx context.Context, id string) error
}

// ReviewStore 审核相关存储接口。
type ReviewStore interface {
	SaveFlowConfig(ctx context.Context, flow domain.ReviewFlowConfig) error
	GetFlowConfig(ctx context.Context, id string) (*domain.ReviewFlowConfig, error)
	ListFlowConfigs(ctx context.Context) ([]domain.ReviewFlowConfig, error)
	DeleteFlowConfig(ctx context.Context, id string) error

	SaveTask(ctx context.Context, task domain.ReviewTask) error
	GetTask(ctx context.Context, id string) (*domain.ReviewTask, error)
	GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error)
	UpdateTask(ctx context.Context, task domain.ReviewTask) error

	SaveRecord(ctx context.Context, record domain.ReviewRecord) error
	ListRecordsByTaskID(ctx context.Context, taskID string) ([]domain.ReviewRecord, error)
}

// ImageStore 图片相关存储接口。
type ImageStore interface {
	SavePrompt(ctx context.Context, prompt domain.ImagePrompt) error
	GetPrompt(ctx context.Context, id string) (*domain.ImagePrompt, error)
	GetPromptByQuestionID(ctx context.Context, questionID string) (*domain.ImagePrompt, error)

	SaveImage(ctx context.Context, img domain.GeneratedImage) error
	GetImage(ctx context.Context, id string) (*domain.GeneratedImage, error)
	ListImagesByQuestionID(ctx context.Context, questionID string) ([]domain.GeneratedImage, error)
	ListImagesByPromptID(ctx context.Context, promptID string) ([]domain.GeneratedImage, error)
	UpdateImageStatus(ctx context.Context, id string, status domain.ImageStatus, note string) error

	SaveReviewRecord(ctx context.Context, record domain.ImageReviewRecord) error
	ListReviewRecordsByImageID(ctx context.Context, imageID string) ([]domain.ImageReviewRecord, error)
}

// KnowledgeStore 知识点存储接口。
type KnowledgeStore interface {
	SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, error)
	GetPoint(ctx context.Context, id string) (*domain.KnowledgePoint, error)
	ListPoints(ctx context.Context) ([]domain.KnowledgePoint, error)
	SearchPoints(ctx context.Context, keyword string) ([]domain.KnowledgePoint, error)
	ListBySubject(ctx context.Context, subject string) ([]domain.KnowledgePoint, error)
	DeletePoint(ctx context.Context, id string) error
	KPCount(ctx context.Context) (int, error)
}

// AuditStore 操作日志存储接口。
type AuditStore interface {
	SaveLog(ctx context.Context, log domain.AuditLog) error
	ListLogs(ctx context.Context, limit int) ([]domain.AuditLog, error)
	ListLogsByQuestion(ctx context.Context, questionID string) ([]domain.AuditLog, error)
	ListLogsByActor(ctx context.Context, actor string) ([]domain.AuditLog, error)
}

// BatchJob 批量任务记录（数据库存储）。
type BatchJobRecord struct {
	ID           string `json:"id"`
	JobName      string `json:"job_name"`
	Status       string `json:"status"`
	TotalCount   int    `json:"total_count"`
	Completed    int    `json:"completed"`
	Failed       int    `json:"failed"`
	OutputFileID string `json:"output_file_id"`
	PointsJSON   string `json:"points_json"` // 知识点列表JSON
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// BatchJobStore 批量任务存储接口。
type BatchJobStore interface {
	SaveBatchJob(ctx context.Context, job BatchJobRecord) error
	UpdateBatchJob(ctx context.Context, job BatchJobRecord) error
	GetBatchJob(ctx context.Context, id string) (*BatchJobRecord, error)
	ListBatchJobs(ctx context.Context, limit int) ([]BatchJobRecord, error)
	SearchBatchJobs(ctx context.Context, name string, limit int) ([]BatchJobRecord, error)
}
