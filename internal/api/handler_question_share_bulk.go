package api

import (
	"errors"
	"net/http"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/storage"
)

const maxBulkShares = 500

func (s *Server) handlePreviewQuestionShares(w http.ResponseWriter, r *http.Request) {
	store, ok := s.shareStore.(storage.BulkQuestionShareStore)
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
	ids, total, err := store.PreviewQuestionShares(r.Context(), filter, maxBulkShares)
	if err != nil {
		writeError(w, 500, "查询可分享题目失败")
		return
	}
	writeJSON(w, 200, map[string]any{"question_ids": ids, "total": total, "limit": maxBulkShares})
}

func (s *Server) handleCreateQuestionShares(w http.ResponseWriter, r *http.Request) {
	store, ok := s.shareStore.(storage.BulkQuestionShareStore)
	if !ok {
		writeError(w, 503, "批量分享功能不可用")
		return
	}
	var req struct {
		QuestionIDs []string `json:"question_ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if len(req.QuestionIDs) == 0 || len(req.QuestionIDs) > maxBulkShares {
		writeError(w, 400, "每次请选择 1–500 道题目")
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
	if s.auditSvc != nil {
		for _, id := range created {
			_ = s.auditSvc.Log(r.Context(), id, "question_share_request", auth.GetUsername(r.Context()), "批量申请将个人正式题目分享至全局题库")
		}
	}
	writeJSON(w, 200, map[string]any{"submitted": len(created), "skipped": len(ids) - len(created), "question_ids": created})
}
