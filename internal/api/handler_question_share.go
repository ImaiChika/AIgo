package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

// questionShareView 是分享申请页面专用的展示结构。题目本身不携带出题人信息；
// OwnerName 只在管理员审批队列中显示申请人，不进入题目详情接口。
type questionShareView struct {
	Request       domain.QuestionShareRequest `json:"request"`
	Question      domain.A2Question           `json:"question"`
	OwnerName     string                      `json:"owner_name,omitempty"`
	OwnerUsername string                      `json:"owner_username,omitempty"`
}

func (s *Server) handleCreateQuestionShare(w http.ResponseWriter, r *http.Request) {
	if s.shareStore == nil {
		writeError(w, http.StatusServiceUnavailable, "全局题库分享功能不可用")
		return
	}
	questionID := r.PathValue("id")
	q, err := s.questionStore.GetQuestion(r.Context(), questionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询题目失败")
		return
	}
	if q == nil {
		writeError(w, http.StatusNotFound, "题目不存在")
		return
	}
	if q.OwnerID != auth.GetUserID(r.Context()) {
		writeError(w, http.StatusForbidden, "只能分享本人题库中的题目")
		return
	}
	if q.Status != domain.StatusPublished {
		writeError(w, http.StatusConflict, storage.ErrQuestionShareNotFormal.Error())
		return
	}
	if existing, err := s.shareStore.GetQuestionShareByQuestionID(r.Context(), questionID); err != nil {
		writeError(w, http.StatusInternalServerError, "查询分享申请失败")
		return
	} else if existing != nil {
		writeError(w, http.StatusConflict, "该题目已经提交过分享申请，审批结果不可重复提交")
		return
	}

	request := domain.QuestionShareRequest{
		ID:         fmt.Sprintf("share-%s-%d", questionID, time.Now().UnixNano()),
		QuestionID: questionID,
		OwnerID:    auth.GetUserID(r.Context()),
		Status:     domain.QuestionSharePending,
		CreatedAt:  time.Now(),
	}
	if err := s.shareStore.CreateQuestionShare(r.Context(), request); err != nil {
		if existing, lookupErr := s.shareStore.GetQuestionShareByQuestionID(r.Context(), questionID); lookupErr == nil && existing != nil {
			writeError(w, http.StatusConflict, "该题目已经提交过分享申请，审批结果不可重复提交")
			return
		}
		writeError(w, http.StatusInternalServerError, "保存分享申请失败")
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), questionID, "question_share_request", actor, "申请将个人正式题目分享至全局题库")
	}
	writeJSON(w, http.StatusCreated, questionShareView{Request: request, Question: *q})
}

func (s *Server) handleListQuestionShares(w http.ResponseWriter, r *http.Request) {
	if s.shareStore == nil {
		writeError(w, http.StatusServiceUnavailable, "全局题库分享功能不可用")
		return
	}
	userID := auth.GetUserID(r.Context())
	hasReview, err := s.authSvc.HasPermission(r.Context(), userID, domain.PermQuestionShareReview)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "权限查询失败")
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	ownerID := userID
	if scope == "pending" || scope == "all" || status != "" {
		if !hasReview {
			writeError(w, http.StatusForbidden, "无权查看全局题库分享审批队列")
			return
		}
		ownerID = ""
		if scope == "pending" && status == "" {
			status = string(domain.QuestionSharePending)
		}
	}
	if scope != "" && scope != "mine" && scope != "pending" && scope != "all" {
		writeError(w, http.StatusBadRequest, "无效的分享申请范围")
		return
	}
	items, err := s.shareStore.ListQuestionShares(r.Context(), status, ownerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询分享申请失败")
		return
	}
	views := make([]questionShareView, 0, len(items))
	for _, item := range items {
		view := questionShareView{Request: item.Request, Question: item.Question}
		if hasReview {
			view.OwnerName = item.OwnerName
			view.OwnerUsername = item.OwnerUsername
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views, "total": len(views)})
}

func (s *Server) handleReviewQuestionShare(w http.ResponseWriter, r *http.Request) {
	if s.shareStore == nil {
		writeError(w, http.StatusServiceUnavailable, "全局题库分享功能不可用")
		return
	}
	var req struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	status := domain.QuestionShareStatus(strings.TrimSpace(req.Status))
	if status != domain.QuestionShareApproved && status != domain.QuestionShareRejected {
		writeError(w, http.StatusBadRequest, "审批状态只能是 approved 或 rejected")
		return
	}
	request, err := s.shareStore.ReviewQuestionShare(r.Context(), r.PathValue("id"), auth.GetUserID(r.Context()), status, req.Note)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrQuestionShareNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, storage.ErrQuestionShareAlreadyReviewed), errors.Is(err, storage.ErrQuestionShareNotFormal):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "审批分享申请失败")
		}
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	action := "question_share_approve"
	if status == domain.QuestionShareRejected {
		action = "question_share_reject"
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), request.QuestionID, action, actor, strings.TrimSpace(req.Note))
	}
	writeJSON(w, http.StatusOK, request)
}
