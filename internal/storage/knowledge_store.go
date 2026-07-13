package storage

import (
	"context"
	"strings"
	"sync"

	"aigo/internal/domain"
)

// KnowledgeStore 知识点存储接口。
type KnowledgeStore interface {
	// SavePoints 批量保存知识点（新增或更新）。
	SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, error)
	// GetPoint 根据 ID 获取知识点。
	GetPoint(ctx context.Context, id string) (*domain.KnowledgePoint, error)
	// ListPoints 列出所有知识点。
	ListPoints(ctx context.Context) ([]domain.KnowledgePoint, error)
	// SearchPoints 搜索知识点（按名称或关键词匹配）。
	SearchPoints(ctx context.Context, keyword string) ([]domain.KnowledgePoint, error)
	// ListBySubject 按科目筛选知识点。
	ListBySubject(ctx context.Context, subject string) ([]domain.KnowledgePoint, error)
	// DeletePoint 删除知识点。
	DeletePoint(ctx context.Context, id string) error
	// KPCount 返回知识点总数。
	KPCount(ctx context.Context) (int, error)
}

// MemoryKnowledgeStore 内存知识点存储。
type MemoryKnowledgeStore struct {
	mu     sync.Mutex
	points []domain.KnowledgePoint
	index  map[string]int // ID → 切片索引
}

func NewMemoryKnowledgeStore() *MemoryKnowledgeStore {
	return &MemoryKnowledgeStore{
		index: make(map[string]int),
	}
}

// SavePoints 批量保存知识点，已存在的会更新。
func (s *MemoryKnowledgeStore) SavePoints(_ context.Context, points []domain.KnowledgePoint) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, p := range points {
		if idx, ok := s.index[p.ID]; ok {
			s.points[idx] = p // 更新
		} else {
			s.points = append(s.points, p) // 新增
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

// SearchPoints 搜索知识点，匹配 topic、outline_ref 或 keywords。
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

// DeletePoint 删除知识点，用最后一个元素覆盖被删除位置。
func (s *MemoryKnowledgeStore) DeletePoint(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.index[id]
	if !ok {
		return nil
	}
	lastIdx := len(s.points) - 1
	if idx != lastIdx {
		s.points[idx] = s.points[lastIdx]
		s.index[s.points[idx].ID] = idx
	}
	s.points = s.points[:lastIdx]
	delete(s.index, id)
	return nil
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
