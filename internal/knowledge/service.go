// Package knowledge 提供知识点管理服务。
// 包括知识点的导入、搜索、筛选、统计和删除。
package knowledge

import (
	"context"
	"fmt"

	"aigo/internal/domain"
	"aigo/internal/importer"
	"aigo/internal/storage"
)

// Service 知识点服务，封装知识点的业务操作。
type Service struct {
	store storage.KnowledgeStore
}

// NewService 创建知识点服务实例。
func NewService(store storage.KnowledgeStore) *Service {
	return &Service{store: store}
}

// SavePoints 批量保存知识点。
func (s *Service) SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, error) {
	return s.store.SavePoints(ctx, points)
}

// DeletePoint 根据 ID 删除知识点。
func (s *Service) DeletePoint(ctx context.Context, id string) error {
	return s.store.DeletePoint(ctx, id)
}

// ImportFromXlsx 从 xlsx 文件批量导入知识点。
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

// Search 搜索知识点（按名称或关键词匹配）。
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

// GetByID 根据 ID 获取知识点。
func (s *Service) GetByID(ctx context.Context, id string) (*domain.KnowledgePoint, error) {
	return s.store.GetPoint(ctx, id)
}

// Count 返回知识点总数。
func (s *Service) Count(ctx context.Context) (int, error) {
	return s.store.KPCount(ctx)
}

// ListSystems 列出所有系统分类及各分类数量。
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
