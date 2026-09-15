package review

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
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
	// ListReviewers 列出可审核指定题库的用户（角色或直接审题权限 + 用户题库分配范围）。
	ListReviewers(ctx context.Context, bankID string) ([]string, error)
	// ListReviewCandidates 列出全部有审题权限的用户（流程配置选审核人用）。
	ListReviewCandidates(ctx context.Context) ([]Candidate, error)
}

// displayNameResolver 是可选的用户显示名解析能力。最终把关人可以只有
// review:final 权限、没有 review:do 权限，因此不会出现在审题人候选列表中；
// 评语快照仍应保存其中文显示名。
type displayNameResolver interface {
	GetUserDisplayName(ctx context.Context, userID string) (string, error)
}

type Service struct {
	expertStore   storage.ExpertStore
	reviewStore   storage.ReviewStore
	questionStore storage.QuestionStore
	users         UserResolver
	// RequireAICheck 送审强制前置：开启后仅 AI 检查通过（ai_reviewed）
	// 及人工审核回流状态（revision_required/rejected）可提交审核。
	RequireAICheck bool
}

func NewService(expertStore storage.ExpertStore, reviewStore storage.ReviewStore, questionStore storage.QuestionStore, users UserResolver) *Service {
	return &Service{
		expertStore:   expertStore,
		reviewStore:   reviewStore,
		questionStore: questionStore,
		users:         users,
	}
}

// submittableStatuses 返回当前配置下允许提交审核的题目状态集合。
// rejected 是终态锁定：禁止修改与重新提交，不进入送审白名单。
// revision_required 在新模型下仅作为审核任务上的标记保留（题目本体已恢复 ai_reviewed），
// 白名单保留它以兼容存量数据。
func (s *Service) submittableStatuses() map[domain.QuestionStatus]bool {
	if s.RequireAICheck {
		return map[domain.QuestionStatus]bool{
			domain.StatusAIReviewed:       true,
			domain.StatusRevisionRequired: true,
		}
	}
	return map[domain.QuestionStatus]bool{
		domain.StatusAIDraft:          true,
		domain.StatusAutoChecked:      true,
		domain.StatusAIReviewed:       true,
		domain.StatusRevisionRequired: true,
	}
}

// withReviewMutation 先获取跨进程审核锁，再在单一事务中执行关联写入。
// 锁防止并发覆盖，事务保证审核记录、任务和题目状态要么全部提交、要么全部回滚。
func (s *Service) withReviewMutation(ctx context.Context, fn func(txCtx context.Context) error) (mutationErr error) {
	release, err := s.reviewStore.AcquireReviewMutationLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := release(); mutationErr == nil {
			mutationErr = releaseErr
		}
	}()
	return s.reviewStore.WithReviewTransaction(ctx, s.questionStore, fn)
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
	release, err := s.reviewStore.AcquireReviewMutationLock(ctx)
	if err != nil {
		return err
	}
	err = s.createFlowLocked(ctx, flow)
	if releaseErr := release(); err == nil {
		err = releaseErr
	}
	return err
}

func (s *Service) createFlowLocked(ctx context.Context, flow domain.ReviewFlowConfig) error {
	if flow.ID == "" {
		return fmt.Errorf("流程ID不能为空")
	}
	// 重复 ID 拒绝（防止静默覆盖已有流程）；检查在锁内执行，避免并发创建同 ID 流程时查重被绕过
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
	release, err := s.reviewStore.AcquireReviewMutationLock(ctx)
	if err != nil {
		return err
	}
	err = s.updateFlowLocked(ctx, flow)
	if releaseErr := release(); err == nil {
		err = releaseErr
	}
	return err
}

func (s *Service) updateFlowLocked(ctx context.Context, flow domain.ReviewFlowConfig) error {
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
	release, err := s.reviewStore.AcquireReviewMutationLock(ctx)
	if err != nil {
		return err
	}
	err = s.deleteFlowLocked(ctx, id)
	if releaseErr := release(); err == nil {
		err = releaseErr
	}
	return err
}

func (s *Service) deleteFlowLocked(ctx context.Context, id string) error {
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
	return s.SubmitQuestionForBank(ctx, questionID, flowID, "")
}

// SubmitQuestionForOwner 供命题老师提交本人题目。新工作流不要求出题人选择
// 分类子题库；审核流程若绑定了分类仍由流程本身校验，未绑定时任务保留空分类快照。
func (s *Service) SubmitQuestionForOwner(ctx context.Context, questionID, flowID string) (*domain.ReviewTask, error) {
	var task *domain.ReviewTask
	err := s.withReviewMutation(ctx, func(txCtx context.Context) error {
		var submitErr error
		task, submitErr = s.submitQuestionLocked(txCtx, questionID, flowID, "", true)
		return submitErr
	})
	return task, err
}

// SubmitQuestionForBank 将题目按明确的分类子题库提交到审核流程。
// submissionBankID 会固化到任务，后续分类变化不会改变本批次的专家路由。
// 为空时仅对“流程已绑定题库”或“题目只属于一个子题库”的情况做安全推导。
func (s *Service) SubmitQuestionForBank(ctx context.Context, questionID, flowID, submissionBankID string) (*domain.ReviewTask, error) {
	var task *domain.ReviewTask
	err := s.withReviewMutation(ctx, func(txCtx context.Context) error {
		var submitErr error
		task, submitErr = s.submitQuestionLocked(txCtx, questionID, flowID, submissionBankID, false)
		return submitErr
	})
	return task, err
}

func (s *Service) submitQuestionLocked(ctx context.Context, questionID, flowID, requestedBankID string, allowNoBank bool) (*domain.ReviewTask, error) {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if q == nil {
		return nil, fmt.Errorf("题目 %s 不存在", questionID)
	}
	if err := q.ValidateForReview(); err != nil {
		return nil, fmt.Errorf("题目 %s 无法提交审核: %w", questionID, err)
	}

	// 只允许特定状态的题目提交审核
	allowedStatuses := s.submittableStatuses()
	if !allowedStatuses[q.Status] {
		switch {
		case s.RequireAICheck && (q.Status == domain.StatusAIDraft || q.Status == domain.StatusAutoChecked):
			return nil, fmt.Errorf("%w: 题目 %s 尚未通过 AI 质量检查（当前状态 %s），请等待自动检查完成后再提交审核", domain.ErrReviewNotSubmittable, questionID, q.Status)
		case q.Status == domain.StatusRejected:
			return nil, fmt.Errorf("%w: 题目 %s 已被驳回（终态锁定），不允许修改或重新提交审核", domain.ErrReviewNotSubmittable, questionID)
		default:
			return nil, fmt.Errorf("%w: 题目 %s 当前状态为 %s，不允许提交审核", domain.ErrReviewNotSubmittable, questionID, q.Status)
		}
	}

	flow, err := s.reviewStore.GetFlowConfig(ctx, flowID)
	if err != nil {
		return nil, err
	}
	if flow == nil {
		return nil, fmt.Errorf("审核流程 %s 不存在", flowID)
	}
	submissionBankID, err := resolveSubmissionBank(q, flow, requestedBankID, allowNoBank)
	if err != nil {
		return nil, err
	}

	existing, _ := s.reviewStore.GetTaskByQuestionID(ctx, questionID)
	if existing != nil {
		// 如果是"需修改"状态，恢复为审核中（保持原轮，清空本轮投票重新审核，进入新一批次）
		if existing.Status == domain.StatusRevisionRequired {
			if q.Version <= existing.QuestionVersion {
				return nil, fmt.Errorf("%w: 题目已退回修改，但当前仍是送审版本 %d；请先在「待我修改」中提交新版本后再送审", domain.ErrReviewNotSubmittable, existing.QuestionVersion)
			}
			if existing.FlowID != flowID {
				return nil, fmt.Errorf("%w: 题目已按审核流程 %s 退回修改，修改后需沿用原流程重新送审", domain.ErrReviewNotSubmittable, existing.FlowID)
			}
			roundIdx := existing.CurrentRound - 1
			if roundIdx < 0 || roundIdx >= len(flow.Rounds) {
				return nil, fmt.Errorf("审核流程 %s 不包含第 %d 轮", flowID, existing.CurrentRound)
			}
			assigned, err := s.resolveReviewers(ctx, submissionBankID, flow.Rounds[roundIdx].ExpertIDs)
			if err != nil {
				return nil, err
			}
			existing.Status = domain.StatusReviewing
			existing.FlowID = flowID
			existing.SubmissionBankID = submissionBankID
			existing.AssignedTo = assigned
			existing.FinalReviewerIDs = flow.FinalReviewerIDs
			existing.QuestionPrevStatus = q.Status // 保留本次重新送审前的状态快照
			existing.QuestionVersion = q.Version
			if roundIdx >= 0 && roundIdx < len(existing.RoundResults) {
				existing.RoundResults[roundIdx].Reviews = nil
				existing.RoundResults[roundIdx].ApprovedCount = 0
				existing.RoundResults[roundIdx].RejectedCount = 0
				existing.RoundResults[roundIdx].RevisionCount = 0
				existing.RoundResults[roundIdx].Passed = false
			}
			existing.FinalDecision = nil
			existing.Attempt++
			existing.UpdatedAt = time.Now()
			if err := s.reviewStore.UpdateTask(ctx, *existing); err != nil {
				return nil, err
			}
			if err := s.updateQuestionStatus(ctx, questionID, domain.StatusReviewing); err != nil {
				return nil, err
			}
			return existing, nil
		}
		// 已通过或已归档（published/archived）的题目由管理员撤回到 ai_reviewed 后，
		// 或题目被编辑回草稿（旧任务终态）时，允许创建新任务重新走流程。
		isTerminalTask := existing.Status == domain.StatusPublished || existing.Status == domain.StatusArchived
		if (q.Status == domain.StatusAIDraft || q.Status == domain.StatusAIReviewed) && isTerminalTask {
			// 继续向下创建新任务
		} else {
			return nil, fmt.Errorf("%w: 题目 %s 已提交审核（任务状态：%s），无需重复提交", domain.ErrReviewNotSubmittable, questionID, existing.Status)
		}
	}

	// 解析第一轮审核人（显式配置优先，否则按审题权限+题库范围自动匹配）
	assigned, err := s.resolveReviewers(ctx, submissionBankID, flow.Rounds[0].ExpertIDs)
	if err != nil {
		return nil, err
	}

	// 更新题目状态（版本号保持不变，版本由内容编辑递增，AI 检查结果依赖它判断过期）
	// 记录提交前状态快照，再更新题目为审核中
	prevStatus := q.Status
	q.Status = domain.StatusReviewing
	q.UpdatedAt = time.Now()
	if err := s.questionStore.SaveQuestion(ctx, *q); err != nil {
		return nil, fmt.Errorf("更新题目状态失败: %w", err)
	}

	// 创建审核任务（保留提交前状态快照，兼容历史任务数据）
	task := domain.ReviewTask{
		ID:                 fmt.Sprintf("task-%s-%d", questionID, time.Now().UnixNano()),
		QuestionID:         questionID,
		FlowID:             flowID,
		SubmissionBankID:   submissionBankID,
		CurrentRound:       1,
		Status:             domain.StatusReviewing,
		AssignedTo:         assigned,
		FinalReviewerIDs:   flow.FinalReviewerIDs,
		QuestionPrevStatus: prevStatus,
		QuestionVersion:    q.Version,
		Attempt:            1,
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
// 显式配置的审核人直接使用；为空时按"审题权限 + 题库分配范围"自动匹配。
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
		return nil, fmt.Errorf("题库「%s」没有分配审题人：请给用户分配审题权限并设置题库范围，或在流程中显式指定审核人", bankNameOrAll(bankID))
	}
	return reviewers, nil
}

func resolveSubmissionBank(q *domain.A2Question, flow *domain.ReviewFlowConfig, requested string, allowNoBank bool) (string, error) {
	bankID := strings.TrimSpace(requested)
	if bankID == "" && flow != nil {
		bankID = strings.TrimSpace(flow.BankID)
	}
	if bankID == "" && q != nil && len(q.BankIDs) == 1 {
		bankID = q.BankIDs[0]
	}
	if bankID == "" {
		if allowNoBank && (flow == nil || flow.BankID == "") {
			return "", nil
		}
		return "", fmt.Errorf("%w: 题目必须先归入一个分类子题库；通用流程送审时也必须明确选择本次提交题库", domain.ErrReviewBadRequest)
	}
	if flow != nil && flow.BankID != "" && flow.BankID != bankID {
		return "", fmt.Errorf("%w: 审核流程 %s 绑定的是题库 %s，不能按题库 %s 提交", domain.ErrReviewBadRequest, flow.ID, flow.BankID, bankID)
	}
	if q == nil {
		return "", fmt.Errorf("%w: 题目不存在", domain.ErrReviewBadRequest)
	}
	for _, candidate := range q.BankIDs {
		if candidate == bankID {
			return bankID, nil
		}
	}
	return "", fmt.Errorf("%w: 题目 %s 不属于本次提交题库 %s", domain.ErrReviewBadRequest, q.ID, bankID)
}

func bankNameOrAll(bankID string) string {
	if bankID == "" {
		return "待归类题目"
	}
	return bankID
}

// ===== 审核结果汇总 =====

// ReviewResultItem 单题审核结果汇总：题目 + 任务 + 全部专家审核记录（评语）。
type ReviewResultItem struct {
	Question    domain.A2Question     `json:"question"`       // 题目
	Task        *domain.ReviewTask    `json:"task,omitempty"` // 当前审核任务（可为空=从未提交）
	Records     []domain.ReviewRecord `json:"records"`        // 全部审核记录（含专家评语）
	FinalStatus string                `json:"final_status"`   // 最终状态分类：pending/reviewing/conflict/rejected/revision_required/published
}

// ReviewStats 审核结果统计。
type ReviewStats struct {
	Published        int `json:"published"`         // 已通过（唯一成功终态）
	Rejected         int `json:"rejected"`          // 已驳回
	RevisionRequired int `json:"revision_required"` // 需修改
	Reviewing        int `json:"reviewing"`         // 审核中
	Conflict         int `json:"conflict"`          // 待决断
	Pending          int `json:"pending"`           // 未提交审核
	Total            int `json:"total"`             // 题目总数
}

func addReviewStat(stats *ReviewStats, finalStatus string, count int) {
	stats.Total += count
	switch finalStatus {
	case "published":
		stats.Published += count
	case "rejected":
		stats.Rejected += count
	case "revision_required":
		stats.RevisionRequired += count
	case "reviewing":
		stats.Reviewing += count
	case "conflict":
		stats.Conflict += count
	default:
		stats.Pending += count
	}
}

// SearchResultsForViewer 优先使用生产存储的数据库端审核记录查询能力，只加载当前页。
// 轻量测试存储未实现该能力时回退到内存筛选，保持接口兼容。
func (s *Service) SearchResultsForViewer(ctx context.Context, query storage.ReviewResultQuery, userID string, fullAccess bool) ([]ReviewResultItem, int, ReviewStats, error) {
	if fastStore, ok := s.reviewStore.(storage.ReviewResultQueryStore); ok {
		page, err := fastStore.SearchReviewResults(ctx, query)
		if err != nil {
			return nil, 0, ReviewStats{}, err
		}
		stats := ReviewStats{}
		for status, count := range page.Stats {
			addReviewStat(&stats, status, count)
		}
		items := make([]ReviewResultItem, 0, len(page.Rows))
		for _, row := range page.Rows {
			task := row.Task
			if !fullAccess && task != nil && taskInFlight(task.Status) {
				task = sanitizeTaskForReviewer(task)
			}
			items = append(items, ReviewResultItem{Question: row.Question, Task: task, FinalStatus: row.FinalStatus})
		}
		return items, page.Total, stats, nil
	}

	items, _, err := s.ListResultsForViewer(ctx, userID, fullAccess)
	if err != nil {
		return nil, 0, ReviewStats{}, err
	}
	stats := ReviewStats{}
	filtered := make([]ReviewResultItem, 0, len(items))
	for _, item := range items {
		if matchesReviewQuestionFilter(item.Question, query.StatsFilter) {
			addReviewStat(&stats, item.FinalStatus, 1)
		}
		if !matchesReviewQuestionFilter(item.Question, query.Filter) ||
			(query.FinalStatus != "" && item.FinalStatus != query.FinalStatus) {
			continue
		}
		filtered = append(filtered, item)
	}
	total := len(filtered)
	page, pageSize := query.Page, query.PageSize
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return filtered[start:end], total, stats, nil
}

func matchesReviewQuestionFilter(q domain.A2Question, filter storage.QuestionFilter) bool {
	if filter.Status != "" && string(q.Status) != filter.Status {
		return false
	}
	if len(filter.Tiers) > 0 {
		matched := false
		for _, tier := range filter.Tiers {
			if q.Tier() == domain.QuestionTier(tier) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if filter.Difficulty != "" && string(q.Difficulty) != filter.Difficulty {
		return false
	}
	if filter.DifficultyBand != "" && !storage.DifficultyMatchesBand(string(q.Difficulty), filter.DifficultyBand) {
		return false
	}
	if len(filter.Professions) > 0 {
		matched := false
		for _, profession := range filter.Professions {
			if q.Profession == profession {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if filter.OutlineCode != "" && !strings.HasPrefix(q.OutlineCode, filter.OutlineCode) {
		return false
	}
	if !storage.QuestionMatchesKeyword(q, filter.Keyword) {
		return false
	}
	if filter.BankID != "" {
		found := false
		for _, bankID := range q.BankIDs {
			if bankID == filter.BankID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Unclassified && len(q.BankIDs) > 0 {
		return false
	}
	if filter.ClassifiableOnly && q.Status != domain.StatusAIDraft && q.Status != domain.StatusAutoChecked &&
		q.Status != domain.StatusAIReviewed && q.Status != domain.StatusRevisionRequired {
		return false
	}
	if filter.ScopeRestricted {
		found := false
		for _, bankID := range q.BankIDs {
			for _, allowed := range filter.BankScope {
				if bankID == allowed {
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// finalStatusOf 根据题目与任务计算最终状态分类。
func finalStatusOf(q domain.A2Question, task *domain.ReviewTask) string {
	if task != nil {
		switch task.Status {
		case domain.StatusRevisionRequired:
			return "revision_required"
		case domain.StatusConflict:
			return "conflict"
		}
	}
	switch q.Status {
	case domain.StatusPublished:
		return "published"
	case domain.StatusRejected:
		return "rejected"
	case domain.StatusRevisionRequired:
		return "revision_required"
	case domain.StatusReviewing:
		return "reviewing"
	default:
		return "pending"
	}
}

// ListResults 汇总全部题目的审核结果（题目 + 任务 + 统计）。
// 按查看者做分层隔离：全量可见者（把关人/管理员）看到全部评语；
// 普通用户对进行中的任务（审核中/待决断/需修改）只能看到自己的记录与脱敏任务摘要，
// 已结束的任务（已通过/已驳回/归档）属于历史档案，保留完整评语。
// 评语不在本方法加载：调用方筛选分页定稿后，用 AttachRecordsForItems 为当页条目补齐，
// 避免审核记录表增长后每次请求全表加载。
func (s *Service) ListResultsForViewer(ctx context.Context, userID string, fullAccess bool) ([]ReviewResultItem, ReviewStats, error) {
	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		return nil, ReviewStats{}, err
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
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

	items := make([]ReviewResultItem, 0, len(questions))
	stats := ReviewStats{Total: len(questions)}
	for i := range questions {
		q := &questions[i]
		task := taskByQuestion[q.ID]
		fs := finalStatusOf(*q, task)
		// 非全量可见者：进行中的任务做脱敏裁剪（评语由 AttachRecordsForItems 按同一规则裁剪）
		if !fullAccess && task != nil && taskInFlight(task.Status) {
			task = sanitizeTaskForReviewer(task)
		}
		items = append(items, ReviewResultItem{
			Question:    *q,
			Task:        task,
			FinalStatus: fs,
		})
		switch fs {
		case "published":
			stats.Published++
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

// AttachRecordsForItems 为筛选分页后的汇总条目加载审核记录（仅当页任务）。
// 非全量可见者的进行中任务保持隔离规则：只保留该用户自己的评语。
func (s *Service) AttachRecordsForItems(ctx context.Context, items []ReviewResultItem, userID string, fullAccess bool) error {
	taskIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.Task != nil {
			taskIDs = append(taskIDs, item.Task.ID)
		}
	}
	if len(taskIDs) == 0 {
		return nil
	}
	records, err := s.reviewStore.ListRecordsByTaskIDs(ctx, taskIDs)
	if err != nil {
		return err
	}
	recordsByTask := make(map[string][]domain.ReviewRecord, len(taskIDs))
	for _, r := range records {
		recordsByTask[r.TaskID] = append(recordsByTask[r.TaskID], r)
	}
	for i := range items {
		item := &items[i]
		if item.Task == nil {
			item.Records = nil
			continue
		}
		taskRecords := recordsByTask[item.Task.ID]
		if !fullAccess && taskInFlight(item.Task.Status) {
			taskRecords = ownRecords(taskRecords, userID)
		}
		item.Records = taskRecords
	}
	return nil
}

// MyTaskItem 待我审核的任务（含题目、流程轮次、投票进度）。
type MyTaskItem struct {
	Task            *domain.ReviewTask `json:"task"`             // 审核任务
	Question        domain.A2Question  `json:"question"`         // 题目完整信息
	FlowName        string             `json:"flow_name"`        // 流程名
	RoundIndex      int                `json:"round_index"`      // 当前轮序号（从1）
	RoundCount      int                `json:"round_count"`      // 总轮数
	RoundName       string             `json:"round_name"`       // 当前轮名称
	Voted           int                `json:"voted"`            // 已投人数
	Assigned        int                `json:"assigned"`         // 应投人数
	Approved        int                `json:"approved"`         // 通过票
	Rejected        int                `json:"rejected"`         // 驳回票
	Revision        int                `json:"revision"`         // 需修改票
	MyVoted         bool               `json:"my_voted"`         // 我是否已投过本轮
	CanVote         bool               `json:"can_vote"`         // 我是否可以投票
	NeedsDecision   bool               `json:"needs_decision"`   // 待决断（把关人可见）
	VersionMismatch bool               `json:"version_mismatch"` // 任务版本与当前题目不一致
}

// MyTasks 列出待我审核的任务：
//   - 仅当前轮分配名单包含我、且我尚未投票的任务（reviewing / revision_required）
//   - 无论是否为把关人：自己的审核工作结束（投过票）即移开列表
//   - 待决断任务不在此列（把关人请用 MyDecisions / 「待决断」页面）
func (s *Service) MyTasks(ctx context.Context, userID string, hasFinalRight bool) ([]MyTaskItem, error) {
	// SQL 端预过滤：按题去重取最新任务 + 状态 reviewing + 当前轮分配含我，
	// 避免装载全部任务与全部题目；下方细粒度判断与原实现逐字一致。
	tasks, err := s.listTodoTasks(ctx, storage.ReviewTodoQuery{
		Statuses:        []string{string(domain.StatusReviewing)},
		AssignedTo:      userID,
		DedupByQuestion: true,
	})
	if err != nil {
		return nil, err
	}
	questionByID, err := s.loadTodoQuestions(ctx, tasks)
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

	var result []MyTaskItem
	for i := range tasks {
		task := &tasks[i]
		// 待决断/终态任务不显示（待决断走「待决断」页面）；
		// revision_required 也暂不显示（等管理员编辑题目重新提交后恢复 reviewing 再出现）
		if task.Status != domain.StatusReviewing {
			continue
		}
		q := questionByID[task.QuestionID]
		if q == nil {
			continue // 题目已删除（原实现按题目遍历自然跳过）
		}
		roundIdx := task.CurrentRound - 1
		if roundIdx < 0 || roundIdx >= len(task.RoundResults) {
			continue
		}
		round := &task.RoundResults[roundIdx]

		// 当前轮分配名单必须包含我（只显示自己的审核工作；SQL 已预过滤，此处保持一致校验）
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
			Task:            task,
			Question:        *q,
			RoundIndex:      task.CurrentRound,
			Voted:           len(round.Reviews),
			Assigned:        len(task.AssignedTo),
			Approved:        round.ApprovedCount,
			Rejected:        round.RejectedCount,
			Revision:        round.RevisionCount,
			MyVoted:         myVoted,
			CanVote:         task.QuestionVersion == q.Version,
			NeedsDecision:   false,
			VersionMismatch: task.QuestionVersion != q.Version,
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
		// 同轮隔离：非把关人看不到他人实时票数与评语，避免界面动态更新他人状态
		if !hasFinalRight {
			item.Task = sanitizeTaskForReviewer(task)
			item.Voted = 0
			item.Approved = 0
			item.Rejected = 0
			item.Revision = 0
		}
		result = append(result, item)
	}
	// 按更新时间倒序（最新提交的优先审核）
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Task.UpdatedAt.After(result[j].Task.UpdatedAt)
	})
	return result, nil
}

// listTodoTasks 经由存储的待办预过滤能力取任务；存储未实现该能力时
// 回退为 ListAllTasks + 内存过滤（轻量测试存储路径），语义一致。
func (s *Service) listTodoTasks(ctx context.Context, query storage.ReviewTodoQuery) ([]domain.ReviewTask, error) {
	if todoStore, ok := s.reviewStore.(storage.ReviewTodoQueryStore); ok {
		return todoStore.ListReviewTodoTasks(ctx, query)
	}
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domain.ReviewTask, 0, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		status := string(domain.CanonicalLifecycleStatus(t.Status))
		matched := false
		for _, want := range query.Statuses {
			if want == status {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if query.AssignedTo != "" {
			in := false
			for _, id := range t.AssignedTo {
				if id == query.AssignedTo {
					in = true
					break
				}
			}
			if !in {
				continue
			}
		}
		if !query.FinalAny && query.FinalReviewer != "" && len(t.FinalReviewerIDs) != 0 {
			in := false
			for _, id := range t.FinalReviewerIDs {
				if id == query.FinalReviewer {
					in = true
					break
				}
			}
			if !in {
				continue
			}
		}
		filtered = append(filtered, *t)
	}
	if query.DedupByQuestion {
		latest := make(map[string]domain.ReviewTask, len(filtered))
		for _, t := range filtered {
			cur, ok := latest[t.QuestionID]
			if !ok || t.CreatedAt.After(cur.CreatedAt) {
				latest[t.QuestionID] = t
			}
		}
		filtered = filtered[:0]
		for _, t := range latest {
			filtered = append(filtered, t)
		}
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
		})
	}
	return filtered, nil
}

// loadTodoQuestions 批量装载待办任务对应的题目，返回 questionID → 题目。
func (s *Service) loadTodoQuestions(ctx context.Context, tasks []domain.ReviewTask) (map[string]*domain.A2Question, error) {
	ids := make([]string, 0, len(tasks))
	for i := range tasks {
		ids = append(ids, tasks[i].QuestionID)
	}
	questions, err := s.questionStore.GetQuestionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*domain.A2Question, len(questions))
	for i := range questions {
		byID[questions[i].ID] = &questions[i]
	}
	return byID, nil
}

// MyDecisions 列出待我决断的任务（最终把关人专用）：
// 所有 conflict（待决断）任务，且我在该流程的把关人名单内（名单空=全部把关人可见）。
// 含题目完整信息、流程轮次、票数统计，供「待决断」独立页面使用。
func (s *Service) MyDecisions(ctx context.Context, userID string) ([]MyTaskItem, error) {
	return s.MyDecisionsForViewer(ctx, userID, false)
}

// MyDecisionsForViewer 支持系统管理员查看全部待决断任务；普通把关人仍受任务快照名单限制。
func (s *Service) MyDecisionsForViewer(ctx context.Context, userID string, isSystemAdmin bool) ([]MyTaskItem, error) {
	// SQL 端预过滤：按题去重取最新任务 + 状态 conflict + 把关人名单
	// （名单空=任意把关人；系统管理员不按名单过滤），细粒度判断保持原样。
	query := storage.ReviewTodoQuery{
		Statuses:        []string{string(domain.StatusConflict)},
		DedupByQuestion: true,
	}
	if isSystemAdmin {
		query.FinalAny = true
	} else {
		query.FinalReviewer = userID
	}
	tasks, err := s.listTodoTasks(ctx, query)
	if err != nil {
		return nil, err
	}
	questionByID, err := s.loadTodoQuestions(ctx, tasks)
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

	var result []MyTaskItem
	for i := range tasks {
		task := &tasks[i]
		if task.Status != domain.StatusConflict {
			continue
		}
		q := questionByID[task.QuestionID]
		if q == nil {
			continue // 题目已删除（原实现按题目遍历自然跳过）
		}
		// 把关人名单校验（名单空 = 任意把关人；SQL 已预过滤，此处保持一致校验）
		if len(task.FinalReviewerIDs) > 0 && !isSystemAdmin {
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
			Task:            task,
			Question:        *q,
			RoundIndex:      task.CurrentRound,
			Voted:           len(round.Reviews),
			Assigned:        len(task.AssignedTo),
			Approved:        round.ApprovedCount,
			Rejected:        round.RejectedCount,
			Revision:        round.RevisionCount,
			CanVote:         false,
			NeedsDecision:   true,
			VersionMismatch: task.QuestionVersion != q.Version,
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
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Task.UpdatedAt.After(result[j].Task.UpdatedAt)
	})
	return result, nil
}

// ReviewRequest 专家审核请求。
type ReviewRequest struct {
	TaskID        string                `json:"task_id"`
	ExpertID      string                `json:"expert_id"`
	Action        domain.QuestionStatus `json:"action"`          // approved / rejected / revision_required
	Opinion       string                `json:"opinion"`         // 自由文本意见（兼容旧客户端；非通过时视为「其他」栏）
	Comment       *domain.ReviewComment `json:"comment"`         // 结构化评语（题干/选项/答案与解析/其他）
	HasFinalRight bool                  `json:"has_final_right"` // 最终把关人可审任意轮
}

// effectiveComment 归并评语输入：结构化评语优先；旧版自由文本意见映射到「其他」栏。
func (req ReviewRequest) effectiveComment() *domain.ReviewComment {
	if req.Comment != nil {
		return req.Comment
	}
	if opinion := strings.TrimSpace(req.Opinion); opinion != "" {
		return &domain.ReviewComment{Other: opinion}
	}
	return nil
}

// validateComment 非通过结论必须留下至少一栏评语；通过时评语可选。
func validateComment(action domain.QuestionStatus, comment *domain.ReviewComment) error {
	if action == domain.StatusApproved {
		return nil
	}
	if comment.IsEmpty() {
		return fmt.Errorf("驳回或退回修改必须填写评语：请在题干/选项/答案与解析/其他至少一栏说明理由")
	}
	return nil
}

// Review 专家执行审核（投票）。
// 新规则：单人反对不再立即退回/驳回，所有分配审核人审核完毕后自动统计：
//   - 无反对票且通过数达门槛 → 本轮通过（进下一轮/完成）
//   - 全员审完但存在反对票 → 任务进入 conflict，等待最终把关人决断
//
// 评语规则：通过时评语可选；驳回/需修改必须填写结构化评语（至少一栏）。
func (s *Service) Review(ctx context.Context, req ReviewRequest) error {
	return s.withReviewMutation(ctx, func(txCtx context.Context) error {
		return s.reviewLocked(txCtx, req)
	})
}

func (s *Service) reviewLocked(ctx context.Context, req ReviewRequest) error {
	task, err := s.reviewStore.GetTask(ctx, req.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("审核任务 %s 不存在", req.TaskID)
	}
	if err := s.ensureTaskQuestionVersion(ctx, task); err != nil {
		return err
	}

	flow, err := s.reviewStore.GetFlowConfig(ctx, task.FlowID)
	if err != nil {
		return err
	}
	if flow == nil {
		return fmt.Errorf("审核流程 %s 不存在（可能已被删除）", task.FlowID)
	}

	// 只有审核中的任务允许投票；需修改必须先提交新版本并由管理员重新送审。
	if task.Status != domain.StatusReviewing {
		if task.Status == domain.StatusConflict {
			return fmt.Errorf("本轮审核票数冲突，请等待最终把关人决断，不能再投票")
		}
		if task.Status == domain.StatusRevisionRequired {
			return fmt.Errorf("题目已退回修改，需提交新版本后由管理员重新送审，不能继续本轮投票")
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

	// 评语校验：通过时可选，非通过必须写至少一栏
	comment := req.effectiveComment()
	if err := validateComment(req.Action, comment); err != nil {
		return err
	}

	// 记录审核结果
	now := time.Now()
	expertReview := domain.ExpertReview{
		ExpertID:   req.ExpertID,
		ExpertName: s.resolveExpertName(ctx, req.ExpertID),
		Conclusion: req.Action,
		Opinion:    comment.Flatten(),
		Comment:    comment,
		ReviewedAt: now,
	}
	task.RoundResults[roundIdx].Reviews = append(task.RoundResults[roundIdx].Reviews, expertReview)

	// 保存审核记录（ID 带时间戳，同一专家打回后重新审核不会主键冲突）
	record := domain.ReviewRecord{
		ID:          fmt.Sprintf("rec-%s-%d-%s-%d", task.ID, task.CurrentRound, req.ExpertID, now.UnixNano()),
		TaskID:      task.ID,
		QuestionID:  task.QuestionID,
		RoundNumber: task.CurrentRound,
		Attempt:     attemptOrOne(task.Attempt),
		ExpertID:    req.ExpertID,
		ExpertName:  expertReview.ExpertName,
		Conclusion:  req.Action,
		Opinion:     expertReview.Opinion,
		Comment:     comment,
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
			if err := s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRejected); err != nil {
				return err
			}
			return nil
		}
		if req.Action == domain.StatusRevisionRequired {
			// 退回修改：任务标记 revision_required，题目本体恢复 ai_reviewed
			task.Status = domain.StatusRevisionRequired
			task.UpdatedAt = now
			if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
				return fmt.Errorf("更新审核任务失败: %w", err)
			}
			if err := s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusAIReviewed); err != nil {
				return err
			}
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
		if err := s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusPublished); err != nil {
			return err
		}
	}
	task.UpdatedAt = now
	return s.reviewStore.UpdateTask(ctx, *task)
}

// resolveAssignedForTask 为任务解析某轮审核人。
// 始终使用任务提交时固化的分类子题库，不能被题目后续分类调整改变。
func (s *Service) resolveAssignedForTask(ctx context.Context, task *domain.ReviewTask, configured []string) ([]string, error) {
	if len(configured) > 0 {
		return configured, nil
	}
	if task.SubmissionBankID == "" {
		// 兼容迁移前已存在的任务：只有当前题目恰好属于一个子题库时才能无歧义补齐。
		q, err := s.questionStore.GetQuestion(ctx, task.QuestionID)
		if err != nil {
			return nil, err
		}
		if q == nil || len(q.BankIDs) != 1 {
			return nil, fmt.Errorf("审核任务 %s 缺少提交题库快照，无法安全分配下一轮审核人", task.ID)
		}
		task.SubmissionBankID = q.BankIDs[0]
	}
	return s.resolveReviewers(ctx, task.SubmissionBankID, nil)
}

// FinalizeRequest 最终把关决断请求。
type FinalizeRequest struct {
	TaskID        string                `json:"task_id"`
	ReviewerID    string                `json:"reviewer_id"`     // 决断管理员 ID
	Action        domain.QuestionStatus `json:"action"`          // approved / rejected / revision_required
	Opinion       string                `json:"opinion"`         // 自由文本意见（兼容旧客户端）
	Comment       *domain.ReviewComment `json:"comment"`         // 结构化评语
	IsSystemAdmin bool                  `json:"is_system_admin"` // 系统管理员可跳过把关人名单
}

// Finalize 最终把关人对冲突任务做决断。
// 仅任务处于 conflict 状态时可决断；决断人必须在流程配置的把关人名单内
// （名单为空则任意把关权限者；系统管理员不受名单限制）。
func (s *Service) Finalize(ctx context.Context, req FinalizeRequest) error {
	return s.withReviewMutation(ctx, func(txCtx context.Context) error {
		return s.finalizeLocked(txCtx, req)
	})
}

func (s *Service) finalizeLocked(ctx context.Context, req FinalizeRequest) error {
	task, err := s.reviewStore.GetTask(ctx, req.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("审核任务 %s 不存在", req.TaskID)
	}
	if err := s.ensureTaskQuestionVersion(ctx, task); err != nil {
		return err
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
	// 决断评语：通过时可选，非通过必须写至少一栏
	comment := (&ReviewRequest{Opinion: req.Opinion, Comment: req.Comment}).effectiveComment()
	if err := validateComment(req.Action, comment); err != nil {
		return err
	}
	if req.Action == domain.StatusApproved {
		q, err := s.questionStore.GetQuestion(ctx, task.QuestionID)
		if err != nil {
			return err
		}
		if q == nil {
			return fmt.Errorf("题目 %s 不存在", task.QuestionID)
		}
		if err := q.ValidateForReview(); err != nil {
			return fmt.Errorf("题目 %s 无法通过最终决断: %w", task.QuestionID, err)
		}
	}

	roundIdx := task.CurrentRound - 1
	if roundIdx < 0 || roundIdx >= len(task.RoundResults) {
		return fmt.Errorf("当前轮次 %d 超出任务范围", task.CurrentRound)
	}

	now := time.Now()
	decision := domain.ExpertReview{
		ExpertID:   req.ReviewerID,
		ExpertName: s.resolveExpertName(ctx, req.ReviewerID),
		Conclusion: req.Action,
		Opinion:    comment.Flatten(),
		Comment:    comment,
		ReviewedAt: now,
	}
	task.FinalDecision = &decision

	// 决断记录写入审核记录留痕（round_number 使用当前轮）
	record := domain.ReviewRecord{
		ID:          fmt.Sprintf("rec-final-%s-%d", task.ID, now.UnixNano()),
		TaskID:      task.ID,
		QuestionID:  task.QuestionID,
		RoundNumber: task.CurrentRound,
		Attempt:     attemptOrOne(task.Attempt),
		ExpertID:    req.ReviewerID,
		ExpertName:  decision.ExpertName,
		Conclusion:  req.Action,
		Opinion:     decision.Opinion,
		Comment:     comment,
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
		if err := s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRejected); err != nil {
			return err
		}
		return nil
	case domain.StatusRevisionRequired:
		// 退回修改：任务标记为 revision_required（生成者"待我修改"入口），
		// 题目本体恢复 ai_reviewed（AI 检查已通过的状态），生成者修改后由管理员重新送审。
		task.Status = domain.StatusRevisionRequired
		task.UpdatedAt = now
		if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
			return fmt.Errorf("更新审核任务失败: %w", err)
		}
		if err := s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusAIReviewed); err != nil {
			return err
		}
		return nil
	}
	return nil
}

// GetTask 获取审核任务详情。
func (s *Service) GetTask(ctx context.Context, taskID string) (*domain.ReviewTask, error) {
	return s.reviewStore.GetTask(ctx, taskID)
}

// RevisionItem 待修改条目：被退回修改的任务及其题目。
type RevisionItem struct {
	Task     domain.ReviewTask `json:"task"`
	Question domain.A2Question `json:"question"`
	// Reason 是最近一次"需修改"的退修意见（轮内投票或把关人决断）。
	Reason string `json:"reason"`
	// Modified 表示生成者已提交修改（题目版本已新于任务绑定的送审版本），等待管理员重新送审。
	Modified bool `json:"modified"`
}

// MyRevisions 列出退回给指定生成者修改的题目：
// 审核任务处于 revision_required 且题目由该用户创建（CreatedBy 匹配）。
// 按分层可见性规则裁剪：生成者仅能看到"需修改"的退修意见，看不到各专家的态度与通过/驳回评语。
func (s *Service) MyRevisions(ctx context.Context, createdBy string) ([]RevisionItem, error) {
	// SQL 端预过滤：仅装载 revision_required 任务；出题人归属/题目存在性
	// 校验保持原实现逐题判定。
	tasks, err := s.listTodoTasks(ctx, storage.ReviewTodoQuery{
		Statuses: []string{string(domain.StatusRevisionRequired)},
	})
	if err != nil {
		return nil, err
	}
	items := make([]RevisionItem, 0, 8)
	for _, task := range tasks {
		if task.Status != domain.StatusRevisionRequired {
			continue
		}
		q, err := s.questionStore.GetQuestion(ctx, task.QuestionID)
		if err != nil || q == nil {
			continue
		}
		if q.CreatedBy != createdBy {
			continue
		}
		// 深拷贝并裁剪：仅保留退修意见
		trimmed, err := cloneTaskForRevisions(task)
		if err != nil {
			return nil, err
		}
		// 退修意见：优先取把关人决断记录，其次轮内需修改票
		reason := ""
		records, _ := s.reviewStore.ListRecordsByTaskID(ctx, task.ID)
		for i := len(records) - 1; i >= 0; i-- {
			if records[i].Conclusion != domain.StatusRevisionRequired {
				continue
			}
			if records[i].Comment != nil {
				c := records[i].Comment
				reason = strings.Join(strings.Fields(strings.TrimSpace(c.Stem+" "+c.Options+" "+c.Answer+" "+c.Other)), " ")
			}
			if reason == "" {
				reason = records[i].Opinion
			}
			if reason != "" {
				break
			}
		}
		items = append(items, RevisionItem{
			Task:     trimmed,
			Question: *q,
			Reason:   reason,
			Modified: q.Version > task.QuestionVersion,
		})
	}
	// 最近退回的排在前面
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Task.UpdatedAt.After(items[j].Task.UpdatedAt)
	})
	return items, nil
}

// cloneTaskForRevisions 深拷贝任务并裁剪掉非"需修改"的专家记录。
func cloneTaskForRevisions(task domain.ReviewTask) (domain.ReviewTask, error) {
	raw, err := json.Marshal(task)
	if err != nil {
		return task, err
	}
	var trimmed domain.ReviewTask
	if err := json.Unmarshal(raw, &trimmed); err != nil {
		return task, err
	}
	for i := range trimmed.RoundResults {
		rr := &trimmed.RoundResults[i]
		keep := make([]domain.ExpertReview, 0, len(rr.Reviews))
		for _, rev := range rr.Reviews {
			if rev.Conclusion == domain.StatusRevisionRequired {
				keep = append(keep, rev)
			}
		}
		rr.Reviews = keep
	}
	return trimmed, nil
}

// GetTaskByQuestionID 根据题目 ID 获取审核任务。
func (s *Service) GetTaskByQuestionID(ctx context.Context, questionID string) (*domain.ReviewTask, error) {
	return s.reviewStore.GetTaskByQuestionID(ctx, questionID)
}

// HasReviewHistory 判断题目是否存在审核任务（任意状态，含终态）。
// 用于删除策略：有人工审核史的题目删除时改为归档，保留审核记录。
func (s *Service) HasReviewHistory(ctx context.Context, questionID string) (bool, error) {
	task, err := s.reviewStore.GetTaskByQuestionID(ctx, questionID)
	if err != nil {
		return false, err
	}
	return task != nil, nil
}

// HasSubmissionBankHistory 判断分类子题库是否已被任何审核任务引用。
// 审核任务需要永久保留提交题库快照，因此使用过的子题库不能物理删除。
func (s *Service) HasSubmissionBankHistory(ctx context.Context, bankID string) (bool, error) {
	tasks, err := s.reviewStore.ListAllTasks(ctx)
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.SubmissionBankID == bankID {
			return true, nil
		}
		if task.SubmissionBankID == "" {
			q, err := s.questionStore.GetQuestion(ctx, task.QuestionID)
			if err != nil {
				return false, err
			}
			if q != nil {
				for _, currentBankID := range q.BankIDs {
					if currentBankID == bankID {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

// ListRecords 列出某审核任务的所有记录。
func (s *Service) ListRecords(ctx context.Context, taskID string) ([]domain.ReviewRecord, error) {
	return s.reviewStore.ListRecordsByTaskID(ctx, taskID)
}

// ListAllTasks 返回审核任务摘要，供个人累计指标和后台管理使用。
func (s *Service) ListAllTasks(ctx context.Context) ([]domain.ReviewTask, error) {
	return s.reviewStore.ListAllTasks(ctx)
}

// PublishQuestion 是旧客户端兼容接口。当前 published 已是唯一“已通过”终态，调用保持幂等。
func (s *Service) PublishQuestion(ctx context.Context, questionID string) error {
	return s.withReviewMutation(ctx, func(txCtx context.Context) error {
		return s.publishQuestionLocked(txCtx, questionID)
	})
}

func (s *Service) publishQuestionLocked(ctx context.Context, questionID string) error {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("题目 %s 不存在", questionID)
	}
	if q.Status != domain.StatusPublished {
		return fmt.Errorf("题目 %s 当前状态为 %s，尚未通过最终审核", questionID, q.Status)
	}
	if err := q.Validate(); err != nil {
		return fmt.Errorf("题目 %s 无法发布: %w", questionID, err)
	}
	return nil
}

func (s *Service) updateQuestionStatus(ctx context.Context, questionID string, status domain.QuestionStatus) error {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return fmt.Errorf("获取题目 %s 失败: %w", questionID, err)
	}
	if q == nil {
		return fmt.Errorf("题目 %s 不存在", questionID)
	}
	q.Status = status
	q.UpdatedAt = time.Now()
	if err := s.questionStore.SaveQuestion(ctx, *q); err != nil {
		return fmt.Errorf("更新题目 %s 状态为 %s 失败: %w", questionID, status, err)
	}
	return nil
}

func (s *Service) ensureTaskQuestionVersion(ctx context.Context, task *domain.ReviewTask) error {
	q, err := s.questionStore.GetQuestion(ctx, task.QuestionID)
	if err != nil {
		return fmt.Errorf("获取审核题目失败: %w", err)
	}
	if q == nil {
		return fmt.Errorf("题目 %s 不存在", task.QuestionID)
	}
	if task.QuestionVersion < 1 {
		return fmt.Errorf("审核任务 %s 未绑定有效题目版本，不能继续审核", task.ID)
	}
	if q.Version != task.QuestionVersion {
		return fmt.Errorf("%w: 审核任务绑定版本 %d，但题目当前为版本 %d；请由管理员处理当前审核任务后再重新提交", domain.ErrQuestionVersionConflict, task.QuestionVersion, q.Version)
	}
	return nil
}

// ===== 评语可见性（分层隔离） =====
//
// 规则：
//   - 同一轮审核人之间互不可见评语与态度，仅能看到题目和自己的输入；
//   - 跨轮次默认隔离，普通审核人只能看到历史轮次的脱敏统计摘要（票数），
//     看不到任何人的评语与姓名归属；
//   - 最终把关人（及系统管理员）全量可见，按轮次对比所有专家的结构化评语。

// attemptOrOne 兼容旧数据：批次号缺省视为第 1 批。
func attemptOrOne(attempt int) int {
	if attempt < 1 {
		return 1
	}
	return attempt
}

// taskInFlight 判断任务是否仍在审核流程中（评语隔离窗口期）。
func taskInFlight(status domain.QuestionStatus) bool {
	return status == domain.StatusReviewing ||
		status == domain.StatusConflict ||
		status == domain.StatusRevisionRequired
}

// ownRecords 只保留某位审核人自己的记录。
func ownRecords(records []domain.ReviewRecord, userID string) []domain.ReviewRecord {
	var own []domain.ReviewRecord
	for _, r := range records {
		if r.ExpertID == userID {
			own = append(own, r)
		}
	}
	return own
}

// sanitizeTaskForReviewer 按隔离规则裁剪任务快照：
// 去掉全部评语明细与决断意见；历史轮次仅保留票数汇总，当前轮次连票数也清零
// （当前轮的实时票数会随同轮老师提交而变化，属于他人态度信息）。
func sanitizeTaskForReviewer(task *domain.ReviewTask) *domain.ReviewTask {
	if task == nil {
		return nil
	}
	copied := *task
	copied.FinalDecision = nil
	copied.RoundResults = make([]domain.RoundResult, len(task.RoundResults))
	for i, rr := range task.RoundResults {
		rr.Reviews = nil
		if rr.RoundNumber == task.CurrentRound {
			rr.ApprovedCount = 0
			rr.RejectedCount = 0
			rr.RevisionCount = 0
			rr.Passed = false
		}
		copied.RoundResults[i] = rr
	}
	return &copied
}

// resolveExpertName 解析审核人显示名（快照到评语，历史可比对）。解析失败不阻断审核。
func (s *Service) resolveExpertName(ctx context.Context, userID string) string {
	if s.users == nil || userID == "" {
		return ""
	}
	if resolver, ok := s.users.(displayNameResolver); ok {
		if name, err := resolver.GetUserDisplayName(ctx, userID); err == nil && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	candidates, err := s.users.ListReviewCandidates(ctx)
	if err != nil {
		return ""
	}
	for _, c := range candidates {
		if c.ID == userID {
			if c.DisplayName != "" {
				return c.DisplayName
			}
			return c.Username
		}
	}
	return ""
}

// TaskForViewer 按可见性规则获取任务详情：
// 全量可见者（把关人/管理员）拿到完整任务；普通审核人拿到裁剪后的任务。
func (s *Service) TaskForViewer(ctx context.Context, taskID, userID string, fullAccess bool) (*domain.ReviewTask, error) {
	task, err := s.reviewStore.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}
	if fullAccess {
		return task, nil
	}
	return sanitizeTaskForReviewer(task), nil
}

// TaskByQuestionForViewer 同 TaskForViewer，按题目 ID 查询。
func (s *Service) TaskByQuestionForViewer(ctx context.Context, questionID, userID string, fullAccess bool) (*domain.ReviewTask, error) {
	task, err := s.reviewStore.GetTaskByQuestionID(ctx, questionID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, nil
	}
	if fullAccess {
		return task, nil
	}
	return sanitizeTaskForReviewer(task), nil
}

// RecordsForViewer 按可见性规则返回审核记录：
// 全量可见者拿到全部记录；任务进行中时普通审核人仅能看到自己提交的记录；
// 任务结束后（已通过/已驳回/归档）属于历史档案，全部记录对有权查看题目的人开放。
func (s *Service) RecordsForViewer(ctx context.Context, taskID, userID string, fullAccess bool) ([]domain.ReviewRecord, error) {
	task, err := s.reviewStore.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	records, err := s.reviewStore.ListRecordsByTaskID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if fullAccess || task == nil || !taskInFlight(task.Status) {
		return records, nil
	}
	var own []domain.ReviewRecord
	for _, r := range records {
		if r.ExpertID == userID {
			own = append(own, r)
		}
	}
	return own, nil
}
