package api

import (
	"net/http"
	"strconv"

	"aigo/internal/domain"
)

// handleAICheck 批量 AI 检查题目。
// 请求：{"question_ids": ["id1", "id2", ...]}
// 只检查当前用户题库范围内的题目，越权题目直接剔除。
func (s *Server) handleAICheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionIDs []string `json:"question_ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if len(req.QuestionIDs) == 0 {
		writeError(w, 400, "请选择至少一道题目")
		return
	}

	// 剔除超出题库范围的题目
	var allowed []string
	for _, id := range req.QuestionIDs {
		if q, status, err := s.loadScopedQuestion(r, id, domain.PermQuestionView); err == nil && q != nil && status == 0 {
			allowed = append(allowed, q.ID)
		}
	}
	if len(allowed) == 0 {
		writeError(w, 403, "无权检查这些题目（超出题库范围）")
		return
	}

	results, err := s.aiCheckSvc.CheckQuestions(r.Context(), allowed)
	if err != nil {
		writeError(w, 500, "AI 检查失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"results": results,
		"total":   len(results),
	})
}

// handleAICheckResult 获取某题最新的 AI 检查结果。校验题库范围（question:view）。
func (s *Server) handleAICheckResult(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	if questionID == "" {
		writeError(w, 400, "缺少 questionId")
		return
	}

	if _, status, err := s.loadScopedQuestion(r, questionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
		return
	}

	result, err := s.aiCheckSvc.GetResult(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, "查询失败: "+err.Error())
		return
	}
	if result == nil {
		writeError(w, 404, "未找到检查结果")
		return
	}

	writeJSON(w, 200, result)
}

// handleAICheckResults 列出所有 AI 检查结果。
// 查询参数：limit=数量上限（默认50，最大1000）。
func (s *Server) handleAICheckResults(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}
	results, err := s.aiCheckSvc.ListResults(r.Context(), limit)
	if err != nil {
		writeError(w, 500, "查询失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"results": results,
		"total":   len(results),
	})
}
