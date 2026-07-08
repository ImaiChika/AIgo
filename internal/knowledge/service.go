package knowledge

import (
	"context"
	"fmt"

	"aigo/internal/domain"
	"aigo/internal/importer"
	"aigo/internal/storage"
)

// Service 知识点服务。
type Service struct {
	store storage.KnowledgeStore
}

func NewService(store storage.KnowledgeStore) *Service {
	return &Service{store: store}
}

// ImportFromXlsx 从 xlsx 文件导入知识点。
func (s *Service) ImportFromXlsx(ctx context.Context, path string) (int, error) {
	rows, err := importer.ReadKnowledgePoints(path)
	if err != nil {
		return 0, fmt.Errorf("读取知识点文件失败: %w", err)
	}
	points := importer.ConvertToKnowledgePoints(rows)
	if len(points) == 0 {
		return 0, fmt.Errorf("未找到有效知识点")
	}
	count, err := s.store.SavePoints(ctx, points)
	if err != nil {
		return 0, fmt.Errorf("保存知识点失败: %w", err)
	}
	return count, nil
}

// Search 搜索知识点。
func (s *Service) Search(ctx context.Context, keyword string) ([]domain.KnowledgePoint, error) {
	return s.store.SearchPoints(ctx, keyword)
}

// ListAll 列出所有知识点。
func (s *Service) ListAll(ctx context.Context) ([]domain.KnowledgePoint, error) {
	return s.store.ListPoints(ctx)
}

// ListBySystem 按系统筛选知识点。
func (s *Service) ListBySystem(ctx context.Context, system string) ([]domain.KnowledgePoint, error) {
	all, err := s.store.ListPoints(ctx)
	if err != nil {
		return nil, err
	}
	var result []domain.KnowledgePoint
	for _, p := range all {
		if p.System == system {
			result = append(result, p)
		}
	}
	return result, nil
}

// GetByID 按 ID 获取知识点。
func (s *Service) GetByID(ctx context.Context, id string) (*domain.KnowledgePoint, error) {
	return s.store.GetPoint(ctx, id)
}

// Count 统计知识点数量。
func (s *Service) Count(ctx context.Context) (int, error) {
	return s.store.KPCount(ctx)
}

// ListSystems 列出所有系统分类。
func (s *Service) ListSystems(ctx context.Context) (map[string]int, error) {
	all, err := s.store.ListPoints(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, p := range all {
		sys := p.System
		if sys == "" {
			sys = "未分类"
		}
		counts[sys]++
	}
	return counts, nil
}
