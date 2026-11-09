package api

import (
	"errors"
	"net/http"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

const maxBulkShares = 500

func (s *Server) handlePreviewQuestionShares(w http.ResponseWriter, r *http.Request) {
	store, ok := s.shareStore.(storage.VersionedBulkQuestionShareStore)
	if !ok {
		writeError(w, 503, "批量分享功能不可用")
		return
	}
	var req struct {
		Keyword    string `json:"q"`
		BankID     string `json:"bank_id"`
		Profession string `json:"profession"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	filter := storage.QuestionFilter{OwnerID: auth.GetUserID(r.Context()), Keyword: req.Keyword, BankID: req.BankID}
	if filter.BankID == "__unclassified__" {
		filter.BankID = ""
		filter.Unclassified = true
	}
	if req.Profession != "" {
		filter.Professions = []string{req.Profession}
	}
	items, total, err := store.PreviewQuestionShareItems(r.Context(), filter, maxBulkShares)
	if err != nil {
		writeError(w, 500, "查询可分享题目失败")
		return
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.QuestionID)
	}
	writeJSON(w, 200, map[string]any{"question_ids": ids, "items": items, "total": total, "limit": maxBulkShares})
}

func (s *Server) handleCreateQuestionShares(w http.ResponseWriter, r *http.Request) {
	store, ok := s.shareStore.(storage.BulkQuestionShareStore)
	if !ok {
		writeError(w, 503, "批量分享功能不可用")
		return
	}
	var req struct {
		QuestionIDs []string                          `json:"question_ids"`
		Items       []domain.QuestionShareExpectation `json:"items"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if (len(req.QuestionIDs) > 0 && len(req.Items) > 0) || len(req.QuestionIDs)+len(req.Items) == 0 || len(req.QuestionIDs)+len(req.Items) > maxBulkShares {
		writeError(w, 400, "每次请选择 1–500 道题目")
		return
	}
	if len(req.Items) > 0 {
		versioned, ok := s.shareStore.(storage.VersionedBulkQuestionShareStore)
		if !ok {
			writeError(w, 503, "固定版本批量分享不可用")
			return
		}
		seen := map[string]bool{}
		for i := range req.Items {
			item := &req.Items[i]
			item.QuestionID = strings.TrimSpace(item.QuestionID)
			if item.QuestionID == "" || !validShareExpectation(*item) || seen[item.QuestionID] {
				writeError(w, 400, "分享题目及三项版本必须完整且不能重复")
				return
			}
			seen[item.QuestionID] = true
		}
		created, err := versioned.CreateQuestionShareItems(r.Context(), auth.GetUserID(r.Context()), req.Items)
		if err != nil {
			writeShareCreateError(w, err)
			return
		}
		s.writeBulkShareResult(w, r, created, len(req.Items))
		return
	}
	ids := make([]string, 0, len(req.QuestionIDs))
	seen := map[string]bool{}
	for _, id := range req.QuestionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			writeError(w, 400, "题目 ID 不能为空")
			return
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	created, err := store.CreateQuestionShares(r.Context(), auth.GetUserID(r.Context()), ids)
	if err != nil {
		if errors.Is(err, storage.ErrQuestionShareNotOwner) {
			writeError(w, 403, err.Error())
			return
		}
		writeError(w, 500, "提交分享申请失败，请重试")
		return
	}
	s.writeBulkShareResult(w, r, created, len(ids))
}
func validShareExpectation(item domain.QuestionShareExpectation) bool {
	return item.ExpectedVersion > 0 && item.ExpectedLibraryRevision != nil && *item.ExpectedLibraryRevision > 0 && item.ExpectedReviewRevision != nil && *item.ExpectedReviewRevision >= 0
}
func writeShareCreateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrQuestionShareNotOwner):
		writeError(w, 403, err.Error())
	case errors.Is(err, storage.ErrQuestionShareNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, storage.ErrQuestionShareVersionConflict), errors.Is(err, storage.ErrQuestionShareNotFormal):
		writeError(w, 409, err.Error())
	default:
		writeError(w, 500, "提交分享申请失败，请重试")
	}
}
func (s *Server) writeBulkShareResult(w http.ResponseWriter, r *http.Request, created []string, total int) {
	if s.auditSvc != nil {
		for _, id := range created {
			_ = s.auditSvc.Log(r.Context(), id, "question_share_request", auth.GetUsername(r.Context()), "批量申请将个人已确认题目分享至全局题库")
		}
	}
	writeJSON(w, 200, map[string]any{"submitted": len(created), "skipped": total - len(created), "question_ids": created})
}
