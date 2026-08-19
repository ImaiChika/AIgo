package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// Candidate 审核人候选（有审题权限的用户）。
type Candidate struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	BankIDs     []string `json:"bank_ids"` // 直接分配权限的题库范围（空=全部）
}

// UserResolver 审核人解析接口：从用户权限体系获取审核人。
// 由 auth.Service 实现，避免 review 包直接依赖 auth。
type UserResolver interface {
	// ListReviewers 列出可审核指定题库的用户（直接勾选审题权限且题库范围匹配、账号启用）。
	ListReviewers(ctx context.Context, bankID string) ([]string, error)
	// ListFallbackReviewers 列出角色模板含审题权限的非管理员用户（兜底匹配用）。
	ListFallbackReviewers(ctx context.Context) ([]string, error)
	// HasFinalRight 检查用户是否拥有最终把关权限。
	HasFinalRight(ctx context.Context, userID string) (bool, error)
	// ListReviewCandidates 列出全部有审题权限的用户（流程配置选审核人用）。
	ListReviewCandidates(ctx context.Context) ([]Candidate, error)
}

type Service struct {
	expertStore   storage.ExpertStore
	reviewStore   storage.ReviewStore
	questionStore storage.QuestionStore
	users         UserResolver
}

func NewService(expertStore storage.ExpertStore, reviewStore storage.ReviewStore, questionStore storage.QuestionStore, users UserResolver) *Service {
	return &Service{
		expertStore:   expertStore,
		reviewStore:   reviewStore,
		questionStore: questionStore,
		users:         users,
	}
}

// ===== 专家管理 =====

// CreateExpert 创建专家。
func (s *Service) CreateExpert(ctx context.Context, expert domain.Expert) error {
	if expert.ID == "" {
		return fmt.Errorf("专家ID不能为空")
	}
	if expert.Name == "" {
		return fmt.Errorf("专家姓名不能为空")
	}
	expert.Enabled = true
	return s.expertStore.SaveExpert(ctx, expert)
}

// ListExperts 列出所有专家。
func (s *Service) ListExperts(ctx context.Context) ([]domain.Expert, error) {
	return s.expertStore.ListExperts(ctx)
}

// GetExpert 获取专家。
func (s *Service) GetExpert(ctx context.Context, id string) (*domain.Expert, error) {
	return s.expertStore.GetExpert(ctx, id)
}

// UpdateExpert 更新专家。
func (s *Service) UpdateExpert(ctx context.Context, expert domain.Expert) error {
	return s.expertStore.UpdateExpert(ctx, expert)
}

// DeleteExpert 删除专家。
func (s *Service) DeleteExpert(ctx context.Context, id string) error {
	return s.expertStore.DeleteExpert(ctx, id)
}

// ===== 流程管理 =====

// CreateFlow 创建审核流程配置。
func (s *Service) CreateFlow(ctx context.Context, flow domain.ReviewFlowConfig) error {
	if flow.ID == "" {
		return fmt.Errorf("流程ID不能为空")
	}
	// 重复 ID 拒绝（防止静默覆盖已有流程）
	if existing, _ := s.reviewStore.GetFlowConfig(ctx, flow.ID); existing != nil {
		return fmt.Errorf("流程 ID %s 已存在，请更换 ID", flow.ID)
	}
	if err := s.validateFlow(ctx, flow); err != nil {
		return err
	}
	flow.CreatedAt = time.Now()
	return s.reviewStore.SaveFlowConfig(ctx, flow)
}

// UpdateFlow 更新审核流程配置。有进行中的任务引用该流程时禁止修改。
func (s *Service) UpdateFlow(ctx context.Context, flow domain.ReviewFlowConfig) error {
	existing, _ := s.reviewStore.GetFlowConfig(ctx, flow.ID)
	if existing == nil {
		return fmt.Errorf("流程 %s 不存在", flow.ID)
	}
	// 进行中的任务引用该流程时禁止修改，防止轮次变化破坏审核任务
	activeCount, err := s.reviewStore.CountActiveTasksByFlow(ctx, flow.ID)
	if err != nil {
		return fmt.Errorf("检查流程引用失败: %w", err)
	}
	if activeCount > 0 {
		return fmt.Errorf("流程 %s 有 %d 个进行中的审核任务，暂不能修改", flow.ID, activeCount)
	}
	if err := s.validateFlow(ctx, flow); err != nil {
		return err
	}
	return s.reviewStore.SaveFlowConfig(ctx, flow)
}

// validateFlow 校验流程配置（名称、轮次、审核人、通过人数、把关人）。
func (s *Service) validateFlow(ctx context.Context, flow domain.ReviewFlowConfig) error {
	if flow.Name == "" {
		return fmt.Errorf("流程名称不能为空")
	}
	if len(flow.Rounds) == 0 {
		return fmt.Errorf("至少需要一轮审核配置")
	}
	// 校验投票规则合法
	if flow.VoteRule != "" && flow.VoteRule != "veto" {
		return fmt.Errorf("无效的投票规则: %s（可选：空/veto）", flow.VoteRule)
	}
	// 一票否决规则：通过条件固定为全员通过，required_count 无效
	if flow.VoteRule == "veto" {
		for i := range flow.Rounds {
			flow.Rounds[i].RequiredCount = 0 // 0 = 全员
		}
	}
	// 校验每一轮配置
	for i := range flow.Rounds {
		round := &flow.Rounds[i]
		if len(round.ExpertIDs) == 0 {
			// 审核人留空：提交时按"审题权限 + 题库范围"自动匹配
			// required_count 保留用户配置（0 = 全员，提交时按实际匹配人数确定）
			if round.RequiredCount < 0 {
				round.RequiredCount = 0
			}
		} else {
			// 专家去重：同一轮不能出现重复专家
			seen := make(map[string]bool)
			// 有审题权限的用户集合（流程配置直接从用户列表选审核人）
			validUsers := make(map[string]bool)
			if s.users != nil {
				if candidates, err := s.users.ListReviewCandidates(ctx); err == nil {
					for _, c := range candidates {
						validUsers[c.ID] = true
					}
				}
			}
			for _, expertID := range round.ExpertIDs {
				if seen[expertID] {
					return fmt.Errorf("第 %d 轮「%s」审核人 %s 重复", round.RoundNumber, round.Name, expertID)
				}
				seen[expertID] = true
				if validUsers[expertID] {
					continue // 用户有效（有审题权限且启用）
				}
				// 兼容历史：专家库直建的专家（E 前缀，无登录账号）
				expert, err := s.expertStore.GetExpert(ctx, expertID)
				if err != nil {
					return fmt.Errorf("查询审核人 %s 失败: %w", expertID, err)
				}
				if expert == nil {
					return fmt.Errorf("第 %d 轮审核人 %s 不存在（无登录账号或未分配审题权限）", round.RoundNumber, expertID)
				}
				if !expert.Enabled {
					return fmt.Errorf("第 %d 轮审核人 %s（%s）已停用", round.RoundNumber, expertID, expert.Name)
				}
			}
			// required_count=0 表示全部通过（无反对票）
			if round.RequiredCount == 0 {
				round.RequiredCount = len(round.ExpertIDs)
			}
			if round.RequiredCount < 1 || round.RequiredCount > len(round.ExpertIDs) {
				return fmt.Errorf("第 %d 轮「%s」通过人数 %d 超出范围 [1, %d]", round.RoundNumber, round.Name, round.RequiredCount, len(round.ExpertIDs))
			}
		}
		// 轮次号补全（防止配置遗漏轮次号）
		if round.RoundNumber == 0 {
			round.RoundNumber = i + 1
		}
	}
	return nil
}

// LoadFlowsFromFile 从 JSON 文件加载审核流程配置。
func (s *Service) LoadFlowsFromFile(ctx context.Context, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("读取配置文件失败: %w", err)
	}
	var config domain.FlowsConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		return 0, fmt.Errorf("解析配置文件失败: %w", err)
	}
	count := 0
	for _, flow := range config.Flows {
		if err := s.CreateFlow(ctx, flow); err != nil {
			return count, fmt.Errorf("加载流程 %s 失败: %w", flow.ID, err)
		}
		count++
	}
	return count, nil
}

// ListFlows 列出所有审核流程。
func (s *Service) ListFlows(ctx context.Context) ([]domain.ReviewFlowConfig, error) {
	return s.reviewStore.ListFlowConfigs(ctx)
}

// DeleteFlow 删除审核流程。有任务引用该流程（含历史任务）时拒绝删除，
// 保证历史审核记录始终能追溯到当时的流程配置。
func (s *Service) DeleteFlow(ctx context.Context, id string) error {
	activeCount, err := s.reviewStore.CountActiveTasksByFlow(ctx, id)
	if err != nil {
		return fmt.Errorf("检查流程引用失败: %w", err)
	}
	if activeCount > 0 {
		return fmt.Errorf("流程 %s 有 %d 个进行中的审核任务，无法删除", id, activeCount)
	}
	totalCount, err := s.reviewStore.CountTasksByFlow(ctx, id)
	if err != nil {
		return fmt.Errorf("检查流程历史引用失败: %w", err)
	}
	if totalCount > 0 {
		return fmt.Errorf("流程 %s 有 %d 个历史审核任务引用，无法删除（历史记录需保留）", id, totalCount)
	}
	return s.reviewStore.DeleteFlowConfig(ctx, id)
}

// ===== 审核操作 =====

// SubmitQuestion 将题目提交到审核流程，创建审核任务。
func (s *Service) SubmitQuestion(ctx context.Context, questionID string, flowID string) (*domain.ReviewTask, error) {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, fmt.Errorf("题目 %s 不存在", questionID)
	}

	// 只允许特定状态的题目提交审核
	allowedStatuses := map[domain.QuestionStatus]bool{
		domain.StatusAIDraft:          true,
		domain.StatusAutoChecked:      true,
		domain.StatusAIReviewed:       true,
		domain.StatusRevisionRequired: true,
		domain.StatusRejected:         true,
	}
	if !allowedStatuses[q.Status] {
		return nil, fmt.Errorf("题目 %s 当前状态为 %s，不允许提交审核", questionID, q.Status)
	}

	flow, err := s.reviewStore.GetFlowConfig(ctx, flowID)
	if err != nil {
		return nil, err
	}
	if flow == nil {
		return nil, fmt.Errorf("审核流程 %s 不存在", flowID)
	}

	existing, _ := s.reviewStore.GetTaskByQuestionID(ctx, questionID)
	if existing != nil {
		// 如果是"已驳回"状态，允许重新提交（重置任务从头再来）
		if existing.Status == domain.StatusRejected {
			assigned, err := s.resolveReviewers(ctx, bankIDForQuestion(q), flow.Rounds[0].ExpertIDs)
			if err != nil {
				return nil, err
			}
			existing.Status = domain.StatusReviewing
			existing.FlowID = flowID // 更新为本次选择的流程
			existing.CurrentRound = 1
			existing.AssignedTo = assigned
			existing.FinalReviewerIDs = flow.FinalReviewerIDs
			existing.FinalDecision = nil
			existing.QuestionPrevStatus = q.Status // 记录重提前状态（撤销时恢复）
			existing.RoundResults = make([]domain.RoundResult, len(flow.Rounds))
			for i := range existing.RoundResults {
				existing.RoundResults[i].RoundNumber = i + 1
			}
			existing.UpdatedAt = time.Now()
			if err := s.reviewStore.UpdateTask(ctx, *existing); err != nil {
				return nil, err
			}
			s.updateQuestionStatus(ctx, questionID, domain.StatusReviewing)
			return existing, nil
		}
		// 如果是"需修改"状态，恢复为审核中（保持原轮，清空本轮投票重新审核）
		if existing.Status == domain.StatusRevisionRequired {
			existing.Status = domain.StatusReviewing
			existing.QuestionPrevStatus = q.Status // 记录重提前状态（撤销时恢复）
			roundIdx := existing.CurrentRound - 1
			if roundIdx >= 0 && roundIdx < len(existing.RoundResults) {
				existing.RoundResults[roundIdx].Reviews = nil
				existing.RoundResults[roundIdx].ApprovedCount = 0
				existing.RoundResults[roundIdx].RejectedCount = 0
				existing.RoundResults[roundIdx].RevisionCount = 0
				existing.RoundResults[roundIdx].Passed = false
			}
			existing.FinalDecision = nil
			existing.UpdatedAt = time.Now()
			if err := s.reviewStore.UpdateTask(ctx, *existing); err != nil {
				return nil, err
			}
			s.updateQuestionStatus(ctx, questionID, domain.StatusReviewing)
			return existing, nil
		}
		// 题目被编辑后回退为草稿（旧任务处于终态），允许创建新任务重新走流程
		isTerminalTask := existing.Status == domain.StatusApproved || existing.Status == domain.StatusPublished || existing.Status == domain.StatusArchived
		if q.Status == domain.StatusAIDraft && isTerminalTask {
			// 继续向下创建新任务
		} else {
			return nil, fmt.Errorf("题目 %s 已有审核任务 %s，状态为 %s", questionID, existing.ID, existing.Status)
		}
	}

	// 解析第一轮审核人（显式配置优先，否则按审题权限+题库范围自动匹配）
	assigned, err := s.resolveReviewers(ctx, bankIDForQuestion(q), flow.Rounds[0].ExpertIDs)
	if err != nil {
		return nil, err
	}

	// 更新题目状态（版本号保持不变，版本由内容编辑递增，AI 检查结果依赖它判断过期）
	// 记录提交前状态（撤销时据此恢复题目），再更新题目为审核中
	prevStatus := q.Status
	q.Status = domain.StatusReviewing
	q.UpdatedAt = time.Now()
	if err := s.questionStore.SaveQuestion(ctx, *q); err != nil {
		return nil, fmt.Errorf("更新题目状态失败: %w", err)
	}

	// 创建审核任务（记录提交前状态，撤销时据此恢复题目）
	task := domain.ReviewTask{
		ID:                 fmt.Sprintf("task-%s-%d", questionID, time.Now().UnixNano()),
		QuestionID:         questionID,
		FlowID:             flowID,
		CurrentRound:       1,
		Status:             domain.StatusReviewing,
		AssignedTo:         assigned,
		FinalReviewerIDs:   flow.FinalReviewerIDs,
		QuestionPrevStatus: prevStatus,
		RoundResults:       make([]domain.RoundResult, len(flow.Rounds)),
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
	// 初始化每轮结果
	for i := range task.RoundResults {
		task.RoundResults[i].RoundNumber = i + 1
	}
	if err := s.reviewStore.SaveTask(ctx, task); err != nil {
		return nil, err
	}

	return &task, nil
}

// resolveReviewers 解析本轮审核人：
// 显式配置的审核人直接使用；为空时按"审题权限 + 题库范围"自动匹配；
// 一个直接权限审题人都没有时，兜底使用角色模板含审题权限的非管理员用户。
func (s *Service) resolveReviewers(ctx context.Context, bankID string, configured []string) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	if s.users == nil {
		return nil, fmt.Errorf("未配置审核人解析器")
	}
	reviewers, err := s.users.ListReviewers(ctx, bankID)
	if err != nil {
		return nil, fmt.Errorf("查询审题人失败: %w", err)
	}
	if len(reviewers) == 0 {
		// 兜底：角色模板含审题权限的非管理员用户（如 expert 角色账号）
		if fallback, ferr := s.users.ListFallbackReviewers(ctx); ferr == nil && len(fallback) > 0 {
			reviewers = fallback
		}
	}
	if len(reviewers) == 0 {
		return nil, fmt.Errorf("题库「%s」没有分配审题人：请给用户分配审题权限并设置题库范围，或在流程中显式指定审核人", bankNameOrAll(bankID))
	}
	return reviewers, nil
}

// bankIDForQuestion 取题目的首个所属题库（多对多时用于审核人自动匹配）。
func bankIDForQuestion(q *domain.A2Question) string {
	if q != nil && len(q.BankIDs) > 0 {
		return q.BankIDs[0]
	}
	return ""
}

func bankNameOrAll(bankID string) string {
	if bankID == "" {
		return "未分类"
	}
	return bankID
}

// RevokeFlowResult 撤销结果。
type RevokeFlowResult struct {
	Revoked  int `json:"revoked"`  // 撤销的任务数（未完成审核）
	Restored int `json:"restored"` // 恢复状态的题目数
	Kept     int `json:"kept"`     // 保留的终态任务数（审核已结束，不受影响）
}

// RevokeFlow 撤销流程下所有未完成的审核任务：
//   - 任务状态为 reviewing / revision_required / conflict 的：删除任务（级联删审核记录），
//     题目状态恢复为提交前状态（task.QuestionPrevStatus，兜底 ai_draft）
//   - 终态任务（approved / rejected / published 等）：保留不动，题目状态也不变
//
// 用于管理员发现误提交后整体撤回，避免任务卡死。
func (s *Service) RevokeFlow(ctx context.Context, flowID string) (*RevokeFlowResult, error) {
	flow, err := s.reviewStore.GetFlowConfig(ctx, flowID)
	if err != nil {
		return nil, err
	}
	if flow == nil {
		return nil, fmt.Errorf("审核流程 %s 不存在", flowID)
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return nil, err
	}
	result := &RevokeFlowResult{}
	for i := range tasks {
		t := &tasks[i]
		if t.FlowID != flowID {
			continue
		}
		// 终态任务保留
		if t.Status != domain.StatusReviewing && t.Status != domain.StatusRevisionRequired && t.Status != domain.StatusConflict {
			result.Kept++
			continue
		}
		// 恢复题目状态
		q, err := s.questionStore.GetQuestion(ctx, t.QuestionID)
		if err == nil && q != nil {
			prev := t.QuestionPrevStatus
			if prev == "" {
				prev = domain.StatusAIDraft
			}
			if q.Status == domain.StatusReviewing || q.Status == domain.StatusRevisionRequired || q.Status == domain.StatusConflict || q.Status == prev {
				q.Status = prev
				q.UpdatedAt = time.Now()
				if err := s.questionStore.SaveQuestion(ctx, *q); err == nil {
					result.Restored++
				}
			}
		}
		// 删除任务（级联删审核记录）
		if err := s.reviewStore.DeleteTask(ctx, t.ID); err != nil {
			return result, fmt.Errorf("删除任务 %s 失败: %w", t.ID, err)
		}
		result.Revoked++
	}
	return result, nil
}

// SubmitBankResult 批量提交结果（含跳过统计，便于前端提示题库状态冲突）。
type SubmitBankResult struct {
	Submitted        int      `json:"submitted"`         // 成功提交数量
	Failed           []string `json:"failed"`            // 失败明细
	SkippedReviewing int      `json:"skipped_reviewing"` // 已在审核中/待决断，跳过
	SkippedFinished  int      `json:"skipped_finished"`  // 已通过/已发布/已归档，跳过
}

// SubmitBank 按题库批量提交审核：把题库内所有可提交状态的题目统一提交到流程。
// 已在审核中（reviewing/conflict）和已审核结束（approved/published/archived）的题目自动跳过，
// 返回跳过统计，便于提示题库内状态冲突。
func (s *Service) SubmitBank(ctx context.Context, bankID, flowID string) (*SubmitBankResult, error) {
	flow, err := s.reviewStore.GetFlowConfig(ctx, flowID)
	if err != nil {
		return nil, err
	}
	if flow == nil {
		return nil, fmt.Errorf("审核流程 %s 不存在", flowID)
	}
	// 流程绑定了其他题库时校验
	if flow.BankID != "" && bankID != "" && flow.BankID != bankID {
		return nil, fmt.Errorf("审核流程 %s 绑定的是题库 %s，不能提交题库 %s", flowID, flow.BankID, bankID)
	}
	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		return nil, err
	}
	allowedStatuses := map[domain.QuestionStatus]bool{
		domain.StatusAIDraft:          true,
		domain.StatusAutoChecked:      true,
		domain.StatusAIReviewed:       true,
		domain.StatusRevisionRequired: true,
		domain.StatusRejected:         true,
	}
	result := &SubmitBankResult{}
	for i := range questions {
		q := &questions[i]
		// 题目属于该题库（多对多）
		inBank := false
		for _, b := range q.BankIDs {
			if b == bankID {
				inBank = true
				break
			}
		}
		if !inBank {
			continue
		}
		if !allowedStatuses[q.Status] {
			switch q.Status {
			case domain.StatusReviewing, domain.StatusConflict:
				result.SkippedReviewing++
			default:
				result.SkippedFinished++
			}
			continue
		}
		if _, err := s.SubmitQuestion(ctx, q.ID, flowID); err != nil {
			result.Failed = append(result.Failed, fmt.Sprintf("%s: %s", q.ID, err.Error()))
			continue
		}
		result.Submitted++
	}
	return result, nil
}

// ===== 审核结果汇总 =====

// ReviewResultItem 单题审核结果汇总：题目 + 任务 + 全部专家审核记录（评语）。
type ReviewResultItem struct {
	Question    domain.A2Question     `json:"question"`       // 题目
	Task        *domain.ReviewTask    `json:"task,omitempty"` // 当前审核任务（可为空=从未提交）
	Records     []domain.ReviewRecord `json:"records"`        // 全部审核记录（含专家评语）
	FinalStatus string                `json:"final_status"`   // 最终状态分类：pending/reviewing/conflict/approved/rejected/revision_required/published
}

// ReviewStats 审核结果统计。
type ReviewStats struct {
	Published        int `json:"published"`         // 已入库
	Approved         int `json:"approved"`          // 审核通过待发布
	Rejected         int `json:"rejected"`          // 已驳回
	RevisionRequired int `json:"revision_required"` // 需修改
	Reviewing        int `json:"reviewing"`         // 审核中
	Conflict         int `json:"conflict"`          // 待决断
	Pending          int `json:"pending"`           // 未提交审核
	Total            int `json:"total"`             // 题目总数
}

// finalStatusOf 根据题目与任务计算最终状态分类。
func finalStatusOf(q domain.A2Question, task *domain.ReviewTask) string {
	switch q.Status {
	case domain.StatusPublished:
		return "published"
	case domain.StatusApproved:
		return "approved"
	case domain.StatusRejected:
		return "rejected"
	case domain.StatusRevisionRequired:
		return "revision_required"
	case domain.StatusReviewing:
		if task != nil && task.Status == domain.StatusConflict {
			return "conflict"
		}
		return "reviewing"
	default:
		return "pending"
	}
}

// ListResults 汇总全部题目的审核结果（题目 + 任务 + 专家评语 + 统计）。
// 调用方在内存中做筛选与分页。
func (s *Service) ListResults(ctx context.Context) ([]ReviewResultItem, ReviewStats, error) {
	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		return nil, ReviewStats{}, err
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return nil, ReviewStats{}, err
	}
	records, err := s.reviewStore.ListAllRecords(ctx)
	if err != nil {
		return nil, ReviewStats{}, err
	}

	// 每题取最新任务（GetTaskByQuestionID 语义：created_at DESC LIMIT 1）
	taskByQuestion := make(map[string]*domain.ReviewTask, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		existing, ok := taskByQuestion[t.QuestionID]
		if !ok || t.CreatedAt.After(existing.CreatedAt) {
			taskByQuestion[t.QuestionID] = t
		}
	}
	recordsByTask := make(map[string][]domain.ReviewRecord)
	for _, r := range records {
		recordsByTask[r.TaskID] = append(recordsByTask[r.TaskID], r)
	}

	items := make([]ReviewResultItem, 0, len(questions))
	stats := ReviewStats{Total: len(questions)}
	for i := range questions {
		q := &questions[i]
		task := taskByQuestion[q.ID]
		var taskRecords []domain.ReviewRecord
		if task != nil {
			taskRecords = recordsByTask[task.ID]
		}
		fs := finalStatusOf(*q, task)
		items = append(items, ReviewResultItem{
			Question:    *q,
			Task:        task,
			Records:     taskRecords,
			FinalStatus: fs,
		})
		switch fs {
		case "published":
			stats.Published++
		case "approved":
			stats.Approved++
		case "rejected":
			stats.Rejected++
		case "revision_required":
			stats.RevisionRequired++
		case "reviewing":
			stats.Reviewing++
		case "conflict":
			stats.Conflict++
		default:
			stats.Pending++
		}
	}
	return items, stats, nil
}

// MyTaskItem 待我审核的任务（含题目、流程轮次、投票进度）。
type MyTaskItem struct {
	Task          *domain.ReviewTask `json:"task"`           // 审核任务
	Question      domain.A2Question  `json:"question"`       // 题目完整信息
	FlowName      string             `json:"flow_name"`      // 流程名
	RoundIndex    int                `json:"round_index"`    // 当前轮序号（从1）
	RoundCount    int                `json:"round_count"`    // 总轮数
	RoundName     string             `json:"round_name"`     // 当前轮名称
	Voted         int                `json:"voted"`          // 已投人数
	Assigned      int                `json:"assigned"`       // 应投人数
	Approved      int                `json:"approved"`       // 通过票
	Rejected      int                `json:"rejected"`       // 驳回票
	Revision      int                `json:"revision"`       // 需修改票
	MyVoted       bool               `json:"my_voted"`       // 我是否已投过本轮
	CanVote       bool               `json:"can_vote"`       // 我是否可以投票
	NeedsDecision bool               `json:"needs_decision"` // 待决断（把关人可见）
}

// MyTasks 列出待我审核的任务：
//   - 仅当前轮分配名单包含我、且我尚未投票的任务（reviewing / revision_required）
//   - 无论是否为把关人：自己的审核工作结束（投过票）即移开列表
//   - 待决断任务不在此列（把关人请用 MyDecisions / 「待决断」页面）
func (s *Service) MyTasks(ctx context.Context, userID string, hasFinalRight bool) ([]MyTaskItem, error) {
	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return nil, err
	}
	flows, err := s.reviewStore.ListFlowConfigs(ctx)
	if err != nil {
		return nil, err
	}
	flowByName := make(map[string]*domain.ReviewFlowConfig, len(flows))
	for i := range flows {
		f := &flows[i]
		flowByName[f.ID] = f
	}

	taskByQuestion := make(map[string]*domain.ReviewTask, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		existing, ok := taskByQuestion[t.QuestionID]
		if !ok || t.CreatedAt.After(existing.CreatedAt) {
			taskByQuestion[t.QuestionID] = t
		}
	}

	var result []MyTaskItem
	for i := range questions {
		q := &questions[i]
		task := taskByQuestion[q.ID]
		if task == nil {
			continue
		}
		// 待决断/终态任务不显示（待决断走「待决断」页面）；
		// revision_required 也暂不显示（等管理员编辑题目重新提交后恢复 reviewing 再出现）
		if task.Status != domain.StatusReviewing {
			continue
		}
		roundIdx := task.CurrentRound - 1
		if roundIdx < 0 || roundIdx >= len(task.RoundResults) {
			continue
		}
		round := &task.RoundResults[roundIdx]

		// 当前轮分配名单必须包含我（只显示自己的审核工作）
		inRound := false
		for _, id := range task.AssignedTo {
			if id == userID {
				inRound = true
				break
			}
		}
		if !inRound {
			continue
		}

		// 我是否已投过本轮 → 投过即移开
		myVoted := false
		for _, r := range round.Reviews {
			if r.ExpertID == userID {
				myVoted = true
				break
			}
		}
		if myVoted {
			continue
		}

		flow := flowByName[task.FlowID]
		item := MyTaskItem{
			Task:          task,
			Question:      *q,
			RoundIndex:    task.CurrentRound,
			Voted:         len(round.Reviews),
			Assigned:      len(task.AssignedTo),
			Approved:      round.ApprovedCount,
			Rejected:      round.RejectedCount,
			Revision:      round.RevisionCount,
			MyVoted:       myVoted,
			CanVote:       true,
			NeedsDecision: false,
		}
		if flow != nil {
			item.FlowName = flow.Name
			item.RoundCount = len(flow.Rounds)
			if roundIdx < len(flow.Rounds) {
				item.RoundName = flow.Rounds[roundIdx].Name
			}
		} else {
			item.RoundCount = len(task.RoundResults)
		}
		result = append(result, item)
	}
	// 按更新时间倒序（最新提交的优先审核）
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Task.UpdatedAt.After(result[i].Task.UpdatedAt) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result, nil
}

// MyDecisions 列出待我决断的任务（最终把关人专用）：
// 所有 conflict（待决断）任务，且我在该流程的把关人名单内（名单空=全部把关人可见）。
// 含题目完整信息、流程轮次、票数统计，供「待决断」独立页面使用。
func (s *Service) MyDecisions(ctx context.Context, userID string) ([]MyTaskItem, error) {
	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return nil, err
	}
	flows, err := s.reviewStore.ListFlowConfigs(ctx)
	if err != nil {
		return nil, err
	}
	flowByName := make(map[string]*domain.ReviewFlowConfig, len(flows))
	for i := range flows {
		f := &flows[i]
		flowByName[f.ID] = f
	}

	taskByQuestion := make(map[string]*domain.ReviewTask, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		existing, ok := taskByQuestion[t.QuestionID]
		if !ok || t.CreatedAt.After(existing.CreatedAt) {
			taskByQuestion[t.QuestionID] = t
		}
	}

	var result []MyTaskItem
	for i := range questions {
		q := &questions[i]
		task := taskByQuestion[q.ID]
		if task == nil || task.Status != domain.StatusConflict {
			continue
		}
		// 把关人名单校验（名单空 = 任意把关人）
		if len(task.FinalReviewerIDs) > 0 {
			allowed := false
			for _, id := range task.FinalReviewerIDs {
				if id == userID {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		roundIdx := task.CurrentRound - 1
		if roundIdx < 0 || roundIdx >= len(task.RoundResults) {
			continue
		}
		round := &task.RoundResults[roundIdx]
		flow := flowByName[task.FlowID]
		item := MyTaskItem{
			Task:          task,
			Question:      *q,
			RoundIndex:    task.CurrentRound,
			Voted:         len(round.Reviews),
			Assigned:      len(task.AssignedTo),
			Approved:      round.ApprovedCount,
			Rejected:      round.RejectedCount,
			Revision:      round.RevisionCount,
			CanVote:       false,
			NeedsDecision: true,
		}
		if flow != nil {
			item.FlowName = flow.Name
			item.RoundCount = len(flow.Rounds)
			if roundIdx < len(flow.Rounds) {
				item.RoundName = flow.Rounds[roundIdx].Name
			}
		} else {
			item.RoundCount = len(task.RoundResults)
		}
		result = append(result, item)
	}
	// 按更新时间倒序
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Task.UpdatedAt.After(result[i].Task.UpdatedAt) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result, nil
}

// ReviewRequest 专家审核请求。
type ReviewRequest struct {
	TaskID        string                `json:"task_id"`
	ExpertID      string                `json:"expert_id"`
	Action        domain.QuestionStatus `json:"action"` // approved / rejected / revision_required
	Opinion       string                `json:"opinion"`
	HasFinalRight bool                  `json:"has_final_right"` // 最终把关人可审任意轮
}

// Review 专家执行审核（投票）。
// 新规则：单人反对不再立即退回/驳回，所有分配审核人审核完毕后自动统计：
//   - 无反对票且通过数达门槛 → 本轮通过（进下一轮/完成）
//   - 全员审完但存在反对票 → 任务进入 conflict，等待最终把关人决断
func (s *Service) Review(ctx context.Context, req ReviewRequest) error {
	task, err := s.reviewStore.GetTask(ctx, req.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("审核任务 %s 不存在", req.TaskID)
	}

	flow, err := s.reviewStore.GetFlowConfig(ctx, task.FlowID)
	if err != nil {
		return err
	}
	if flow == nil {
		return fmt.Errorf("审核流程 %s 不存在（可能已被删除）", task.FlowID)
	}

	// 只有进行中的任务允许审核，终态/冲突态不可再投票
	if task.Status != domain.StatusReviewing && task.Status != domain.StatusRevisionRequired {
		if task.Status == domain.StatusConflict {
			return fmt.Errorf("本轮审核票数冲突，请等待最终把关人决断，不能再投票")
		}
		return fmt.Errorf("审核任务已结束，当前状态为 %s，不能再审核", task.Status)
	}

	roundIdx := task.CurrentRound - 1
	if roundIdx < 0 || roundIdx >= len(flow.Rounds) {
		return fmt.Errorf("当前轮次 %d 超出流程配置范围", task.CurrentRound)
	}
	currentRoundConfig := flow.Rounds[roundIdx]

	// 校验审核人是否在本轮分配名单（最终把关人可审任意轮）
	isAssigned := req.HasFinalRight
	if !isAssigned {
		for _, id := range task.AssignedTo {
			if id == req.ExpertID {
				isAssigned = true
				break
			}
		}
	}
	if !isAssigned {
		return fmt.Errorf("用户 %s 未被分配到第 %d 轮审核", req.ExpertID, task.CurrentRound)
	}

	// 检查是否已经审核过
	for _, review := range task.RoundResults[roundIdx].Reviews {
		if review.ExpertID == req.ExpertID {
			return fmt.Errorf("用户 %s 已经审核过第 %d 轮，不能重复审核", req.ExpertID, task.CurrentRound)
		}
	}

	// 校验审核动作
	if req.Action != domain.StatusApproved && req.Action != domain.StatusRejected && req.Action != domain.StatusRevisionRequired {
		return fmt.Errorf("无效的审核动作: %s", req.Action)
	}

	// 记录审核结果
	now := time.Now()
	expertReview := domain.ExpertReview{
		ExpertID:   req.ExpertID,
		Conclusion: req.Action,
		Opinion:    req.Opinion,
		ReviewedAt: now,
	}
	task.RoundResults[roundIdx].Reviews = append(task.RoundResults[roundIdx].Reviews, expertReview)

	// 保存审核记录（ID 带时间戳，同一专家打回后重新审核不会主键冲突）
	record := domain.ReviewRecord{
		ID:          fmt.Sprintf("rec-%s-%d-%s-%d", task.ID, task.CurrentRound, req.ExpertID, now.UnixNano()),
		TaskID:      task.ID,
		QuestionID:  task.QuestionID,
		RoundNumber: task.CurrentRound,
		ExpertID:    req.ExpertID,
		Conclusion:  req.Action,
		Opinion:     req.Opinion,
		CreatedAt:   now,
	}
	if err := s.reviewStore.SaveRecord(ctx, record); err != nil {
		return fmt.Errorf("保存审核记录失败: %w", err)
	}

	// 统计本轮票数
	roundResult := &task.RoundResults[roundIdx]
	switch req.Action {
	case domain.StatusApproved:
		roundResult.ApprovedCount++
	case domain.StatusRejected:
		roundResult.RejectedCount++
	case domain.StatusRevisionRequired:
		roundResult.RevisionCount++
	}

	// 通过票数门槛（0=全员）
	requiredCount := currentRoundConfig.RequiredCount
	if requiredCount < 1 {
		requiredCount = len(task.AssignedTo)
	}

	// === 一票否决规则（flow.VoteRule == "veto"）===
	// 任一审核人驳回 → 题目直接驳回；任一需修改 → 退回修改；全员通过且达门槛 → 过轮。
	if flow.VoteRule == "veto" {
		if req.Action == domain.StatusRejected {
			task.Status = domain.StatusRejected
			task.UpdatedAt = now
			if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
				return fmt.Errorf("更新审核任务失败: %w", err)
			}
			s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRejected)
			return nil
		}
		if req.Action == domain.StatusRevisionRequired {
			task.Status = domain.StatusRevisionRequired
			task.UpdatedAt = now
			if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
				return fmt.Errorf("更新审核任务失败: %w", err)
			}
			s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRevisionRequired)
			return nil
		}
		// approved：达到通过票数 → 过轮（最终轮进入最终待决断）
		if roundResult.ApprovedCount >= requiredCount {
			return s.advanceRoundAfterVote(ctx, task, roundIdx)
		}
		task.UpdatedAt = now
		return s.reviewStore.UpdateTask(ctx, *task)
	}

	// === 通过票数规则（默认）===
	// 达到通过票数 → 本轮通过（无视反对票；反对票累积，最终决断时供把关人参考）。
	// 例：通过票数=1 时，只要有 1 票通过即过轮进入下一轮，即使有人投了驳回。
	if roundResult.ApprovedCount >= requiredCount {
		return s.advanceRoundAfterVote(ctx, task, roundIdx)
	}

	// 所有分配审题人都投完票仍未达标（如通过票数=全员但有反对票）→ 分歧，进入待决断
	if len(roundResult.Reviews) >= len(task.AssignedTo) {
		task.Status = domain.StatusConflict
		task.UpdatedAt = now
		if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
			return fmt.Errorf("更新审核任务失败: %w", err)
		}
		return nil
	}

	// 还有审核人未投票，继续等待
	task.UpdatedAt = now
	return s.reviewStore.UpdateTask(ctx, *task)
}

// advanceRoundAfterVote 投票达成通过条件后推进：
//   - 非最终轮 → 直接进入下一轮（无需把关）
//   - 最终轮 → 所有轮次审核完成，进入「最终待决断」，由把关人做最终决断后入库
func (s *Service) advanceRoundAfterVote(ctx context.Context, task *domain.ReviewTask, roundIdx int) error {
	flow, err := s.reviewStore.GetFlowConfig(ctx, task.FlowID)
	if err != nil {
		return err
	}
	if flow == nil {
		return fmt.Errorf("审核流程 %s 不存在（可能已被删除）", task.FlowID)
	}

	task.RoundResults[roundIdx].Passed = true
	now := time.Now()

	if task.CurrentRound < len(flow.Rounds) {
		// 进入下一轮（专家审核正常推进，把关人此时不介入）
		task.CurrentRound++
		nextRound := flow.Rounds[task.CurrentRound-1]
		assigned, err := s.resolveAssignedForTask(ctx, task, nextRound.ExpertIDs)
		if err != nil {
			return err
		}
		task.AssignedTo = assigned
		task.Status = domain.StatusReviewing
	} else {
		// 所有轮次审核完成 → 交给最终把关人决断（决断通过后才入库）
		task.Status = domain.StatusConflict
	}
	task.UpdatedAt = now
	return s.reviewStore.UpdateTask(ctx, *task)
}

// advanceRoundAfterFinalize 把关人决断通过后推进：
//   - 非最终轮 → 进入下一轮
//   - 最终轮 → 直接入库（决断通过即发布，无需手动发布）
func (s *Service) advanceRoundAfterFinalize(ctx context.Context, task *domain.ReviewTask, roundIdx int) error {
	flow, err := s.reviewStore.GetFlowConfig(ctx, task.FlowID)
	if err != nil {
		return err
	}
	if flow == nil {
		return fmt.Errorf("审核流程 %s 不存在（可能已被删除）", task.FlowID)
	}

	task.RoundResults[roundIdx].Passed = true
	now := time.Now()

	if task.CurrentRound < len(flow.Rounds) {
		// 进入下一轮
		task.CurrentRound++
		nextRound := flow.Rounds[task.CurrentRound-1]
		assigned, err := s.resolveAssignedForTask(ctx, task, nextRound.ExpertIDs)
		if err != nil {
			return err
		}
		task.AssignedTo = assigned
		task.Status = domain.StatusReviewing
	} else {
		// 最终轮决断通过 → 直接入库
		task.Status = domain.StatusPublished
		s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusPublished)
	}
	task.UpdatedAt = now
	return s.reviewStore.UpdateTask(ctx, *task)
}

// resolveAssignedForTask 为任务解析某轮的审核人（需要题目所属题库）。
// 题目可属于多个题库：自动匹配时使用第一个题库（提交时按流程对应题库解析，
// 若题目未归属则按空库匹配全部范围审题人）。
func (s *Service) resolveAssignedForTask(ctx context.Context, task *domain.ReviewTask, configured []string) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	q, err := s.questionStore.GetQuestion(ctx, task.QuestionID)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, fmt.Errorf("题目 %s 不存在", task.QuestionID)
	}
	bankID := ""
	if len(q.BankIDs) > 0 {
		bankID = q.BankIDs[0]
	}
	return s.resolveReviewers(ctx, bankID, nil)
}

// FinalizeRequest 最终把关决断请求。
type FinalizeRequest struct {
	TaskID        string                `json:"task_id"`
	ReviewerID    string                `json:"reviewer_id"` // 决断管理员 ID
	Action        domain.QuestionStatus `json:"action"`      // approved / rejected / revision_required
	Opinion       string                `json:"opinion"`
	IsSystemAdmin bool                  `json:"is_system_admin"` // 系统管理员可跳过把关人名单
}

// Finalize 最终把关人对冲突任务做决断。
// 仅任务处于 conflict 状态时可决断；决断人必须在流程配置的把关人名单内
// （名单为空则任意把关权限者；系统管理员不受名单限制）。
func (s *Service) Finalize(ctx context.Context, req FinalizeRequest) error {
	task, err := s.reviewStore.GetTask(ctx, req.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("审核任务 %s 不存在", req.TaskID)
	}
	if task.Status != domain.StatusConflict {
		return fmt.Errorf("任务当前状态为 %s，只有票数冲突的任务需要最终把关", task.Status)
	}

	// 把关人名单校验（名单为空 = 任意有最终把关权限的用户；系统管理员不受限）
	if len(task.FinalReviewerIDs) > 0 && !req.IsSystemAdmin {
		allowed := false
		for _, id := range task.FinalReviewerIDs {
			if id == req.ReviewerID {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("用户 %s 不在该流程的最终把关人名单中", req.ReviewerID)
		}
	}

	// 校验决断动作
	if req.Action != domain.StatusApproved && req.Action != domain.StatusRejected && req.Action != domain.StatusRevisionRequired {
		return fmt.Errorf("无效的决断动作: %s", req.Action)
	}

	roundIdx := task.CurrentRound - 1
	if roundIdx < 0 || roundIdx >= len(task.RoundResults) {
		return fmt.Errorf("当前轮次 %d 超出任务范围", task.CurrentRound)
	}

	now := time.Now()
	decision := domain.ExpertReview{
		ExpertID:   req.ReviewerID,
		Conclusion: req.Action,
		Opinion:    req.Opinion,
		ReviewedAt: now,
	}
	task.FinalDecision = &decision

	// 决断记录写入审核记录留痕（round_number 使用当前轮）
	record := domain.ReviewRecord{
		ID:          fmt.Sprintf("rec-final-%s-%d", task.ID, now.UnixNano()),
		TaskID:      task.ID,
		QuestionID:  task.QuestionID,
		RoundNumber: task.CurrentRound,
		ExpertID:    req.ReviewerID,
		Conclusion:  req.Action,
		Opinion:     req.Opinion,
		CreatedAt:   now,
	}
	if err := s.reviewStore.SaveRecord(ctx, record); err != nil {
		return fmt.Errorf("保存决断记录失败: %w", err)
	}

	switch req.Action {
	case domain.StatusApproved:
		// 决断通过：本轮视为通过，进入下一轮；最终轮则直接入库
		task.RoundResults[roundIdx].Passed = true
		return s.advanceRoundAfterFinalize(ctx, task, roundIdx)
	case domain.StatusRejected:
		task.Status = domain.StatusRejected
		task.UpdatedAt = now
		if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
			return fmt.Errorf("更新审核任务失败: %w", err)
		}
		s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRejected)
		return nil
	case domain.StatusRevisionRequired:
		task.Status = domain.StatusRevisionRequired
		task.UpdatedAt = now
		if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
			return fmt.Errorf("更新审核任务失败: %w", err)
		}
		s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRevisionRequired)
		return nil
	}
	return nil
}

// GetTask 获取审核任务详情。
func (s *Service) GetTask(ctx context.Context, taskID string) (*domain.ReviewTask, error) {
	return s.reviewStore.GetTask(ctx, taskID)
}

// GetTaskByQuestionID 根据题目 ID 获取审核任务。
func (s *Service) GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error) {
	return s.reviewStore.GetTaskByQuestionID(ctx, questionID)
}

// ListRecords 列出某审核任务的所有记录。
func (s *Service) ListRecords(ctx context.Context, taskID string) ([]domain.ReviewRecord, error) {
	return s.reviewStore.ListRecordsByTaskID(ctx, taskID)
}

// PublishQuestion 将审核通过的题目发布到正式题库。
func (s *Service) PublishQuestion(ctx context.Context, questionID string) error {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("题目 %s 不存在", questionID)
	}
	if q.Status != domain.StatusApproved {
		return fmt.Errorf("题目 %s 当前状态为 %s，只有审核通过的题目才能发布", questionID, q.Status)
	}
	q.Status = domain.StatusPublished
	q.UpdatedAt = time.Now()
	return s.questionStore.SaveQuestion(ctx, *q)
}

func (s *Service) updateQuestionStatus(ctx context.Context, questionID string, status domain.QuestionStatus) {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		fmt.Printf("⚠ 获取题目 %s 失败: %v\n", questionID, err)
		return
	}
	if q == nil {
		return
	}
	q.Status = status
	q.UpdatedAt = time.Now()
	if err := s.questionStore.SaveQuestion(ctx, *q); err != nil {
		fmt.Printf("⚠ 更新题目 %s 状态为 %s 失败: %v\n", questionID, status, err)
	}
}
