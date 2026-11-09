package api

import (
	"fmt"
	"net/http"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

// Shared access authorizes only the frozen request body. It never grants access
// to the author's current question, AI evidence, task, or private history.
func (s *Server) loadSharedQuestion(r *http.Request, id string) (*domain.A2Question, *domain.QuestionShareRequest, int, error) {
	store, ok := s.shareStore.(storage.QuestionShareSnapshotStore)
	if !ok {
		return nil, nil, 503, fmt.Errorf("共享固定版本服务暂不可用")
	}
	q, share, err := store.GetQuestionShareSnapshot(r.Context(), id)
	if err != nil {
		return nil, nil, 503, fmt.Errorf("无法核验共享固定版本，请稍后重试")
	}
	if q == nil || share == nil {
		return nil, nil, 404, fmt.Errorf("共享题目不存在")
	}
	global, tier := s.globalTierPermissions(share.Status)
	if global == "" || !s.hasPermission(r, global) || !s.hasPermission(r, tier) || !s.permissionInQuestionBank(r, q, tier) {
		return nil, nil, 403, fmt.Errorf("无权访问该共享题目")
	}
	return q, share, 0, nil
}

func (s *Server) loadQuestionForRead(r *http.Request, id string) (*domain.A2Question, *domain.QuestionShareRequest, int, error) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "global" {
		return s.loadSharedQuestion(r, id)
	}
	if scope != "" && scope != "personal" {
		return nil, nil, 400, fmt.Errorf("无效的题库范围")
	}
	q, status, err := s.loadViewableQuestion(r, id)
	if err == nil {
		return q, nil, 0, nil
	}
	if scope == "personal" || status == 500 {
		return nil, nil, status, err
	}
	// Older clients omit scope. Owners keep their private read; foreign viewers
	// can fall back only to the snapshot, never to current private content.
	if q != nil && q.OwnerID == auth.GetUserID(r.Context()) {
		return nil, nil, status, err
	}
	if s.questionShare(r, id) != nil {
		return s.loadSharedQuestion(r, id)
	}
	return nil, nil, status, err
}
