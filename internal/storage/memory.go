// Package storage 定义数据存储接口和实现。
// 当前提供内存存储（用于测试）和 PostgreSQL 持久化存储。
package storage

import (
	"context"
	"sync"

	"aigo/internal/domain"
)

// QuestionStore 题目存储接口，定义题目的增删改查操作。
type QuestionStore interface {
	// SaveQuestion 保存单道题目（新增或更新）。
	SaveQuestion(ctx context.Context, question domain.A2Question) error
	// SaveQuestions 批量保存题目，返回成功数量。
	SaveQuestions(ctx context.Context, questions []domain.A2Question) (int, error)
	// ListQuestions 列出所有题目。
	ListQuestions(ctx context.Context) ([]domain.A2Question, error)
	// GetQuestion 根据 ID 获取单道题目。
	GetQuestion(ctx context.Context, id string) (*domain.A2Question, error)
	// DeleteQuestion 根据 ID 删除题目。
	DeleteQuestion(ctx context.Context, id string) error
	// Count 返回题目总数。
	Count(ctx context.Context) (int, error)
}

// MemoryStore 内存题目存储，数据保存在切片中，重启丢失。
// 适用于开发测试，生产环境应使用 PostgreSQL。
type MemoryStore struct {
	mu        sync.Mutex             // 互斥锁，保护并发访问
	questions []domain.A2Question    // 题目切片
	index     map[string]int         // ID 到切片索引的映射，加速查找
}

// NewMemoryStore 创建内存题目存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		index: make(map[string]int),
	}
}

// SaveQuestion 保存题目。如果 ID 已存在则覆盖，否则追加。
func (s *MemoryStore) SaveQuestion(_ context.Context, question domain.A2Question) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 检查是否已存在，存在则覆盖原位置
	if idx, ok := s.index[question.ID]; ok {
		s.questions[idx] = question
		return nil
	}
	// 新增：追加到切片末尾
	s.questions = append(s.questions, question)
	s.index[question.ID] = len(s.questions) - 1
	return nil
}

// SaveQuestions 批量保存题目。
func (s *MemoryStore) SaveQuestions(_ context.Context, questions []domain.A2Question) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, q := range questions {
		s.questions = append(s.questions, q)
		s.index[q.ID] = len(s.questions) - 1
		count++
	}
	return count, nil
}

// ListQuestions 返回所有题目的副本（不影响原始数据）。
func (s *MemoryStore) ListQuestions(_ context.Context) ([]domain.A2Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.A2Question, len(s.questions))
	copy(out, s.questions)
	return out, nil
}

// GetQuestion 根据 ID 获取题目。返回 nil 表示不存在。
func (s *MemoryStore) GetQuestion(_ context.Context, id string) (*domain.A2Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil, nil
	}
	q := s.questions[idx]
	return &q, nil
}

// DeleteQuestion 删除题目。使用"用最后一个覆盖目标位置"的策略，避免切片移动。
func (s *MemoryStore) DeleteQuestion(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil
	}
	// 用最后一个元素覆盖被删除的位置
	lastIdx := len(s.questions) - 1
	if idx != lastIdx {
		s.questions[idx] = s.questions[lastIdx]
		s.index[s.questions[idx].ID] = idx
	}
	// 截断切片，删除最后一个元素
	s.questions = s.questions[:lastIdx]
	delete(s.index, id)
	return nil
}

// Count 返回题目总数。
func (s *MemoryStore) Count(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.questions), nil
}
