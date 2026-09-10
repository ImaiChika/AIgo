package api

import (
	"context"
	"errors"
	"fmt"
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

// aiReviewsForQuestions 批量查询多题最新 AI 检查结果；服务不可用时返回空 map。
func (s *Server) aiReviewsForQuestions(ctx context.Context, questionIDs []string) map[string]domain.AIReviewResult {
	if s.aiCheckSvc == nil || len(questionIDs) == 0 {
		return nil
	}
	results, err := s.aiCheckSvc.GetResultsByQuestionIDs(ctx, questionIDs)
	if err != nil {
		fmt.Printf("⚠ 加载 AI 检查结果失败: %v\n", err)
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

	task, err := s.reviewSvc.SubmitQuestionForBank(r.Context(), req.QuestionID, req.FlowID, req.BankID)
	if err != nil {
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
		fmt.Printf("⚠ 写提交日志失败: %v\n", err)
	}

	writeJSON(w, 200, task)
}

// handleReviewAction 执行审核投票（通过/驳回/需修改）。
// 单人反对不再立即退回；所有分配审核人投票完毕后自动统计，冲突进入待决断。
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

// handleReviewFinalize 最终把关人对票数冲突的任务做决断。
// 请求：{"task_id": "xxx", "action": "approved", "opinion": "...", "comment": {"stem": "...", ...}}
// action：approved（通过进下一轮/完成）/ rejected（驳回）/ revision_required（退回修改）
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

// handleSubmitBankReview 按题库统一提交审核：把题库内所有可提交状态的题目
// 批量提交到审核流程（不再需要在审核页一题一题点击提交）。
// 已在审核中/已审核结束的题目自动跳过并在结果中统计（提示题库状态冲突）。
// 请求：{"bank_id": "xxx", "flow_id": "xxx"}（bank_id 为空=提交未分类题目）
func (s *Server) handleSubmitBankReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BankID string `json:"bank_id"`
		FlowID string `json:"flow_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if req.FlowID == "" {
		writeError(w, 400, "请指定审核流程")
		return
	}
	if req.BankID == "" {
		writeError(w, 400, "批量送审必须选择分类子题库")
		return
	}
	bank, err := s.bankSvc.GetBank(r.Context(), req.BankID)
	if err != nil {
		writeError(w, 500, "查询题库失败")
		return
	}
	if bank == nil {
		writeError(w, 400, "分类子题库不存在或已删除")
		return
	}
	result, err := s.reviewSvc.SubmitBank(r.Context(), req.BankID, req.FlowID)
	if err != nil {
		writeError(w, 400, "批量提交失败: "+err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	s.auditSvc.Log(r.Context(), "", "submit_bank", actor, fmt.Sprintf("批量提交题库 %s 到流程 %s：成功 %d，跳过审核中 %d，跳过已结束 %d，失败 %d", bankLabel(req.BankID), req.FlowID, result.Submitted, result.SkippedReviewing, result.SkippedFinished, len(result.Failed)))
	writeJSON(w, 200, result)
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
	writeJSON(w, 200, map[string]any{"tasks": s.attachAIReviews(r.Context(), tasks), "total": len(tasks)})
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
	writeJSON(w, 200, map[string]any{"tasks": s.attachAIReviews(r.Context(), tasks), "total": len(tasks)})
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

func bankLabel(bankID string) string {
	if bankID == "" {
		return "未分类"
	}
	return bankID
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

	writeJSON(w, 200, map[string]any{
		"items":     pageItems,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  page*pageSize < total,
		"stats":     stats,
	})
}

// handleRevokeFlow 撤销流程下所有未完成的审核任务（管理员防误提交/卡死用）。
// 未完成审核的题目恢复提交前状态；已审核结束的题目不受影响。
func (s *Server) handleRevokeFlow(w http.ResponseWriter, r *http.Request) {
	flowID := r.PathValue("id")
	result, err := s.reviewSvc.RevokeFlow(r.Context(), flowID)
	if err != nil {
		writeError(w, 400, "撤销失败: "+err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	s.auditSvc.LogFlow(r.Context(), flowID, actor, "revoke", fmt.Sprintf("撤销未完成审核任务 %d 个，恢复题目 %d 道，保留终态 %d 个", result.Revoked, result.Restored, result.Kept))
	writeJSON(w, 200, result)
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
	if !s.ensureFlowBankExists(w, r, flow.BankID) {
		return
	}
	if err := s.reviewSvc.CreateFlow(r.Context(), flow); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if err := s.auditSvc.LogFlow(r.Context(), flow.ID, actor, "create", fmt.Sprintf("创建流程「%s」（%d 轮）", flow.Name, len(flow.Rounds))); err != nil {
		fmt.Printf("⚠ 写流程日志失败: %v\n", err)
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
	if !s.ensureFlowBankExists(w, r, flow.BankID) {
		return
	}
	if err := s.reviewSvc.UpdateFlow(r.Context(), flow); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if err := s.auditSvc.LogFlow(r.Context(), flow.ID, actor, "update", fmt.Sprintf("修改流程「%s」（%d 轮）", flow.Name, len(flow.Rounds))); err != nil {
		fmt.Printf("⚠ 写流程日志失败: %v\n", err)
	}
	writeJSON(w, 200, flow)
}

func (s *Server) ensureFlowBankExists(w http.ResponseWriter, r *http.Request, bankID string) bool {
	if strings.TrimSpace(bankID) == "" {
		return true
	}
	bank, err := s.bankSvc.GetBank(r.Context(), bankID)
	if err != nil {
		writeError(w, 500, "查询分类子题库失败")
		return false
	}
	if bank == nil {
		writeError(w, 400, "审核流程绑定的分类子题库不存在")
		return false
	}
	return true
}

// handleDeleteFlow 删除审核流程。
func (s *Server) handleDeleteFlow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.reviewSvc.DeleteFlow(r.Context(), id); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if err := s.auditSvc.LogFlow(r.Context(), id, actor, "delete", "删除流程"); err != nil {
		fmt.Printf("⚠ 写流程日志失败: %v\n", err)
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
	writeJSON(w, 200, map[string]any{"records": records, "total": len(records)})
}
