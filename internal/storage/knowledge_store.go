package storage

import (
	"context"
	"strings"
	"sync"

	"aigo/internal/domain"
)

// KnowledgeStore 知识点存储接口。
type KnowledgeStore interface {
	// 批量导入
	SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, error)

	// 查询
	GetPoint(ctx context.Context, id string) (*domain.KnowledgePoint, error)
	ListPoints(ctx context.Context) ([]domain.KnowledgePoint, error)
	SearchPoints(ctx context.Context, keyword string) ([]domain.KnowledgePoint, error)
	ListBySubject(ctx context.Context, subject string) ([]domain.KnowledgePoint, error)

	// 统计
	KPCount(ctx context.Context) (int, error)
}

// MemoryKnowledgeStore 内存知识点存储。
type MemoryKnowledgeStore struct {
	mu     sync.Mutex
	points []domain.KnowledgePoint
	index  map[string]int // id → slice index
}

func NewMemoryKnowledgeStore() *MemoryKnowledgeStore {
	return &MemoryKnowledgeStore{
		index: make(map[string]int),
	}
}

func (s *MemoryKnowledgeStore) SavePoints(_ context.Context, points []domain.KnowledgePoint) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, p := range points {
		if idx, ok := s.index[p.ID]; ok {
			s.points[idx] = p
		} else {
			s.points = append(s.points, p)
			s.index[p.ID] = len(s.points) - 1
		}
		count++
	}
	return count, nil
}

func (s *MemoryKnowledgeStore) GetPoint(_ context.Context, id string) (*domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil, nil
	}
	p := s.points[idx]
	return &p, nil
}

func (s *MemoryKnowledgeStore) ListPoints(_ context.Context) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.KnowledgePoint, len(s.points))
	copy(out, s.points)
	return out, nil
}

func (s *MemoryKnowledgeStore) SearchPoints(_ context.Context, keyword string) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kw := strings.ToLower(keyword)
	var result []domain.KnowledgePoint
	for _, p := range s.points {
		if matchPoint(p, kw) {
			result = append(result, p)
		}
	}
	return result, nil
}

func (s *MemoryKnowledgeStore) ListBySubject(_ context.Context, subject string) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.KnowledgePoint
	for _, p := range s.points {
		if p.Subject == subject {
			result = append(result, p)
		}
	}
	return result, nil
}

func (s *MemoryKnowledgeStore) KPCount(_ context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.points), nil
}

// matchPoint 检查知识点是否匹配关键词。
func matchPoint(p domain.KnowledgePoint, keyword string) bool {
	if strings.Contains(strings.ToLower(p.Topic), keyword) {
		return true
	}
	if strings.Contains(strings.ToLower(p.OutlineRef), keyword) {
		return true
	}
	for _, kw := range p.Keywords {
		if strings.Contains(strings.ToLower(kw), keyword) {
			return true
		}
	}
	return false
}
