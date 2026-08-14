package api

import (
	"net/http"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// handleGenerate 调用千问大模型生成 A2 型试题。
// 请求：{"subject": "消化", "difficulty": "0.65", "topic": "消化性溃疡", "outline_code": "110.4.3.1.1", "count": 1}
// 返回：{"questions": [...], "count": N}
// 生成的题目自动保存到数据库，状态为 auto_checked（直接入库）。
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject     string `json:"subject"`      // 专业科目
		Category    string `json:"category"`     // 分类：基础医学/临床综合
		Difficulty  string `json:"difficulty"`    // 难度系数：0.55/0.65/0.75/0.85
		Topic       string `json:"topic"`         // 知识点主题
		OutlineCode string `json:"outline_code"`  // 大纲代码
		Count       int    `json:"count"`         // 生成数量
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	// 设置默认值
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Subject == "" {
		req.Subject = "临床医学"
	}
	if req.Difficulty == "" {
		req.Difficulty = "0.65"
	}
	if req.Topic == "" {
		req.Topic = "常见症状鉴别诊断"
	}

	// 从数据库获取完整知识点信息（包含 Category、Unit、SubItem 等）
	kp := domain.KnowledgePoint{
		ID:          req.OutlineCode,
		Category:    req.Category,
		Subject:     req.Subject,
		Topic:       req.Topic,
		OutlineCode: req.OutlineCode,
	}
	if req.OutlineCode != "" {
		dbKP, err := s.kpSvc.GetByID(r.Context(), req.OutlineCode)
		if err == nil && dbKP != nil {
			kp = *dbKP // 使用数据库中的完整知识点
		}
	}
	genReq := domain.GenerationRequest{
		Subject:         req.Subject,
		Difficulty:      domain.Difficulty(req.Difficulty),
		KnowledgePoints: []domain.KnowledgePoint{kp},
		Count:           req.Count,
	}

	// 调用 pipeline 生成题目（内部会调千问API + 解析JSON + 存入数据库）
	questions, err := s.pipe.Generate(r.Context(), genReq)
	if err != nil {
		writeError(w, 500, "生成失败: "+err.Error())
		return
	}

	// 将生成的题目状态改为 auto_checked（直接入库，无需再次确认）
	for i := range questions {
		questions[i].Status = domain.StatusAutoChecked
		questions[i].UpdatedAt = time.Now()
		if err := s.questionStore.SaveQuestion(r.Context(), questions[i]); err != nil {
			writeError(w, 500, "保存题目失败: "+err.Error())
			return
		}
	}

	// 记录日志
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = "ai"
	}
	for _, q := range questions {
		s.auditSvc.LogCreate(r.Context(), q.ID, actor)
	}

	writeJSON(w, 200, map[string]any{
		"questions": questions,
		"count":     len(questions),
	})
}

// handleListQuestions 列出所有题目。
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

// handleGetQuestion 根据 ID 获取单道题目详情。
func (s *Server) handleGetQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id") // 从 URL 路径提取题目 ID
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

// handleUpdateQuestion 修改题目内容（题干、选项、答案、解析）。
// 只更新请求中提供的字段，版本号自动递增。
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
		ClinicalStem string `json:"clinical_stem"` // 题干
		Options      []struct {
			Label string `json:"label"` // 选项标签 A-E
			Text  string `json:"text"`  // 选项内容
		} `json:"options"`
		Answer      string `json:"answer"`      // 正确答案
		Explanation string `json:"explanation"` // 解析
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	// 按字段更新（空值表示不修改）
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
	existing.Version++              // 版本号递增
	existing.UpdatedAt = time.Now() // 更新时间

	if err := s.questionStore.SaveQuestion(r.Context(), *existing); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}

	// 记录日志
	actor := auth.GetUsername(r.Context())
	s.auditSvc.LogUpdate(r.Context(), existing.ID, actor, "修改题目内容")

	writeJSON(w, 200, existing)
}

// handleDeleteQuestion 删除题目。
func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// 记录日志（删除前记录，因为删除后就查不到了）
	actor := auth.GetUsername(r.Context())
	s.auditSvc.LogDelete(r.Context(), id, actor)

	if err := s.questionStore.DeleteQuestion(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handlePublishQuestion 将审核通过的题目发布到正式题库。
func (s *Server) handlePublishQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actor := auth.GetUsername(r.Context())
	if err := s.reviewSvc.PublishQuestion(r.Context(), id); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	// 记录发布日志
	s.auditSvc.LogPublish(r.Context(), id, actor)

	q, _ := s.questionStore.GetQuestion(r.Context(), id)
	writeJSON(w, 200, q)
}

// handleSearchQuestions 搜索题目。
// 查询参数：q=关键词（匹配题干和答案），status=状态筛选。
func (s *Server) handleSearchQuestions(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	ctx := r.Context()

	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	// 逐条筛选
	var filtered []domain.A2Question
	for _, q := range questions {
		// 按状态筛选
		if status != "" && string(q.Status) != status {
			continue
		}
		// 按关键词筛选（ID、题干或答案包含关键词）
		if keyword != "" {
			keywordLower := strings.ToLower(keyword)
			idMatch := strings.Contains(strings.ToLower(q.ID), keywordLower)
			stemMatch := strings.Contains(strings.ToLower(q.ClinicalStem), keywordLower)
			answerMatch := strings.Contains(strings.ToLower(q.Answer), keywordLower)
			if !idMatch && !stemMatch && !answerMatch {
				continue
			}
		}
		filtered = append(filtered, q)
	}

	writeJSON(w, 200, map[string]any{"questions": filtered, "total": len(filtered)})
}
