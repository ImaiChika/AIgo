// Package knowledge 提供知识点管理服务。
// 包括知识点的导入、搜索、筛选、统计和删除。
package knowledge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// defaultSnapshotTTL 快照缓存兜底过期时间：服务层写入会立即失效缓存，
// TTL 只用于覆盖进程外变更（如 CLI kp-import 直接写库）的场景。
const defaultSnapshotTTL = 2 * time.Minute

// versionSnapshot 单个大纲版本的全量知识点快照（已按大纲代码排序）。
type versionSnapshot struct {
	generation uint64
	builtAt    time.Time
	points     []domain.KnowledgePoint
}

// Service 知识点服务，封装知识点的业务操作。
type Service struct {
	store storage.KnowledgeStore

	// versionSnapshots 按版本缓存全量知识点，供列表筛选、目录树、元数据和
	// 统计复用，避免每个请求都全量装载（7200+ 条时每次约 100ms）。
	// 服务层任何写入都会递增 generation 使缓存失效；单进程部署内即时生效。
	mu         sync.Mutex
	snapshots  map[string]*versionSnapshot
	generation uint64
	// CacheTTL 快照兜底过期时间，零值使用 defaultSnapshotTTL；仅供测试调整。
	CacheTTL time.Duration
}

// NewService 创建知识点服务实例。
func NewService(store storage.KnowledgeStore) *Service {
	return &Service{store: store, snapshots: map[string]*versionSnapshot{}}
}

// invalidate 使全部版本快照失效（服务层写入成功后调用）。
func (s *Service) invalidate() {
	s.mu.Lock()
	s.generation++
	s.mu.Unlock()
}

// snapshotTTL 返回当前生效的快照过期时间。
func (s *Service) snapshotTTL() time.Duration {
	if s.CacheTTL > 0 {
		return s.CacheTTL
	}
	return defaultSnapshotTTL
}

// snapshot 返回指定版本的全量知识点快照（已排序）。缓存未命中、服务层
// 发生过写入或超过 TTL 时重新装载。读多写少场景下命中率接近 100%。
func (s *Service) snapshot(ctx context.Context, versionID string) ([]domain.KnowledgePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	gen := s.generation
	if entry, ok := s.snapshots[versionID]; ok && entry.generation == gen && time.Since(entry.builtAt) < s.snapshotTTL() {
		return entry.points, nil
	}
	points, err := s.store.ListVersionPoints(ctx, versionID)
	if err != nil {
		return nil, err
	}
	// ListVersionPoints 已按版本过滤；这里再按版本号压实一次，剔除脏数据。
	compact := points[:0]
	for _, p := range points {
		if p.VersionID == versionID {
			compact = append(compact, p)
		}
	}
	sortPoints(compact)
	s.snapshots[versionID] = &versionSnapshot{generation: gen, builtAt: time.Now(), points: compact}
	return compact, nil
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
	inserted, updated, duplicated, err := s.store.SaveVersionPoints(ctx, v.ID, points, false)
	if err == nil {
		s.invalidate()
	}
	return inserted, updated, duplicated, err
}

// DeletePoint 根据 ID 删除知识点。
func (s *Service) DeletePoint(ctx context.Context, id string) error {
	if err := s.store.DeletePoint(ctx, id); err != nil {
		return err
	}
	s.invalidate()
	return nil
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
// 数据源为版本快照缓存：筛选语义与排序规则保持不变，仅免去每次请求的全量装载。
func (s *Service) SearchFiltered(ctx context.Context, opts KPSearchOptions) ([]domain.KnowledgePoint, error) {
	version, err := s.ResolveVersion(ctx, opts.VersionID)
	if opts.VersionID == "" && errors.Is(err, storage.ErrKnowledgeNoDefault) {
		return []domain.KnowledgePoint{}, nil
	}
	if err != nil {
		return nil, err
	}
	points, err := s.snapshot(ctx, version.ID)
	if err != nil {
		return nil, err
	}
	keywords := storage.SearchTokens(opts.Keyword)
	result := []domain.KnowledgePoint{}
	for i := range points {
		p := &points[i]
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
		result = append(result, *p)
	}
	return result, nil
}

// ListCategoriesAndSubjects 返回全部分类与专业列表（搜索筛选下拉用）。
func (s *Service) ListCategoriesAndSubjects(ctx context.Context, versionID ...string) (categories, subjects []string, err error) {
	version := ""
	if len(versionID) > 0 {
		version = versionID[0]
	}
	v, err := s.ResolveVersion(ctx, version)
	if version == "" && errors.Is(err, storage.ErrKnowledgeNoDefault) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	points, err := s.snapshot(ctx, v.ID)
	if err != nil {
		return nil, nil, err
	}
	catSet := make(map[string]bool)
	subSet := make(map[string]bool)
	for i := range points {
		if points[i].Category != "" {
			catSet[points[i].Category] = true
		}
		if points[i].Subject != "" {
			subSet[points[i].Subject] = true
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
			cat = "未设置分类"
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
// 复用版本快照缓存，不再单独全量加载；各版本总数复用 ListKnowledgeVersions 自带的 PointCount。
func (s *Service) KPStats(ctx context.Context) (*KPVersionStats, map[string]int, error) {
	version, err := s.ResolveVersion(ctx, "")
	if err != nil {
		if errors.Is(err, storage.ErrKnowledgeNoDefault) {
			return &KPVersionStats{Categories: map[string]int{}}, map[string]int{}, nil
		}
		return nil, nil, err
	}
	points, err := s.snapshot(ctx, version.ID)
	if err != nil {
		return nil, nil, err
	}
	stats := &KPVersionStats{
		VersionID:   version.ID,
		VersionName: version.Name,
		Year:        version.Year,
		Enabled:     version.Status == "published",
		Total:       len(points),
		Categories:  map[string]int{},
	}
	for i := range points {
		cat := points[i].Category
		if cat == "" {
			cat = "未设置分类"
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
