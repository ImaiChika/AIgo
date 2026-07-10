package api

import (
	"net/http"
	"strings"
	"time"

	"aigo/internal/domain"
)

// handleGenerate 调用千问生成试题。
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

// handleGetQuestion 获取单个题目详情。
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

// handleUpdateQuestion 修改题目内容。
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

// handleDeleteQuestion 删除题目。
func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.questionStore.DeleteQuestion(r.Context(), id); err != nil {
		writeError(w, 500, "删除失败: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handleSearchQuestions 搜索题目（按题干或答案关键词）。
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
		// 按状态筛选
		if status != "" && string(q.Status) != status {
			continue
		}
		// 按关键词筛选
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
