package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"aigo/internal/domain"
	"aigo/internal/image"
	"aigo/internal/knowledge"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage"
)

// Server HTTP API 服务。
type Server struct {
	pipe      *pipeline.Pipeline
	kpSvc     *knowledge.Service
	imgSvc    *image.Service
	reviewSvc *review.Service
	questionStore storage.QuestionStore
}

func NewServer(
	pipe *pipeline.Pipeline,
	kpSvc *knowledge.Service,
	imgSvc *image.Service,
	reviewSvc *review.Service,
	questionStore storage.QuestionStore,
) *Server {
	return &Server{
		pipe:          pipe,
		kpSvc:         kpSvc,
		imgSvc:        imgSvc,
		reviewSvc:     reviewSvc,
		questionStore: questionStore,
	}
}

// Handler 返回配置好路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 题目
	mux.HandleFunc("POST /api/questions/generate", s.handleGenerate)
	mux.HandleFunc("GET /api/questions", s.handleListQuestions)
	mux.HandleFunc("GET /api/questions/{id}", s.handleGetQuestion)

	// 知识点
	mux.HandleFunc("GET /api/knowledge-points", s.handleListKP)
	mux.HandleFunc("GET /api/knowledge-points/search", s.handleSearchKP)
	mux.HandleFunc("POST /api/knowledge-points/import", s.handleImportKP)

	// 图片
	mux.HandleFunc("POST /api/images/prompt", s.handleImagePrompt)
	mux.HandleFunc("POST /api/images/generate", s.handleImageGenerate)
	mux.HandleFunc("GET /api/images/{questionId}", s.handleListImages)
	mux.HandleFunc("POST /api/images/review", s.handleImageReview)

	// 审核
	mux.HandleFunc("POST /api/review/submit", s.handleSubmitReview)
	mux.HandleFunc("POST /api/review/action", s.handleReviewAction)
	mux.HandleFunc("GET /api/review/task/{id}", s.handleGetReviewTask)
	mux.HandleFunc("GET /api/review/records/{taskId}", s.handleReviewRecords)

	// 统计
	mux.HandleFunc("GET /api/stats", s.handleStats)

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
		Action   string `json:"action"` // approved / rejected / revision_required
		Opinion  string `json:"opinion"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	err := s.reviewSvc.Review(r.Context(), review.ReviewRequest{
		TaskID:   req.TaskID,
		ExpertID: req.ExpertID,
		Action:   domain.QuestionStatus(req.Action),
		Opinion:  req.Opinion,
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
