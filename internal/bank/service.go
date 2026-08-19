// Package bank 提供题库（分库）管理服务。
// 题库是大题库的子集：一道题可属于多个题库（多对多）。
// 创建题库时可指定专业范围（professions）自动归纳题目，
// 也可人工搜索题目单个/批量加入（含所有状态），或从题库移出。
package bank

import (
	"context"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// Service 题库管理服务。
type Service struct {
	store         storage.BankStore
	questionStore storage.QuestionStore
}

// NewService 创建题库服务。
func NewService(store storage.BankStore, questionStore storage.QuestionStore) *Service {
	return &Service{store: store, questionStore: questionStore}
}

// CreateBank 创建题库。指定专业范围时立即自动归纳存量未分类题目。
func (s *Service) CreateBank(ctx context.Context, id, name, description string, professions []string) (*domain.QuestionBank, error) {
	if name == "" {
		return nil, fmt.Errorf("题库名称不能为空")
	}
	if id == "" {
		id = fmt.Sprintf("bank-%d", time.Now().UnixNano())
	}
	existing, _ := s.store.GetBank(ctx, id)
	if existing != nil {
		return nil, fmt.Errorf("题库 ID %s 已存在", id)
	}
	bank := domain.QuestionBank{
		ID:          id,
		Name:        name,
		Description: description,
		Professions: professions,
		CreatedAt:   time.Now(),
	}
	if err := s.store.SaveBank(ctx, bank); err != nil {
		return nil, err
	}
	// 自动归纳：把匹配专业范围的题目加入本库
	if len(professions) > 0 {
		if _, err := s.Collect(ctx, bank); err != nil {
			return nil, err
		}
	}
	return &bank, nil
}

// UpdateBank 更新题库。修改专业范围后自动重新归纳。
func (s *Service) UpdateBank(ctx context.Context, id, name, description string, professions []string) (*domain.QuestionBank, error) {
	if name == "" {
		return nil, fmt.Errorf("题库名称不能为空")
	}
	existing, err := s.store.GetBank(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("题库 %s 不存在", id)
	}
	existing.Name = name
	existing.Description = description
	existing.Professions = professions
	if err := s.store.SaveBank(ctx, *existing); err != nil {
		return nil, err
	}
	if len(professions) > 0 {
		if _, err := s.Collect(ctx, *existing); err != nil {
			return nil, err
		}
	}
	return existing, nil
}

// DeleteBank 删除题库（题目保留，仅解除成员关系）。
func (s *Service) DeleteBank(ctx context.Context, id string) error {
	if _, err := s.store.GetBank(ctx, id); err != nil {
		return err
	}
	return s.store.DeleteBank(ctx, id)
}

// ListBanks 列出所有题库。
func (s *Service) ListBanks(ctx context.Context) ([]domain.QuestionBank, error) {
	return s.store.ListBanks(ctx)
}

// GetBank 获取题库。
func (s *Service) GetBank(ctx context.Context, id string) (*domain.QuestionBank, error) {
	return s.store.GetBank(ctx, id)
}

// Collect 归纳：把"专业匹配且尚未在本库"的题目加入本库（不抢其他库，多对多共享）。
func (s *Service) Collect(ctx context.Context, bank domain.QuestionBank) (int, error) {
	if len(bank.Professions) == 0 {
		return 0, nil
	}
	questions, err := s.questionStore.ListQuestions(ctx)
	if err != nil {
		return 0, err
	}
	profSet := make(map[string]bool, len(bank.Professions))
	for _, p := range bank.Professions {
		profSet[p] = true
	}
	count := 0
	for i := range questions {
		q := &questions[i]
		if !profSet[q.Profession] {
			continue
		}
		already := false
		for _, b := range q.BankIDs {
			if b == bank.ID {
				already = true
				break
			}
		}
		if already {
			continue
		}
		q.BankIDs = append(q.BankIDs, bank.ID)
		if err := s.questionStore.SaveQuestion(ctx, *q); err != nil {
			return count, fmt.Errorf("归纳题目 %s 失败: %w", q.ID, err)
		}
		count++
	}
	return count, nil
}

// AssignBank 自动分配题库：题目未指定题库且专业匹配时，加入第一个匹配题库（append，多对多）。
func (s *Service) AssignBank(ctx context.Context, q *domain.A2Question) error {
	if len(q.BankIDs) > 0 || q.Profession == "" {
		return nil
	}
	banks, err := s.store.ListBanks(ctx)
	if err != nil {
		return err
	}
	for _, b := range banks {
		for _, p := range b.Professions {
			if p == q.Profession {
				q.BankIDs = append(q.BankIDs, b.ID)
				return nil
			}
		}
	}
	return nil
}

// AddQuestionToBank 把题目加入指定题库（已存在则忽略，不影响其他库归属）。
func (s *Service) AddQuestionToBank(ctx context.Context, questionID, bankID string) error {
	if bankID == "" {
		return fmt.Errorf("请指定题库")
	}
	b, err := s.store.GetBank(ctx, bankID)
	if err != nil {
		return err
	}
	if b == nil {
		return fmt.Errorf("题库 %s 不存在", bankID)
	}
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("题目 %s 不存在", questionID)
	}
	for _, id := range q.BankIDs {
		if id == bankID {
			return nil // 已在库
		}
	}
	q.BankIDs = append(q.BankIDs, bankID)
	q.UpdatedAt = time.Now()
	return s.questionStore.SaveQuestion(ctx, *q)
}

// AddQuestionsToBankBatch 批量把题目加入题库（高效 SQL，一次完成，多对多）。
func (s *Service) AddQuestionsToBankBatch(ctx context.Context, questionIDs []string, bankID string) (int, error) {
	if bankID == "" {
		return 0, fmt.Errorf("请指定题库")
	}
	if len(questionIDs) == 0 {
		return 0, nil
	}
	return s.store.AddQuestionsToBank(ctx, questionIDs, bankID)
}

// RemoveQuestionFromBank 把题目从指定题库移出（不影响其他库归属）。
func (s *Service) RemoveQuestionFromBank(ctx context.Context, questionID, bankID string) error {
	q, err := s.questionStore.GetQuestion(ctx, questionID)
	if err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("题目 %s 不存在", questionID)
	}
	removed := false
	out := q.BankIDs[:0]
	for _, id := range q.BankIDs {
		if id == bankID {
			removed = true
			continue
		}
		out = append(out, id)
	}
	if !removed {
		return nil // 本就不在库
	}
	q.BankIDs = out
	q.UpdatedAt = time.Now()
	return s.questionStore.SaveQuestion(ctx, *q)
}
