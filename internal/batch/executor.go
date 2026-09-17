package batch

import (
	"context"
	"errors"
	"fmt"

	"aigo/internal/domain"
)

// ErrUnavailable 表示当前部署未启用可用的批量执行器。
var ErrUnavailable = errors.New("批量执行器不可用")

// ErrNotReady 表示任务存在但尚未到可导入结果的终态。
var ErrNotReady = errors.New("批量任务尚未完成")

// Capabilities 是可安全返回给前端的批量运行信息，不包含密钥或完整端点。
type Capabilities struct {
	Backend       string `json:"backend"`
	Available     bool   `json:"available"`
	ExecutionMode string `json:"execution_mode"`
	Model         string `json:"model,omitempty"`
	Message       string `json:"message"`
	Endpoint      string `json:"-"`
}

// Executor 隔离 AIgo 批量任务语义与具体执行方式。
// 当前唯一实现是 LocalExecutor：每道题单独调用单题生成 API，
// 不再绑定 Qwen 平台的 Files + Batches 批量协议。
type Executor interface {
	GenerateAndSubmit(ctx context.Context, points []domain.KnowledgePoint, countPerPoint int, jobName string) (jobID string, requestCount int, err error)
	GetJobStatus(ctx context.Context, jobID string) (*BatchJob, error)
	ListJobs(ctx context.Context, name, status string, limit int) ([]BatchJob, error)
	ImportResults(ctx context.Context, jobID string, points []domain.KnowledgePoint) (*ImportResult, error)
	Capabilities() Capabilities
}

// BatchJob 批量任务状态。
type BatchJob struct {
	JobID        string `json:"job_id"`
	OwnerID      string `json:"owner_id,omitempty"` // 提交任务的用户；管理员查看他人任务时用于只读标识
	Backend      string `json:"backend,omitempty"`
	Model        string `json:"model,omitempty"`
	JobName      string `json:"job_name"` // 自定义任务名称
	Status       string `json:"status"`   // pending/in_progress/completed/failed/cancelled
	TotalCount   int    `json:"total_count"`
	Completed    int    `json:"completed"`
	Failed       int    `json:"failed"`
	OutputFileID string `json:"output_file_id"`
	CreatedAt    int64  `json:"created_at"`
	Error        string `json:"error,omitempty"`
	// ImportedAt 非空表示结果已完成导入（导入幂等）；前端据此决定是否触发自动导入。
	ImportedAt string `json:"imported_at,omitempty"`
	// Tracked 表示该任务在本地 batch_jobs 有记录（导入幂等可用）。
	Tracked bool `json:"tracked,omitempty"`
}

// ImportResult 导入结果详情。
type ImportResult struct {
	Saved       int          `json:"saved"`        // 实际成功入库题目数
	Failed      int          `json:"failed"`       // 失败请求行数（不是题目数）
	Items       []ImportItem `json:"items"`        // 每条导入详情
	QuestionIDs []string     `json:"question_ids"` // 本次入库的题目 ID，用于触发 AI 自动检查
	Simulated   bool         `json:"simulated,omitempty"`
	Message     string       `json:"message,omitempty"`
}

// ImportItem 单条导入结果。
type ImportItem struct {
	OutlineCode string `json:"outline_code"` // 大纲代码
	Count       int    `json:"count"`        // 导入题目数
	Status      string `json:"status"`       // "ok" / "failed"
	Error       string `json:"error"`        // 失败原因
}

// batchPointID 返回任务内知识点的稳定标识：历史大纲（无版本）按大纲代码，
// 版本化大纲按知识点 ID，保证跨版本快照不串位。
func batchPointID(p domain.KnowledgePoint) string {
	if p.OutlineCode != "" && (p.VersionID == "" || p.VersionID == domain.LegacyKnowledgeVersion) {
		return p.OutlineCode
	}
	return p.ID
}

// UnavailableExecutor 是明确的能力占位实现，用于批量生成被部署停用的场景。
type UnavailableExecutor struct {
	info Capabilities
}

func NewUnavailableExecutor(backend, reason string) *UnavailableExecutor {
	if backend == "" {
		backend = "disabled"
	}
	if reason == "" {
		reason = "当前部署未配置批量执行器"
	}
	return &UnavailableExecutor{info: Capabilities{
		Backend:       backend,
		Available:     false,
		ExecutionMode: "unavailable",
		Message:       reason,
	}}
}

func (e *UnavailableExecutor) Capabilities() Capabilities { return e.info }

func (e *UnavailableExecutor) unavailable() error {
	return fmt.Errorf("%w: %s", ErrUnavailable, e.info.Message)
}

func (e *UnavailableExecutor) GenerateAndSubmit(context.Context, []domain.KnowledgePoint, int, string) (string, int, error) {
	return "", 0, e.unavailable()
}

func (e *UnavailableExecutor) GetJobStatus(context.Context, string) (*BatchJob, error) {
	return nil, e.unavailable()
}

func (e *UnavailableExecutor) ListJobs(context.Context, string, string, int) ([]BatchJob, error) {
	return []BatchJob{}, nil
}

func (e *UnavailableExecutor) ImportResults(context.Context, string, []domain.KnowledgePoint) (*ImportResult, error) {
	return nil, e.unavailable()
}
