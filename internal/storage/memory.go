package storage

import (
	"context"
	"sync"

	"aigo/internal/domain"
)

type QuestionStore interface {
	SaveQuestion(ctx context.Context, question domain.A2Question) error
	SaveQuestions(ctx context.Context, questions []domain.A2Question) (int, error)
	ListQuestions(ctx context.Context) ([]domain.A2Question, error)
	GetQuestion(ctx context.Context, id string) (*domain.A2Question, error)
	DeleteQuestion(ctx context.Context, id string) error
	Count(ctx context.Context) (int, error)
}

type MemoryStore struct {
	mu        sync.Mutex
	questions []domain.A2Question
	index     map[string]int // id → slice index
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		index: make(map[string]int),
	}
}

func (s *MemoryStore) SaveQuestion(ctx context.Context, question domain.A2Question) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx, ok := s.index[question.ID]; ok {
		s.questions[idx] = question
		return nil
	}
	s.questions = append(s.questions, question)
	s.index[question.ID] = len(s.questions) - 1
	return nil
}

func (s *MemoryStore) SaveQuestions(ctx context.Context, questions []domain.A2Question) (int, error) {
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

func (s *MemoryStore) ListQuestions(ctx context.Context) ([]domain.A2Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.A2Question, len(s.questions))
	copy(out, s.questions)
	return out, nil
}

func (s *MemoryStore) GetQuestion(ctx context.Context, id string) (*domain.A2Question, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil, nil
	}
	q := s.questions[idx]
	return &q, nil
}

func (s *MemoryStore) DeleteQuestion(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil
	}
	// 删除：用最后一个覆盖当前位置，然后截断
	lastIdx := len(s.questions) - 1
	if idx != lastIdx {
		s.questions[idx] = s.questions[lastIdx]
		s.index[s.questions[idx].ID] = idx
	}
	s.questions = s.questions[:lastIdx]
	delete(s.index, id)
	return nil
}

func (s *MemoryStore) Count(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.questions), nil
}
