package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"fmt"
	"net/http"
)

type journeyError struct {
	status  int
	message string
}

func (e *journeyError) Error() string { return e.message }
func (s *Server) ownedAuthoringTask(ctx context.Context, kind, id string) (*storage.AuthoringTask, error) {
	if (kind != "single" && kind != "batch") || len(id) == 0 || len(id) > 128 {
		return nil, &journeyError{400, "本次任务标识无效"}
	}
	st, ok := s.questionStore.(storage.AuthoringJourneyStore)
	if !ok {
		return nil, &journeyError{503, "当前服务尚不支持本次任务定位，请更新后端"}
	}
	task, err := st.ReadAuthoringTask(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if task == nil || task.OwnerID == "" || task.OwnerID != auth.GetUserID(ctx) {
		return nil, &journeyError{404, "本次任务不存在或无权访问"}
	}
	if len(task.QuestionIDs) > 1000 {
		return nil, &journeyError{400, "本次任务范围过大，请从历史记录分批查看"}
	}
	return task, nil
}
func writeJourneyError(w http.ResponseWriter, err error) {
	if e, ok := err.(*journeyError); ok {
		writeError(w, e.status, e.message)
	} else {
		writeError(w, 503, "暂时无法读取本次任务，请稍后重试")
	}
}
func (s *Server) handleAuthoringJourney(w http.ResponseWriter, r *http.Request) {
	task, err := s.ownedAuthoringTask(r.Context(), r.URL.Query().Get("task_kind"), r.URL.Query().Get("task_id"))
	if err != nil {
		writeJourneyError(w, err)
		return
	}
	counts, err := s.questionStore.(storage.AuthoringJourneyStore).CountAuthoringQuestions(r.Context(), auth.GetUserID(r.Context()), task.QuestionIDs)
	if err != nil {
		writeJourneyError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"task": task, "counts": counts, "generating": max(0, task.Total-len(task.QuestionIDs)-task.GenerationFailed)})
}
func (s *Server) applyAuthoringTaskFilter(w http.ResponseWriter, r *http.Request, f storage.QuestionFilter) (storage.QuestionFilter, bool) {
	kind, id := r.URL.Query().Get("task_kind"), r.URL.Query().Get("task_id")
	if kind == "" && id == "" {
		return f, true
	}
	if r.URL.Query().Get("scope") == "global" {
		writeError(w, 400, "本次命题只定位个人题目")
		return f, false
	}
	if !s.hasPermission(r, domain.PermQuestionView) {
		writeError(w, 403, "当前身份无个人题目查看权限")
		return f, false
	}
	task, err := s.ownedAuthoringTask(r.Context(), kind, id)
	if err != nil {
		writeJourneyError(w, err)
		return f, false
	}
	f.IDs = append([]string{}, task.QuestionIDs...)
	f.OwnerID = auth.GetUserID(r.Context())
	f.IncludeLegacyOwner = false
	return f, true
}
func (s *Server) exportAuthoringTask(r *http.Request, req questionExportRequest) ([]string, error) {
	if req.TaskKind == "" && req.TaskID == "" {
		return nil, nil
	}
	if req.Scope != "personal" {
		return nil, &journeyError{400, "本次命题仅支持个人题库导出"}
	}
	task, err := s.ownedAuthoringTask(r.Context(), req.TaskKind, req.TaskID)
	if err != nil {
		return nil, err
	}
	ids := append([]string{}, task.QuestionIDs...)
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	for _, id := range req.QuestionIDs {
		if !set[id] {
			return nil, &journeyError{400, fmt.Sprintf("所选题目不属于本次任务")}
		}
	}
	return ids, nil
}
