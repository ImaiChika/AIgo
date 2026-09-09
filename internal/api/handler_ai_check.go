package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
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

// handleAICheckAsync 将题目加入后台 AI 检查队列，立即返回不等待检查完成。
// 用于题库页批量补查存量草稿：检查在后台 worker 中执行，结果可通过结果接口查询。
// 请求：{"question_ids": ["id1", ...]}；越权题目直接剔除。
func (s *Server) handleAICheckAsync(w http.ResponseWriter, r *http.Request) {
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
	if s.aiCheckSvc == nil {
		writeError(w, 503, "AI 检查服务不可用")
		return
	}

	s.aiCheckSvc.CheckAsync(allowed...)
	writeJSON(w, 200, map[string]any{"queued": len(allowed)})
}

// handleAICheckProgress 查询题目粒度的检查进度（排队/检查中/结论/异常）。
// 登录即可调用，逐题校验 question:view 题库范围，越权题目剔除。
// 请求：{"question_ids": ["id1", ...]}（上限 500）；响应含逐题状态与计数统计，
// 供生成页/批量页的"检查中 x/N"分段进度轮询使用。
func (s *Server) handleAICheckProgress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionIDs []string `json:"question_ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if len(req.QuestionIDs) == 0 {
		writeError(w, 400, "请提供至少一道题目 ID")
		return
	}
	if len(req.QuestionIDs) > 500 {
		writeError(w, 400, "单次最多查询 500 道题目的进度")
		return
	}
	if s.aiCheckSvc == nil {
		writeError(w, 503, "AI 检查服务不可用")
		return
	}

	items, counts, err := s.aiCheckSvc.ProgressByQuestionIDs(r.Context(), req.QuestionIDs)
	if err != nil {
		writeError(w, 500, "查询进度失败: "+err.Error())
		return
	}
	// 范围过滤：存活的题目逐题校验题库范围；
	// 已淘汰的题目（检查不通过自动删除）不在任何题库中，直接放行其淘汰原因。
	filtered := make([]aicheck.CheckProgress, 0, len(items))
	for _, it := range items {
		if it.Discarded {
			filtered = append(filtered, it)
			continue
		}
		if q, status, err := s.loadScopedQuestion(r, it.QuestionID, domain.PermQuestionView); err == nil && q != nil && status == 0 {
			filtered = append(filtered, it)
		}
	}
	// 重算存活题计数（淘汰计数已由服务端给出）
	recount := map[string]int{"discarded": counts["discarded"]}
	for _, it := range filtered {
		if it.Discarded {
			continue
		}
		switch it.TaskStatus {
		case "pending":
			recount["pending"]++
		case "running":
			recount["running"]++
		case "exhausted":
			recount["exhausted"]++
		}
		switch it.Verdict {
		case "pass":
			recount["passed"]++
		case "issues_found", "reject":
			recount["issues"]++
		}
	}
	writeJSON(w, 200, map[string]any{
		"items":  filtered,
		"counts": recount,
		"total":  len(filtered),
	})
}

// handleAICheckSummary AI 检查概况统计（质量检查权限）：
// 任务维度的排队/执行中/耗専数 + 题目维度的草稿/已初评/已检查数（按查询者题库范围）。
func (s *Server) handleAICheckSummary(w http.ResponseWriter, r *http.Request) {
	if s.aiCheckSvc == nil {
		writeError(w, 503, "AI 检查服务不可用")
		return
	}
	tasks, err := s.aiCheckSvc.TaskSummary(r.Context())
	if err != nil {
		writeError(w, 500, "统计任务失败: "+err.Error())
		return
	}

	scope, scopeRestricted := s.questionBankScope(r, domain.PermQuestionView)
	questions := map[string]int{}
	for _, status := range []domain.QuestionStatus{
		domain.StatusAIDraft, domain.StatusAutoChecked, domain.StatusAIReviewed,
	} {
		_, total, err := s.questionStore.SearchQuestions(r.Context(), storage.QuestionFilter{
			Status:          string(status),
			BankScope:       scope,
			ScopeRestricted: scopeRestricted,
		}, 1, 1)
		if err != nil {
			writeError(w, 500, "统计题目失败: "+err.Error())
			return
		}
		questions[string(status)] = total
	}

	writeJSON(w, 200, map[string]any{
		"tasks":     tasks,
		"questions": questions,
	})
}

// handleAICheckOverride 强制通过 AI 检查：把未通过/未检查的草稿直接置为已检查（ai_reviewed）。
// 仅用户管理权限（user:manage）可用，与送审权限一致；仅草稿态可操作；写题目版本记录与审计日志留痕到人。
func (s *Server) handleAICheckOverride(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"`
		Reason     string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if req.QuestionID == "" {
		writeError(w, 400, "缺少 question_id")
		return
	}

	q, status, err := s.loadScopedQuestion(r, req.QuestionID, domain.PermQuestionEdit)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if q.Status != domain.StatusAIDraft && q.Status != domain.StatusAutoChecked {
		writeError(w, http.StatusConflict, "仅草稿状态的题目可以强制通过；审核中的题目请走审核流程")
		return
	}

	q.Status = domain.StatusAIReviewed
	q.UpdatedAt = time.Now()
	note := "强制通过 AI 质量检查"
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		note += "：" + reason
	}
	overrideCtx := storage.WithQuestionChange(r.Context(), storage.QuestionChange{
		Actor:      auth.GetUsername(r.Context()),
		ChangeType: "ai_check_override",
		ChangeNote: note,
	})
	if err := s.questionStore.SaveQuestion(overrideCtx, *q); err != nil {
		writeError(w, 500, "保存失败: "+err.Error())
		return
	}
	s.auditSvc.Log(r.Context(), q.ID, "ai_check_override", auth.GetUsername(r.Context()), note)
	writeJSON(w, 200, q)
}

// handleAICheckResult 获取某题最新的 AI 检查结果，按题目实际生命周期分层校验查看权限。
func (s *Server) handleAICheckResult(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	if questionID == "" {
		writeError(w, 400, "缺少 questionId")
		return
	}

	if _, status, err := s.loadViewableQuestion(r, questionID); err != nil {
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
