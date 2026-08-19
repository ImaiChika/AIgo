package api

import (
	"fmt"
	"net/http"

	"aigo/internal/auth"
	"aigo/internal/domain"
)

// questionInScope 校验当前登录用户对题目是否拥有指定权限的题库范围访问权。
// 全范围用户（角色模板授权或直接权限 bank_ids 为空）直接放行；
// 受限范围用户仅能访问属于其可见题库之一的题目（多对多任一命中即可）。
// 未分类题目（不属于任何题库）对受限范围用户不可见。
func (s *Server) questionInScope(r *http.Request, q *domain.A2Question, perm string) bool {
	userID := auth.GetUserID(r.Context())
	if userID == "" {
		return false
	}
	scope, fullScope, hasPerm, err := s.authSvc.GetBankScope(r.Context(), userID, perm)
	if err != nil || !hasPerm {
		return false
	}
	if fullScope {
		return true
	}
	bankSet := make(map[string]bool, len(scope))
	for _, b := range scope {
		bankSet[b] = true
	}
	for _, b := range q.BankIDs {
		if bankSet[b] {
			return true
		}
	}
	return false
}

// loadScopedQuestion 加载题目并校验题库范围。
// 返回 (题目, 0, nil) 成功；(nil, http状态码, 错误信息) 失败。
// 越权访问返回 403，与列表接口的题库范围过滤保持一致。
func (s *Server) loadScopedQuestion(r *http.Request, id, perm string) (*domain.A2Question, int, error) {
	q, err := s.questionStore.GetQuestion(r.Context(), id)
	if err != nil {
		return nil, 500, err
	}
	if q == nil {
		return nil, 404, fmt.Errorf("题目不存在")
	}
	if !s.questionInScope(r, q, perm) {
		return nil, 403, fmt.Errorf("无权访问该题目（超出题库范围）")
	}
	return q, 0, nil
}
