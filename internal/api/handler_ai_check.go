package api

import (
	"net/http"
)

// handleAICheck 批量 AI 检查题目。
// 请求：{"question_ids": ["id1", "id2", ...]}
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

	results, err := s.aiCheckSvc.CheckQuestions(r.Context(), req.QuestionIDs)
	if err != nil {
		writeError(w, 500, "AI 检查失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"results": results,
		"total":   len(results),
	})
}

// handleAICheckResult 获取某题最新的 AI 检查结果。
func (s *Server) handleAICheckResult(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	if questionID == "" {
		writeError(w, 400, "缺少 questionId")
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
func (s *Server) handleAICheckResults(w http.ResponseWriter, r *http.Request) {
	results, err := s.aiCheckSvc.ListResults(r.Context(), 50)
	if err != nil {
		writeError(w, 500, "查询失败: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]any{
		"results": results,
		"total":   len(results),
	})
}
