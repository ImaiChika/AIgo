package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/learning"
	"aigo/internal/storage"
)

// A read-only service also supports existing local executors which have not
// started learning workers. Only the explicitly wired service can call a model.
func (s *Server) learningService() *learning.Service {
	if s.learningSvc != nil {
		return s.learningSvc
	}
	if store, ok := s.questionStore.(storage.LearningStore); ok {
		return learning.NewService(store, nil)
	}
	return nil
}
func writeLearningError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, storage.ErrLearningDenied):
		status = 403
	case errors.Is(err, storage.ErrLearningNotFound):
		status = 404
	case errors.Is(err, storage.ErrLearningInvalid):
		status = 400
	case errors.Is(err, storage.ErrLearningConflict):
		status = 409
	case errors.Is(err, storage.ErrLearningBusy):
		w.Header().Set("Retry-After", "5")
		status = 429
	case errors.Is(err, storage.ErrLearningUnavailable):
		status = 503
	}
	if status == 500 {
		writeError(w, 500, "个人学习操作失败，请稍后重试")
		return
	}
	writeError(w, status, err.Error())
}

func writeLearningOperationError(w http.ResponseWriter, err error) bool {
	for _, kind := range []error{storage.ErrLearningDenied, storage.ErrLearningNotFound, storage.ErrLearningInvalid, storage.ErrLearningConflict, storage.ErrLearningBusy, storage.ErrLearningUnavailable} {
		if errors.Is(err, kind) {
			writeLearningError(w, err)
			return true
		}
	}
	return false
}
func (s *Server) handleLearningComparison(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	result, err := service.Compare(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"))
	if err != nil {
		writeLearningError(w, err)
		return
	}
	result.ExpressionCardsSupported = s.knowledgeBaseSvc != nil
	writeJSON(w, 200, result)
}
func (s *Server) handleLearningSubmit(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	var req domain.LearningSubmitRequest
	if err := readJSON(r, &req); err != nil {
		writeLearningError(w, storage.ErrLearningInvalid)
		return
	}
	req.QuestionID = r.PathValue("id")
	job, replayed, err := service.Submit(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), req)
	if err != nil {
		writeLearningError(w, err)
		return
	}
	writeJSON(w, 202, map[string]any{"job": job, "replayed": replayed})
}
func (s *Server) handleLearningJob(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	job, err := service.GetJob(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"))
	if err != nil {
		writeLearningError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"job": job})
}
func (s *Server) handleLearningJobs(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	jobs, err := service.ListJobs(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"), 20)
	if err != nil {
		writeLearningError(w, err)
		return
	}
	if jobs == nil {
		jobs = []domain.LearningJob{}
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}
func (s *Server) handleLearningImport(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	var req domain.LearningImportRequest
	if err := readJSON(r, &req); err != nil {
		writeLearningError(w, storage.ErrLearningInvalid)
		return
	}
	req.JobID = r.PathValue("id")
	items, replayed, err := service.Import(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), req)
	if err != nil {
		writeLearningError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"experiences": items, "replayed": replayed})
}
func (s *Server) handlePersonalExperiences(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "draft" && status != "active" && status != "archived" {
		writeLearningError(w, storage.ErrLearningInvalid)
		return
	}
	if queryStore, ok := service.Store.(storage.ExperienceQueryStore); ok {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit < 1 || limit > 100 {
			limit = 20
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		var items []domain.AuthoringExperience
		var total int
		err := service.Store.WithLearningAccess(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), func(ctx context.Context, permissions []string) error {
			caps := domain.PersonalKnowledgeCapabilitiesFor(permissions)
			if !caps.View {
				return storage.ErrLearningDenied
			}
			var e error
			items, total, e = queryStore.SearchAuthoringExperiences(ctx, auth.GetUserID(r.Context()), strings.TrimSpace(r.URL.Query().Get("q")), status, limit, offset)
			return e
		})
		if err != nil {
			writeLearningError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"experiences": items, "total": total})
		return
	}
	items, err := service.ListExperiences(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), true)
	if err != nil {
		writeLearningError(w, err)
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	filtered := []domain.AuthoringExperience{}
	for _, item := range items {
		if (status == "" || status == item.Status) && (query == "" || strings.Contains(strings.ToLower(item.Title), query)) {
			filtered = append(filtered, item)
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	writeJSON(w, 200, map[string]any{"experiences": filtered[offset:end], "total": total})
}

func (s *Server) handleCreatePersonalExperience(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	var req domain.PersonalKnowledgeCreateRequest
	if err := readJSON(r, &req); err != nil {
		writeLearningError(w, storage.ErrLearningInvalid)
		return
	}
	item, replayed, err := service.CreateExperienceWithReplay(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), req)
	if err != nil {
		writeLearningError(w, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, item)
}
func (s *Server) handlePersonalExperienceSave(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	var req struct {
		Title                 string                 `json:"title"`
		Scope                 domain.ExperienceScope `json:"scope"`
		Rules                 []domain.LearningRule  `json:"rules"`
		Status                string                 `json:"status"`
		ExpectedRevision      int                    `json:"expected_revision"`
		ConfirmMedicalChanges bool                   `json:"confirm_medical_changes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeLearningError(w, storage.ErrLearningInvalid)
		return
	}
	item, err := service.SaveExperience(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"), domain.AuthoringExperience{Title: req.Title, Scope: req.Scope, Rules: req.Rules, Status: req.Status}, req.ExpectedRevision, req.ConfirmMedicalChanges)
	if err != nil {
		writeLearningError(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (s *Server) handlePersonalExperienceHistory(w http.ResponseWriter, r *http.Request) {
	service := s.learningService()
	if service == nil {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	items, err := service.ExperienceVersions(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), r.PathValue("id"))
	if err != nil {
		writeLearningError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"versions": items})
}
func (s *Server) handleQuestionPersonalExperiences(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("scope") == "global" {
		writeLearningError(w, storage.ErrLearningDenied)
		return
	}
	q, status, err := s.loadViewableQuestion(r, r.PathValue("id"))
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if q.OwnerID != auth.GetUserID(r.Context()) || !s.hasPermission(r, domain.PermQuestionView) {
		writeLearningError(w, storage.ErrLearningDenied)
		return
	}
	store, ok := s.questionStore.(storage.LearningStore)
	if !ok {
		writeLearningError(w, storage.ErrLearningUnavailable)
		return
	}
	var snapshot *domain.PersonalExperienceSnapshot
	err = store.WithLearningAccess(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), func(ctx context.Context, permissions []string) error {
		caps := domain.LearningCapabilitiesFor(permissions)
		if !caps.Analyze && !caps.Save && !caps.Use {
			return storage.ErrLearningDenied
		}
		reader, ok := s.questionStore.(interface {
			GetQuestionPersonalExperiences(context.Context, string) (*domain.PersonalExperienceSnapshot, error)
		})
		if !ok {
			return storage.ErrLearningUnavailable
		}
		var e error
		snapshot, e = reader.GetQuestionPersonalExperiences(ctx, q.ID)
		if e != nil {
			return e
		}
		if snapshot != nil && snapshot.OwnerID != q.OwnerID {
			return storage.ErrLearningDenied
		}
		return nil
	})
	if err != nil {
		writeLearningError(w, err)
		return
	}
	if snapshot != nil {
		copy := *snapshot
		copy.OwnerID = ""
		snapshot = &copy
	}
	writeJSON(w, 200, map[string]any{"snapshot": snapshot})
}

func (s *Server) withPersonalExperiences(r *http.Request, apply *bool, points []domain.KnowledgePoint, next func(*domain.PersonalExperienceSnapshot) error) error {
	if apply != nil && !*apply {
		return next(nil)
	}
	allowed, permissionErr := s.authSvc.HasPermission(r.Context(), auth.GetUserID(r.Context()), domain.PermLearningUse)
	if permissionErr != nil {
		return storage.ErrLearningUnavailable
	}
	if !allowed {
		view, err := s.authSvc.HasPermission(r.Context(), auth.GetUserID(r.Context()), domain.PermExperienceView)
		if err != nil {
			return storage.ErrLearningUnavailable
		}
		use, err := s.authSvc.HasPermission(r.Context(), auth.GetUserID(r.Context()), domain.PermExperienceUse)
		if err != nil {
			return storage.ErrLearningUnavailable
		}
		allowed = view && use
	}
	if !allowed {
		if apply != nil && *apply {
			return storage.ErrLearningDenied
		}
		return next(nil)
	}
	service := s.learningService()
	if service == nil {
		if apply == nil {
			return next(nil)
		}
		return storage.ErrLearningUnavailable
	}
	snapshot, err := service.FreezeExperiences(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), points)
	if err != nil {
		return err
	}
	if snapshot != nil && len(snapshot.Entries) == 0 {
		snapshot = nil
	}
	return next(snapshot)
}
