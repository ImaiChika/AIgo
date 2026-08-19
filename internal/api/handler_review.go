package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/review"
)

// handleSubmitReview 将题目提交到指定的审核流程。
// 请求：{"question_id": "xxx", "flow_id": "flow-a2-2round"}
// 提交后题目状态变为 reviewing，生成审核任务。
func (s *Server) handleSubmitReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuestionID string `json:"question_id"` // 题目 ID
		FlowID     string `json:"flow_id"`     // 审核流程 ID
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	task, err := s.reviewSvc.SubmitQuestion(r.Context(), req.QuestionID, req.FlowID)
	if err != nil {
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
// 请求：{"task_id": "xxx", "action": "approved", "opinion": "通过"}
func (s *Server) handleReviewAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID   string `json:"task_id"`   // 审核任务 ID
		ExpertID string `json:"expert_id"` // 审核人 ID（有最终把关权限可指定，其他用户忽略此字段）
		Action   string `json:"action"`    // approved / rejected / revision_required
		Opinion  string `json:"opinion"`   // 审核意见
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, err.Error())
		return
	}

	currentUserID := auth.GetUserID(r.Context())

	// 最终把关权限查询
	hasFinalRight, err := s.authSvc.HasFinalRight(r.Context(), currentUserID)
	if err != nil {
		writeError(w, 500, "权限查询失败")
		return
	}

	// 普通用户强制使用自己的 ID，防止冒名；把关人可指定任意审核人
	expertID := currentUserID
	if hasFinalRight && req.ExpertID != "" {
		expertID = req.ExpertID
	}

	err = s.reviewSvc.Review(r.Context(), review.ReviewRequest{
		TaskID:        req.TaskID,
		ExpertID:      expertID,
		Action:        domain.QuestionStatus(req.Action),
		Opinion:       req.Opinion,
		HasFinalRight: hasFinalRight,
	})
	if err != nil {
		writeError(w, 500, "审核失败: "+err.Error())
		return
	}

	// 记录审核日志（从任务中获取题目ID）
	task, _ := s.reviewSvc.GetTask(r.Context(), req.TaskID)
	if task != nil {
		s.auditSvc.LogReview(r.Context(), task.QuestionID, expertID, req.Action, req.Opinion)
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleReviewFinalize 最终把关人对票数冲突的任务做决断。
// 请求：{"task_id": "xxx", "action": "approved", "opinion": "..."}
// action：approved（通过进下一轮/完成）/ rejected（驳回）/ revision_required（退回修改）
func (s *Server) handleReviewFinalize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID  string `json:"task_id"`
		Action  string `json:"action"`
		Opinion string `json:"opinion"`
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
		IsSystemAdmin: isSysAdmin,
	}); err != nil {
		writeError(w, 400, "决断失败: "+err.Error())
		return
	}

	task, _ := s.reviewSvc.GetTask(r.Context(), req.TaskID)
	if task != nil {
		s.auditSvc.LogReview(r.Context(), task.QuestionID, currentUserID, "final_"+req.Action, req.Opinion)
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
	hasFinalRight, err := s.authSvc.HasFinalRight(r.Context(), userID)
	if err != nil {
		writeError(w, 500, "权限查询失败")
		return
	}
	// 仅审题/把关相关用户可查看
	hasReview, err := s.authSvc.HasPermission(r.Context(), userID, domain.PermReviewDo)
	if err != nil {
		writeError(w, 500, "权限查询失败")
		return
	}
	if !hasReview && !hasFinalRight {
		writeError(w, 403, "权限不足")
		return
	}
	tasks, err := s.reviewSvc.MyTasks(r.Context(), userID, hasFinalRight)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks, "total": len(tasks)})
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
	tasks, err := s.reviewSvc.MyDecisions(r.Context(), userID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"tasks": tasks, "total": len(tasks)})
}

func bankLabel(bankID string) string {
	if bankID == "" {
		return "未分类"
	}
	return bankID
}

// handleReviewResults 审核结果汇总：统计 + 题目列表 + 专家评语。
// 查询参数：final_status（pending/reviewing/conflict/approved/rejected/revision_required/published）、
// bank_id、q（关键词）、page、page_size。
func (s *Server) handleReviewResults(w http.ResponseWriter, r *http.Request) {
	items, stats, err := s.reviewSvc.ListResults(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}

	finalStatus := r.URL.Query().Get("final_status")
	bankID := r.URL.Query().Get("bank_id")
	keyword := strings.ToLower(r.URL.Query().Get("q"))

	var filtered []review.ReviewResultItem
	for _, item := range items {
		// 题库范围过滤（与题目列表一致：受限用户只能看到其题库内的题目）
		if !s.questionInScope(r, &item.Question, domain.PermQuestionView) {
			continue
		}
		if finalStatus != "" && item.FinalStatus != finalStatus {
			continue
		}
		// bank_id 筛选：__unclassified__ = 未分类题目（不属于任何题库）
		if bankID == "__unclassified__" {
			if len(item.Question.BankIDs) > 0 {
				continue
			}
		} else if bankID != "" {
			inBank := false
			for _, b := range item.Question.BankIDs {
				if b == bankID {
					inBank = true
					break
				}
			}
			if !inBank {
				continue
			}
		}
		if keyword != "" {
			if !strings.Contains(strings.ToLower(item.Question.ClinicalStem), keyword) &&
				!strings.Contains(strings.ToLower(item.Question.ID), keyword) &&
				!strings.Contains(strings.ToLower(item.Question.Profession), keyword) {
				continue
			}
		}
		filtered = append(filtered, item)
	}

	// 分页
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	total := len(filtered)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	writeJSON(w, 200, map[string]any{
		"items":     filtered[start:end],
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  end < total,
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
	writeJSON(w, 200, map[string]any{"reviewers": candidates, "total": len(candidates)})
}

// handleGetReviewTask 根据任务 ID 获取审核任务详情。
// 校验任务对应题目的题库范围（question:view）。
func (s *Server) handleGetReviewTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, err := s.reviewSvc.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeError(w, 404, "审核任务不存在")
		return
	}
	if _, status, err := s.loadScopedQuestion(r, task.QuestionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, 200, task)
}

// handleGetTaskByQuestion 根据题目 ID 获取关联的审核任务。
// 用于前端选中题目后自动加载审核状态。校验题库范围（question:view）。
func (s *Server) handleGetTaskByQuestion(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("questionId")
	if _, status, err := s.loadScopedQuestion(r, questionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
		return
	}
	task, err := s.reviewSvc.GetTaskByQuestionID(r.Context(), questionID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if task == nil {
		writeJSON(w, 200, nil) // 没有审核任务返回 null
		return
	}
	writeJSON(w, 200, task)
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

// handleReviewRecords 获取某审核任务的所有审核记录。
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
	if _, status, err := s.loadScopedQuestion(r, task.QuestionID, domain.PermQuestionView); err != nil {
		writeError(w, status, err.Error())
		return
	}
	records, err := s.reviewSvc.ListRecords(r.Context(), taskId)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"records": records, "total": len(records)})
}
