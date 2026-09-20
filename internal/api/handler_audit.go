package api

import (
	"net/http"
	"strconv"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// handleListAuditLogs 按页列出操作日志。
// 查询参数：page=页码（默认1）、limit=每页数量（默认200，上限500）
func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	if limit > 500 {
		limit = 500
	}
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	offset := (page - 1) * limit
	if s.hasPermission(r, domain.PermQuestionViewGlobal) {
		logs, total, err := s.auditSvc.ListLogsPaged(r.Context(), limit, offset)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"logs": logs, "total": total, "page": page, "page_size": limit})
		return
	}
	// 审计权限可以下放给客户，但只能追溯自己的操作，不能借日志旁路查看
	// 其他老师的题目或账号信息。
	all, err := s.auditSvc.ListByActor(r.Context(), auth.GetUsername(r.Context()))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	writeJSON(w, 200, map[string]any{"logs": all[offset:end], "total": total, "page": page, "page_size": limit})
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
