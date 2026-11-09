package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"net/http"
	"strconv"
	"strings"
)

func collectionQuery(r *http.Request) knowledgebase.Filter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	return knowledgebase.Filter{Query: q.Get("q"), Status: q.Get("status"), Limit: limit, Offset: offset}
}
func (s *Server) handlePrivateKnowledge(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	uid, role := auth.GetUserID(r.Context()), auth.GetRole(r.Context())
	if r.Method == http.MethodGet {
		if id := r.PathValue("id"); id != "" {
			if !strings.HasSuffix(r.URL.Path, "/history") {
				card, e := s.knowledgeBaseSvc.PrivateDetail(r.Context(), uid, role, id)
				if e != nil {
					s.knowledgeBaseError(w, e)
					return
				}
				// Selection is independent list metadata, never a field in the editable body.
				writeJSON(w, 200, struct {
					domain.KnowledgeCard
					Origin *domain.ExperienceSource `json:"origin,omitempty"`
				}{card.KnowledgeCard, card.Origin})
				return
			}

			rows, e := s.knowledgeBaseSvc.PrivateHistory(r.Context(), uid, role, id)
			if e != nil {
				s.knowledgeBaseError(w, e)
				return
			}
			writeJSON(w, 200, map[string]any{"versions": rows})
			return
		}
		if r.URL.Query().Get("view") == "summary" {
			items, total, e := s.knowledgeBaseSvc.PrivateSummaries(r.Context(), uid, role, collectionQuery(r))
			if e != nil {
				s.knowledgeBaseError(w, e)
				return
			}
			writeJSON(w, 200, map[string]any{"view": "summary", "items": items, "total": total, "supports_expression": true})
			return
		}
		rows, total, e := s.knowledgeBaseSvc.PrivateList(r.Context(), uid, role, collectionQuery(r))
		if e != nil {
			s.knowledgeBaseError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"cards": rows, "total": total, "supports_expression": true})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 800000)
	var input struct {
		Key   string `json:"key"`
		Cards []struct {
			domain.KnowledgeCard
			IsSummary bool `json:"is_summary"`
		} `json:"cards"`
	}
	if readJSON(r, &input) != nil {
		writeError(w, 400, "知识卡请求格式错误")
		return
	}
	req := domain.KnowledgeCardSaveRequest{Key: input.Key, Cards: []domain.KnowledgeCard{}}
	for _, card := range input.Cards {
		if card.IsSummary {
			writeError(w, 400, "摘要不能作为知识卡正文保存，请先读取完整卡片")
			return
		}
		req.Cards = append(req.Cards, card.KnowledgeCard)
	}
	rows, e := s.knowledgeBaseSvc.SavePrivate(r.Context(), uid, role, req, nil)
	if e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"cards": rows})
}
func (s *Server) handleKnowledgeChoices(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	uid, role := auth.GetUserID(r.Context()), auth.GetRole(r.Context())
	if r.Method == http.MethodGet {
		refs, e := s.knowledgeBaseSvc.Choices(r.Context(), uid, role)
		if e != nil {
			s.knowledgeBaseError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"cards": refs})
		return
	}
	var req struct {
		Scope    string `json:"scope"`
		ID       string `json:"id"`
		Selected bool   `json:"selected"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if readJSON(r, &req) != nil {
		writeError(w, 400, "资料选择无效")
		return
	}
	if e := s.knowledgeBaseSvc.ChooseCard(r.Context(), uid, role, req.Scope, req.ID, req.Selected); e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"selected": req.Selected})
}
func (s *Server) handleKnowledgeSubmissions(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	uid, role := auth.GetUserID(r.Context()), auth.GetRole(r.Context())
	if r.Method == http.MethodGet {
		items, total, e := s.knowledgeBaseSvc.Submissions(r.Context(), uid, role, r.URL.Query().Get("review") == "true", collectionQuery(r))
		if e != nil {
			s.knowledgeBaseError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"submissions": items, "total": total})
		return
	}
	var req domain.KnowledgeShareRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16000)
	if readJSON(r, &req) != nil {
		writeError(w, 400, "共享申请格式错误")
		return
	}
	items, e := s.knowledgeBaseSvc.SubmitCards(r.Context(), uid, role, req)
	if e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"submissions": items})
}
func (s *Server) handleKnowledgeDecisions(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	var req domain.KnowledgeDecisionRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16000)
	if readJSON(r, &req) != nil {
		writeError(w, 400, "审核请求格式错误")
		return
	}
	items, e := s.knowledgeBaseSvc.DecideCards(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), req)
	if e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"submissions": items})
}
func (s *Server) handleLearningKnowledgeCards(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	service := s.learningService()
	if service == nil {
		writeError(w, 503, "学习服务未就绪")
		return
	}
	var req domain.LearningImportRequest
	r.Body = http.MaxBytesReader(w, r.Body, 80000)
	if readJSON(r, &req) != nil {
		writeError(w, 400, "候选请求格式错误")
		return
	}
	req.JobID = r.PathValue("id")
	var cards []domain.PrivateKnowledgeCard
	err := service.WithKnowledgeCandidates(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), req, func(ctx context.Context, input []domain.KnowledgeCard, origins []*domain.ExperienceSource) error {
		var e error
		cards, e = s.knowledgeBaseSvc.SavePrivate(ctx, auth.GetUserID(r.Context()), auth.GetRole(r.Context()), domain.KnowledgeCardSaveRequest{Key: req.ImportKey, Cards: input}, origins)
		return e
	})
	if err != nil {
		if !writeLearningOperationError(w, err) {
			s.knowledgeBaseError(w, err)
		}
		return
	}
	writeJSON(w, 200, map[string]any{"cards": cards})
}
