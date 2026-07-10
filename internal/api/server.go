package api

import (
	"context"
	"net/http"

	"aigo/internal/auth"
	"aigo/internal/image"
	"aigo/internal/knowledge"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage"
)

// Server HTTP API 服务，负责路由注册和依赖管理。
type Server struct {
	pipe          *pipeline.Pipeline
	kpSvc         *knowledge.Service
	imgSvc        *image.Service
	reviewSvc     *review.Service
	questionStore storage.QuestionStore
	authSvc       *auth.Service
}

// NewServer 创建 API 服务实例。
func NewServer(
	pipe *pipeline.Pipeline,
	kpSvc *knowledge.Service,
	imgSvc *image.Service,
	reviewSvc *review.Service,
	questionStore storage.QuestionStore,
	authSvc *auth.Service,
) *Server {
	return &Server{
		pipe:          pipe,
		kpSvc:         kpSvc,
		imgSvc:        imgSvc,
		reviewSvc:     reviewSvc,
		questionStore: questionStore,
		authSvc:       authSvc,
	}
}

// Handler 返回配置好路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// === 认证（公开接口） ===
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

	// === 题目 ===
	mux.HandleFunc("POST /api/questions/generate", s.requireAuth("question:generate", s.handleGenerate))
	mux.HandleFunc("GET /api/questions", s.requireAuth("question:list", s.handleListQuestions))
	mux.HandleFunc("GET /api/questions/search", s.requireAuth("question:list", s.handleSearchQuestions))
	mux.HandleFunc("GET /api/questions/{id}", s.requireAuth("question:view", s.handleGetQuestion))
	mux.HandleFunc("PUT /api/questions/{id}", s.requireAuth("question:generate", s.handleUpdateQuestion))
	mux.HandleFunc("DELETE /api/questions/{id}", s.requireAuth("question:delete", s.handleDeleteQuestion))

	// === 知识点 ===
	mux.HandleFunc("GET /api/knowledge-points", s.requireAuth("knowledge:list", s.handleListKP))
	mux.HandleFunc("GET /api/knowledge-points/search", s.requireAuth("knowledge:search", s.handleSearchKP))
	mux.HandleFunc("POST /api/knowledge-points/import", s.requireAuth("knowledge:import", s.handleImportKP))
	mux.HandleFunc("POST /api/knowledge-points", s.requireAuth("knowledge:import", s.handleCreateKP))
	mux.HandleFunc("DELETE /api/knowledge-points/{id}", s.requireAuth("knowledge:import", s.handleDeleteKP))

	// === 专家 ===
	mux.HandleFunc("GET /api/experts", s.requireAuth("expert:list", s.handleListExperts))
	mux.HandleFunc("POST /api/experts", s.requireAuth("expert:create", s.handleCreateExpert))
	mux.HandleFunc("PUT /api/experts/{id}", s.requireAuth("expert:update", s.handleUpdateExpert))
	mux.HandleFunc("DELETE /api/experts/{id}", s.requireAuth("expert:update", s.handleDeleteExpert))

	// === 图片 ===
	mux.HandleFunc("POST /api/images/prompt", s.requireAuth("image:generate", s.handleImagePrompt))
	mux.HandleFunc("POST /api/images/generate", s.requireAuth("image:generate", s.handleImageGenerate))
	mux.HandleFunc("GET /api/images/{questionId}", s.requireAuth("image:view", s.handleListImages))
	mux.HandleFunc("POST /api/images/review", s.requireAuth("image:review", s.handleImageReview))

	// === 审核 ===
	mux.HandleFunc("POST /api/review/submit", s.requireAuth("review:submit", s.handleSubmitReview))
	mux.HandleFunc("POST /api/review/action", s.requireAuth("review:action", s.handleReviewAction))
	mux.HandleFunc("GET /api/review/task/{id}", s.requireAuth("review:view", s.handleGetReviewTask))
	mux.HandleFunc("GET /api/review/task-by-question/{questionId}", s.requireAuth("review:view", s.handleGetTaskByQuestion))
	mux.HandleFunc("GET /api/review/records/{taskId}", s.requireAuth("review:view", s.handleReviewRecords))
	mux.HandleFunc("GET /api/review/flows", s.requireAuth("review:view", s.handleListFlows))

	return withCORS(withJSON(mux))
}

// handleStats 返回系统统计信息（题目数、知识点数、系统分布）。
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	questionCount, _ := s.questionStore.Count(ctx)
	kpCount, _ := s.kpSvc.Count(ctx)
	systems, _ := s.kpSvc.ListSystems(ctx)

	writeJSON(w, 200, map[string]any{
		"question_count":    questionCount,
		"knowledge_count":   kpCount,
		"knowledge_systems": systems,
	})
}

// requireAuth 创建带 JWT 认证和 RBAC 权限检查的 handler。
// action 为空时只需登录，不检查具体权限。
func (s *Server) requireAuth(action string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 从 Header 提取 Bearer token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, 401, "缺少登录凭证")
			return
		}
		token := authHeader
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			token = authHeader[7:]
		}

		// 验证 JWT
		claims, err := s.authSvc.ValidateToken(token)
		if err != nil {
			writeError(w, 401, err.Error())
			return
		}

		// 注入用户信息到 context
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
