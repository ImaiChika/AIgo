package api

import (
	"net/http"
	"strconv"
)

// handleListAuditLogs 列出最近的操作日志。
// 查询参数：limit=返回数量（默认100）
func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	logs, err := s.auditSvc.ListLogs(r.Context(), limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"logs": logs, "total": len(logs)})
}

// handleAuditLogsByQuestion 列出某题目的操作日志。
func (s *Server) handleAuditLogsByQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	logs, err := s.auditSvc.ListByActor(r.Context(), actor)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"logs": logs, "total": len(logs)})
}
