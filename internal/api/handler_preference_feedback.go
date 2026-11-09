package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
	"errors"
	"net/http"
)

func (s *Server) handlePreferenceSummary(w http.ResponseWriter, r *http.Request) {
	store, ok := s.questionStore.(storage.PreferenceSummaryStore)
	if !ok {
		writeError(w, 503, "个人需求反馈汇总暂不可用")
		return
	}
	result, err := store.PreferenceSummary(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()))
	if err != nil {
		s.preferenceFeedbackError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) handlePreferenceFeedback(w http.ResponseWriter, r *http.Request) {
	store, ok := s.questionStore.(storage.PreferenceFeedbackStore)
	if !ok {
		writeError(w, 503, "个人需求反馈暂不可用")
		return
	}
	q, status, err := s.loadViewableQuestion(r, r.PathValue("id"))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if q.OwnerID != auth.GetUserID(r.Context()) {
		writeError(w, 404, "题目不存在")
		return
	}
	uid, role := auth.GetUserID(r.Context()), auth.GetRole(r.Context())
	if r.Method == http.MethodGet {
		feedback, version, writable, e := store.GetPreferenceFeedback(r.Context(), uid, role, q.ID)
		if e != nil {
			s.preferenceFeedbackError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"feedback": feedback, "question_version": version, "writable": writable, "scope": "current_content_version", "notice": "出题人的主观需求反馈，仅针对该题目版本；不改变AI首检或人工审核结果。"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var request struct {
		QuestionVersion int    `json:"question_version"`
		Revision        int    `json:"revision"`
		Rating          string `json:"rating"`
		Comment         string `json:"comment"`
	}
	if readJSON(r, &request) != nil {
		writeError(w, 400, "个人需求反馈格式无效")
		return
	}
	saved, e := store.SavePreferenceFeedback(r.Context(), uid, role, auth.GetUsername(r.Context()), domain.PreferenceFeedback{QuestionID: q.ID, QuestionVersion: request.QuestionVersion, Revision: request.Revision, Rating: request.Rating, Comment: request.Comment})
	if e != nil {
		s.preferenceFeedbackError(w, e)
		return
	}
	writeJSON(w, 200, saved)
}
func (s *Server) preferenceFeedbackError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrPreferenceDenied), errors.Is(err, knowledgebase.ErrDenied):
		writeError(w, 403, "当前身份无权查看或填写此题的个人需求反馈")
	case errors.Is(err, storage.ErrPreferenceConflict):
		writeError(w, 409, err.Error())
	case errors.Is(err, storage.ErrPreferenceReadOnly):
		writeError(w, 409, err.Error())
	case errors.Is(err, storage.ErrPreferenceInvalid):
		writeError(w, 400, err.Error())
	default:
		writeError(w, 503, "个人需求反馈暂时未能保存或读取，请稍后重试")
	}
}
