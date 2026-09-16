package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/review"
	"aigo/internal/storage"
)

// reviewTaskDetail 审核任务的 API 展示结构：任务字段平铺 + 附加该题最新 AI 检查结果，
// 供专家审核时参考（verdict/分数/issues）。ai_review 为空表示暂无检查结果。
type reviewTaskDetail struct {
	domain.ReviewTask
	AIReview *domain.AIReviewResult `json:"ai_review,omitempty"`
}

// myTaskItemDetail 待审任务列表项的展示结构，附加 AI 检查结果。
type myTaskItemDetail struct {
	review.MyTaskItem
	AIReview *domain.AIReviewResult `json:"ai_review,omitempty"`
}

var errReviewSubmitForbidden = errors.New("只能提交本人生成的题目")

// aiReviewsForQuestions 批量查询多题最新 AI 检查结果；服务不可用时返回空 map。
func (s *Server) aiReviewsForQuestions(ctx context.Context, questionIDs []string) map[string]domain.AIReviewResult {
	if s.aiCheckSvc == nil || len(questionIDs) == 0 {
		return nil
	}
	results, err := s.aiCheckSvc.GetResultsByQuestionIDs(ctx, questionIDs)
	if err != nil {
		slog.Warn("加载 AI 检查结果失败", "error", err)
		return nil
	}
	return results
}

// attachAIReviews 为待审任务列表附加 AI 检查结果。
func (s *Server) attachAIReviews(ctx context.Context, tasks []review.MyTaskItem) []myTaskItemDetail {
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t.Task != nil {
			ids = append(ids, t.Task.QuestionID)
		}
	}
	results := s.aiReviewsForQuestions(ctx, ids)
	details := make([]myTaskItemDetail, len(tasks))
	for i, t := range tasks {
		detail := myTaskItemDetail{MyTaskItem: t}
		if t.Task != nil {
			if r, ok := results[t.Task.QuestionID]; ok {
				result := r
				detail.AIReview = &result
			}
		}
		details[i] = detail
	}
	return details
}

// handleSubmitReview 将题目提交到指定的审核流程。
// 请求：{"question_id": "xxx", "flow_id": "flow-a2-2round"}
// 提交后题目状态变为 reviewing，生成审核任务。
func (s *Server) handleSubmitReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"` // 题目 ID
		FlowID     string `json:"flow_id"`     // 审核流程 ID
		BankID     string `json:"bank_id"`     // 本次提交的分类子题库（多归属题必填）
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	task, err := s.submitReviewForActor(r, req.QuestionID, req.FlowID, req.BankID)
	if err != nil {
		if errors.Is(err, auth.ErrPermissionDenied) || errors.Is(err, errReviewSubmitForbidden) {
			writeError(w, http.StatusForbidden, "提交审核失败: "+err.Error())
			return
		}
		if errors.Is(err, domain.ErrInvalidQuestion) {
			writeError(w, 400, "提交审核失败: "+err.Error())
			return
		}
		if errors.Is(err, domain.ErrQuestionVersionConflict) || errors.Is(err, domain.ErrReviewNotSubmittable) {
			writeError(w, http.StatusConflict, "提交审核失败: "+err.Error())
			return
		}
		if errors.Is(err, domain.ErrReviewBadRequest) {
			writeError(w, http.StatusBadRequest, "提交审核失败: "+err.Error())
			return
		}
		writeError(w, 500, "提交审核失败: "+err.Error())
		return
	}

	// 记录提交/重提日志：通过历史审核记录判断是否为重提
	records, _ := s.reviewSvc.ListRecords(r.Context(), task.ID)
	isResubmit := len(records) > 0
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if err := s.auditSvc.LogSubmit(r.Context(), req.QuestionID, req.FlowID, actor, isResubmit); err != nil {
		slog.Warn("写提交日志失败", "question_id", req.QuestionID, "error", err)
	}

	writeJSON(w, 200, task)
}

// submitReviewForActor 优先处理当前账号自己的新题；即使账号同时拥有管理权限，
// 从新题工作区提交时也不再要求历史分类子题库。显式 bank_id 仅保留旧接口兼容。
func (s *Server) submitReviewForActor(r *http.Request, questionID, flowID, bankID string) (*domain.ReviewTask, error) {
	userID := auth.GetUserID(r.Context())
	canSubmitOwn := s.hasPermission(r, domain.PermReviewSubmit)
	canManage := s.hasPermission(r, domain.PermUserManage)
	if !canSubmitOwn && !canManage {
		return nil, auth.ErrPermissionDenied
	}
	question, err := s.questionStore.GetQuestion(r.Context(), questionID)
	if err != nil {
		return nil, err
	}
	if question == nil {
		return nil, fmt.Errorf("题目不存在")
	}
	if strings.TrimSpace(bankID) == "" && question.OwnerID == userID && canSubmitOwn {
		return s.reviewSvc.SubmitQuestionForOwner(r.Context(), questionID, flowID)
	}
	if canManage {
		return s.reviewSvc.SubmitQuestionForBank(r.Context(), questionID, flowID, bankID)
	}
	if question.OwnerID != userID {
		return nil, errReviewSubmitForbidden
	}
	if strings.TrimSpace(bankID) != "" {
		return nil, fmt.Errorf("命题教师提交审核时不需要选择分类子题库")
	}
	return s.reviewSvc.SubmitQuestionForOwner(r.Context(), questionID, flowID)
}

// handleSubmitReviewBatch 逐题提交本人题目，单题失败不会回滚已经成功的题目。
// 请求：{"question_ids":["q-1","q-2"],"flow_id":"flow-a2"}
func (s *Server) handleSubmitReviewBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionIDs []string `json:"question_ids"`
		FlowID      string   `json:"flow_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	if strings.TrimSpace(req.FlowID) == "" {
		writeError(w, http.StatusBadRequest, "请指定审核流程")
		return
	}
	if len(req.QuestionIDs) == 0 || len(req.QuestionIDs) > 500 {
		writeError(w, http.StatusBadRequest, "一次最多选择 500 道题目")
		return
	}
	type failedItem struct {
		QuestionID string `json:"question_id"`
		Error      string `json:"error"`
	}
	seen := make(map[string]bool, len(req.QuestionIDs))
	failed := make([]failedItem, 0)
	submitted := 0
	for _, questionID := range req.QuestionIDs {
		questionID = strings.TrimSpace(questionID)
		if questionID == "" || seen[questionID] {
			continue
		}
		seen[questionID] = true
		if _, err := s.submitReviewForActor(r, questionID, req.FlowID, ""); err != nil {
			failed = append(failed, failedItem{QuestionID: questionID, Error: err.Error()})
			continue
		}
		submitted++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"submitted": submitted,
		"failed":    failed,
		"total":     submitted + len(failed),
	})
}

// handleAvailableReviewFlows 只向命题教师暴露可选择的流程摘要，不泄露每轮
// 审核人 ID；完整流程配置仍只由管理员查看和维护。
func (s *Server) handleAvailableReviewFlows(w http.ResponseWriter, r *http.Request) {
	flows, err := s.reviewSvc.ListFlows(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type flowSummary struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Subject     string `json:"subject"`
		RoundCount  int    `json:"round_count"`
	}
	result := make([]flowSummary, 0, len(flows))
	for _, flow := range flows {
		// 历史绑定分类子题库的流程只供旧任务追溯，不能再用于新题提交。
		if strings.TrimSpace(flow.BankID) != "" {
			continue
		}
		result = append(result, flowSummary{
			ID: flow.ID, Name: flow.Name, Description: flow.Description,
			Subject: flow.Subject, RoundCount: len(flow.Rounds),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"flows": result, "total": len(result)})
}

// handleReviewAction 执行审核投票（通过/驳回/需修改）。
// 达到通过票数立即进入下一轮；未达门槛时收齐本轮意见后，需修改优先退修，否则驳回终止。
// 拥有最终把关权限的用户可以审核任意轮次。
// 评语规则：通过时评语可选；驳回/需修改必须填写结构化评语（题干/选项/答案与解析/其他至少一栏）。
// 请求：{"task_id": "xxx", "action": "rejected", "comment": {"stem": "...", "options": "...", "answer": "...", "other": "..."}}
// 兼容旧客户端：仅传 opinion 时视为「其他」栏评语。
func (s *Server) handleReviewAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID   string                `json:"task_id"`   // 审核任务 ID
		ExpertID string                `json:"expert_id"` // 审核人 ID（有最终把关权限可指定，其他用户忽略此字段）
		Action   string                `json:"action"`    // approved / rejected / revision_required
		Opinion  string                `json:"opinion"`   // 自由文本意见（兼容旧客户端）
		Comment  *domain.ReviewComment `json:"comment"`   // 结构化评语
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	currentUserID := auth.GetUserID(r.Context())

	// 普通把关人仍须在“待我审核”的分配名单内；仅系统管理员可代为处理任意审核任务。
	hasFinalRight, err := s.authSvc.HasPermission(r.Context(), currentUserID, domain.PermUserManage)
	if err != nil {
		writeError(w, 500, "权限查询失败")
		return
	}

	// 普通用户强制使用自己的 ID，防止冒名；仅系统管理员可指定代办审核人
	expertID := currentUserID
	if hasFinalRight && req.ExpertID != "" {
		expertID = req.ExpertID
	}

	err = s.reviewSvc.Review(r.Context(), review.ReviewRequest{
		TaskID:        req.TaskID,
		ExpertID:      expertID,
		Action:        domain.QuestionStatus(req.Action),
		Opinion:       req.Opinion,
		Comment:       req.Comment,
		HasFinalRight: hasFinalRight,
	})
	if err != nil {
		if errors.Is(err, domain.ErrQuestionVersionConflict) {
			writeError(w, http.StatusConflict, "审核失败: "+err.Error())
			return
		}
		writeError(w, 400, "审核失败: "+err.Error())
		return
	}

	// 记录审核日志（从任务中获取题目ID）
	task, _ := s.reviewSvc.GetTask(r.Context(), req.TaskID)
	if task != nil {
		opinion := req.Opinion
		if req.Comment != nil && !req.Comment.IsEmpty() {
			opinion = req.Comment.Flatten()
		}
		s.auditSvc.LogReview(r.Context(), task.QuestionID, expertID, req.Action, opinion)
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleReviewFinalize 最终把关人对所有轮次已通过的任务做轮外决断。
// 请求：{"task_id": "xxx", "action": "approved", "opinion": "...", "comment": {"stem": "...", ...}}
// action：approved（所有轮次完成后正式通过）/ rejected（驳回）/ revision_required（退回修改）
// 非通过决断必须填写至少一栏结构化评语；通过时评语可选。
func (s *Server) handleReviewFinalize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID  string                `json:"task_id"`
		Action  string                `json:"action"`
		Opinion string                `json:"opinion"` // 兼容旧客户端
		Comment *domain.ReviewComment `json:"comment"` // 结构化评语
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	currentUserID := auth.GetUserID(r.Context())
	// 系统管理员（用户管理权限）不受把关人名单限制
	isSysAdmin, _ := s.authSvc.HasPermission(r.Context(), currentUserID, domain.PermUserManage)
	if err := s.reviewSvc.Finalize(r.Context(), review.FinalizeRequest{
		TaskID:        req.TaskID,
		ReviewerID:    currentUserID,
		Action:        domain.QuestionStatus(req.Action),
		Opinion:       req.Opinion,
		Comment:       req.Comment,
		IsSystemAdmin: isSysAdmin,
	}); err != nil {
		if errors.Is(err, domain.ErrQuestionVersionConflict) {
			writeError(w, http.StatusConflict, "决断失败: "+err.Error())
			return
		}
		writeError(w, 400, "决断失败: "+err.Error())
		return
	}

	task, _ := s.reviewSvc.GetTask(r.Context(), req.TaskID)
	if task != nil {
		opinion := req.Opinion
		if req.Comment != nil && !req.Comment.IsEmpty() {
			opinion = req.Comment.Flatten()
		}
		s.auditSvc.LogReview(r.Context(), task.QuestionID, currentUserID, "final_"+req.Action, opinion)
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleMyTasks 列出待我审核的任务（含题目完整信息、流程轮次、投票进度）。
// 仅当前轮分配给我的任务；投票完成即移开；待决断任务在「待决断」页面处理。
func (s *Server) handleMyTasks(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	hasReview, err := s.authSvc.HasPermission(r.Context(), userID, domain.PermReviewDo)
	if err != nil {
		writeError(w, 500, "权限查询失败")
		return
	}
	if !hasReview {
		writeError(w, 403, "权限不足")
		return
	}
	isAdmin, _ := s.authSvc.HasPermission(r.Context(), userID, domain.PermUserManage)
	tasks, err := s.reviewSvc.MyTasks(r.Context(), userID, isAdmin)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	page, pageSize := reviewTodoPageParams(r)
	pageTasks, total, hasMore := paginateReviewTodo(tasks, page, pageSize)
	writeJSON(w, 200, map[string]any{
		"tasks": s.attachAIReviews(r.Context(), pageTasks), "total": total,
		"page": page, "page_size": pageSize, "has_more": hasMore,
	})
}

// handleMyDecisions 列出待我决断的任务（最终把关人专用，供「待决断」页面）。
func (s *Server) handleMyDecisions(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r.Context())
	hasFinalRight, err := s.authSvc.HasFinalRight(r.Context(), userID)
	if err != nil {
		writeError(w, 500, "权限查询失败")
		return
	}
	if !hasFinalRight {
		writeError(w, 403, "权限不足")
		return
	}
	isAdmin, _ := s.authSvc.HasPermission(r.Context(), userID, domain.PermUserManage)
	tasks, err := s.reviewSvc.MyDecisionsForViewer(r.Context(), userID, isAdmin)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	page, pageSize := reviewTodoPageParams(r)
	pageTasks, total, hasMore := paginateReviewTodo(tasks, page, pageSize)
	writeJSON(w, 200, map[string]any{
		"tasks": s.attachAIReviews(r.Context(), pageTasks), "total": total,
		"page": page, "page_size": pageSize, "has_more": hasMore,
	})
}

func reviewTodoPageParams(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return page, pageSize
}

func paginateReviewTodo(items []review.MyTaskItem, page, pageSize int) ([]review.MyTaskItem, int, bool) {
	total := len(items)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return items[start:end], total, end < total
}

// handleMyRevisions 列出退回给当前用户修改的题目（待我修改页数据源）。
// 生成者身份以题目 created_by（首次生成的用户）为准；修改完成后由管理员重新送审。
func (s *Server) handleMyRevisions(w http.ResponseWriter, r *http.Request) {
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	items, err := s.reviewSvc.MyRevisions(r.Context(), actor)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func isTechnicalUserID(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "user-")
}

// enrichReviewDisplayNames 兼容历史记录中因最终把关人没有 review:do
// 权限而保存的 user-... 技术 ID；只对调用者已经有权看到的记录补齐名称。
func (s *Server) enrichReviewDisplayNames(ctx context.Context, records []domain.ReviewRecord) {
	for i := range records {
		if strings.TrimSpace(records[i].ExpertName) != "" && !isTechnicalUserID(records[i].ExpertName) {
			continue
		}
		user, err := s.authSvc.GetUserByID(records[i].ExpertID)
		if err != nil || user == nil {
			continue
		}
		if strings.TrimSpace(user.DisplayName) != "" {
			records[i].ExpertName = strings.TrimSpace(user.DisplayName)
		} else {
			records[i].ExpertName = user.Username
		}
	}
}

func (s *Server) enrichReviewTaskDisplayName(ctx context.Context, task *domain.ReviewTask) {
	if task == nil || task.FinalDecision == nil {
		return
	}
	if strings.TrimSpace(task.FinalDecision.ExpertName) != "" && !isTechnicalUserID(task.FinalDecision.ExpertName) {
		return
	}
	user, err := s.authSvc.GetUserByID(task.FinalDecision.ExpertID)
	if err != nil || user == nil {
		return
	}
	if strings.TrimSpace(user.DisplayName) != "" {
		task.FinalDecision.ExpertName = strings.TrimSpace(user.DisplayName)
	} else {
		task.FinalDecision.ExpertName = user.Username
	}
}

// reviewFullAccess 仅表示跨任务的全量评语权限。
// 把关人只在“待我决断”的已分配任务中看完整评语；系统管理员才拥有跨任务全量权限。
func (s *Server) reviewFullAccess(r *http.Request) bool {
	userID := auth.GetUserID(r.Context())
	if hasManage, err := s.authSvc.HasPermission(r.Context(), userID, domain.PermUserManage); err == nil && hasManage {
		return true
	}
	return false
}

// handleReviewResults 审核结果汇总：统计 + 题目列表 + 专家评语。
// 进行中的任务按分层隔离规则裁剪：非把关人/管理员只能看到自己的评语与脱敏摘要。
// 查询参数：final_status（pending/reviewing/conflict/rejected/revision_required/published）、
// bank_id、q（关键词）、page、page_size。
func (s *Server) handleReviewResults(w http.ResponseWriter, r *http.Request) {
	finalStatus := r.URL.Query().Get("final_status")
	bankID := r.URL.Query().Get("bank_id")
	filter := storage.QuestionFilter{Keyword: r.URL.Query().Get("q")}
	if bankID == "__unclassified__" {
		filter.Unclassified = true
	} else {
		filter.BankID = bankID
	}
	requestedScope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if requestedScope != "" && requestedScope != "personal" && requestedScope != "global" {
		writeError(w, http.StatusBadRequest, "无效的审核记录范围: "+requestedScope)
		return
	}
	if requestedScope == "global" && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看全局审核记录")
		return
	}
	globalScope := requestedScope == "global" || (requestedScope == "" && s.hasPermission(r, domain.PermQuestionViewGlobal))
	// 客户侧审核记录只汇总本人个人题库；管理员仍可查看全局管理范围。
	if globalScope {
		filter.Tiers = nil
		filter.IncludeLegacyGlobal = true
		for _, tier := range []domain.QuestionTier{domain.TierFormal, domain.TierWorking, domain.TierEliminated} {
			_, tierPerm := s.globalTierPermissions(globalShareStatusForTier(tier))
			if s.hasPermission(r, tierPerm) {
				filter.GlobalStatuses = append(filter.GlobalStatuses, string(globalShareStatusForTier(tier)))
			}
		}
	} else if requestedScope == "personal" || (requestedScope == "" && !s.hasPermission(r, domain.PermQuestionViewGlobal)) {
		filter.OwnerID = auth.GetUserID(r.Context())
		filter.IncludeLegacyOwner = requestedScope == ""
	}
	visible := s.visibleTiers(r)
	// 显式个人范围与个人题库页面保持一致：question:view 可读本人三层题库。
	// 旧客户端不传 scope 时仍保留原有分层与历史题权限边界。
	if requestedScope == "personal" {
		if !s.hasPermission(r, domain.PermQuestionView) {
			writeError(w, http.StatusForbidden, "无权查看个人题库审核记录")
			return
		}
		visible = []domain.QuestionTier{domain.TierFormal, domain.TierWorking, domain.TierEliminated}
	}
	if len(visible) == 0 {
		writeJSON(w, 200, map[string]any{
			"items": []review.ReviewResultItem{}, "total": 0, "page": 1, "page_size": 50,
			"has_more": false, "stats": review.ReviewStats{},
		})
		return
	}
	if !globalScope {
		for _, tier := range visible {
			filter.Tiers = append(filter.Tiers, string(tier))
		}
	}
	filter.BankScope, filter.ScopeRestricted = s.questionBankScope(r, domain.TierViewPerm(visible[0]))
	if requestedScope == "personal" {
		filter.BankScope, filter.ScopeRestricted = nil, false
	}
	statsFilter := storage.QuestionFilter{
		Tiers: append([]string(nil), filter.Tiers...), GlobalStatuses: append([]string(nil), filter.GlobalStatuses...),
		IncludeLegacyGlobal: filter.IncludeLegacyGlobal, BankScope: append([]string(nil), filter.BankScope...),
		ScopeRestricted: filter.ScopeRestricted, OwnerID: filter.OwnerID, IncludeLegacyOwner: filter.IncludeLegacyOwner,
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	pageItems, total, stats, err := s.reviewSvc.SearchResultsForViewer(r.Context(), storage.ReviewResultQuery{
		Filter: filter, StatsFilter: statsFilter, FinalStatus: finalStatus, Page: page, PageSize: pageSize,
	}, auth.GetUserID(r.Context()), s.reviewFullAccess(r))
	if err != nil {
		if strings.Contains(err.Error(), "无效的审核结果状态") {
			writeError(w, 400, err.Error())
			return
		}
		writeError(w, 500, err.Error())
		return
	}

	// 评语只为当页条目加载，避免审核记录全表进入内存
	if err := s.reviewSvc.AttachRecordsForItems(r.Context(), pageItems, auth.GetUserID(r.Context()), s.reviewFullAccess(r)); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for i := range pageItems {
		s.enrichReviewTaskDisplayName(r.Context(), pageItems[i].Task)
		s.enrichReviewDisplayNames(r.Context(), pageItems[i].Records)
	}

	writeJSON(w, 200, map[string]any{
		"items":     pageItems,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  page*pageSize < total,
		"stats":     stats,
	})
}

// handleListReviewers 列出有审题权限的用户（流程配置选审核人、名字映射用）。
func (s *Server) handleListReviewers(w http.ResponseWriter, r *http.Request) {
	candidates, err := s.authSvc.ListReviewCandidates(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	userID := auth.GetUserID(r.Context())
	canConfigure, _ := s.authSvc.HasPermission(r.Context(), userID, domain.PermFlowManage)
	canManageUsers, _ := s.authSvc.HasPermission(r.Context(), userID, domain.PermUserManage)
	if !canConfigure && !canManageUsers {
		for i := range candidates {
			candidates[i].BankIDs = nil
		}
	}
	writeJSON(w, 200, map[string]any{"reviewers": candidates, "total": len(candidates)})
}

// handleGetReviewTask 根据任务 ID 获取审核任务详情。
// 普通审核人拿到裁剪后的任务（无评语明细、无实时票数）；把关人/管理员全量可见。
// 校验任务对应题目的题库范围（question:view）。
func (s *Server) handleGetReviewTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rawTask, err := s.reviewSvc.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if rawTask == nil {
		writeError(w, 404, "审核任务不存在")
		return
	}
	allowedByTask, fullByTask := s.reviewTaskAccess(r, rawTask)
	if !allowedByTask {
		if _, status, viewErr := s.loadViewableQuestion(r, rawTask.QuestionID); viewErr != nil {
			writeError(w, status, viewErr.Error())
			return
		}
	}
	task, err := s.reviewSvc.TaskForViewer(r.Context(), id, auth.GetUserID(r.Context()), fullByTask || s.reviewFullAccess(r))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.enrichReviewTaskDisplayName(r.Context(), task)
	detail := reviewTaskDetail{ReviewTask: *task}
	if s.aiCheckSvc != nil {
		if result, err := s.aiCheckSvc.GetResult(r.Context(), task.QuestionID); err == nil && result != nil {
			detail.AIReview = result
		}
	}
	writeJSON(w, 200, detail)
}

// handleGetTaskByQuestion 根据题目 ID 获取关联的审核任务。
// 用于前端选中题目后自动加载审核状态；可见性规则同 handleGetReviewTask。校验题库范围（question:view）。
func (s *Server) handleGetTaskByQuestion(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	rawTask, err := s.reviewSvc.GetTaskByQuestionID(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if rawTask == nil {
		if _, status, viewErr := s.loadViewableQuestion(r, questionID); viewErr != nil {
			writeError(w, status, viewErr.Error())
			return
		}
		writeJSON(w, 200, nil) // 没有审核任务返回 null
		return
	}
	allowedByTask, fullByTask := s.reviewTaskAccess(r, rawTask)
	if !allowedByTask {
		if _, status, viewErr := s.loadViewableQuestion(r, questionID); viewErr != nil {
			writeError(w, status, viewErr.Error())
			return
		}
	}
	task, err := s.reviewSvc.TaskByQuestionForViewer(r.Context(), questionID, auth.GetUserID(r.Context()), fullByTask || s.reviewFullAccess(r))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.enrichReviewTaskDisplayName(r.Context(), task)
	detail := reviewTaskDetail{ReviewTask: *task}
	if s.aiCheckSvc != nil {
		if result, err := s.aiCheckSvc.GetResult(r.Context(), questionID); err == nil && result != nil {
			detail.AIReview = result
		}
	}
	writeJSON(w, 200, detail)
}

// handleListFlows 列出所有审核流程配置。
func (s *Server) handleListFlows(w http.ResponseWriter, r *http.Request) {
	flows, err := s.reviewSvc.ListFlows(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"flows": flows, "total": len(flows)})
}

// handleCreateFlow 创建审核流程。
func (s *Server) handleCreateFlow(w http.ResponseWriter, r *http.Request) {
	var flow domain.ReviewFlowConfig
	if err := readJSON(r, &flow); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	flow.BankID = ""
	actor := auth.GetUsername(r.Context())
	ok := s.withAuditedTx(w, r, "创建审核流程",
		func(txCtx context.Context) error {
			if err := s.reviewSvc.CreateFlow(txCtx, flow); err != nil {
				writeError(w, 400, err.Error())
				return errResponded
			}
			return nil
		},
		func(txCtx context.Context) error {
			return s.auditSvc.LogFlow(txCtx, flow.ID, actor, "create", fmt.Sprintf("创建流程「%s」（%d 轮）", flow.Name, len(flow.Rounds)))
		})
	if !ok {
		return
	}
	writeJSON(w, 201, flow)
}

// handleUpdateFlow 更新审核流程（有进行中任务时禁止修改）。
func (s *Server) handleUpdateFlow(w http.ResponseWriter, r *http.Request) {
	var flow domain.ReviewFlowConfig
	if err := readJSON(r, &flow); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	flow.ID = r.PathValue("id")
	flow.BankID = ""
	actor := auth.GetUsername(r.Context())
	ok := s.withAuditedTx(w, r, "更新审核流程",
		func(txCtx context.Context) error {
			if err := s.reviewSvc.UpdateFlow(txCtx, flow); err != nil {
				writeError(w, 400, err.Error())
				return errResponded
			}
			return nil
		},
		func(txCtx context.Context) error {
			return s.auditSvc.LogFlow(txCtx, flow.ID, actor, "update", fmt.Sprintf("修改流程「%s」（%d 轮）", flow.Name, len(flow.Rounds)))
		})
	if !ok {
		return
	}
	writeJSON(w, 200, flow)
}

// handleDeleteFlow 删除审核流程。
func (s *Server) handleDeleteFlow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actor := auth.GetUsername(r.Context())
	ok := s.withAuditedTx(w, r, "删除审核流程",
		func(txCtx context.Context) error {
			if err := s.reviewSvc.DeleteFlow(txCtx, id); err != nil {
				writeError(w, 500, err.Error())
				return errResponded
			}
			return nil
		},
		func(txCtx context.Context) error {
			return s.auditSvc.LogFlow(txCtx, id, actor, "delete", "删除流程")
		})
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok", "id": id})
}

// handleReviewRecords 获取某审核任务的所有审核记录（结构化评语）。
// 分层隔离：把关人/管理员全量可见；普通审核人仅能看到自己提交的记录。
// 校验任务对应题目的题库范围（question:view）。
func (s *Server) handleReviewRecords(w http.ResponseWriter, r *http.Request) {
	taskId := r.PathValue("taskId")
	task, err := s.reviewSvc.GetTask(r.Context(), taskId)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeError(w, 404, "审核任务不存在")
		return
	}
	allowedByTask, fullByTask := s.reviewTaskAccess(r, task)
	if !allowedByTask {
		if _, status, viewErr := s.loadViewableQuestion(r, task.QuestionID); viewErr != nil {
			writeError(w, status, viewErr.Error())
			return
		}
	}
	records, err := s.reviewSvc.RecordsForViewer(r.Context(), taskId, auth.GetUserID(r.Context()), fullByTask || s.reviewFullAccess(r))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.enrichReviewDisplayNames(r.Context(), records)
	writeJSON(w, 200, map[string]any{"records": records, "total": len(records)})
}
