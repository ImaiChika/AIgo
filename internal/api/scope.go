package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/storage"
)

// canViewTier 校验当前登录用户是否拥有指定题库分层的查看权限。
func (s *Server) canViewTier(r *http.Request, tier domain.QuestionTier) bool {
	userID := auth.GetUserID(r.Context())
	if userID == "" {
		return false
	}
	_, _, hasPerm, err := s.authSvc.GetBankScope(r.Context(), userID, domain.TierViewPerm(tier))
	return err == nil && hasPerm
}

// visibleTiers 返回当前用户可查看的题库分层（固定顺序：正式、过程、淘汰）。
func (s *Server) visibleTiers(r *http.Request) []domain.QuestionTier {
	tiers := []domain.QuestionTier{domain.TierFormal, domain.TierWorking, domain.TierEliminated}
	visible := make([]domain.QuestionTier, 0, len(tiers))
	for _, t := range tiers {
		if s.canViewTier(r, t) {
			visible = append(visible, t)
		}
	}
	return visible
}

// canViewQuestion 将生命周期分层权限与用户的分类子题库边界一起校验。
// 三个逻辑题库的查看权限彼此独立，不能再借用 question:view 读取正式/淘汰题。
func (s *Server) canViewQuestion(r *http.Request, q *domain.A2Question) bool {
	if q == nil {
		return false
	}
	userID := auth.GetUserID(r.Context())
	if userID == "" {
		return false
	}
	// 新生成题目归属于用户个人题库；个人三层题库统一使用 question:view，
	// 不要求客户额外获得全局正式/淘汰题库权限。
	if q.OwnerID != "" && q.OwnerID == userID {
		ok, err := s.authSvc.HasPermission(r.Context(), userID, domain.PermQuestionView)
		return err == nil && ok
	}
	if share := s.questionShare(r, q.ID); share != nil {
		globalPerm, tierPerm := s.globalTierPermissions(share.Status)
		if globalPerm == "" || !s.hasPermission(r, globalPerm) || !s.hasPermission(r, tierPerm) {
			return false
		}
		return s.permissionInQuestionBank(r, q, tierPerm)
	}
	// 老数据没有 owner_id/share 记录时保留旧权限语义，避免升级后历史题目
	// 的详情、审核记录和统计入口突然失效；新生成题目不会走此分支。
	return s.permissionInQuestionBank(r, q, domain.TierViewPerm(q.Tier()))
}

// loadViewableQuestion 用题目所属生命周期分层自动选择正确的查看权限。
func (s *Server) loadViewableQuestion(r *http.Request, id string) (*domain.A2Question, int, error) {
	q, err := s.questionStore.GetQuestion(r.Context(), id)
	if err != nil {
		return nil, 500, err
	}
	if q == nil {
		return nil, 404, fmt.Errorf("题目不存在")
	}
	if !s.canViewQuestion(r, q) {
		return nil, 403, fmt.Errorf("无权访问%s题目（超出权限或分类子题库范围）", q.Tier().Name())
	}
	return q, 0, nil
}

// bankScopeEval 一次性取得的题库范围判定器。
// 与 questionInScope 判定逻辑一致，但把 GetBankScope 查询提出逐题循环，
// 批量场景（统计、列表过滤）用它避免每道题重复查库（N+1）。
type bankScopeEval struct {
	hasPerm   bool
	fullScope bool
	bankSet   map[string]bool
}

// bankScopeEvalFor 获取用户对指定权限点的范围判定器（一次查库）。
func (s *Server) bankScopeEvalFor(ctx context.Context, userID, perm string) bankScopeEval {
	scope, fullScope, hasPerm, err := s.authSvc.GetBankScope(ctx, userID, perm)
	if err != nil || !hasPerm {
		return bankScopeEval{}
	}
	e := bankScopeEval{hasPerm: true, fullScope: fullScope}
	if !fullScope {
		e.bankSet = make(map[string]bool, len(scope))
		for _, b := range scope {
			e.bankSet[b] = true
		}
	}
	return e
}

// allows 校验题目是否在判定器代表的权限与题库范围内。
func (e bankScopeEval) allows(q *domain.A2Question) bool {
	if !e.hasPerm {
		return false
	}
	if e.fullScope {
		return true
	}
	for _, b := range q.BankIDs {
		if e.bankSet[b] {
			return true
		}
	}
	return false
}

// tierFilter 把题库分层与若干范围判定器合成存储端过滤条件（批量统计/列表用）。
// 任一判定器权限缺失时返回 nil（无可 visible 题目）；多个受限判定器取题库交集。
func tierFilter(tier domain.QuestionTier, evals ...bankScopeEval) *storage.QuestionFilter {
	for _, e := range evals {
		if !e.hasPerm {
			return nil
		}
	}
	f := &storage.QuestionFilter{Tiers: []string{string(tier)}}
	var intersect map[string]bool
	for _, e := range evals {
		if e.fullScope {
			continue
		}
		if intersect == nil {
			intersect = make(map[string]bool, len(e.bankSet))
			for b := range e.bankSet {
				intersect[b] = true
			}
		} else {
			for b := range intersect {
				if !e.bankSet[b] {
					delete(intersect, b)
				}
			}
		}
	}
	if intersect != nil {
		f.ScopeRestricted = true
		f.BankScope = make([]string, 0, len(intersect))
		for b := range intersect {
			f.BankScope = append(f.BankScope, b)
		}
	}
	return f
}

// questionInScope 校验当前登录用户对题目是否拥有指定权限的题库范围访问权。
// 全范围用户（bank_ids 为空）直接放行；
// 受限范围用户仅能访问属于其可见题库之一的题目（多对多任一命中即可）。
// 未分类题目（不属于任何题库）对受限范围用户不可见。
func (s *Server) questionInScope(r *http.Request, q *domain.A2Question, perm string) bool {
	userID := auth.GetUserID(r.Context())
	if userID == "" || q == nil {
		return false
	}
	if q != nil && q.OwnerID != "" && q.OwnerID == userID {
		ok, err := s.authSvc.HasPermission(r.Context(), userID, perm)
		return err == nil && ok
	}
	if share := s.questionShare(r, q.ID); share != nil {
		if !s.hasPermission(r, domain.PermQuestionViewGlobal) || !s.hasPermission(r, perm) {
			return false
		}
	}
	return s.permissionInQuestionBank(r, q, perm)
}

func (s *Server) permissionInQuestionBank(r *http.Request, q *domain.A2Question, perm string) bool {
	if q == nil {
		return false
	}
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

func (s *Server) hasPermission(r *http.Request, permission string) bool {
	ok, err := s.authSvc.HasPermission(r.Context(), auth.GetUserID(r.Context()), permission)
	return err == nil && ok
}

func (s *Server) questionShare(r *http.Request, questionID string) *domain.QuestionShareRequest {
	if s.shareStore == nil || questionID == "" {
		return nil
	}
	request, err := s.shareStore.GetQuestionShareByQuestionID(r.Context(), questionID)
	if err != nil {
		return nil
	}
	return request
}

// globalTierPermissions 返回全局访问开关和该全局分层对应的原有分层权限。
func (s *Server) globalTierPermissions(status domain.QuestionShareStatus) (global, tier string) {
	global = domain.PermQuestionViewGlobal
	switch status {
	case domain.QuestionSharePending:
		return global, domain.PermQuestionView
	case domain.QuestionShareApproved:
		return global, domain.PermQuestionViewFormal
	case domain.QuestionShareRejected:
		return global, domain.PermQuestionViewEliminated
	default:
		return "", ""
	}
}

func globalShareStatusForTier(tier domain.QuestionTier) domain.QuestionShareStatus {
	switch tier {
	case domain.TierWorking:
		return domain.QuestionSharePending
	case domain.TierFormal:
		return domain.QuestionShareApproved
	case domain.TierEliminated:
		return domain.QuestionShareRejected
	default:
		return ""
	}
}

// applyQuestionScope 将题目查询限制为个人题库或全局题库。管理员旧客户端
// 未传 scope 时保留历史全量语义；新版页面始终显式传 scope，避免误看全局库。
func (s *Server) applyQuestionScope(w http.ResponseWriter, r *http.Request, filter storage.QuestionFilter) (storage.QuestionFilter, bool) {
	requested := strings.TrimSpace(r.URL.Query().Get("scope"))
	legacyOwnerFallback := false
	if requested == "" {
		if s.hasPermission(r, domain.PermQuestionViewGlobal) {
			return filter, true
		}
		requested = "personal"
		legacyOwnerFallback = true
	}
	userID := auth.GetUserID(r.Context())
	switch requested {
	case "personal":
		canViewPersonal := s.hasPermission(r, domain.PermQuestionView)
		if legacyOwnerFallback {
			canViewPersonal = canViewPersonal || s.hasPermission(r, domain.PermQuestionViewFormal) || s.hasPermission(r, domain.PermQuestionViewEliminated)
		}
		if !canViewPersonal {
			writeError(w, http.StatusForbidden, "无权查看个人题库")
			return filter, false
		}
		filter.OwnerID = userID
		filter.IncludeLegacyOwner = legacyOwnerFallback
		return filter, true
	case "global":
		if s.shareStore == nil {
			writeError(w, http.StatusServiceUnavailable, "全局题库功能不可用")
			return filter, false
		}
		if !s.hasPermission(r, domain.PermQuestionViewGlobal) {
			writeError(w, http.StatusForbidden, "无权查看全局题库")
			return filter, false
		}
		filter.IncludeLegacyGlobal = true
		return filter, true
	default:
		writeError(w, http.StatusBadRequest, "无效的题库范围: "+requested)
		return filter, false
	}
}

// loadScopedQuestion 加载题目并校验题库范围与题库分层。
// 返回 (题目, 0, nil) 成功；(nil, http状态码, 错误信息) 失败。
// 越权访问返回 403，与列表接口的题库范围过滤保持一致。
// 正式/淘汰题库题目额外要求对应分层查看权限（正式题库密封：操作它先要能看它）。
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
	if !s.canViewQuestion(r, q) {
		return nil, 403, fmt.Errorf("无权访问%s题目", q.Tier().Name())
	}
	return q, 0, nil
}

// reviewTaskAccess 返回用户是否可通过任务关系访问题目，以及是否可看完整投票/评语。
// 普通审题人只访问当前分配给自己的任务；被指定（或名单为空）的把关人可访问待决断任务；
// 系统管理员拥有全部任务访问权。任务访问不授予题库列表浏览权。
func (s *Server) reviewTaskAccess(r *http.Request, task *domain.ReviewTask) (allowed, full bool) {
	if task == nil {
		return false, false
	}
	userID := auth.GetUserID(r.Context())
	if userID == "" {
		return false, false
	}
	if admin, err := s.authSvc.HasPermission(r.Context(), userID, domain.PermUserManage); err == nil && admin {
		return true, true
	}
	for _, assigned := range task.AssignedTo {
		if assigned == userID {
			return true, false
		}
	}
	hasFinal, err := s.authSvc.HasFinalRight(r.Context(), userID)
	if err != nil || !hasFinal || task.Status != domain.StatusConflict {
		return false, false
	}
	if len(task.FinalReviewerIDs) == 0 {
		return true, true
	}
	for _, reviewer := range task.FinalReviewerIDs {
		if reviewer == userID {
			return true, true
		}
	}
	return false, false
}

func (s *Server) canAccessQuestionViaReviewTask(r *http.Request, questionID string) bool {
	task, err := s.reviewSvc.GetTaskByQuestionID(r.Context(), questionID)
	if err != nil || task == nil {
		return false
	}
	allowed, _ := s.reviewTaskAccess(r, task)
	return allowed
}

func (s *Server) ensureQuestionViewOrReviewTask(r *http.Request, questionID string) (int, error) {
	if _, status, err := s.loadViewableQuestion(r, questionID); err == nil {
		return 0, nil
	} else if s.canAccessQuestionViaReviewTask(r, questionID) {
		return 0, nil
	} else {
		return status, err
	}
}

// bankCatalogScope 决定普通下拉框/筛选器可以暴露哪些分类子题库。
// 管理类权限需要完整目录；其他用户只看到其用户级 bank_ids 范围，且必须至少拥有
// 一个会使用分类子题库的业务权限。返回 restricted=false 表示全部可见。
func (s *Server) bankCatalogScope(r *http.Request) (scope map[string]bool, restricted bool) {
	userID := auth.GetUserID(r.Context())
	user, err := s.authSvc.GetUserByID(userID)
	if err != nil || user == nil {
		return map[string]bool{}, true
	}
	has := func(permission string) bool {
		for _, candidate := range user.Permissions {
			if candidate == permission {
				return true
			}
		}
		return false
	}
	for _, permission := range []string{
		domain.PermUserManage, domain.PermRoleManage, domain.PermBankManage, domain.PermFlowManage,
	} {
		if has(permission) {
			return nil, false
		}
	}
	relevant := false
	for _, permission := range []string{
		domain.PermQuestionView, domain.PermQuestionViewFormal, domain.PermQuestionViewEliminated,
		domain.PermQuestionGenerate, domain.PermQuestionEdit, domain.PermQuestionDelete,
		domain.PermQuestionDeleteFormal, domain.PermQuestionDownload, domain.PermStatsView,
	} {
		if has(permission) {
			relevant = true
			break
		}
	}
	if !relevant {
		return map[string]bool{}, true
	}
	if len(user.BankIDs) == 0 {
		return nil, false
	}
	scope = make(map[string]bool, len(user.BankIDs))
	for _, bankID := range user.BankIDs {
		scope[bankID] = true
	}
	return scope, true
}
