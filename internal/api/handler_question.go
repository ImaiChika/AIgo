package api

import (
	"net/http"
	"strings"
	"time"

	"aigo/internal/domain"
)

// handleGenerate 调用千问大模型生成 A2 型试题。
// 请求：{"subject": "临床医学", "difficulty": "medium", "topic": "肺炎", "count": 1}
// 返回：{"questions": [...], "count": N}
// 生成的题目自动保存到数据库，状态为 ai_draft。
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subject    string `json:"subject"`    // 专业科目
		Difficulty string `json:"difficulty"` // 难度：easy/medium/hard
		Topic      string `json:"topic"`      // 知识点主题
		Count      int    `json:"count"`      // 生成数量
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
		req.Difficulty = "medium"
	}
	if req.Topic == "" {
		req.Topic = "常见症状鉴别诊断"
	}

	// 构建生成请求，知识点作为生成上下文
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

	// 调用 pipeline 生成题目（内部会调千问API + 解析JSON + 存入数据库）
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
	writeJSON(w, 200, existing)
}

// handleDeleteQuestion 删除题目。
func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.questionStore.DeleteQuestion(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
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
		// 按关键词筛选（题干或答案包含关键词）
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
