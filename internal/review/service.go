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

type Service struct {
	expertStore   storage.ExpertStore
	reviewStore   storage.ReviewStore
	questionStore storage.QuestionStore
}

func NewService(expertStore storage.ExpertStore, reviewStore storage.ReviewStore, questionStore storage.QuestionStore) *Service {
	return &Service{
		expertStore:   expertStore,
		reviewStore:   reviewStore,
		questionStore: questionStore,
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
	if flow.Name == "" {
		return fmt.Errorf("流程名称不能为空")
	}
	if len(flow.Rounds) == 0 {
		return fmt.Errorf("至少需要一轮审核配置")
	}
	// 给每轮设置默认 RequiredCount
	for i := range flow.Rounds {
		if flow.Rounds[i].RequiredCount == 0 {
			flow.Rounds[i].RequiredCount = len(flow.Rounds[i].ExpertIDs)
		}
	}
	flow.CreatedAt = time.Now()
	return s.reviewStore.SaveFlowConfig(ctx, flow)
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

// DeleteFlow 删除审核流程。如果有进行中的任务引用该流程，拒绝删除。
func (s *Service) DeleteFlow(ctx context.Context, id string) error {
	count, err := s.reviewStore.CountActiveTasksByFlow(ctx, id)
	if err != nil {
		return fmt.Errorf("检查流程引用失败: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("流程 %s 有 %d 个进行中的审核任务，无法删除", id, count)
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
			existing.Status = domain.StatusReviewing
			existing.CurrentRound = 1
			existing.AssignedTo = flow.Rounds[0].ExpertIDs
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
		// 如果是"需修改"状态，恢复为审核中（保持在原轮，不清投票，专家可继续审核）
		if existing.Status == domain.StatusRevisionRequired {
			existing.Status = domain.StatusReviewing
			existing.UpdatedAt = time.Now()
			if err := s.reviewStore.UpdateTask(ctx, *existing); err != nil {
				return nil, err
			}
			s.updateQuestionStatus(ctx, questionID, domain.StatusReviewing)
			return existing, nil
		}
		return nil, fmt.Errorf("题目 %s 已有审核任务 %s，状态为 %s", questionID, existing.ID, existing.Status)
	}

	// 更新题目状态
	q.Status = domain.StatusReviewing
	q.Version = 1
	q.UpdatedAt = time.Now()
	if err := s.questionStore.SaveQuestion(ctx, *q); err != nil {
		return nil, fmt.Errorf("更新题目状态失败: %w", err)
	}

	// 创建审核任务
	firstRound := flow.Rounds[0]
	task := domain.ReviewTask{
		ID:           fmt.Sprintf("task-%s-%d", questionID, time.Now().UnixNano()),
		QuestionID:   questionID,
		FlowID:       flowID,
		CurrentRound: 1,
		Status:       domain.StatusReviewing,
		AssignedTo:   firstRound.ExpertIDs,
		RoundResults: make([]domain.RoundResult, len(flow.Rounds)),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
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

// ReviewRequest 专家审核请求。
type ReviewRequest struct {
	TaskID   string             `json:"task_id"`
	ExpertID string             `json:"expert_id"`
	Action   domain.QuestionStatus `json:"action"` // approved / rejected / revision_required
	Opinion  string             `json:"opinion"`
	Role     string             `json:"role"` // admin 可审核任意轮次
}

// Review 专家执行审核。
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

	roundIdx := task.CurrentRound - 1
	if roundIdx < 0 || roundIdx >= len(flow.Rounds) {
		return fmt.Errorf("当前轮次 %d 超出流程配置范围", task.CurrentRound)
	}
	currentRoundConfig := flow.Rounds[roundIdx]

	// 校验审核人是否在当前轮次（admin 可审核任意轮次）
	isAssigned := req.Role == "admin"
	if !isAssigned {
		for _, id := range currentRoundConfig.ExpertIDs {
			if id == req.ExpertID {
				isAssigned = true
				break
			}
		}
	}
	if !isAssigned {
		return fmt.Errorf("专家 %s 未被分配到第 %d 轮审核", req.ExpertID, task.CurrentRound)
	}

	// 检查是否已经审核过
	for _, review := range task.RoundResults[roundIdx].Reviews {
		if review.ExpertID == req.ExpertID {
			return fmt.Errorf("专家 %s 已经审核过第 %d 轮，不能重复审核", req.ExpertID, task.CurrentRound)
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

	// 保存审核记录
	record := domain.ReviewRecord{
		ID:          fmt.Sprintf("rec-%s-%d-%s", task.ID, task.CurrentRound, req.ExpertID),
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

	// 统计本轮结果
	roundResult := &task.RoundResults[roundIdx]
	switch req.Action {
	case domain.StatusApproved:
		roundResult.ApprovedCount++
	case domain.StatusRejected:
		roundResult.RejectedCount++
	}

	// 判断本轮是否已有结论
	if req.Action == domain.StatusRejected {
		// 任何一位专家驳回，本轮不通过
		roundResult.Passed = false
		task.Status = domain.StatusRejected
		task.UpdatedAt = now
		if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
			return fmt.Errorf("更新审核任务失败: %w", err)
		}
		s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRejected)
		return nil
	}

	if req.Action == domain.StatusRevisionRequired {
		// 需要修改：重置本轮审核记录，允许修改后重新审核
		task.Status = domain.StatusRevisionRequired
		task.RoundResults[roundIdx].Reviews = nil
		task.RoundResults[roundIdx].ApprovedCount = 0
		task.RoundResults[roundIdx].RejectedCount = 0
		task.RoundResults[roundIdx].Passed = false
		task.UpdatedAt = now
		if err := s.reviewStore.UpdateTask(ctx, *task); err != nil {
			return fmt.Errorf("更新审核任务失败: %w", err)
		}
		s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusRevisionRequired)
		return nil
	}

	// approved 情况：检查是否达到通过门槛
	requiredCount := currentRoundConfig.RequiredCount
	if roundResult.ApprovedCount >= requiredCount {
		// 本轮通过
		roundResult.Passed = true

		if task.CurrentRound < len(flow.Rounds) {
			// 进入下一轮
			task.CurrentRound++
			nextRound := flow.Rounds[task.CurrentRound-1]
			task.AssignedTo = nextRound.ExpertIDs
			task.Status = domain.StatusReviewing
		} else {
			// 所有轮次通过，等待管理员发布
			task.Status = domain.StatusApproved
			s.updateQuestionStatus(ctx, task.QuestionID, domain.StatusApproved)
		}
	}
	// 还没达到门槛，继续等待其他专家审核

	task.UpdatedAt = now
	return s.reviewStore.UpdateTask(ctx, *task)
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
