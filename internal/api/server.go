// Package api 提供 HTTP REST API 服务。
// 将后端业务逻辑通过 HTTP 接口暴露给前端调用。
// 所有接口统一返回 JSON 格式，需要认证的接口通过 Bearer token 验证。
package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/audit"
	"aigo/internal/auth"
	"aigo/internal/bank"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/knowledge"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage"
)

// Server HTTP API 服务，持有所有业务服务的引用，负责路由注册和依赖管理。
type Server struct {
	pipe               *pipeline.Pipeline            // 流程编排（出题、评估、存储）
	kpSvc              *knowledge.Service            // 知识点服务
	reviewSvc          *review.Service               // 审核服务
	auditSvc           *audit.Service                // 审计日志服务
	questionStore      storage.QuestionStore         // 题目存储
	generationRunStore storage.GenerationRunStore    // 单题命题运行记录（刷新恢复与幂等）
	shareStore         storage.QuestionShareStore    // 个人题目分享申请存储
	authSvc            *auth.Service                 // 认证服务
	batchSvc           batch.Executor                // 可替换的批量推理执行器
	aiCheckSvc         *aicheck.Service              // AI 检查服务
	bankSvc            *bank.Service                 // 题库服务
	aiProviderStore    storage.AIProviderConfigStore // 系统级 AI 服务配置
	quotaStore         storage.GenerationQuotaStore  // 生成任务准入配置与当前用量
	corsOrigins        []string                      // 允许的跨域来源白名单（空=禁止跨域）
	registerEnabled    bool                          // 是否开放用户自助注册
	trustProxyHeaders  bool                          // 是否信任反向代理写入的客户端 IP 头
	readinessChecker   storage.ReadinessChecker
	readinessTimeout   time.Duration
	observabilityOnce  sync.Once
	httpMetrics        *httpMetrics
	requestLimiterOnce sync.Once
	requestLimitConfig RequestLimitConfig
	requestLimiter     *requestLimiter
}

// NewServer 创建 API 服务实例，注入所有依赖。
func NewServer(
	pipe *pipeline.Pipeline,
	kpSvc *knowledge.Service,
	reviewSvc *review.Service,
	auditSvc *audit.Service,
	questionStore storage.QuestionStore,
	authSvc *auth.Service,
	batchSvc batch.Executor,
	aiCheckSvc *aicheck.Service,
	bankSvc *bank.Service,
	corsOrigins []string,
	registerEnabled bool,
	trustProxyHeaders bool,
) *Server {
	readinessChecker, _ := questionStore.(storage.ReadinessChecker)
	shareStore, _ := questionStore.(storage.QuestionShareStore)
	generationRunStore, _ := questionStore.(storage.GenerationRunStore)
	aiProviderStore, _ := questionStore.(storage.AIProviderConfigStore)
	quotaStore, _ := questionStore.(storage.GenerationQuotaStore)
	return &Server{
		pipe:               pipe,
		kpSvc:              kpSvc,
		reviewSvc:          reviewSvc,
		auditSvc:           auditSvc,
		questionStore:      questionStore,
		generationRunStore: generationRunStore,
		shareStore:         shareStore,
		authSvc:            authSvc,
		batchSvc:           batchSvc,
		aiCheckSvc:         aiCheckSvc,
		bankSvc:            bankSvc,
		aiProviderStore:    aiProviderStore,
		quotaStore:         quotaStore,
		corsOrigins:        corsOrigins,
		registerEnabled:    registerEnabled,
		trustProxyHeaders:  trustProxyHeaders,
		readinessChecker:   readinessChecker,
		readinessTimeout:   2 * time.Second,
	}
}

// Handler 返回配置好所有路由的 http.Handler。
// 权限动作与权限点一一对应（见 internal/domain/permission.go）。
func (s *Server) Handler() http.Handler {
	s.ensureObservability()
	s.ensureRequestLimiter()
	mux := http.NewServeMux()

	// === 进程健康检查（公开、无业务数据和配置泄露） ===
	mux.HandleFunc("GET /health/live", s.handleLiveness)
	mux.HandleFunc("GET /health/ready", s.handleReadiness)

	// === 认证（公开接口，不需要 token） ===
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("GET /api/auth/register-enabled", s.handleRegisterEnabled)

	// === 认证（需登录） ===
	mux.HandleFunc("GET /api/auth/me", s.requireAuthAllowRevokedRole(s.handleMe))
	mux.HandleFunc("POST /api/auth/switch-role", s.requireAuthAllowRevokedRole(s.handleSwitchRole))
	mux.HandleFunc("PUT /api/auth/profile", s.requireAuth("", s.handleUpdateProfile))
	mux.HandleFunc("POST /api/auth/change-password", s.requireAuth("", s.handleChangePassword))
	mux.HandleFunc("GET /api/my/summary", s.requireAuth("", s.handleMySummary))

	// === 权限元数据（登录即可，供前端渲染权限矩阵） ===
	mux.HandleFunc("GET /api/permissions", s.requireAuth("", s.handleListPermissions))

	// === 角色模板管理 ===
	// 管理员需要读取可分配的业务角色；角色模板的新增、修改、删除仍仅限超级管理员。
	mux.HandleFunc("GET /api/roles", s.requireAuthAny([]string{domain.PermRoleManage, domain.PermUserManage}, s.handleListRoles))
	mux.HandleFunc("POST /api/roles", s.requireAuth(domain.PermRoleManage, s.handleCreateRole))
	mux.HandleFunc("PUT /api/roles/{id}", s.requireAuth(domain.PermRoleManage, s.handleUpdateRole))
	mux.HandleFunc("DELETE /api/roles/{id}", s.requireAuth(domain.PermRoleManage, s.handleDeleteRole))

	// === 系统级 AI 服务配置（只能由唯一超级管理员查看和修改） ===
	mux.HandleFunc("GET /api/system/ai-providers", s.requireSuperAdmin(s.handleListAIProviders))
	mux.HandleFunc("POST /api/system/ai-providers", s.requireSuperAdmin(s.handleCreateAIProvider))
	mux.HandleFunc("PUT /api/system/ai-providers/{id}", s.requireSuperAdmin(s.handleUpdateAIProvider))
	mux.HandleFunc("POST /api/system/ai-providers/{id}/activate", s.requireSuperAdmin(s.handleActivateAIProvider))
	mux.HandleFunc("DELETE /api/system/ai-providers/{id}", s.requireSuperAdmin(s.handleDeleteAIProvider))
	// 运行指标只向唯一超级管理员开放，不包含用户内容、DSN 或模型凭证。
	mux.HandleFunc("GET /api/system/runtime-metrics", s.requireSuperAdmin(s.handleRuntimeMetrics))
	mux.HandleFunc("GET /api/system/generation-quota", s.requireAuth(domain.PermGenerationQuotaManage, s.handleGetGenerationQuota))
	mux.HandleFunc("PUT /api/system/generation-quota", s.requireAuth(domain.PermGenerationQuotaManage, s.handlePutGenerationQuota))

	// === 题库管理 ===
	mux.HandleFunc("GET /api/banks", s.requireAuth("", s.handleListBanks))
	mux.HandleFunc("POST /api/banks", s.requireAuth(domain.PermBankManage, s.handleCreateBank))
	mux.HandleFunc("PUT /api/banks/{id}", s.requireAuth(domain.PermBankManage, s.handleUpdateBank))
	mux.HandleFunc("DELETE /api/banks/{id}", s.requireAuth(domain.PermBankManage, s.handleDeleteBank))
	mux.HandleFunc("GET /api/banks/{id}/questions", s.requireAuth(domain.PermBankManage, s.handleListBankQuestions))
	mux.HandleFunc("POST /api/banks/{id}/collect", s.requireAuth(domain.PermBankManage, s.handleCollectBank))

	// === 用户管理 ===
	mux.HandleFunc("GET /api/users", s.requireAuth(domain.PermUserManage, s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.requireAuth(domain.PermUserManage, s.handleCreateUser))
	mux.HandleFunc("PUT /api/users/{id}", s.requireAuth(domain.PermUserManage, s.handleUpdateUser))
	mux.HandleFunc("POST /api/users/{id}/reset-password", s.requireAuth(domain.PermUserManage, s.handleResetUserPassword))
	mux.HandleFunc("DELETE /api/users/{id}", s.requireAuth(domain.PermUserManage, s.handleDeleteUser))

	// === 统计：与题库入口同口径，任一题库分层查看权限即可进入；
	// 页内数据按各权限点独立裁剪（分层计数、AI 检查、用户数等） ===
	mux.HandleFunc("GET /api/stats", s.requireAuthAny([]string{
		domain.PermStatsView,
		domain.PermQuestionView,
		domain.PermQuestionViewFormal,
		domain.PermQuestionViewEliminated,
	}, s.handleStats))

	// === 操作日志 ===
	mux.HandleFunc("GET /api/audit-logs", s.requireAuth(domain.PermAuditView, s.handleListAuditLogs))
	mux.HandleFunc("GET /api/audit-logs/actions", s.requireAuth(domain.PermAuditView, s.handleListAuditActions))
	mux.HandleFunc("GET /api/audit-logs/question/{id}", s.requireAuth(domain.PermAuditView, s.handleAuditLogsByQuestion))
	mux.HandleFunc("GET /api/audit-logs/actor/{actor}", s.requireAuth(domain.PermAuditView, s.handleAuditLogsByActor))

	// === 题目 CRUD（题目唯一来源是 AI 生成；无手动新建） ===
	mux.HandleFunc("POST /api/questions/generate", s.requireAuth(domain.PermQuestionGenerate, s.handleGenerate))
	mux.HandleFunc("GET /api/generation-runs/{id}", s.requireAuth(domain.PermQuestionGenerate, s.handleGetGenerationRun))
	mux.HandleFunc("GET /api/questions", s.requireAuth("", s.handleListQuestions))
	mux.HandleFunc("GET /api/questions/search", s.requireAuth("", s.handleSearchQuestions))
	mux.HandleFunc("GET /api/questions/my-new", s.requireAuth(domain.PermReviewSubmit, s.handleMyNewQuestions))
	mux.HandleFunc("GET /api/questions/{id}/versions", s.requireAuth("", s.handleListQuestionVersions))
	mux.HandleFunc("POST /api/questions/{id}/restore", s.requireAuth(domain.PermQuestionEdit, s.handleRestoreQuestionVersion))
	mux.HandleFunc("POST /api/questions/{id}/unpublish", s.requireAuth(domain.PermUserManage, s.handleUnpublishQuestion))
	mux.HandleFunc("GET /api/questions/{id}", s.requireAuth("", s.handleGetQuestion))
	mux.HandleFunc("POST /api/questions/{id}/share", s.requireAuth(domain.PermQuestionShare, s.handleCreateQuestionShare))
	mux.HandleFunc("PUT /api/questions/{id}", s.requireAuth(domain.PermQuestionEdit, s.handleUpdateQuestion))
	mux.HandleFunc("POST /api/questions/{id}/bank", s.requireAuth(domain.PermBankManage, s.handleAddQuestionBank))
	mux.HandleFunc("DELETE /api/questions/{id}/bank/{bankId}", s.requireAuth(domain.PermBankManage, s.handleRemoveQuestionBank))
	mux.HandleFunc("POST /api/questions/bank-move-batch", s.requireAuth(domain.PermBankManage, s.handleMoveQuestionsBankBatch))
	// 删除=移入淘汰题库留档：有权限者可删其范围内题目（含全局题库清理），
	// 题目所有者本人无需删除权限；细分校验在 handler 内完成。
	mux.HandleFunc("DELETE /api/questions/{id}", s.requireAuth("", s.handleDeleteQuestion))
	mux.HandleFunc("POST /api/questions/{id}/publish", s.requireAuth(domain.PermReviewFinal, s.handlePublishQuestion))

	// 个人题目分享至全局题库：申请人只能看自己的申请，管理员可看待审批队列并一次性审批。
	mux.HandleFunc("GET /api/question-shares", s.requireAuthAny([]string{domain.PermQuestionShare, domain.PermQuestionShareReview}, s.handleListQuestionShares))
	mux.HandleFunc("POST /api/question-shares/preview", s.requireAuth(domain.PermQuestionShare, s.handlePreviewQuestionShares))
	mux.HandleFunc("POST /api/question-shares/batch", s.requireAuth(domain.PermQuestionShare, s.handleCreateQuestionShares))
	mux.HandleFunc("POST /api/question-shares/{id}/review", s.requireAuth(domain.PermQuestionShareReview, s.handleReviewQuestionShare))

	// === 元数据（登录即可） ===
	mux.HandleFunc("GET /api/meta/professions", s.requireAuth("", s.handleListProfessions))

	// === 知识点 ===
	mux.HandleFunc("GET /api/knowledge-points", s.requireAuth("", s.handleListKP))
	mux.HandleFunc("GET /api/knowledge-points/search", s.requireAuth("", s.handleSearchKP))
	mux.HandleFunc("GET /api/knowledge-points/meta", s.requireAuth("", s.handleKPMeta))
	mux.HandleFunc("GET /api/knowledge-points/tree", s.requireAuth("", s.handleKPTree))
	mux.HandleFunc("GET /api/knowledge-versions", s.requireAuth("", s.handleKPVersions))
	mux.HandleFunc("POST /api/knowledge-versions", s.requireAuth(domain.PermKnowledgeMng, s.handleCreateKPVersion))
	mux.HandleFunc("DELETE /api/knowledge-versions/{id}", s.requireAuth(domain.PermKnowledgeMng, s.handleDeleteKPVersion))
	mux.HandleFunc("POST /api/knowledge-versions/{id}/publish", s.requireAuth(domain.PermKnowledgeMng, s.handlePublishKPVersion))
	mux.HandleFunc("PUT /api/knowledge-points/{id}", s.requireAuth(domain.PermKnowledgeMng, s.handleUpdateKP))
	mux.HandleFunc("POST /api/knowledge-points/import", s.requireAuth(domain.PermKnowledgeMng, s.handleImportKP))
	mux.HandleFunc("POST /api/knowledge-points/export", s.requireAuth(domain.PermKnowledgeMng, s.handleExportKP))
	mux.HandleFunc("POST /api/knowledge-points", s.requireAuth(domain.PermKnowledgeMng, s.handleCreateKP))
	mux.HandleFunc("DELETE /api/knowledge-points/{id}", s.requireAuth(domain.PermKnowledgeMng, s.handleDeleteKP))

	// === 专家（查看登录即可，管理需专家库权限） ===
	mux.HandleFunc("GET /api/experts", s.requireAuth("", s.handleListExperts))
	mux.HandleFunc("POST /api/experts", s.requireAuth(domain.PermExpertManage, s.handleCreateExpert))
	mux.HandleFunc("PUT /api/experts/{id}", s.requireAuth(domain.PermExpertManage, s.handleUpdateExpert))
	mux.HandleFunc("DELETE /api/experts/{id}", s.requireAuth(domain.PermExpertManage, s.handleDeleteExpert))

	// === 导出（需下载权限，下载文件同样需要认证） ===
	mux.HandleFunc("POST /api/export/xlsx", s.requireAuth(domain.PermQuestionDownload, s.handleExportXlsx))
	mux.HandleFunc("POST /api/export/docx", s.requireAuth(domain.PermQuestionDownload, s.handleExportDocx))
	mux.HandleFunc("GET /api/export/download/{filename}", s.requireAuth(domain.PermQuestionDownload, s.handleDownloadExport))

	// === 批量推理（逐题调用单题生成 API，本地队列落库）===
	// 出题操作要求 batch:run；历史读取允许 batch:run 或全局查看权限，
	// 具体任务归属在 handler 内再次校验。
	mux.HandleFunc("GET /api/batch/capabilities", s.requireAuth(domain.PermBatchRun, s.handleBatchCapabilities))
	mux.HandleFunc("POST /api/batch/submit", s.requireAuth(domain.PermBatchRun, s.handleBatchSubmit))
	mux.HandleFunc("GET /api/batch/list", s.requireAuth(domain.PermBatchRun, s.handleBatchList))
	mux.HandleFunc("GET /api/batch/history", s.requireAuth("", s.handleBatchHistory))
	mux.HandleFunc("GET /api/batch/status/{jobId}", s.requireAuth("", s.handleBatchStatus))
	mux.HandleFunc("POST /api/batch/download/{jobId}", s.requireAuth("", s.handleBatchDownload))
	mux.HandleFunc("POST /api/batch/retry-failed/{jobId}", s.requireAuth(domain.PermBatchRun, s.handleBatchRetryFailed))

	// === AI 检查（题目首次生成后唯一一次自动执行）===
	// 不提供人工复检或强制通过入口；检查故障会耗尽并阻断人工审核流程。
	// 进度查询登录即可，逐题校验题库范围（生成/批量页轮询自己的检查进度）
	mux.HandleFunc("POST /api/ai-check/progress", s.requireAuth("", s.handleAICheckProgress))
	mux.HandleFunc("GET /api/ai-check/summary", s.requireAuth("", s.handleAICheckSummary))
	mux.HandleFunc("GET /api/ai-check/result/{questionId}", s.requireAuth("", s.handleAICheckResult))
	mux.HandleFunc("GET /api/ai-check/results", s.requireAuth(domain.PermUserManage, s.handleAICheckResults))

	// === 审核流程 ===
	// 提交审核仅系统管理员（user:manage）可操作；审核人员只负责投票
	mux.HandleFunc("POST /api/review/submit", s.requireAuthAny([]string{domain.PermReviewSubmit, domain.PermUserManage}, s.handleSubmitReview))
	mux.HandleFunc("POST /api/review/submit-batch", s.requireAuthAny([]string{domain.PermReviewSubmit, domain.PermUserManage}, s.handleSubmitReviewBatch))
	mux.HandleFunc("POST /api/review/resubmit-revisions", s.requireAuth(domain.PermReviewSubmit, s.handleResubmitRevisions))
	mux.HandleFunc("POST /api/review/action", s.requireAuth(domain.PermReviewDo, s.handleReviewAction))
	mux.HandleFunc("POST /api/review/finalize", s.requireAuth(domain.PermReviewFinal, s.handleReviewFinalize))
	mux.HandleFunc("GET /api/review/task/{id}", s.requireAuth("", s.handleGetReviewTask))
	mux.HandleFunc("GET /api/review/task-by-question/{questionId}", s.requireAuth("", s.handleGetTaskByQuestion))
	mux.HandleFunc("GET /api/review/records/{taskId}", s.requireAuth("", s.handleReviewRecords))
	// 审核记录是独立汇总能力，不能由个人题库查看权限旁路获得。
	mux.HandleFunc("GET /api/review/results", s.requireAuth(domain.PermReviewResults, s.handleReviewResults))
	mux.HandleFunc("GET /api/review/my-tasks", s.requireAuth("", s.handleMyTasks))
	mux.HandleFunc("GET /api/review/my-decisions", s.requireAuth("", s.handleMyDecisions))
	mux.HandleFunc("GET /api/review/my-revisions", s.requireAuth("", s.handleMyRevisions))
	mux.HandleFunc("GET /api/review/reviewers", s.requireAuth("", s.handleListReviewers))
	mux.HandleFunc("GET /api/review/available-flows", s.requireAuthAny([]string{domain.PermReviewSubmit, domain.PermUserManage}, s.handleAvailableReviewFlows))
	mux.HandleFunc("GET /api/review/flows", s.requireAuth(domain.PermFlowManage, s.handleListFlows))
	mux.HandleFunc("POST /api/review/flows", s.requireAuth(domain.PermFlowManage, s.handleCreateFlow))
	mux.HandleFunc("PUT /api/review/flows/{id}", s.requireAuth(domain.PermFlowManage, s.handleUpdateFlow))
	mux.HandleFunc("DELETE /api/review/flows/{id}", s.requireAuth(domain.PermFlowManage, s.handleDeleteFlow))

	// 包装中间件：请求体大小限制 + CORS 跨域 + JSON Content-Type
	return s.withRequestObservability(s.requestLimiter.middleware(withBodyLimit(withCORS(s.corsOrigins, withJSON(mux)))))
}

// maxRequestBody 请求体大小上限（10MB，覆盖 JSON 请求与文件上传）。
const maxRequestBody = 10 << 20

// withBodyLimit 限制请求体大小，防止无上限 io.ReadAll 耗尽内存。
// 超限请求会被 MaxBytesReader 截断并在读取时报错，返回 400/413。
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// handleListPermissions 返回权限点目录；admin_only 表示内置管理员专属、不可直接分配。
func (s *Server) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	type permissionItem struct {
		domain.PermissionMeta
		AdminOnly bool `json:"admin_only,omitempty"`
	}
	permissions := make([]permissionItem, 0, len(domain.AllPermissions()))
	for _, p := range domain.AllPermissions() {
		permissions = append(permissions, permissionItem{PermissionMeta: p, AdminOnly: p.Code == domain.PermGenerationQuotaManage})
	}
	writeJSON(w, 200, map[string]any{"permissions": permissions})
}

// handleStats 返回系统统计信息：题目数、知识点数、各分类分布。
// 计数在存储端聚合完成，权限范围一次取回后内存判定，避免整表载入与逐题查库（N+1）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := auth.GetUserID(ctx)
	requestedStatsScope := r.URL.Query().Get("scope")
	if requestedStatsScope != "" && requestedStatsScope != "personal" && requestedStatsScope != "global" {
		writeError(w, http.StatusBadRequest, "无效的统计范围: "+requestedStatsScope)
		return
	}
	if requestedStatsScope == "global" && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看全局统计")
		return
	}

	globalStats := requestedStatsScope == "global" || (requestedStatsScope == "" && s.hasPermission(r, domain.PermQuestionViewGlobal))
	legacyStats := requestedStatsScope == ""
	// 题库范围一次取回：待审核层统计与题库页同口径，只看 question:view 的范围。
	// stats:view 不再叠加额外限制——users.bank_ids 是用户级边界，两个权限点的
	// 题库范围完全一致，历史上要求“同时具备”只会把只有查看权限的老师挡在门外。
	// 全局统计即系统真实总量：不再按分享状态筛题，各分层总量与分布覆盖全部用户。
	viewScope := s.bankScopeEvalFor(ctx, userID, domain.PermQuestionView)
	workingFilter := tierFilter(domain.TierWorking, viewScope)
	if workingFilter != nil && !globalStats {
		workingFilter.OwnerID = userID
		workingFilter.IncludeLegacyOwner = legacyStats
	}

	// ===== 待审核题库统计（存储端聚合） =====
	statusDist := map[string]int{}
	difficultyDist := map[string]int{}
	professionDist := map[string]int{}
	creationTrend := map[string]int{}
	bankDist := map[string]int{}
	questionCount := 0
	kpCovered := 0
	if workingFilter != nil {
		if agg, err := s.questionStore.AggregateQuestionStats(ctx, *workingFilter, 90); err == nil {
			questionCount = agg.Total
			statusDist = agg.ByStatus
			creationTrend = agg.ByDay
			for profession, n := range agg.ByProfession {
				if profession == "" {
					profession = "未填写"
				}
				professionDist[profession] = n
			}
			for diff, n := range agg.ByDifficulty {
				if diff == "" {
					diff = "未填写"
				}
				difficultyDist[diff] = n
			}
			bankDist = agg.ByBank
			if agg.Unclassified > 0 {
				bankDist["待归类"] = agg.Unclassified
			}
			if ids, err := s.questionStore.CoveredKnowledgePointIDs(ctx, *workingFilter); err == nil {
				kpCovered = len(ids)
			}
		} else {
			slog.Warn("统计聚合查询失败", "error", err)
		}
	}

	// ===== 知识点统计（默认版本一次加载；各已发布版本总数复用列表自带的 PointCount） =====
	knowledgeCategories := map[string]int{}
	kpTotal := 0
	var kpVersion *knowledge.KPVersionStats
	kpVersionsTotal := map[string]int{}
	if s.kpSvc != nil {
		if kpStats, perVersion, err := s.kpSvc.KPStats(ctx); err == nil {
			kpVersion = kpStats
			kpTotal = kpStats.Total
			knowledgeCategories = kpStats.Categories
			kpVersionsTotal = perVersion
		} else {
			slog.Warn("知识点统计查询失败", "error", err)
		}
	}

	// ===== 题库分层计数：各层独立授权，仅返回用户有权查看的层 =====
	tierCounts := map[string]int{}
	if workingFilter != nil {
		tierCounts["working"] = questionCount
	}
	for _, tier := range []domain.QuestionTier{domain.TierFormal, domain.TierEliminated} {
		viewPerm := domain.TierViewPerm(tier)
		scope := s.bankScopeEvalFor(ctx, userID, viewPerm)
		filter := tierFilter(tier, scope)
		if filter == nil {
			continue
		}
		if !globalStats {
			filter.OwnerID = userID
			filter.IncludeLegacyOwner = legacyStats
		}
		counts, err := s.questionStore.CountQuestionsByStatus(ctx, *filter)
		if err != nil {
			continue
		}
		total := 0
		for _, n := range counts {
			total += n
		}
		tierCounts[string(tier)] = total
	}

	// ===== AI 质量检查统计：全局按全量聚合；个人范围按题目归属人过滤。
	// 此前只在全局范围计算，导致“我的数据”下即使个人题库有题，AI 检查也永远为空。
	// 淘汰档案带 owner_id（旧档案无归属仅计入全局）；检查结果经 JOIN questions 按归属过滤。
	aiCheck := map[string]any{}
	if s.aiCheckSvc != nil {
		if globalStats {
			if verdicts, err := s.aiCheckSvc.VerdictSummary(ctx); err == nil {
				aiCheck["verdicts"] = verdicts
			}
			if discarded, err := s.aiCheckSvc.DiscardCount(ctx); err == nil {
				aiCheck["discarded"] = discarded
			}
			if tasks, err := s.aiCheckSvc.TaskSummary(ctx); err == nil {
				aiCheck["tasks"] = tasks
			}
		} else {
			if verdicts, err := s.aiCheckSvc.VerdictSummaryForOwner(ctx, userID); err == nil {
				aiCheck["verdicts"] = verdicts
			}
			if discarded, err := s.aiCheckSvc.DiscardCountForOwner(ctx, userID); err == nil {
				aiCheck["discarded"] = discarded
			}
			if tasks, err := s.aiCheckSvc.TaskSummaryForOwner(ctx, userID); err == nil {
				aiCheck["tasks"] = tasks
			}
		}
	}

	// ===== 批量推理任务统计（尽力而为：批量服务不可用时静默跳过） =====
	batchStats := map[string]int{}
	if s.batchSvc != nil && globalStats {
		if jobs, err := s.batchSvc.ListJobs(ctx, "", "", 200); err == nil {
			for _, job := range jobs {
				batchStats["total"]++
				batchStats[job.Status]++
			}
		}
	}

	// ===== 用户数：系统级指标，仅“全局数据 + 用户管理权限”才返回；字段缺省时前端隐藏卡片 =====
	userCount := 0
	hasUserCount := false
	if globalStats {
		if canManageUsers, _ := s.authSvc.HasPermission(ctx, userID, domain.PermUserManage); canManageUsers {
			if users, err := s.authSvc.ListUsers(); err == nil {
				userCount = len(users)
				hasUserCount = true
			}
		}
	}

	response := map[string]any{
		// 兼容保留的旧字段
		"question_count":          questionCount,
		"knowledge_count":         kpTotal,
		"knowledge_categories":    knowledgeCategories,
		"status_distribution":     statusDist,
		"difficulty_distribution": difficultyDist,
		"bank_distribution":       bankDist,
		"tier_distribution":       map[string]int{"working": questionCount},
		// 新增统计
		"kp_version":              kpVersion,
		"kp_versions_total":       kpVersionsTotal,
		"kp_covered":              kpCovered,
		"profession_distribution": professionDist,
		"creation_trend":          creationTrend,
		"tier_counts":             tierCounts,
		"ai_check":                aiCheck,
		"batch_jobs":              batchStats,
		"generated_at":            time.Now().Format(time.RFC3339),
	}
	if hasUserCount {
		// 仅授权时携带，未授权时不输出 user_count=0（前端会把 0 当成真实值展示）。
		response["user_count"] = userCount
	}
	writeJSON(w, 200, response)
}

// requireAuth 创建带 JWT 认证和权限检查的 handler 包装函数。
// action 为空字符串时只需登录成功即可，不检查具体权限。
// 权限从数据库实时查询（角色模板权限 ∪ 直接分配权限），同时校验账号启用状态。
func (s *Server) requireAuth(action string, handler http.HandlerFunc) http.HandlerFunc {
	return s.requireAuthWithOptions(action, false, handler)
}

// requireAuthAllowRevokedRole 跳过“token 中的身份仍被分配”校验，仅供
// switch-role / me 使用：账号本身仍然有效，当前身份被管理员撤销后，
// 用户仍可读取剩余身份并重新选择身份，而不是被强制退出重新登录。
// 被撤销的旧身份不会注入 context（按空角色走默认身份解析），其权限随之失效。
func (s *Server) requireAuthAllowRevokedRole(handler http.HandlerFunc) http.HandlerFunc {
	return s.requireAuthWithOptions("", true, handler)
}

func (s *Server) requireAuthWithOptions(action string, allowRevokedRole bool, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 从 Authorization 头提取 Bearer token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, 401, "缺少登录凭证")
			return
		}
		token := authHeader
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			token = authHeader[7:]
		}

		// 验证 JWT 签名和有效期
		claims, err := s.authSvc.ValidateToken(token)
		if err != nil {
			writeError(w, 401, err.Error())
			return
		}

		// 校验账号仍存在且启用（停用后 token 立即失效）
		user, err := s.authSvc.GetUserByID(claims.UserID)
		if err != nil || user == nil {
			writeError(w, 401, "用户不存在")
			return
		}
		if !user.Enabled {
			writeError(w, 401, "账号已被禁用")
			return
		}
		if claims.Role != "" {
			assigned := false
			for _, roleID := range user.Roles {
				if roleID == claims.Role {
					assigned = true
					break
				}
			}
			if !assigned {
				if !allowRevokedRole {
					writeError(w, 401, "当前身份已被管理员撤销，请重新选择身份")
					return
				}
				// 以空角色继续：后续 handler 按默认身份解析，已撤销身份的权限不生效。
				claims.Role = ""
			}
		}

		// 将用户信息注入 context，后续 handler 可通过 auth.GetUserID(ctx) 等方法获取
		ctx := r.Context()
		ctx = context.WithValue(ctx, auth.UserIDKey, claims.UserID)
		ctx = context.WithValue(ctx, auth.UsernameKey, claims.Username)
		ctx = context.WithValue(ctx, auth.DisplayNameKey, claims.DisplayName)
		ctx = context.WithValue(ctx, auth.RoleKey, claims.Role)

		// 权限检查（action 为空表示只需登录）
		if action != "" {
			ok, err := s.authSvc.HasPermission(ctx, claims.UserID, action)
			if err != nil {
				writeError(w, 500, "权限查询失败")
				return
			}
			if !ok {
				writeError(w, 403, "权限不足：需要 "+action)
				return
			}
		}

		handler(w, r.WithContext(ctx))
	}
}

// requireSuperAdmin 是系统级凭证配置的第二道后端边界。即使普通管理员被
// 授予了其他全部业务权限，也不能通过伪造权限点访问 AI API Key 配置。
func (s *Server) requireSuperAdmin(handler http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth("", func(w http.ResponseWriter, r *http.Request) {
		// 空角色旧 token（账号事后被挂 super_admin）不视为超管，要求重登/选身份。
		if auth.GetRole(r.Context()) == "" {
			writeError(w, http.StatusForbidden, "当前登录未选择工作身份，请重新登录后再操作")
			return
		}
		ok, err := s.authSvc.IsSuperAdmin(r.Context(), auth.GetUserID(r.Context()))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "超级管理员身份校验失败")
			return
		}
		if !ok {
			writeError(w, http.StatusForbidden, "仅超级管理员可以管理 AI 服务配置")
			return
		}
		handler(w, r)
	})
}

// requireAuthAny 用于“读取信息”和“管理动作”共用的只读接口：满足任一
// 权限即可。每个真正的写接口仍使用单独的最小权限点。
func (s *Server) requireAuthAny(actions []string, handler http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth("", func(w http.ResponseWriter, r *http.Request) {
		userID := auth.GetUserID(r.Context())
		for _, action := range actions {
			ok, err := s.authSvc.HasPermission(r.Context(), userID, action)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "权限查询失败")
				return
			}
			if ok {
				handler(w, r)
				return
			}
		}
		writeError(w, http.StatusForbidden, "权限不足")
	})
}
