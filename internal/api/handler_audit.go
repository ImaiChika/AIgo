package api

import (
	"net/http"
	"strconv"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

// handleListAuditLogs 按页列出操作日志。
// 查询参数：page=页码（默认1）、limit=每页数量（默认200，上限500）、
// action=操作行为、actor=操作人、question=题目ID（三个筛选参数可任意组合；
// 无全局查看权限的用户 actor 一律强制为本人）。
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
	filter := storage.AuditLogFilter{
		Action:     strings.TrimSpace(r.URL.Query().Get("action")),
		Actor:      strings.TrimSpace(r.URL.Query().Get("actor")),
		QuestionID: strings.TrimSpace(r.URL.Query().Get("question")),
		Limit:      limit,
		Offset:     (page - 1) * limit,
	}
	if !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		// 审计权限可以下放给客户，但只能追溯自己的操作，不能借日志旁路查看
		// 其他老师的题目或账号信息。
		filter.Actor = auth.GetUsername(r.Context())
	}
	logs, total, err := s.auditSvc.Search(r.Context(), filter)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"logs": logs, "total": total, "page": page, "page_size": limit})
}

// handleListAuditActions 返回日志中实际出现过的操作行为及次数，
// 供前端行为筛选下拉使用；无全局查看权限时只统计本人日志。
func (s *Server) handleListAuditActions(w http.ResponseWriter, r *http.Request) {
	actor := ""
	if !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		actor = auth.GetUsername(r.Context())
	}
	actions, err := s.auditSvc.ListActions(r.Context(), actor)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"actions": actions})
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
