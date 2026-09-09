package api

import (
	"net/http"
	"strconv"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// handleListAuditLogs 列出最近的操作日志。
// 查询参数：limit=返回数量（默认100）
func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	var logs []domain.AuditLog
	var err error
	if s.hasPermission(r, domain.PermQuestionViewGlobal) {
		logs, err = s.auditSvc.ListLogs(r.Context(), limit)
	} else {
		// 审计权限可以下放给客户，但只能追溯自己的操作，不能借日志旁路查看
		// 其他老师的题目或账号信息。
		logs, err = s.auditSvc.ListByActor(r.Context(), auth.GetUsername(r.Context()))
		if len(logs) > limit {
			logs = logs[:limit]
		}
	}
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"logs": logs, "total": len(logs)})
}

// handleAuditLogsByQuestion 列出某题目的操作日志。
func (s *Server) handleAuditLogsByQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		q, err := s.questionStore.GetQuestion(r.Context(), id)
		if err != nil || q == nil || q.OwnerID != auth.GetUserID(r.Context()) {
			writeError(w, http.StatusForbidden, "无权查看该题目的审计日志")
			return
		}
	}
	logs, err := s.auditSvc.ListByQuestion(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"logs": logs, "total": len(logs)})
}

// handleAuditLogsByActor 列出某用户的操作日志。
func (s *Server) handleAuditLogsByActor(w http.ResponseWriter, r *http.Request) {
	actor := r.PathValue("actor")
	if !s.hasPermission(r, domain.PermQuestionViewGlobal) && actor != auth.GetUsername(r.Context()) {
		writeError(w, http.StatusForbidden, "无权查看其他用户的审计日志")
		return
	}
	logs, err := s.auditSvc.ListByActor(r.Context(), actor)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"logs": logs, "total": len(logs)})
}
