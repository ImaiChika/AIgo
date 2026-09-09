// Package knowledge 提供知识点管理服务。
// 包括知识点的导入、搜索、筛选、统计和删除。
package knowledge

import (
	"context"
	"errors"
	"sort"
	"strings"

	"aigo/internal/domain"
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

// SavePoints 批量保存知识点，返回 (新增, 更新, 重复跳过)。
func (s *Service) SavePoints(ctx context.Context, points []domain.KnowledgePoint) (int, int, int, error) {
	v, err := s.ResolveVersion(ctx, "")
	if err != nil {
		return 0, 0, 0, err
	}
	for i := range points {
		if points[i].VersionID == "" {
			points[i].VersionID = v.ID
			points[i].ID = domain.ScopedKnowledgeID(v.ID, points[i].ID)
		}
	}
	return s.store.SaveVersionPoints(ctx, v.ID, points, false)
}

// DeletePoint 根据 ID 删除知识点。
func (s *Service) DeletePoint(ctx context.Context, id string) error {
	return s.store.DeletePoint(ctx, id)
}

// ImportFromXlsx 从考试大纲 xlsx 文件批量导入知识点。
// 支持新大纲格式（2024年临床医师考试大纲），自动读取所有 Sheet。
// 返回 (新增数, 更新数, 重复跳过数)。
func (s *Service) ImportFromXlsx(ctx context.Context, path string) (int, int, int, error) {
	version, err := s.ResolveVersion(ctx, "")
	if err != nil {
		return 0, 0, 0, err
	}
	return s.ImportDocuments(ctx, version.ID, []string{path}, false)
}

// Search 搜索知识点（按名称或关键词匹配）。
func (s *Service) Search(ctx context.Context, keyword string) ([]domain.KnowledgePoint, error) {
	return s.SearchFiltered(ctx, KPSearchOptions{Keyword: keyword})
}

// KPSearchOptions 知识点筛选条件（精确 + 模糊组合）。
type KPSearchOptions struct {
	VersionID   string
	Path        []string // Exact prefix of category / subject / unit / sub_item, including empty values.
	Keyword     string   // 模糊：topic/unit/sub_item/subject/outline_code 包含
	Subject     string   // 精确：专业
	Category    string   // 精确：分类
	OutlineCode string   // 精确：大纲代码前缀
}

// SearchFiltered 按条件筛选知识点（内存过滤，支持模糊+精确组合）。
func (s *Service) SearchFiltered(ctx context.Context, opts KPSearchOptions) ([]domain.KnowledgePoint, error) {
	version, err := s.ResolveVersion(ctx, opts.VersionID)
	if opts.VersionID == "" && errors.Is(err, storage.ErrKnowledgeNoDefault) {
		return []domain.KnowledgePoint{}, nil
	}
	if err != nil {
		return nil, err
	}
	all, err := s.store.ListVersionPoints(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	keywords := storage.SearchTokens(opts.Keyword)
	result := []domain.KnowledgePoint{}
	for _, p := range all {
		if p.VersionID != version.ID {
			continue
		}
		path := []string{p.Category, p.Subject, p.Unit, p.SubItem}
		matches := len(opts.Path) <= len(path)
		for i, value := range opts.Path {
			if i >= len(path) || path[i] != value {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if opts.Subject != "" && p.Subject != opts.Subject {
			continue
		}
		if opts.Category != "" && p.Category != opts.Category {
			continue
		}
		if opts.OutlineCode != "" && !strings.HasPrefix(p.OutlineCode, opts.OutlineCode) {
			continue
		}
		searchText := strings.ToLower(strings.Join([]string{
			strings.Join(p.Keywords, " "), p.Category, p.Topic, p.Unit, p.SubItem, p.Subject, p.OutlineCode,
		}, " "))
		matchesKeywords := true
		for _, keyword := range keywords {
			if !strings.Contains(searchText, keyword) {
				matchesKeywords = false
				break
			}
		}
		if !matchesKeywords {
			continue
		}
		result = append(result, p)
	}
	sortPoints(result)
	return result, nil
}

// ListCategoriesAndSubjects 返回全部分类与专业列表（搜索筛选下拉用）。
func (s *Service) ListCategoriesAndSubjects(ctx context.Context, versionID ...string) (categories, subjects []string, err error) {
	version := ""
	if len(versionID) > 0 {
		version = versionID[0]
	}
	all, err := s.SearchFiltered(ctx, KPSearchOptions{VersionID: version})
	if err != nil {
		return nil, nil, err
	}
	catSet := make(map[string]bool)
	subSet := make(map[string]bool)
	for _, p := range all {
		if p.Category != "" {
			catSet[p.Category] = true
		}
		if p.Subject != "" {
			subSet[p.Subject] = true
		}
	}
	for c := range catSet {
		categories = append(categories, c)
	}
	for s := range subSet {
		subjects = append(subjects, s)
	}
	sort.Strings(categories)
	sort.Strings(subjects)
	return categories, subjects, nil
}

// ListAll 列出所有知识点。
func (s *Service) ListAll(ctx context.Context) ([]domain.KnowledgePoint, error) {
	return s.SearchFiltered(ctx, KPSearchOptions{})
}

// ListBySubject 按专业/系统筛选知识点。
func (s *Service) ListBySubject(ctx context.Context, subject string) ([]domain.KnowledgePoint, error) {
	return s.SearchFiltered(ctx, KPSearchOptions{Subject: subject})
}

// GetByID 根据 ID 获取知识点。
func (s *Service) GetByID(ctx context.Context, id string) (*domain.KnowledgePoint, error) {
	return s.store.GetPoint(ctx, id)
}

// Count 返回知识点总数。
func (s *Service) Count(ctx context.Context) (int, error) {
	points, err := s.ListAll(ctx)
	return len(points), err
}

// ListCategories 列出所有分类及各分类数量。
func (s *Service) ListCategories(ctx context.Context) (map[string]int, error) {
	all, err := s.ListAll(ctx)
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

// KPVersionStats 单个大纲版本的知识点统计。
type KPVersionStats struct {
	VersionID   string         `json:"version_id"`
	VersionName string         `json:"version_name"`
	Year        int            `json:"year"`
	Enabled     bool           `json:"enabled"`
	Total       int            `json:"total"`
	Categories  map[string]int `json:"categories"`
}

// KPStats 返回大纲版本统计：默认展示当前默认版本，另附各已发布版本的知识点总数。
// 相比 Count+ListCategories 只做一次全量加载；各版本总数复用 ListKnowledgeVersions 自带的 PointCount。
func (s *Service) KPStats(ctx context.Context) (*KPVersionStats, map[string]int, error) {
	version, err := s.ResolveVersion(ctx, "")
	if err != nil {
		if errors.Is(err, storage.ErrKnowledgeNoDefault) {
			return &KPVersionStats{Categories: map[string]int{}}, map[string]int{}, nil
		}
		return nil, nil, err
	}
	all, err := s.store.ListVersionPoints(ctx, version.ID)
	if err != nil {
		return nil, nil, err
	}
	stats := &KPVersionStats{
		VersionID:   version.ID,
		VersionName: version.Name,
		Year:        version.Year,
		Enabled:     version.Status == "published",
		Total:       len(all),
		Categories:  map[string]int{},
	}
	for _, p := range all {
		cat := p.Category
		if cat == "" {
			cat = "未分类"
		}
		stats.Categories[cat]++
	}
	versions, err := s.store.ListKnowledgeVersions(ctx)
	if err != nil {
		return stats, map[string]int{}, nil
	}
	perVersion := map[string]int{}
	for _, v := range versions {
		if v.Status != "published" {
			continue
		}
		perVersion[v.Name] = v.PointCount
	}
	return stats, perVersion, nil
}
