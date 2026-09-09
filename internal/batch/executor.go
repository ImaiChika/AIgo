package batch

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"aigo/internal/domain"
	"aigo/internal/storage"
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

// Executor 隔离 AIgo 批量任务语义与具体推理平台协议。
// 当前 DashScope 实现使用 Files + Batches；未来本地队列、vLLM 或其他
// 执行器只需实现本接口，无需修改 API handler 和前端任务流程。
type Executor interface {
	GenerateAndSubmit(ctx context.Context, points []domain.KnowledgePoint, countPerPoint int, jobName string) (jobID string, requestCount int, err error)
	GetJobStatus(ctx context.Context, jobID string) (*BatchJob, error)
	ListJobs(ctx context.Context, name, status string, limit int) ([]BatchJob, error)
	ImportResults(ctx context.Context, jobID string, points []domain.KnowledgePoint) (*ImportResult, error)
	Capabilities() Capabilities
}

// DirectOutputImporter 仅用于兼容历史 CLI 直接传 DashScope output_file_id 的用法。
// 新的业务/API 路径应始终按 AIgo jobID 调用 Executor.ImportResults。
type DirectOutputImporter interface {
	DownloadAndImport(ctx context.Context, outputFileID string, points []domain.KnowledgePoint) (*ImportResult, error)
}

// RoutingExecutor 把稳定的 AIgo job ref 路由到对应 provider。
// DashScope 保留历史原始 ID；未来非 legacy 后端使用 "backend:provider_job_id"。
// 因此将默认执行器切到 local 后，只要仍注册 DashScope adapter，旧云端任务
// 依然可以查询和导入，不会被误发给新的本地后端。
type RoutingExecutor struct {
	defaultBackend  string
	defaultExecutor Executor
	legacyBackend   string
	routes          map[string]Executor
}

func newRoutingExecutor(defaultBackend string, defaultExecutor Executor, legacyBackend string, routes map[string]Executor) *RoutingExecutor {
	return &RoutingExecutor{
		defaultBackend:  strings.ToLower(strings.TrimSpace(defaultBackend)),
		defaultExecutor: defaultExecutor,
		legacyBackend:   strings.ToLower(strings.TrimSpace(legacyBackend)),
		routes:          routes,
	}
}

func FormatJobRef(backend, providerJobID string) string {
	backend = strings.ToLower(strings.TrimSpace(backend))
	providerJobID = strings.TrimSpace(providerJobID)
	if backend == "" || providerJobID == "" {
		return providerJobID
	}
	if backend == "dashscope" {
		// 保持现有 API/CLI 返回值以及复制到百炼控制台的 ID 完全兼容。
		if parsedBackend, parsedID, explicit := ParseJobRef(providerJobID); explicit && parsedBackend == backend {
			return parsedID
		}
		return providerJobID
	}
	if parsedBackend, _, explicit := ParseJobRef(providerJobID); explicit && parsedBackend == backend {
		return providerJobID
	}
	return backend + ":" + providerJobID
}

func ParseJobRef(jobRef string) (backend, providerJobID string, explicit bool) {
	jobRef = strings.TrimSpace(jobRef)
	backend, providerJobID, found := strings.Cut(jobRef, ":")
	if !found || strings.TrimSpace(backend) == "" || strings.TrimSpace(providerJobID) == "" {
		return "", jobRef, false
	}
	return strings.ToLower(strings.TrimSpace(backend)), strings.TrimSpace(providerJobID), true
}

func (r *RoutingExecutor) Capabilities() Capabilities {
	return r.defaultExecutor.Capabilities()
}

func (r *RoutingExecutor) GenerateAndSubmit(ctx context.Context, points []domain.KnowledgePoint, countPerPoint int, jobName string) (string, int, error) {
	providerJobID, count, err := r.defaultExecutor.GenerateAndSubmit(ctx, points, countPerPoint, jobName)
	if err != nil {
		return "", 0, err
	}
	return FormatJobRef(r.defaultBackend, providerJobID), count, nil
}

func (r *RoutingExecutor) route(jobRef string) (string, string, Executor, error) {
	backend, providerJobID, explicit := ParseJobRef(jobRef)
	if !explicit {
		backend = r.legacyBackend
	}
	executor := r.routes[backend]
	if executor == nil {
		return "", "", nil, fmt.Errorf("%w: 任务 %q 的执行器 %q 未配置", ErrUnavailable, jobRef, backend)
	}
	return backend, providerJobID, executor, nil
}

func (r *RoutingExecutor) GetJobStatus(ctx context.Context, jobRef string) (*BatchJob, error) {
	backend, providerJobID, executor, err := r.route(jobRef)
	if err != nil {
		return nil, err
	}
	job, err := executor.GetJobStatus(ctx, providerJobID)
	if err != nil {
		return nil, err
	}
	job.JobID = FormatJobRef(backend, providerJobID)
	job.Backend = backend
	return job, nil
}

func (r *RoutingExecutor) ImportResults(ctx context.Context, jobRef string, points []domain.KnowledgePoint) (*ImportResult, error) {
	_, providerJobID, executor, err := r.route(jobRef)
	if err != nil {
		return nil, err
	}
	return executor.ImportResults(ctx, providerJobID, points)
}

func (r *RoutingExecutor) ListJobs(ctx context.Context, name, status string, limit int) ([]BatchJob, error) {
	jobsByRef := make(map[string]BatchJob)
	var firstErr error
	for backend, executor := range r.routes {
		jobs, err := executor.ListJobs(ctx, name, status, limit)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, job := range jobs {
			if job.Backend != "" && job.Backend != backend {
				continue // adapter 不得把其他 provider 的共享存储记录冒充为自己的任务
			}
			job.JobID = FormatJobRef(backend, job.JobID)
			job.Backend = backend
			jobsByRef[job.JobID] = job
		}
	}
	if len(jobsByRef) == 0 && firstErr != nil {
		return nil, firstErr
	}
	jobs := make([]BatchJob, 0, len(jobsByRef))
	for _, job := range jobsByRef {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt > jobs[j].CreatedAt })
	if limit > 0 && len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}

func (r *RoutingExecutor) DownloadAndImport(ctx context.Context, outputFileID string, points []domain.KnowledgePoint) (*ImportResult, error) {
	executor := r.routes["dashscope"]
	direct, ok := executor.(DirectOutputImporter)
	if !ok {
		return nil, fmt.Errorf("%w: 未配置 DashScope 结果下载器", ErrUnavailable)
	}
	return direct.DownloadAndImport(ctx, outputFileID, points)
}

// NewExecutor 根据部署配置选择批量执行器。local 值特意保留为不可用占位，
// 直到 AIgo 本地持久队列或经验证的推理引擎适配器落地；绝不把本地
// /v1/chat/completions 误当成 DashScope Files + Batches API。
func NewExecutor(backend string, cfg DashScopeConfig, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore) (Executor, error) {
	backend = strings.ToLower(strings.TrimSpace(backend))
	dashScopeExecutor := NewDashScopeService(cfg, questionStore, batchJobStore)
	routes := make(map[string]Executor)
	if strings.TrimSpace(cfg.APIKey) != "" {
		routes["dashscope"] = dashScopeExecutor
	}
	if backend == "" || backend == "auto" {
		if strings.TrimSpace(cfg.APIKey) == "" {
			unavailable := NewUnavailableExecutor("disabled", "未配置 DASHSCOPE_API_KEY，云端批量已停用；本地批量执行器尚未接入")
			return newRoutingExecutor("disabled", unavailable, "dashscope", routes), nil
		}
		backend = "dashscope"
	}

	var active Executor
	switch backend {
	case "dashscope":
		active = dashScopeExecutor
	case "local":
		active = NewUnavailableExecutor("local", "本地实时推理可用，但本地批量任务队列尚未实现；请暂用单题生成或将批量后端设为 dashscope")
	case "disabled", "none":
		backend = "disabled"
		active = NewUnavailableExecutor("disabled", "当前部署已停用批量生成")
	default:
		return nil, fmt.Errorf("不支持的 QWEN_BATCH_BACKEND: %q", backend)
	}
	return newRoutingExecutor(backend, active, "dashscope", routes), nil
}

// UnavailableExecutor 是明确的能力占位实现，防止本地实时端点被误当成
// 支持 DashScope /files 和 /batches 的服务。
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
