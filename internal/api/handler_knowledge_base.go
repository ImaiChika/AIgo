package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
	"aigo/internal/workflowtrace"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

func writeKnowledgeBaseError(w http.ResponseWriter, err error) bool {
	code := 0
	switch {
	case errors.Is(err, knowledgebase.ErrDenied):
		code = 403
	case errors.Is(err, knowledgebase.ErrInvalid):
		code = 400
	case errors.Is(err, knowledgebase.ErrNotFound):
		code = 404
	case errors.Is(err, knowledgebase.ErrConflict):
		code = 409
	}
	if code == 0 {
		return false
	}
	writeError(w, code, err.Error())
	return true
}
func (s *Server) knowledgeBaseReady(w http.ResponseWriter) bool {
	if s.knowledgeBaseSvc == nil {
		writeError(w, 503, "知识库服务未就绪")
		return false
	}
	return true
}
func (s *Server) knowledgeBaseError(w http.ResponseWriter, err error) {
	if !writeKnowledgeBaseError(w, err) {
		slog.Error("knowledge_base.operation_failed", "error", err)
		writeError(w, 503, "知识库操作失败，请稍后重试")
	}
}
func (s *Server) handleKnowledgeBaseList(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if q.Get("view") == "summary" {
		items, total, caps, e := s.knowledgeBaseSvc.Summaries(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), knowledgebase.Filter{Query: q.Get("q"), Kind: q.Get("kind"), Status: q.Get("status"), KnowledgePointID: q.Get("knowledge_point_id"), Limit: limit, Offset: offset})
		if e != nil {
			s.knowledgeBaseError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"view": "summary", "items": items, "total": total, "capabilities": caps})
		return
	}
	items, total, caps, e := s.knowledgeBaseSvc.List(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), knowledgebase.Filter{Query: q.Get("q"), Kind: q.Get("kind"), Status: q.Get("status"), KnowledgePointID: q.Get("knowledge_point_id"), Limit: limit, Offset: offset})
	if e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"cards": items, "total": total, "capabilities": caps})
}
func (s *Server) knowledgeBaseRead(w http.ResponseWriter, r *http.Request, history bool) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	items, e := s.knowledgeBaseSvc.Detail(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"), history)
	if e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	if history {
		writeJSON(w, 200, map[string]any{"versions": items})
	} else {
		writeJSON(w, 200, items[0])
	}
}
func (s *Server) handleKnowledgeBaseDetail(w http.ResponseWriter, r *http.Request) {
	s.knowledgeBaseRead(w, r, false)
}
func (s *Server) handleKnowledgeBaseHistory(w http.ResponseWriter, r *http.Request) {
	s.knowledgeBaseRead(w, r, true)
}
func (s *Server) handleKnowledgeBaseSave(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 180000)
	var input struct {
		domain.KnowledgeCard
		IsSummary bool `json:"is_summary"`
	}
	if e := readJSON(r, &input); e != nil {
		writeError(w, 400, "知识卡请求格式错误或内容过大")
		return
	}
	if input.IsSummary {
		writeError(w, 400, "摘要不能作为知识卡正文保存，请先读取完整卡片")
		return
	}
	saved, e := s.knowledgeBaseSvc.Save(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"), input.KnowledgeCard)
	if e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	writeJSON(w, 200, saved)
}

func (s *Server) handleKnowledgeImport(w http.ResponseWriter, r *http.Request, confirm bool) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2*knowledgebase.MaxImportBytes+65536)
	var input knowledgebase.ImportRequest
	if readJSON(r, &input) != nil || len(input.Document) > knowledgebase.MaxImportBytes {
		writeError(w, 400, "导入请求无效或文件超过2 MiB")
		return
	}
	uid, role := auth.GetUserID(r.Context()), auth.GetRole(r.Context())
	key := s.authSvc.KnowledgeImportSigningKey()
	if confirm {
		result, err := s.knowledgeBaseSvc.ConfirmImport(r.Context(), uid, role, input, key)
		if err != nil {
			if errors.Is(err, knowledgebase.ErrConflict) {
				writeError(w, http.StatusConflict, "导入预览已失效或相关资料已变化，请重新预览")
				return
			}
			s.knowledgeBaseError(w, err)
			return
		}
		writeJSON(w, 200, result)
	} else {
		preview, err := s.knowledgeBaseSvc.PreviewImport(r.Context(), uid, role, input, key)
		if err != nil {
			s.knowledgeBaseError(w, err)
			return
		}
		writeJSON(w, 200, preview)
	}
}
func (s *Server) handleKnowledgeImportPreview(w http.ResponseWriter, r *http.Request) {
	s.handleKnowledgeImport(w, r, false)
}
func (s *Server) handleKnowledgeImportConfirm(w http.ResponseWriter, r *http.Request) {
	s.handleKnowledgeImport(w, r, true)
}
func (s *Server) handleKnowledgeBaseTransition(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	var input struct {
		Revision int `json:"revision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	if readJSON(r, &input) != nil {
		writeError(w, 400, "需要当前修订号")
		return
	}
	action := "activate"
	if r.Method == http.MethodDelete {
		action = "archive"
	}
	saved, e := s.knowledgeBaseSvc.Transition(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"), action, input.Revision)
	if e != nil {
		s.knowledgeBaseError(w, e)
		return
	}
	writeJSON(w, 200, saved)
}

func (s *Server) withKnowledgeWorkflow(ctx context.Context, mode string, points []domain.KnowledgePoint, presentation *domain.PresentationRequirements, fn func(context.Context, *domain.KnowledgeWorkflowSnapshot) error) error {
	if !knowledgebase.ModeValid(mode) {
		return knowledgebase.ErrInvalid
	}
	if mode != "knowledge" {
		if presentation != nil {
			return fmt.Errorf("%w: 本次分组顺序仅适用于知识库工作流", knowledgebase.ErrInvalid)
		}
		stage := workflowtrace.Begin(ctx, "knowledge_prepare", -1, nil)
		stage.End("not_applicable", "", nil)
		return fn(ctx, nil)
	}
	if s.knowledgeBaseSvc == nil {
		return fmt.Errorf("%w: 知识库服务未就绪", knowledgebase.ErrInvalid)
	}
	return s.knowledgeBaseSvc.WithPreparedPresentation(ctx, auth.GetUserID(ctx), auth.GetRole(ctx), mode, points, presentation, fn)
}
func (s *Server) handleKnowledgeWorkflowPrepare(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 180000)
	var input struct {
		Points       []domain.KnowledgePoint          `json:"knowledge_points"`
		Presentation *domain.PresentationRequirements `json:"presentation"`
	}
	if readJSON(r, &input) != nil {
		writeError(w, 400, "知识点请求格式错误")
		return
	}
	var snapshot *domain.KnowledgeWorkflowSnapshot
	err := s.withKnowledgeWorkflow(r.Context(), "knowledge", input.Points, input.Presentation, func(ctx context.Context, value *domain.KnowledgeWorkflowSnapshot) error { snapshot = value; return nil })
	if err != nil {
		s.knowledgeBaseError(w, err)
		return
	}
	bundles := []map[string]any{}
	for _, b := range snapshot.Bundles {
		cards := []map[string]any{}
		for _, c := range b.Cards {
			cards = append(cards, map[string]any{"id": c.ID, "revision": c.Revision, "kind": c.Kind, "title": c.Title, "source": c.Source})
		}
		bundles = append(bundles, map[string]any{"knowledge_point_id": b.KnowledgePointID, "cards": cards, "targets": knowledgebase.Targets(b), "excluded_cards": b.ExcludedCards})
	}
	writeJSON(w, 200, map[string]any{"version": snapshot.Version, "bundles": bundles, "captured_at": snapshot.CapturedAt, "presentation": snapshot.Presentation})
}
func (s *Server) handleQuestionGenerationContext(w http.ResponseWriter, r *http.Request) {
	if !s.knowledgeBaseReady(w) {
		return
	}
	store, ok := s.questionStore.(storage.QuestionGenerationContextStore)
	if !ok {
		writeError(w, 503, "生成依据存储未就绪")
		return
	}
	var frozen *domain.QuestionGenerationContext
	var viewErr error
	var viewStatus int
	err := s.knowledgeBaseSvc.Store.WithKnowledgeBaseAccess(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), func(ctx context.Context, p []string) error {
		if !knowledgebase.CapabilitiesFor(p).View && !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return knowledgebase.ErrDenied
		}
		question, code, e := s.loadViewableQuestion(r.WithContext(ctx), r.PathValue("id"))
		if e != nil {
			viewErr = e
			viewStatus = code
			return e
		}
		frozen, e = store.GetQuestionGenerationContext(ctx, r.PathValue("id"))
		if e != nil {
			return e
		}
		if frozen != nil {
			for _, card := range frozen.Bundle.Cards {
				if strings.HasPrefix(card.ID, "pkc-") {
					if question.OwnerID != auth.GetUserID(r.Context()) || !domain.PersonalKnowledgeCapabilitiesFor(p).View {
						return knowledgebase.ErrDenied
					}
				} else if !knowledgebase.CapabilitiesFor(p).View {
					return knowledgebase.ErrDenied
				}
			}
		}
		return nil
	})
	if viewErr != nil {
		writeError(w, viewStatus, viewErr.Error())
		return
	}
	if err != nil {
		s.knowledgeBaseError(w, err)
		return
	}
	if frozen == nil {
		writeError(w, 404, "该题未使用知识库工作流")
		return
	}
	writeJSON(w, 200, struct {
		*domain.QuestionGenerationContext
		PresentationFeedback *domain.PresentationFeedback `json:"presentation_feedback,omitempty"`
	}{frozen, knowledgebase.PresentationFeedbackFor(frozen)})
}
