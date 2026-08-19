// Package api 提供 HTTP REST API 服务。
// 将后端业务逻辑通过 HTTP 接口暴露给前端调用。
// 所有接口统一返回 JSON 格式，需要认证的接口通过 Bearer token 验证。
package api

import (
	"context"
	"fmt"
	"net/http"

	"aigo/internal/aicheck"
	"aigo/internal/audit"
	"aigo/internal/auth"
	"aigo/internal/bank"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/image"
	"aigo/internal/knowledge"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage"
)

// Server HTTP API 服务，持有所有业务服务的引用，负责路由注册和依赖管理。
type Server struct {
	pipe          *pipeline.Pipeline    // 流程编排（出题、评估、存储）
	kpSvc         *knowledge.Service    // 知识点服务
	imgSvc        *image.Service        // 图片服务
	reviewSvc     *review.Service       // 审核服务
	auditSvc      *audit.Service        // 审计日志服务
	questionStore storage.QuestionStore // 题目存储
	authSvc       *auth.Service         // 认证服务
	batchSvc      *batch.Service        // 批量推理服务
	aiCheckSvc    *aicheck.Service      // AI 检查服务
	bankSvc       *bank.Service         // 题库服务
}

// NewServer 创建 API 服务实例，注入所有依赖。
func NewServer(
	pipe *pipeline.Pipeline,
	kpSvc *knowledge.Service,
	imgSvc *image.Service,
	reviewSvc *review.Service,
	auditSvc *audit.Service,
	questionStore storage.QuestionStore,
	authSvc *auth.Service,
	batchSvc *batch.Service,
	aiCheckSvc *aicheck.Service,
	bankSvc *bank.Service,
) *Server {
	return &Server{
		pipe:          pipe,
		kpSvc:         kpSvc,
		imgSvc:        imgSvc,
		reviewSvc:     reviewSvc,
		auditSvc:      auditSvc,
		questionStore: questionStore,
		authSvc:       authSvc,
		batchSvc:      batchSvc,
		aiCheckSvc:    aiCheckSvc,
		bankSvc:       bankSvc,
	}
}

// Handler 返回配置好所有路由的 http.Handler。
// 权限动作与权限点一一对应（见 internal/domain/permission.go）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// === 认证（公开接口，不需要 token） ===
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)

	// === 认证（需登录） ===
	mux.HandleFunc("GET /api/auth/me", s.requireAuth("", s.handleMe))
	mux.HandleFunc("PUT /api/auth/profile", s.requireAuth("", s.handleUpdateProfile))
	mux.HandleFunc("POST /api/auth/change-password", s.requireAuth("", s.handleChangePassword))

	// === 权限元数据（登录即可，供前端渲染权限矩阵） ===
	mux.HandleFunc("GET /api/permissions", s.requireAuth("", s.handleListPermissions))

	// === 角色模板管理 ===
	mux.HandleFunc("GET /api/roles", s.requireAuth(domain.PermRoleManage, s.handleListRoles))
	mux.HandleFunc("POST /api/roles", s.requireAuth(domain.PermRoleManage, s.handleCreateRole))
	mux.HandleFunc("PUT /api/roles/{id}", s.requireAuth(domain.PermRoleManage, s.handleUpdateRole))
	mux.HandleFunc("DELETE /api/roles/{id}", s.requireAuth(domain.PermRoleManage, s.handleDeleteRole))

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

	// === 统计（需统计分析权限） ===
	mux.HandleFunc("GET /api/stats", s.requireAuth(domain.PermStatsView, s.handleStats))

	// === 静态文件（图片） ===
	mux.Handle("GET /images/", http.StripPrefix("/images/", http.FileServer(http.Dir("output/images"))))

	// === 操作日志 ===
	mux.HandleFunc("GET /api/audit-logs", s.requireAuth(domain.PermAuditView, s.handleListAuditLogs))
	mux.HandleFunc("GET /api/audit-logs/question/{id}", s.requireAuth(domain.PermAuditView, s.handleAuditLogsByQuestion))
	mux.HandleFunc("GET /api/audit-logs/actor/{actor}", s.requireAuth(domain.PermAuditView, s.handleAuditLogsByActor))

	// === 题目 CRUD ===
	mux.HandleFunc("POST /api/questions/generate", s.requireAuth(domain.PermQuestionGenerate, s.handleGenerate))
	mux.HandleFunc("POST /api/questions", s.requireAuth(domain.PermQuestionCreate, s.handleCreateQuestion))
	mux.HandleFunc("GET /api/questions", s.requireAuth(domain.PermQuestionView, s.handleListQuestions))
	mux.HandleFunc("GET /api/questions/search", s.requireAuth(domain.PermQuestionView, s.handleSearchQuestions))
	mux.HandleFunc("GET /api/questions/{id}", s.requireAuth(domain.PermQuestionView, s.handleGetQuestion))
	mux.HandleFunc("PUT /api/questions/{id}", s.requireAuth(domain.PermQuestionEdit, s.handleUpdateQuestion))
	mux.HandleFunc("POST /api/questions/{id}/bank", s.requireAuth(domain.PermBankManage, s.handleAddQuestionBank))
	mux.HandleFunc("DELETE /api/questions/{id}/bank/{bankId}", s.requireAuth(domain.PermBankManage, s.handleRemoveQuestionBank))
	mux.HandleFunc("POST /api/questions/bank-move-batch", s.requireAuth(domain.PermBankManage, s.handleMoveQuestionsBankBatch))
	mux.HandleFunc("DELETE /api/questions/{id}", s.requireAuth(domain.PermQuestionDelete, s.handleDeleteQuestion))
	mux.HandleFunc("POST /api/questions/{id}/publish", s.requireAuth(domain.PermReviewFinal, s.handlePublishQuestion))

	// === 元数据（登录即可） ===
	mux.HandleFunc("GET /api/meta/professions", s.requireAuth("", s.handleListProfessions))

	// === 知识点 ===
	mux.HandleFunc("GET /api/knowledge-points", s.requireAuth("", s.handleListKP))
	mux.HandleFunc("GET /api/knowledge-points/search", s.requireAuth("", s.handleSearchKP))
	mux.HandleFunc("GET /api/knowledge-points/meta", s.requireAuth("", s.handleKPMeta))
	mux.HandleFunc("POST /api/knowledge-points/import", s.requireAuth(domain.PermKnowledgeMng, s.handleImportKP))
	mux.HandleFunc("POST /api/knowledge-points", s.requireAuth(domain.PermKnowledgeMng, s.handleCreateKP))
	mux.HandleFunc("DELETE /api/knowledge-points/{id}", s.requireAuth(domain.PermKnowledgeMng, s.handleDeleteKP))

	// === 专家（查看登录即可，管理需专家库权限） ===
	mux.HandleFunc("GET /api/experts", s.requireAuth("", s.handleListExperts))
	mux.HandleFunc("POST /api/experts", s.requireAuth(domain.PermExpertManage, s.handleCreateExpert))
	mux.HandleFunc("PUT /api/experts/{id}", s.requireAuth(domain.PermExpertManage, s.handleUpdateExpert))
	mux.HandleFunc("DELETE /api/experts/{id}", s.requireAuth(domain.PermExpertManage, s.handleDeleteExpert))

	// === 图片（提示词、候选图、审核） ===
	mux.HandleFunc("POST /api/images/prompt", s.requireAuth(domain.PermImageGenerate, s.handleImagePrompt))
	mux.HandleFunc("POST /api/images/generate", s.requireAuth(domain.PermImageGenerate, s.handleImageGenerate))
	mux.HandleFunc("GET /api/images/{questionId}", s.requireAuth("", s.handleListImages))
	mux.HandleFunc("POST /api/images/review", s.requireAuth(domain.PermImageReview, s.handleImageReview))

	// === 导出（需下载权限，下载文件同样需要认证） ===
	mux.HandleFunc("POST /api/export/xlsx", s.requireAuth(domain.PermQuestionDownload, s.handleExportXlsx))
	mux.HandleFunc("POST /api/export/docx", s.requireAuth(domain.PermQuestionDownload, s.handleExportDocx))
	mux.HandleFunc("GET /api/export/download/{filename}", s.requireAuth(domain.PermQuestionDownload, s.handleDownloadExport))

	// === 批量推理（DashScope 批量 API）===
	mux.HandleFunc("POST /api/batch/submit", s.requireAuth(domain.PermBatchRun, s.handleBatchSubmit))
	mux.HandleFunc("GET /api/batch/list", s.requireAuth(domain.PermBatchRun, s.handleBatchList))
	mux.HandleFunc("GET /api/batch/status/{jobId}", s.requireAuth(domain.PermBatchRun, s.handleBatchStatus))
	mux.HandleFunc("POST /api/batch/download/{jobId}", s.requireAuth(domain.PermBatchRun, s.handleBatchDownload))

	// === AI 检查 ===
	mux.HandleFunc("POST /api/ai-check", s.requireAuth(domain.PermAICheck, s.handleAICheck))
	mux.HandleFunc("GET /api/ai-check/result/{questionId}", s.requireAuth(domain.PermAICheck, s.handleAICheckResult))
	mux.HandleFunc("GET /api/ai-check/results", s.requireAuth(domain.PermAICheck, s.handleAICheckResults))

	// === 审核流程 ===
	// 提交审核仅系统管理员（user:manage）可操作；审核人员只负责投票
	mux.HandleFunc("POST /api/review/submit", s.requireAuth(domain.PermUserManage, s.handleSubmitReview))
	mux.HandleFunc("POST /api/review/submit-bank", s.requireAuth(domain.PermUserManage, s.handleSubmitBankReview))
	mux.HandleFunc("POST /api/review/action", s.requireAuth(domain.PermReviewDo, s.handleReviewAction))
	mux.HandleFunc("POST /api/review/finalize", s.requireAuth(domain.PermReviewFinal, s.handleReviewFinalize))
	mux.HandleFunc("GET /api/review/task/{id}", s.requireAuth("", s.handleGetReviewTask))
	mux.HandleFunc("GET /api/review/task-by-question/{questionId}", s.requireAuth("", s.handleGetTaskByQuestion))
	mux.HandleFunc("GET /api/review/records/{taskId}", s.requireAuth("", s.handleReviewRecords))
	mux.HandleFunc("GET /api/review/results", s.requireAuth(domain.PermQuestionView, s.handleReviewResults))
	mux.HandleFunc("GET /api/review/my-tasks", s.requireAuth("", s.handleMyTasks))
	mux.HandleFunc("GET /api/review/my-decisions", s.requireAuth("", s.handleMyDecisions))
	mux.HandleFunc("GET /api/review/reviewers", s.requireAuth("", s.handleListReviewers))
	mux.HandleFunc("GET /api/review/flows", s.requireAuth("", s.handleListFlows))
	mux.HandleFunc("POST /api/review/flows", s.requireAuth(domain.PermFlowManage, s.handleCreateFlow))
	mux.HandleFunc("PUT /api/review/flows/{id}", s.requireAuth(domain.PermFlowManage, s.handleUpdateFlow))
	mux.HandleFunc("DELETE /api/review/flows/{id}", s.requireAuth(domain.PermFlowManage, s.handleDeleteFlow))
	mux.HandleFunc("POST /api/review/flows/{id}/revoke", s.requireAuth(domain.PermFlowManage, s.handleRevokeFlow))

	// 包装中间件：CORS 跨域 + JSON Content-Type
	return withCORS(withJSON(mux))
}

// handleListPermissions 返回全部可分配权限点元数据。
func (s *Server) handleListPermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"permissions": domain.AllPermissions()})
}

// handleStats 返回系统统计信息：题目数、知识点数、各分类分布。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	questionCount, err1 := s.questionStore.Count(ctx)
	kpCount, err2 := s.kpSvc.Count(ctx)
	categories, err3 := s.kpSvc.ListCategories(ctx)

	if err1 != nil || err2 != nil || err3 != nil {
		fmt.Printf("⚠ 统计查询部分失败: q=%v kp=%v cat=%v\n", err1, err2, err3)
	}

	// 题目按状态/难度/题库分布
	statusDist := map[string]int{}
	difficultyDist := map[string]int{}
	bankDist := map[string]int{}
	questions, qErr := s.questionStore.ListQuestions(ctx)
	if qErr == nil {
		for _, q := range questions {
			statusDist[string(q.Status)]++
			difficultyDist[string(q.Difficulty)]++
			if len(q.BankIDs) == 0 {
				bankDist["未分类"]++
			} else {
				for _, b := range q.BankIDs {
					bankDist[b]++
				}
			}
		}
	}

	// 用户数与角色数
	userCount := 0
	if users, err := s.authSvc.ListUsers(); err == nil {
		userCount = len(users)
	}

	writeJSON(w, 200, map[string]any{
		"question_count":          questionCount,
		"knowledge_count":         kpCount,
		"knowledge_categories":    categories,
		"status_distribution":     statusDist,
		"difficulty_distribution": difficultyDist,
		"bank_distribution":       bankDist,
		"user_count":              userCount,
	})
}

// requireAuth 创建带 JWT 认证和权限检查的 handler 包装函数。
// action 为空字符串时只需登录成功即可，不检查具体权限。
// 权限从数据库实时查询（角色模板权限 ∪ 直接分配权限），同时校验账号启用状态。
func (s *Server) requireAuth(action string, handler http.HandlerFunc) http.HandlerFunc {
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
