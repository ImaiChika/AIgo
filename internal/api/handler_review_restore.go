package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage"
)

func writeReviewRestoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrReviewRestoreForbidden):
		writeError(w, 403, err.Error())
	case errors.Is(err, domain.ErrReviewConflict) || errors.Is(err, storage.ErrQuestionLibraryConflict):
		writeError(w, 409, err.Error())
	case errors.Is(err, domain.ErrReviewBadRequest):
		writeError(w, 400, err.Error())
	case errors.Is(err, domain.ErrReviewRestoreUnavailable):
		writeError(w, 503, err.Error())
	default:
		writeError(w, 500, "审核还原操作失败，请稍后重试")
	}
}

func (s *Server) handleRestoreCandidates(w http.ResponseWriter, r *http.Request) {
	if s.reviewSvc == nil {
		writeReviewRestoreError(w, domain.ErrReviewRestoreUnavailable)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if size < 1 || size > 100 {
		size = 20
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "reviewing" && status != "conflict" {
		writeError(w, 400, "无效的在途审核状态")
		return
	}
	filter := domain.ReviewRestoreFilter{FlowID: strings.TrimSpace(r.URL.Query().Get("flow_id")), OwnerID: strings.TrimSpace(r.URL.Query().Get("owner_id")), Keyword: r.URL.Query().Get("keyword"), Status: status}
	items, total, err := s.reviewSvc.ListRestoreCandidates(r.Context(), auth.GetUserID(r.Context()), filter, page, size, false)
	if err != nil {
		writeReviewRestoreError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size, "has_more": page*size < total})
}

func (s *Server) handleRestoreOptions(w http.ResponseWriter, r *http.Request) {
	if s.reviewSvc == nil {
		writeReviewRestoreError(w, domain.ErrReviewRestoreUnavailable)
		return
	}
	options, err := s.reviewSvc.ReviewRestoreOptions(r.Context(), auth.GetUserID(r.Context()))
	if err != nil {
		writeReviewRestoreError(w, err)
		return
	}
	writeJSON(w, 200, options)
}

func (s *Server) handleRestoreCandidateDetail(w http.ResponseWriter, r *http.Request) {
	if s.reviewSvc == nil {
		writeReviewRestoreError(w, domain.ErrReviewRestoreUnavailable)
		return
	}
	items, _, err := s.reviewSvc.ListRestoreCandidates(r.Context(), auth.GetUserID(r.Context()), domain.ReviewRestoreFilter{TaskIDs: []string{r.PathValue("taskId")}}, 1, 1, true)
	if err != nil {
		writeReviewRestoreError(w, err)
		return
	}
	if len(items) == 0 {
		writeError(w, 404, "题目已不在可还原的审核任务中，请刷新列表")
		return
	}
	writeJSON(w, 200, items[0])
}

func (s *Server) handleRestorePreview(w http.ResponseWriter, r *http.Request) {
	if s.reviewSvc == nil {
		writeReviewRestoreError(w, domain.ErrReviewRestoreUnavailable)
		return
	}
	var filter domain.ReviewRestoreFilter
	if err := readJSON(r, &filter); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	preview, err := s.reviewSvc.PreviewReviewRestore(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), filter)
	if err != nil {
		writeReviewRestoreError(w, err)
		return
	}
	writeJSON(w, 200, preview)
}

func (s *Server) handleRestoreReview(w http.ResponseWriter, r *http.Request) {
	if s.reviewSvc == nil {
		writeReviewRestoreError(w, domain.ErrReviewRestoreUnavailable)
		return
	}
	var req review.RestoreRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	result, err := s.reviewSvc.RestoreReview(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), req)
	if err != nil {
		writeReviewRestoreError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handleQuestionReviewHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.loadViewableQuestion(r, id); err != nil {
		writeError(w, status, err.Error())
		return
	}
	full := s.reviewFullAccess(r)
	if task, err := s.reviewSvc.GetTaskByQuestionID(r.Context(), id); err == nil && task != nil {
		_, byTask := s.reviewTaskAccess(r, task)
		full = full || byTask
	}
	history, err := s.reviewSvc.QuestionReviewHistory(r.Context(), id, auth.GetUserID(r.Context()), full)
	if err != nil {
		writeError(w, 500, "读取审核历史失败")
		return
	}
	writeJSON(w, 200, map[string]any{"items": history, "total": len(history)})
}
