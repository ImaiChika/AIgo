// Package storage 定义数据存储接口。
// 生产实现为 PostgreSQL (internal/storage/postgres)。
// 测试实现为内存 (internal/storage/testutil)。
package storage

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	"aigo/internal/domain"
)

func DifficultyMatchesBand(value, band string) bool {
	if value == band {
		return true
	}
	numeric, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	switch band {
	case "easy":
		return numeric <= 0.60
	case "medium":
		return numeric > 0.60 && numeric <= 0.80
	case "hard":
		return numeric > 0.80
	default:
		return false
	}
}

// SearchTokens 把空格、逗号和分号分隔的查询拆成 AND 关系关键词。
func SearchTokens(query string) []string {
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(query)), func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '，' || r == ';' || r == '；'
	})
	seen := make(map[string]bool, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" && !seen[part] {
			seen[part] = true
			result = append(result, part)
		}
	}
	return result
}

// BuildQuestionSearchText 生成统一题目检索文本。生产存储把它持久化并建立 trigram 索引，
// 内存存储和导出筛选复用同一语义，避免不同页面“同词不同结果”。
func BuildQuestionSearchText(q domain.A2Question) string {
	values := []string{
		q.ID, q.ClinicalStem, q.Answer, q.Explanation, q.CognitiveLevel,
		q.ExamPoints, q.OutlineCode, q.Profession, q.System,
	}
	for _, option := range q.Options {
		values = append(values, option.Label, option.Text)
	}
	for _, point := range q.KnowledgePoints {
		values = append(values, point.ID, point.VersionName, point.Category, point.Subject,
			point.Unit, point.SubItem, point.Topic, point.OutlineCode, strings.Join(point.Keywords, " "))
	}
	for _, ref := range q.SourceRefs {
		values = append(values, ref.Title, ref.URL, ref.Note)
	}
	return strings.ToLower(strings.Join(values, " "))
}

// QuestionMatchesKeyword 是内存回退实现使用的统一题目全文搜索语义。
// 每个查询词都必须至少命中一个可见业务字段。
func QuestionMatchesKeyword(q domain.A2Question, query string) bool {
	searchText := BuildQuestionSearchText(q)
	for _, token := range SearchTokens(query) {
		if !strings.Contains(searchText, token) {
			return false
		}
	}
	return true
}

type questionChangeContextKey struct{}

// QuestionChange 描述产生新题目内容版本的来源。状态/题库归属变化不会创建内容版本。
type QuestionChange struct {
	Actor      string
	OwnerID    string
	ChangeType string
	ChangeNote string
	CreatedAt  time.Time
}

func WithQuestionChange(ctx context.Context, change QuestionChange) context.Context {
	return context.WithValue(ctx, questionChangeContextKey{}, change)
}

func QuestionChangeFromContext(ctx context.Context) QuestionChange {
	change, _ := ctx.Value(questionChangeContextKey{}).(QuestionChange)
	return change
}

// ReadinessChecker 供进程就绪探针检查数据库连接和关键 Schema。
// 返回错误只用于服务端判定，不应把底层 DSN/SQL 错误直接暴露给客户端。
type ReadinessChecker interface {
	CheckReadiness(ctx context.Context) error
}

// QuestionFilter 描述题目列表/搜索在数据库端的过滤条件。空值/空切片表示不过滤。
type QuestionFilter struct {
	OwnerID             string   // 个人题库范围：仅返回该用户归属的题目
	IncludeLegacyOwner  bool     // 兼容未迁移归属的历史题目（仅旧版未显式 scope 的查询使用）
	IncludeLegacyGlobal bool     // 全局库兼容迁移前无个人归属的旧题目
	GlobalStatuses      []string // 全局题库分享状态（pending/approved/rejected）
	Status              string   // 状态等值
	Tiers               []string // 题库分层多选（formal/working/eliminated，展开为状态集合）
	Difficulty          string   // 难度等值
	DifficultyBand      string   // 难度分组（easy/medium/hard，兼容数值系数与旧文本值）
	Keyword             string   // 多字段全文包含匹配；多关键词按 AND 组合
	OutlineCode         string   // 大纲代码前缀
	Professions         []string // 专业多选（任一命中）
	BankID              string   // 指定题库（多对多成员之一）
	Unclassified        bool     // 仅未归入任何分类子题库的题目
	ClassifiableOnly    bool     // 仅尚未进入审核、允许调整分类子题库的待审核题目
	BankScope           []string // 权限可见题库范围
	ScopeRestricted     bool     // true 时仅返回 BankScope 内题库的题目
}

// ReviewResultQuery 描述审核记录页的数据库端查询。
// Filter 用于当前列表筛选；StatsFilter 只包含调用者权限边界，保证统计卡片不泄露越权数据。
type ReviewResultQuery struct {
	Filter      QuestionFilter
	StatsFilter QuestionFilter
	FinalStatus string
	Page        int
	PageSize    int
}

type ReviewResultRow struct {
	Question    domain.A2Question
	Task        *domain.ReviewTask
	FinalStatus string
}

type ReviewResultPage struct {
	Rows  []ReviewResultRow
	Total int
	Stats map[string]int
}

// ReviewResultQueryStore 是生产存储可选实现的审核记录高效查询能力。
// 未实现时审核服务会回退到内存筛选，便于轻量测试存储继续工作。
type ReviewResultQueryStore interface {
	SearchReviewResults(ctx context.Context, query ReviewResultQuery) (*ReviewResultPage, error)
}

// QuestionStatsAggregate 存储端聚合的题目统计分布（避免整表载入内存）。
type QuestionStatsAggregate struct {
	Total        int            // 过滤范围内题目总数
	ByStatus     map[string]int // 状态 → 数量
	ByDifficulty map[string]int // 难度 → 数量
	ByProfession map[string]int // 专业 → 数量（按数量降序取前 N，N 由实现决定）
	ByDay        map[string]int // 近 days 天每日新增（key: YYYY-MM-DD）
	ByBank       map[string]int // 分类子题库 ID → 数量（多对多按归属计）
	Unclassified int            // 不属于任何分类子题库的题目数
}

// QuestionStore 题目存储接口。
type QuestionStore interface {
	SaveQuestion(ctx context.Context, question domain.A2Question) error
	SaveQuestions(ctx context.Context, questions []domain.A2Question) (int, error)
	ListQuestions(ctx context.Context) ([]domain.A2Question, error)
	// SearchQuestions 按过滤条件在存储端过滤并分页，返回当前页与命中总数。
	// 结果按创建时间倒序（ID 兜底保证分页稳定）；page 从 1 开始。
	SearchQuestions(ctx context.Context, filter QuestionFilter, page, pageSize int) ([]domain.A2Question, int, error)
	// CountQuestionsByStatus 按状态统计题目数量（存储端 GROUP BY，避免整表载入）。
	CountQuestionsByStatus(ctx context.Context, filter QuestionFilter) (map[string]int, error)
	// AggregateQuestionStats 在存储端聚合题目统计分布；days<=0 视为 30。
	AggregateQuestionStats(ctx context.Context, filter QuestionFilter, days int) (*QuestionStatsAggregate, error)
	// CoveredKnowledgePointIDs 返回过滤范围内题目引用的知识点 ID 去重列表。
	CoveredKnowledgePointIDs(ctx context.Context, filter QuestionFilter) ([]string, error)
	GetQuestion(ctx context.Context, id string) (*domain.A2Question, error)
	ListQuestionVersions(ctx context.Context, questionID string) ([]domain.QuestionVersion, error)
	GetQuestionVersion(ctx context.Context, questionID string, version int) (*domain.QuestionVersion, error)
	DeleteQuestion(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
	// ListProfessions 返回题目表中出现过的全部非空专业值（筛选下拉用）。
	ListProfessions(ctx context.Context) ([]string, error)
}

// GenerationRunStore 持久化单题命题运行记录，供刷新恢复、阶段计时和请求幂等使用。
// 运行记录只保存执行状态和题目 ID，不替代题目及 AI 检查任务表。
type GenerationRunStore interface {
	CreateGenerationRun(ctx context.Context, run domain.GenerationRun) (bool, error)
	GetGenerationRun(ctx context.Context, id string) (*domain.GenerationRun, error)
	CompleteGenerationRun(ctx context.Context, id string, questionIDs []string) error
	FailGenerationRun(ctx context.Context, id, message string) error
}

// QuestionShareItem 是分享申请及其题目内容。题目中的 CreatedBy/OwnerID
// 在 JSON 序列化时隐藏，OwnerName 仅供管理员审批页展示申请人，不进入题目对象。
type QuestionShareItem struct {
	Request       domain.QuestionShareRequest `json:"request"`
	Question      domain.A2Question           `json:"question"`
	OwnerName     string                      `json:"owner_name,omitempty"`
	OwnerUsername string                      `json:"owner_username,omitempty"`
}

// QuestionShareStore 持久化个人题目到全局题库的一次性申请。
type QuestionShareStore interface {
	CreateQuestionShare(ctx context.Context, request domain.QuestionShareRequest) error
	GetQuestionShareByQuestionID(ctx context.Context, questionID string) (*domain.QuestionShareRequest, error)
	ListQuestionShares(ctx context.Context, status, ownerID string) ([]QuestionShareItem, error)
	ReviewQuestionShare(ctx context.Context, id, reviewerID string, status domain.QuestionShareStatus, note string) (*domain.QuestionShareRequest, error)
}

// BulkQuestionShareStore previews a bounded snapshot, then submits those exact IDs.
// Existing applications are skipped; the database rechecks ownership and publication.
type BulkQuestionShareStore interface {
	PreviewQuestionShares(ctx context.Context, filter QuestionFilter, limit int) ([]string, int, error)
	CreateQuestionShares(ctx context.Context, ownerID string, questionIDs []string) ([]string, error)
}

var ErrQuestionShareNotOwner = errors.New("只能分享本人题库中的题目")

var (
	ErrQuestionShareNotFound        = errors.New("分享申请不存在")
	ErrQuestionShareAlreadyReviewed = errors.New("分享申请已经审批，不能重复处理")
	ErrQuestionShareNotFormal       = errors.New("只有个人正式题目才能申请分享")
)

// RoleStore 角色模板存储接口。
type RoleStore interface {
	SaveRole(ctx context.Context, role domain.Role) error
	GetRole(ctx context.Context, id string) (*domain.Role, error)
	ListRoles(ctx context.Context) ([]domain.Role, error)
	DeleteRole(ctx context.Context, id string) error
}

// BankStore 题库存储接口。
type BankStore interface {
	SaveBank(ctx context.Context, bank domain.QuestionBank) error
	GetBank(ctx context.Context, id string) (*domain.QuestionBank, error)
	ListBanks(ctx context.Context) ([]domain.QuestionBank, error)
	DeleteBank(ctx context.Context, id string) error
	// AddQuestionsToBank 批量把题目加入题库（多对多，高效 SQL 插入成员关系，返回加入数量）。
	AddQuestionsToBank(ctx context.Context, questionIDs []string, bankID string) (int, error)
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
	// AcquireReviewMutationLock 串行化审核状态变更。
	// 生产实现使用 PostgreSQL advisory lock，确保多进程部署也不会丢票或重复送审；
	// 调用方必须执行返回的 release。
	AcquireReviewMutationLock(ctx context.Context) (release func() error, err error)
	// WithReviewTransaction 将审核表与题目表的关联写入放在同一事务中。
	// fn 必须使用传入的 txCtx 调用 ReviewStore/QuestionStore；返回错误时全部回滚。
	WithReviewTransaction(ctx context.Context, questionStore QuestionStore, fn func(txCtx context.Context) error) error

	SaveFlowConfig(ctx context.Context, flow domain.ReviewFlowConfig) error
	GetFlowConfig(ctx context.Context, id string) (*domain.ReviewFlowConfig, error)
	ListFlowConfigs(ctx context.Context) ([]domain.ReviewFlowConfig, error)
	DeleteFlowConfig(ctx context.Context, id string) error

	SaveTask(ctx context.Context, task domain.ReviewTask) error
	GetTask(ctx context.Context, id string) (*domain.ReviewTask, error)
	GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error)
	UpdateTask(ctx context.Context, task domain.ReviewTask) error
	DeleteTask(ctx context.Context, id string) error                        // 删除任务（级联删审核记录）
	CountActiveTasksByFlow(ctx context.Context, flowID string) (int, error) // 统计某流程的进行中任务数
	CountTasksByFlow(ctx context.Context, flowID string) (int, error)       // 统计某流程的全部任务数（含历史）

	SaveRecord(ctx context.Context, record domain.ReviewRecord) error
	ListRecordsByTaskID(ctx context.Context, taskID string) ([]domain.ReviewRecord, error)
	ListAllTasks(ctx context.Context) ([]domain.ReviewTask, error) // 全部任务（我的审核/待决断/结果汇总用）
	// ListRecordsByTaskIDs 批量读取多个任务的审核记录（审核结果汇总按页加载用）。
	ListRecordsByTaskIDs(ctx context.Context, taskIDs []string) ([]domain.ReviewRecord, error)
}

// KnowledgeStore 知识点存储接口。
var ErrKnowledgeConflict = errors.New("知识点已被修改或大纲代码已存在，请刷新后重试")
var ErrKnowledgeNoDefault = errors.New("暂无已启用的大纲版本")
var ErrKnowledgeNotFound = errors.New("知识点或大纲版本不存在")

type KnowledgeStore interface {
	ListVersionPoints(ctx context.Context, versionID string) ([]domain.KnowledgePoint, error)
	DeleteKnowledgeVersion(ctx context.Context, id string) (int, error)
	ListKnowledgeVersions(ctx context.Context) ([]domain.KnowledgeVersion, error)
	CreateKnowledgeVersion(ctx context.Context, version domain.KnowledgeVersion) error
	PublishKnowledgeVersion(ctx context.Context, id string) error
	SaveVersionPoints(ctx context.Context, versionID string, points []domain.KnowledgePoint, replace bool) (inserted, updated, duplicated int, err error)
	UpdatePoint(ctx context.Context, point domain.KnowledgePoint) error
	SavePoints(ctx context.Context, points []domain.KnowledgePoint) (inserted, updated, duplicated int, err error)
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

// AIProviderConfigStore 持久化系统级 AI 服务配置。
// 配置中的 API Key 由生产存储负责加密后落库；调用方只能通过内部领域对象读取，
// HTTP 层必须返回脱敏信息。实现还必须保证同一时间最多一个 active 配置。
type AIProviderConfigStore interface {
	ListAIProviderConfigs(ctx context.Context) ([]domain.AIProviderConfig, error)
	GetActiveAIProviderConfig(ctx context.Context) (*domain.AIProviderConfig, error)
	SaveAIProviderConfig(ctx context.Context, config domain.AIProviderConfig) error
	ActivateAIProviderConfig(ctx context.Context, id string) error
	DeleteAIProviderConfig(ctx context.Context, id string) error
}

var (
	ErrAIProviderConfigNotFound = errors.New("AI 服务配置不存在")
	ErrAIProviderNoActive       = errors.New("暂无启用的 AI 服务配置")
)

// BatchJob 批量任务记录（数据库存储）。
type BatchJobRecord struct {
	ID             string `json:"id"`
	OwnerID        string `json:"-"` // 提交批量任务的用户，个人批量题目归属边界
	Backend        string `json:"backend"`
	BackendProfile string `json:"backend_profile"`
	Model          string `json:"model"`
	JobName        string `json:"job_name"`
	Status         string `json:"status"`
	TotalCount     int    `json:"total_count"`
	Completed      int    `json:"completed"`
	Failed         int    `json:"failed"`
	OutputFileID   string `json:"output_file_id"`
	PointsJSON     string `json:"points_json"` // 知识点列表JSON
	// ImportedAt 非空表示任务结果已入库（导入幂等标记）；ImportResult 保存上次
	// 导入结果 JSON，重复触发导入时重放而不重新入库。
	ImportedAt   string `json:"imported_at,omitempty"`
	ImportResult string `json:"import_result,omitempty"`
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
	// ClaimBatchJobImport 抢占式标记任务为已导入（imported_at=NOW()）。
	// 返回 false 表示任务已被导入过或正在导入，用于导入幂等。
	ClaimBatchJobImport(ctx context.Context, id string) (bool, error)
	// SaveBatchJobImportResult 覆盖写入导入结果 JSON（抢占成功后调用）。
	SaveBatchJobImportResult(ctx context.Context, id string, resultJSON string) error
	// ReleaseBatchJobImport 释放导入标记；仅用于尚未写入任何题目的前置失败（如下载失败），允许重试。
	ReleaseBatchJobImport(ctx context.Context, id string) error
}

// AIReviewStore AI 检查结果存储接口。
type AIReviewStore interface {
	// SaveReviewResult 保存一条 AI 检查结果
	SaveReviewResult(ctx context.Context, result domain.AIReviewResult) error
	// GetLatestByQuestionID 获取某题最新的 AI 检查结果
	GetLatestByQuestionID(ctx context.Context, questionID string) (*domain.AIReviewResult, error)
	// ListByQuestionIDs 批量获取多题的最新检查结果
	ListByQuestionIDs(ctx context.Context, questionIDs []string) ([]domain.AIReviewResult, error)
	// ListAll 列出所有检查结果（按时间倒序）
	ListAll(ctx context.Context, limit int) ([]domain.AIReviewResult, error)
	// SaveDiscardResult 保存一条 AI 检查淘汰记录（检查不通过的题目删除前留档）
	SaveDiscardResult(ctx context.Context, discard domain.AICheckDiscard) error
	// ListDiscardResultsByQuestionIDs 批量获取多题的淘汰记录（已删除的题目才有）
	ListDiscardResultsByQuestionIDs(ctx context.Context, questionIDs []string) (map[string]domain.AICheckDiscard, error)
	// CountReviewResultsByVerdict 按 verdict 统计 AI 检查结果数量（存储端聚合）。
	CountReviewResultsByVerdict(ctx context.Context) (map[string]int, error)
	// CountDiscardResults 统计 AI 检查淘汰留档总数。
	CountDiscardResults(ctx context.Context) (int, error)
}

// AICheckOutcome 汇总一次 AI 检查结论需要落库的全部写入。
// 事务语义：Result 必须保存；随后按结论处理题目——通过时用 Question（含推进后的
// 状态）保存，不通过且题目仍是草稿时写 Discard 淘汰档案并删除题目；
// CompleteTaskID 非空时同事务把检查任务标记为成功。任一步失败整体回滚。
type AICheckOutcome struct {
	Result           domain.AIReviewResult
	Question         *domain.A2Question     // 非 nil：保存题目（状态推进，内容不变）
	Discard          *domain.AICheckDiscard // 非 nil：保存淘汰档案
	DeleteQuestionID string                 // 非 ""：删除题目（首检不通过的草稿）
	CompleteTaskID   string                 // 非 ""：同事务完成任务
}

// AICheckOutcomeStore 是生产存储可选实现的事务化检查落库能力：
// 把“检查结果、题目状态/淘汰档案、题目删除、任务完成”收敛为一个数据库事务，
// 消除“结果已保存但题目状态未更新”或“淘汰档案已写但题目仍可见”的中间态。
// 未实现该接口的存储（如测试内存实现）由 aicheck 服务回退为分步写入。
type AICheckOutcomeStore interface {
	ApplyAICheckOutcome(ctx context.Context, outcome AICheckOutcome) error
}

// AICheckTaskStore AI 检查任务持久化队列存储接口。
// 语义对应 PostgreSQL 行级抢占（FOR UPDATE SKIP LOCKED），内存实现保持一致。
type AICheckTaskStore interface {
	// EnqueueCheckTask 幂等入队：该题目已存在 pending/running 任务时不新建，返回是否新建成功。
	EnqueueCheckTask(ctx context.Context, task domain.AICheckTask) (bool, error)
	// ClaimNextCheckTask 抢占下一个可执行任务（pending 且到达可重试时间，或 running 租约已过期），
	// 置为 running、执行次数+1 并续租；队列暂无可执行任务时返回 (nil, nil)。
	ClaimNextCheckTask(ctx context.Context, lease time.Duration) (*domain.AICheckTask, error)
	// CompleteCheckTask 标记任务成功完成。
	CompleteCheckTask(ctx context.Context, taskID string) error
	// FailCheckTask 记录一次失败：已达最大执行次数则标记 exhausted（最终失败），
	// 否则回到 pending 并在 backoff 后才可再次被抢占。
	FailCheckTask(ctx context.Context, taskID string, errMsg string, backoff time.Duration) error
	// LatestCheckTasksByQuestionIDs 返回每个题目的最新一条任务记录（无任务记录的题目不出现）。
	LatestCheckTasksByQuestionIDs(ctx context.Context, questionIDs []string) (map[string]domain.AICheckTask, error)
	// CountCheckTasksByStatus 按状态统计全量任务数。
	CountCheckTasksByStatus(ctx context.Context) (map[string]int, error)
}
