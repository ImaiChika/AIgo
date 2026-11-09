package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/questionlib"
	"aigo/internal/storage"
)

var errLibraryAudit = errors.New("保存题库操作记录失败")

// This adapter reads persisted tasks in the library transaction. Stopping the
// review executor does not disable zero-round use or erase an in-flight task.
type reviewTaskActivity struct{ server *Server }

func (a reviewTaskActivity) ActiveTaskForQuestion(ctx context.Context, id string) (bool, error) {
	if store, ok := a.server.questionStore.(interface {
		HasActiveReviewTask(context.Context, string) (bool, error)
	}); ok {
		return store.HasActiveReviewTask(ctx, id)
	}
	return false, questionlib.ErrUnavailable
}
func (s *Server) questionlibService() *questionlib.Service {
	return questionlib.NewService(s.questionStore, reviewTaskActivity{server: s})
}

func (s *Server) handleConfirmQuestions(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermQuestionView) || !s.hasPermission(r, domain.PermQuestionConfirm) {
		writeError(w, http.StatusForbidden, "缺少查看题目或确认入库权限")
		return
	}
	s.handleLibraryMutation(w, r, "confirm")
}
func (s *Server) handleLibraryDelete(w http.ResponseWriter, r *http.Request) {
	s.handleLibraryMutation(w, r, "delete")
}
func (s *Server) handleLibraryRestore(w http.ResponseWriter, r *http.Request) {
	s.handleLibraryMutation(w, r, "restore")
}

// The whole batch, durable provenance checks, and its audit event commit together.
// All three endpoints require the same explicit content/library/review guards.
func (s *Server) handleLibraryMutation(w http.ResponseWriter, r *http.Request, operation string) {
	var req struct {
		Items []questionlib.ConfirmItem `json:"items"`
		IDs   []string                  `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if len(req.IDs) > 0 {
		writeError(w, 400, "请刷新页面并提交包含版本及修订号的 items")
		return
	}
	if err := questionlib.ValidateItems(req.Items); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	tx, ok := s.questionStore.(storage.QuestionLibraryTransactionStore)
	if !ok || s.auditSvc == nil {
		writeError(w, 503, questionlib.ErrUnavailable.Error())
		return
	}
	ids := make([]string, len(req.Items))
	for i, item := range req.Items {
		ids[i] = item.QuestionID
	}
	count := 0
	updated := make([]domain.A2Question, 0, len(req.Items))
	err := tx.WithLibraryTransaction(r.Context(), ids, func(ctx context.Context) error {
		before := make(map[string]int64, len(req.Items))
		for _, id := range ids {
			q, err := s.questionStore.GetQuestion(ctx, id)
			if err != nil {
				return err
			}
			if q != nil {
				before[id] = q.LibraryRevision
			}
		}
		var err error
		svc := s.questionlibService()
		switch operation {
		case "confirm":
			count, err = svc.Confirm(ctx, auth.GetUserID(ctx), req.Items, time.Now())
		case "delete":
			count, err = svc.Delete(ctx, auth.GetUserID(ctx), req.Items, time.Now())
		case "restore":
			count, err = svc.Restore(ctx, auth.GetUserID(ctx), req.Items, time.Now())
		default:
			return fmt.Errorf("未知题库操作")
		}
		if err != nil {
			return err
		}
		changed := false
		for _, id := range ids {
			q, err := s.questionStore.GetQuestion(ctx, id)
			if err != nil {
				return err
			}
			if q != nil {
				updated = append(updated, *q)
				changed = changed || before[id] != q.LibraryRevision
			}
		}
		if !changed {
			return nil
		}
		for i, q := range updated {
			if before[q.ID] == q.LibraryRevision {
				continue
			}
			detail, _ := json.Marshal(struct {
				Expected        questionlib.ConfirmItem `json:"expected"`
				Version         int                     `json:"version"`
				LibraryRevision int64                   `json:"library_revision"`
				ReviewRevision  int64                   `json:"review_revision"`
			}{req.Items[i], q.Version, q.LibraryRevision, q.ReviewRevision})
			if err := s.auditSvc.Log(ctx, q.ID, "library_"+operation, auth.GetUsername(ctx), string(detail)); err != nil {
				return fmt.Errorf("%w: %v", errLibraryAudit, err)
			}
		}
		return nil
	})
	if err != nil {
		code := http.StatusBadRequest
		switch {
		case errors.Is(err, questionlib.ErrNotOwner):
			code = http.StatusForbidden
		case errors.Is(err, errLibraryAudit):
			code = http.StatusInternalServerError
		case errors.Is(err, questionlib.ErrUnavailable):
			code = http.StatusServiceUnavailable
		case errors.Is(err, questionlib.ErrLibraryConflict), errors.Is(err, storage.ErrQuestionLibraryConflict), errors.Is(err, domain.ErrQuestionVersionConflict), errors.Is(err, domain.ErrReviewConflict):
			code = http.StatusConflict
		}
		writeError(w, code, err.Error())
		return
	}
	field := map[string]string{"confirm": "confirmed", "delete": "deleted", "restore": "restored"}[operation]
	writeJSON(w, 200, map[string]any{field: count, "status": "ok", "updated_questions": updated})
}

// libraryStateFromQuery 解析 library 查询参数为归属过滤；空值不过滤。
func libraryStateFromQuery(r *http.Request) ([]string, bool) {
	value := strings.TrimSpace(r.URL.Query().Get("library"))
	if value == "" {
		return nil, true
	}
	state, ok := domain.ParseLibraryState(value)
	if !ok {
		return nil, false
	}
	return []string{string(state)}, true
}

// applyPersonalLibraryScope 个人题库轴：scope=personal 时按 library 参数过滤
// （normal=active / deleted=deleted / unfiled=unfiled；缺省为 normal+unfiled
// 的过程视图，兼容旧“我的题库”三层入口）。
func (s *Server) applyPersonalLibraryScope(w http.ResponseWriter, r *http.Request, filter *storage.QuestionFilter, globalScope bool) bool {
	if globalScope {
		return true
	}
	states, ok := libraryStateFromQuery(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "无效的题库归属: "+r.URL.Query().Get("library"))
		return false
	}
	if states == nil {
		// 未显式指定：维持旧语义（不过滤归属），页面自选页签时再传参。
		return true
	}
	filter.LibraryStates = states
	return true
}
