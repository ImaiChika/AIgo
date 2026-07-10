package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/image"
	"aigo/internal/knowledge"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage"
)

// Server HTTP API 服务。
type Server struct {
	pipe          *pipeline.Pipeline
	kpSvc         *knowledge.Service
	imgSvc        *image.Service
	reviewSvc     *review.Service
	questionStore storage.QuestionStore
	authSvc       *auth.Service
}

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

	// 认证
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/auth/me", s.requireAuth("", s.handleMe))
	mux.HandleFunc("PUT /api/auth/profile", s.requireAuth("", s.handleUpdateProfile))
	mux.HandleFunc("POST /api/auth/change-password", s.requireAuth("", s.handleChangePassword))
	mux.HandleFunc("GET /api/users", s.requireAuth("user:manage", s.handleListUsers))
	mux.HandleFunc("POST /api/users", s.requireAuth("user:manage", s.handleCreateUser))

	// 统计（不需要 token）
	mux.HandleFunc("GET /api/stats", s.handleStats)

	// 题目（需要 token）
	mux.HandleFunc("POST /api/questions/generate", s.requireAuth("question:generate", s.handleGenerate))
	mux.HandleFunc("GET /api/questions", s.requireAuth("question:list", s.handleListQuestions))
	mux.HandleFunc("GET /api/questions/{id}", s.requireAuth("question:view", s.handleGetQuestion))
	mux.HandleFunc("PUT /api/questions/{id}", s.requireAuth("question:generate", s.handleUpdateQuestion))
	mux.HandleFunc("DELETE /api/questions/{id}", s.requireAuth("question:delete", s.handleDeleteQuestion))
	mux.HandleFunc("GET /api/questions/search", s.requireAuth("question:list", s.handleSearchQuestions))

	// 知识点
	mux.HandleFunc("GET /api/knowledge-points", s.requireAuth("knowledge:list", s.handleListKP))
	mux.HandleFunc("GET /api/knowledge-points/search", s.requireAuth("knowledge:search", s.handleSearchKP))
	mux.HandleFunc("POST /api/knowledge-points/import", s.requireAuth("knowledge:import", s.handleImportKP))
	mux.HandleFunc("POST /api/knowledge-points", s.requireAuth("knowledge:import", s.handleCreateKP))
	mux.HandleFunc("DELETE /api/knowledge-points/{id}", s.requireAuth("knowledge:import", s.handleDeleteKP))

	// 图片
	mux.HandleFunc("POST /api/images/prompt", s.requireAuth("image:generate", s.handleImagePrompt))
	mux.HandleFunc("POST /api/images/generate", s.requireAuth("image:generate", s.handleImageGenerate))
	mux.HandleFunc("GET /api/images/{questionId}", s.requireAuth("image:view", s.handleListImages))
	mux.HandleFunc("POST /api/images/review", s.requireAuth("image:review", s.handleImageReview))

	// 专家
	mux.HandleFunc("GET /api/experts", s.requireAuth("expert:list", s.handleListExperts))
	mux.HandleFunc("POST /api/experts", s.requireAuth("expert:create", s.handleCreateExpert))
	mux.HandleFunc("PUT /api/experts/{id}", s.requireAuth("expert:update", s.handleUpdateExpert))
	mux.HandleFunc("DELETE /api/experts/{id}", s.requireAuth("expert:update", s.handleDeleteExpert))

	// 审核
	mux.HandleFunc("POST /api/review/submit", s.requireAuth("review:submit", s.handleSubmitReview))
	mux.HandleFunc("POST /api/review/action", s.requireAuth("review:action", s.handleReviewAction))
	mux.HandleFunc("GET /api/review/task/{id}", s.requireAuth("review:view", s.handleGetReviewTask))
	mux.HandleFunc("GET /api/review/task-by-question/{questionId}", s.requireAuth("review:view", s.handleGetTaskByQuestion))
	mux.HandleFunc("GET /api/review/records/{taskId}", s.requireAuth("review:view", s.handleReviewRecords))
	mux.HandleFunc("GET /api/review/flows", s.requireAuth("review:view", s.handleListFlows))

	return withCORS(withJSON(mux))
}

// ===== 题目 =====

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject    string `json:"subject"`
		Difficulty string `json:"difficulty"`
		Topic      string `json:"topic"`
		Count      int    `json:"count"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Subject == "" {
		req.Subject = "临床医学"
	}
	if req.Difficulty == "" {
		req.Difficulty = "medium"
	}
	if req.Topic == "" {
		req.Topic = "常见症状鉴别诊断"
	}

	kp := domain.KnowledgePoint{
		ID:      "api-kp",
		Subject: req.Subject,
		Topic:   req.Topic,
	}

	genReq := domain.GenerationRequest{
		Subject:         req.Subject,
		Difficulty:      domain.Difficulty(req.Difficulty),
		KnowledgePoints: []domain.KnowledgePoint{kp},
		Count:           req.Count,
	}

	questions, err := s.pipe.Generate(r.Context(), genReq)
	if err != nil {
		writeError(w, 500, "生成失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"questions": questions,
		"count":     len(questions),
	})
}

func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	questions, err := s.questionStore.ListQuestions(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"questions": questions,
		"total":     len(questions),
	})
}

func (s *Server) handleGetQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q, err := s.questionStore.GetQuestion(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if q == nil {
		writeError(w, 404, "题目不存在")
		return
	}
	writeJSON(w, 200, q)
}

func (s *Server) handleUpdateQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.questionStore.GetQuestion(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeError(w, 404, "题目不存在")
		return
	}

	var req struct {
		ClinicalStem string `json:"clinical_stem"`
		Options      []struct {
			Label string `json:"label"`
			Text  string `json:"text"`
		} `json:"options"`
		Answer      string `json:"answer"`
		Explanation string `json:"explanation"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	if req.ClinicalStem != "" {
		existing.ClinicalStem = req.ClinicalStem
	}
	if len(req.Options) > 0 {
		existing.Options = make([]domain.Option, len(req.Options))
		for i, o := range req.Options {
			existing.Options[i] = domain.Option{Label: o.Label, Text: o.Text}
		}
	}
	if req.Answer != "" {
		existing.Answer = req.Answer
	}
	if req.Explanation != "" {
		existing.Explanation = req.Explanation
	}
	existing.Version++
	existing.UpdatedAt = time.Now()

	if err := s.questionStore.SaveQuestion(r.Context(), *existing); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, 200, existing)
}

func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.questionStore.DeleteQuestion(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

func (s *Server) handleSearchQuestions(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	ctx := r.Context()

	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	var filtered []domain.A2Question
	for _, q := range questions {
		if status != "" && string(q.Status) != status {
			continue
		}
		if keyword != "" {
			keywordLower := strings.ToLower(keyword)
			stemMatch := strings.Contains(strings.ToLower(q.ClinicalStem), keywordLower)
			answerMatch := strings.Contains(strings.ToLower(q.Answer), keywordLower)
			if !stemMatch && !answerMatch {
				continue
			}
		}
		filtered = append(filtered, q)
	}

	writeJSON(w, 200, map[string]any{"questions": filtered, "total": len(filtered)})
}

// ===== 知识点 =====

func (s *Server) handleListKP(w http.ResponseWriter, r *http.Request) {
	system := r.URL.Query().Get("system")
	ctx := r.Context()

	var points []domain.KnowledgePoint
	var err error
	if system != "" {
		points, err = s.kpSvc.ListBySystem(ctx, system)
	} else {
		points, err = s.kpSvc.ListAll(ctx)
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// 分页
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(points) {
		start = len(points)
	}
	if end > len(points) {
		end = len(points)
	}

	writeJSON(w, 200, map[string]any{
		"points":    points[start:end],
		"total":     len(points),
		"page":      page,
		"page_size": pageSize,
	})
}

func (s *Server) handleSearchKP(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("q")
	if keyword == "" {
		writeError(w, 400, "缺少搜索关键词 q")
		return
	}
	points, err := s.kpSvc.Search(r.Context(), keyword)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"points": points,
		"total":  len(points),
	})
}

func (s *Server) handleCreateKP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string   `json:"id"`
		Subject  string   `json:"subject"`
		System   string   `json:"system"`
		Topic    string   `json:"topic"`
		Keywords []string `json:"keywords"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.Topic == "" {
		writeError(w, 400, "知识点名称不能为空")
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("kp-custom-%d", time.Now().UnixNano())
	}
	if req.Subject == "" {
		req.Subject = "临床医学"
	}

	kp := domain.KnowledgePoint{
		ID:       req.ID,
		Subject:  req.Subject,
		System:   req.System,
		Topic:    req.Topic,
		Keywords: req.Keywords,
	}
	if _, err := s.kpSvc.SavePoints(r.Context(), []domain.KnowledgePoint{kp}); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, 201, kp)
}

func (s *Server) handleDeleteKP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.kpSvc.DeletePoint(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

func (s *Server) handleImportKP(w http.ResponseWriter, r *http.Request) {
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, 400, "缺少文件字段 file: "+err.Error())
		return
	}
	defer file.Close()

	tmpPath := "/tmp/aigo_kp_import_" + header.Filename
	if err := saveUploadedFile(tmpPath, file); err != nil {
		writeError(w, 500, "保存临时文件失败: "+err.Error())
		return
	}

	count, err := s.kpSvc.ImportFromXlsx(r.Context(), tmpPath)
	if err != nil {
		writeError(w, 500, "导入失败: "+err.Error())
		return
	}

	total, _ := s.kpSvc.Count(r.Context())
	writeJSON(w, 200, map[string]any{
		"imported": count,
		"total":    total,
	})
}

// ===== 图片 =====

func (s *Server) handleImagePrompt(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	prompt, err := s.imgSvc.GeneratePrompt(r.Context(), req.QuestionID)
	if err != nil {
		writeError(w, 500, "生成提示词失败: "+err.Error())
		return
	}
	writeJSON(w, 200, prompt)
}

func (s *Server) handleImageGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		Count      int    `json:"count"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.Count < 3 {
		req.Count = 3
	}

	images, err := s.imgSvc.GenerateImages(r.Context(), req.QuestionID, req.Count)
	if err != nil {
		writeError(w, 500, "生成图片失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"images": images,
		"count":  len(images),
	})
}

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	images, err := s.imgSvc.ListImages(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"images": images,
		"count":  len(images),
	})
}

func (s *Server) handleImageReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImageID  string `json:"image_id"`
		ExpertID string `json:"expert_id"`
		Action   string `json:"action"` // approved / rejected
		Opinion  string `json:"opinion"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	err := s.imgSvc.ReviewImage(r.Context(), req.ImageID, req.ExpertID, domain.ImageStatus(req.Action), req.Opinion)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ===== 审核 =====

func (s *Server) handleSubmitReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		FlowID     string `json:"flow_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	task, err := s.reviewSvc.SubmitQuestion(r.Context(), req.QuestionID, req.FlowID)
	if err != nil {
		writeError(w, 500, "提交审核失败: "+err.Error())
		return
	}
	writeJSON(w, 200, task)
}

func (s *Server) handleReviewAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID   string `json:"task_id"`
		ExpertID string `json:"expert_id"`
		Action   string `json:"action"`
		Opinion  string `json:"opinion"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	role := auth.GetRole(r.Context())

	err := s.reviewSvc.Review(r.Context(), review.ReviewRequest{
		TaskID:   req.TaskID,
		ExpertID: req.ExpertID,
		Action:   domain.QuestionStatus(req.Action),
		Opinion:  req.Opinion,
		Role:     role,
	})
	if err != nil {
		writeError(w, 500, "审核失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleGetReviewTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.reviewSvc.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeError(w, 404, "审核任务不存在")
		return
	}
	writeJSON(w, 200, task)
}

func (s *Server) handleGetTaskByQuestion(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	task, err := s.reviewSvc.GetTaskByQuestionID(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeJSON(w, 200, nil)
		return
	}
	writeJSON(w, 200, task)
}

func (s *Server) handleListFlows(w http.ResponseWriter, r *http.Request) {
	flows, err := s.reviewSvc.ListFlows(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"flows": flows, "total": len(flows)})
}

func (s *Server) handleReviewRecords(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	records, err := s.reviewSvc.ListRecords(r.Context(), taskId)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"records": records,
		"total":   len(records),
	})
}

// ===== 统计 =====

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

// ===== 专家 =====

func (s *Server) handleListExperts(w http.ResponseWriter, r *http.Request) {
	experts, err := s.reviewSvc.ListExperts(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"experts": experts, "total": len(experts)})
}

func (s *Server) handleCreateExpert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Department string   `json:"department"`
		Title      string   `json:"title"`
		Specialties []string `json:"specialties"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.Name == "" {
		writeError(w, 400, "专家姓名不能为空")
		return
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("E%03d", time.Now().UnixNano()%10000)
	}
	expert := domain.Expert{
		ID:          req.ID,
		Name:        req.Name,
		Department:  req.Department,
		Title:       req.Title,
		Specialties: req.Specialties,
		Enabled:     true,
	}
	if err := s.reviewSvc.CreateExpert(r.Context(), expert); err != nil {
		writeError(w, 500, "创建失败: "+err.Error())
		return
	}
	writeJSON(w, 201, expert)
}

func (s *Server) handleUpdateExpert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := s.reviewSvc.GetExpert(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if existing == nil {
		writeError(w, 404, "专家不存在")
		return
	}
	var req struct {
		Name       string   `json:"name"`
		Department string   `json:"department"`
		Title      string   `json:"title"`
		Specialties []string `json:"specialties"`
		Enabled    *bool    `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.Department != "" {
		existing.Department = req.Department
	}
	if req.Title != "" {
		existing.Title = req.Title
	}
	if req.Specialties != nil {
		existing.Specialties = req.Specialties
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if err := s.reviewSvc.UpdateExpert(r.Context(), *existing); err != nil {
		writeError(w, 500, "更新失败: "+err.Error())
		return
	}
	writeJSON(w, 200, existing)
}

func (s *Server) handleDeleteExpert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reviewSvc.DeleteExpert(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// ===== 认证 =====

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}

	token, user, err := s.authSvc.Login(req.Username, req.Password)
	if err != nil {
		writeError(w, 401, err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"token": token,
		"user":  user,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	user, err := s.authSvc.GetUserByID(userID)
	if err != nil || user == nil {
		writeError(w, 401, "用户不存在")
		return
	}
	writeJSON(w, 200, user)
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.DisplayName == "" {
		writeError(w, 400, "昵称不能为空")
		return
	}
	if err := s.authSvc.UpdateDisplayName(userID, req.DisplayName); err != nil {
		writeError(w, 500, "更新失败")
		return
	}
	user, _ := s.authSvc.GetUserByID(userID)
	writeJSON(w, 200, user)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, 400, "新密码至少8位")
		return
	}
	if err := s.authSvc.ChangePassword(userID, req.OldPassword, req.NewPassword); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.authSvc.ListUsers()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"users": users, "total": len(users)})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	user, err := s.authSvc.CreateUser(req.Username, req.Password, req.DisplayName, req.Role)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, user)
}

// requireAuth 创建带认证和权限检查的 handler。
func (s *Server) requireAuth(action string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 解析 token
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, 401, "缺少登录凭证")
			return
		}
		token := authHeader
		if len(authHeader) > 7 && authHeader[:7] == "Bearer " {
			token = authHeader[7:]
		}

		claims, err := s.authSvc.ValidateToken(token)
		if err != nil {
			writeError(w, 401, err.Error())
			return
		}

		// 注入用户信息
		ctx := r.Context()
		ctx = context.WithValue(ctx, auth.UserIDKey, claims.UserID)
		ctx = context.WithValue(ctx, auth.UsernameKey, claims.Username)
		ctx = context.WithValue(ctx, auth.DisplayNameKey, claims.DisplayName)
		ctx = context.WithValue(ctx, auth.RoleKey, claims.Role)

		// 权限检查（action 为空表示只要登录即可）
		if action != "" && !auth.CheckPermission(claims.Role, action) {
			writeError(w, 403, "权限不足")
			return
		}

		handler(w, r.WithContext(ctx))
	}
}

// ===== 工具函数 =====

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// withCORS 添加 CORS 头。
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withJSON 设置默认 Content-Type。
func withJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
