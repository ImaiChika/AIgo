// Package api 提供 HTTP REST API 服务。
// 将后端业务逻辑通过 HTTP 接口暴露给前端调用。
// 所有接口统一返回 JSON 格式，需要认证的接口通过 Bearer token 验证。
package api

import (
	"context"
	"net/http"

	"aigo/internal/audit"
	"aigo/internal/auth"
	"aigo/internal/batch"
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
	questionStore storage.QuestionStore  // 题目存储
	authSvc       *auth.Service         // 认证服务
	batchSvc      *batch.Service        // 批量推理服务
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
	}
}

// Handler 返回配置好所有路由的 http.Handler。
// 路由分组：认证（公开）、用户管理（管理员）、题目、知识点、专家、图片、审核。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// === 认证（公开接口，不需要 token） ===
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// === 认证（需登录） ===
	mux.HandleFunc("GET /api/auth/me", s.requireAuth("", s.handleMe))
	mux.HandleFunc("PUT /api/auth/profile", s.requireAuth("", s.handleUpdateProfile))
	mux.HandleFunc("POST /api/auth/change-password", s.requireAuth("", s.handleChangePassword))

	// === 用户管理（仅管理员） ===
	mux.HandleFunc("GET /api/users", s.requireAuth("user:manage", s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.requireAuth("user:manage", s.handleCreateUser))

	// === 统计（公开） ===
	mux.HandleFunc("GET /api/stats", s.handleStats)

	// === 静态文件（图片） ===
	mux.Handle("GET /images/", http.StripPrefix("/images/", http.FileServer(http.Dir("output/images"))))

	// === 操作日志（仅管理员） ===
	mux.HandleFunc("GET /api/audit-logs", s.requireAuth("user:manage", s.handleListAuditLogs))
	mux.HandleFunc("GET /api/audit-logs/question/{id}", s.requireAuth("user:manage", s.handleAuditLogsByQuestion))
	mux.HandleFunc("GET /api/audit-logs/actor/{actor}", s.requireAuth("user:manage", s.handleAuditLogsByActor))

	// === 题目 CRUD ===
	mux.HandleFunc("POST /api/questions/generate", s.requireAuth("question:generate", s.handleGenerate))
	mux.HandleFunc("GET /api/questions", s.requireAuth("question:list", s.handleListQuestions))
	mux.HandleFunc("GET /api/questions/search", s.requireAuth("question:list", s.handleSearchQuestions))
	mux.HandleFunc("GET /api/questions/{id}", s.requireAuth("question:view", s.handleGetQuestion))
	mux.HandleFunc("PUT /api/questions/{id}", s.requireAuth("question:generate", s.handleUpdateQuestion))
	mux.HandleFunc("DELETE /api/questions/{id}", s.requireAuth("question:delete", s.handleDeleteQuestion))
	mux.HandleFunc("POST /api/questions/{id}/publish", s.requireAuth("expert:create", s.handlePublishQuestion))

	// === 知识点 CRUD ===
	mux.HandleFunc("GET /api/knowledge-points", s.requireAuth("knowledge:list", s.handleListKP))
	mux.HandleFunc("GET /api/knowledge-points/search", s.requireAuth("knowledge:search", s.handleSearchKP))
	mux.HandleFunc("POST /api/knowledge-points/import", s.requireAuth("knowledge:import", s.handleImportKP))
	mux.HandleFunc("POST /api/knowledge-points", s.requireAuth("knowledge:import", s.handleCreateKP))
	mux.HandleFunc("DELETE /api/knowledge-points/{id}", s.requireAuth("knowledge:import", s.handleDeleteKP))

	// === 专家 CRUD ===
	mux.HandleFunc("GET /api/experts", s.requireAuth("expert:list", s.handleListExperts))
	mux.HandleFunc("POST /api/experts", s.requireAuth("expert:create", s.handleCreateExpert))
	mux.HandleFunc("PUT /api/experts/{id}", s.requireAuth("expert:update", s.handleUpdateExpert))
	mux.HandleFunc("DELETE /api/experts/{id}", s.requireAuth("expert:update", s.handleDeleteExpert))

	// === 图片（提示词、候选图、审核） ===
	mux.HandleFunc("POST /api/images/prompt", s.requireAuth("image:generate", s.handleImagePrompt))
	mux.HandleFunc("POST /api/images/generate", s.requireAuth("image:generate", s.handleImageGenerate))
	mux.HandleFunc("GET /api/images/{questionId}", s.requireAuth("image:view", s.handleListImages))
	mux.HandleFunc("POST /api/images/review", s.requireAuth("image:review", s.handleImageReview))

	// === 导出 ===
	mux.HandleFunc("POST /api/export/xlsx", s.requireAuth("question:list", s.handleExportXlsx))
	mux.HandleFunc("POST /api/export/docx", s.requireAuth("question:list", s.handleExportDocx))
	mux.HandleFunc("GET /api/export/download/{filename}", s.handleDownloadExport) // 下载不需要认证

	// === 批量推理（DashScope 批量 API）===
	mux.HandleFunc("POST /api/batch/submit", s.requireAuth("question:generate", s.handleBatchSubmit))
	mux.HandleFunc("GET /api/batch/list", s.requireAuth("question:generate", s.handleBatchList))
	mux.HandleFunc("GET /api/batch/status/{jobId}", s.requireAuth("question:generate", s.handleBatchStatus))
	mux.HandleFunc("POST /api/batch/download/{jobId}", s.requireAuth("question:generate", s.handleBatchDownload))

	// === 审核流程 ===
	mux.HandleFunc("POST /api/review/submit", s.requireAuth("review:submit", s.handleSubmitReview))
	mux.HandleFunc("POST /api/review/action", s.requireAuth("review:action", s.handleReviewAction))
	mux.HandleFunc("GET /api/review/task/{id}", s.requireAuth("review:view", s.handleGetReviewTask))
	mux.HandleFunc("GET /api/review/task-by-question/{questionId}", s.requireAuth("review:view", s.handleGetTaskByQuestion))
	mux.HandleFunc("GET /api/review/records/{taskId}", s.requireAuth("review:view", s.handleReviewRecords))
	mux.HandleFunc("GET /api/review/flows", s.requireAuth("review:view", s.handleListFlows))
	mux.HandleFunc("POST /api/review/flows", s.requireAuth("expert:create", s.handleCreateFlow))
	mux.HandleFunc("DELETE /api/review/flows/{id}", s.requireAuth("expert:create", s.handleDeleteFlow))

	// 包装中间件：CORS 跨域 + JSON Content-Type
	return withCORS(withJSON(mux))
}

// handleStats 返回系统统计信息：题目数、知识点数、各分类分布。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	questionCount, _ := s.questionStore.Count(ctx)
	kpCount, _ := s.kpSvc.Count(ctx)
	categories, _ := s.kpSvc.ListCategories(ctx)

	writeJSON(w, 200, map[string]any{
		"question_count":      questionCount,
		"knowledge_count":     kpCount,
		"knowledge_categories": categories,
	})
}

// requireAuth 创建带 JWT 认证和 RBAC 权限检查的 handler 包装函数。
// action 为空字符串时只需登录成功即可，不检查具体权限。
// 流程：解析 Bearer token → 验证 JWT → 注入用户信息到 context → 检查权限 → 调用原始 handler。
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

		// 将用户信息注入 context，后续 handler 可通过 auth.GetUserID(ctx) 等方法获取
		ctx := r.Context()
		ctx = context.WithValue(ctx, auth.UserIDKey, claims.UserID)
		ctx = context.WithValue(ctx, auth.UsernameKey, claims.Username)
		ctx = context.WithValue(ctx, auth.DisplayNameKey, claims.DisplayName)
		ctx = context.WithValue(ctx, auth.RoleKey, claims.Role)

		// RBAC 权限检查（action 为空表示只需登录）
		if action != "" && !auth.CheckPermission(claims.Role, action) {
			writeError(w, 403, "权限不足")
			return
		}

		handler(w, r.WithContext(ctx))
	}
}
