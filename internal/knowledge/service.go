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

// ImportFromXlsx 从考试大纲 xlsx 文件批量导入知识点。
// 支持新大纲格式（2024年临床医师考试大纲），自动读取所有 Sheet。
func (s *Service) ImportFromXlsx(ctx context.Context, path string) (int, error) {
	points, err := importer.ReadExamOutline(path)
	if err != nil {
		return 0, fmt.Errorf("读取考试大纲文件失败: %w", err)
	}
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

// ListBySubject 按专业/系统筛选知识点。
func (s *Service) ListBySubject(ctx context.Context, subject string) ([]domain.KnowledgePoint, error) {
	all, err := s.store.ListPoints(ctx)
	if err != nil {
		return nil, err
	}
	var result []domain.KnowledgePoint
	for _, p := range all {
		if p.Subject == subject {
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

// ListCategories 列出所有分类及各分类数量。
func (s *Service) ListCategories(ctx context.Context) (map[string]int, error) {
	all, err := s.store.ListPoints(ctx)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, p := range all {
		cat := p.Category
		if cat == "" {
			cat = "未分类"
		}
		counts[cat]++
	}
	return counts, nil
}
