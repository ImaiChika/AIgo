package knowledge

import (
	"aigo/internal/domain"
	"aigo/internal/importer"
	"aigo/internal/storage"
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

func newID(prefix string) string { return fmt.Sprintf("%s-%x", prefix, randomBytes()) }
func randomBytes() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func (s *Service) Versions(ctx context.Context) ([]domain.KnowledgeVersion, error) {
	return s.store.ListKnowledgeVersions(ctx)
}
func (s *Service) ResolveVersion(ctx context.Context, id string) (*domain.KnowledgeVersion, error) {
	versions, err := s.Versions(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		if (id != "" && v.ID == id) || (id == "" && v.Status == "published") {
			return &v, nil
		}
	}
	if id == "" {
		return nil, storage.ErrKnowledgeNoDefault
	}
	return nil, storage.ErrKnowledgeNotFound
}
func (s *Service) CreateVersion(ctx context.Context, name string, year int, description string) (*domain.KnowledgeVersion, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return nil, fmt.Errorf("版本名称需为 1–100 个字符")
	}
	if year < 1900 || year > 2200 {
		return nil, fmt.Errorf("请输入有效考试年份（1900–2200）")
	}
	if len([]rune(description)) > 2000 {
		return nil, fmt.Errorf("版本说明最多 2000 个字符")
	}
	v := domain.KnowledgeVersion{ID: newID("kv"), Name: name, Year: year, Description: strings.TrimSpace(description), Status: "draft", CreatedAt: time.Now()}
	if err := s.store.CreateKnowledgeVersion(ctx, v); err != nil {
		return nil, err
	}
	return &v, nil
}
func (s *Service) PublishVersion(ctx context.Context, id string) error {
	return s.store.PublishKnowledgeVersion(ctx, id)
}
func normalizePoint(p *domain.KnowledgePoint) error {
	p.Topic = strings.TrimSpace(p.Topic)
	p.Category = strings.TrimSpace(p.Category)
	p.Subject = strings.TrimSpace(p.Subject)
	p.Unit = strings.TrimSpace(p.Unit)
	p.SubItem = strings.TrimSpace(p.SubItem)
	p.OutlineCode = strings.TrimSpace(p.OutlineCode)
	if p.Topic == "" || p.Subject == "" || p.Category == "" {
		return fmt.Errorf("分类、专业/系统和知识点内容不能为空")
	}
	if len([]rune(p.Topic)) > 5000 {
		return fmt.Errorf("知识点内容最多 5000 个字符")
	}
	for _, value := range []string{p.Category, p.Subject, p.Unit, p.SubItem, p.OutlineCode} {
		if len([]rune(value)) > 250 {
			return fmt.Errorf("分类、目录或大纲代码最多 250 个字符")
		}
	}
	keywords := []string{}
	seen := map[string]bool{}
	for _, k := range p.Keywords {
		k = strings.TrimSpace(k)
		if len([]rune(k)) > 100 {
			return fmt.Errorf("单个关键词最多 100 个字符")
		}
		if k != "" && !seen[k] {
			seen[k] = true
			keywords = append(keywords, k)
		}
	}
	if len(keywords) > 50 {
		return fmt.Errorf("最多 50 个关键词")
	}
	p.Keywords = keywords
	return nil
}
func (s *Service) CreatePoint(ctx context.Context, p domain.KnowledgePoint) (*domain.KnowledgePoint, error) {
	if err := normalizePoint(&p); err != nil {
		return nil, err
	}
	if p.VersionID == "" {
		return nil, fmt.Errorf("请选择目标大纲版本")
	}
	if _, err := s.ResolveVersion(ctx, p.VersionID); err != nil {
		return nil, err
	}
	p.ID = newID("kp")
	p.Revision = 0
	if err := s.store.UpdatePoint(ctx, p); err != nil {
		return nil, err
	}
	return s.store.GetPoint(ctx, p.ID)
}
func (s *Service) UpdatePoint(ctx context.Context, id string, p domain.KnowledgePoint) (*domain.KnowledgePoint, error) {
	if err := normalizePoint(&p); err != nil {
		return nil, err
	}
	old, err := s.store.GetPoint(ctx, id)
	if err != nil {
		return nil, err
	}
	if old == nil {
		return nil, storage.ErrKnowledgeNotFound
	}
	if p.VersionID != old.VersionID {
		return nil, fmt.Errorf("不能移动知识点到其他版本，请在目标版本新增或导入")
	}
	if p.Revision < 1 {
		return nil, fmt.Errorf("缺少知识点修订号，请刷新后重试")
	}
	p.ID = id
	if err := s.store.UpdatePoint(ctx, p); err != nil {
		return nil, err
	}
	return s.store.GetPoint(ctx, id)
}

// ImportDocuments validates every input before a single atomic storage call.
func (s *Service) ImportDocuments(ctx context.Context, versionID string, paths []string, replace bool) (int, int, int, error) {
	if versionID == "" {
		return 0, 0, 0, fmt.Errorf("请选择目标大纲版本")
	}
	if _, err := s.ResolveVersion(ctx, versionID); err != nil {
		return 0, 0, 0, err
	}
	points := []domain.KnowledgePoint{}
	seen := map[string]domain.KnowledgePoint{}
	for _, path := range paths {
		ps, err := importer.ReadOutlineDocument(path)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("%s：%w", path, err)
		}
		for _, p := range ps {
			if err := normalizePoint(&p); err != nil {
				return 0, 0, 0, err
			}
			if old, ok := seen[p.OutlineCode]; ok && (old.Topic != p.Topic || old.Category != p.Category || old.Subject != p.Subject || old.Unit != p.Unit || old.SubItem != p.SubItem || strings.Join(old.Keywords, "\x00") != strings.Join(p.Keywords, "\x00")) {
				return 0, 0, 0, fmt.Errorf("大纲代码 %s 在本批文件中存在不同内容，请先消除冲突", p.OutlineCode)
			}
			seen[p.OutlineCode] = p
			p.VersionID = versionID
			p.ID = newID("kp")
			points = append(points, p)
			if len(points) > 50000 {
				return 0, 0, 0, fmt.Errorf("单次最多导入 50000 个知识点")
			}
		}
	}
	if len(points) == 0 {
		return 0, 0, 0, fmt.Errorf("请选择包含知识点的文件")
	}
	return s.store.SaveVersionPoints(ctx, versionID, points, replace)
}

// ResolveForGeneration always looks up the selected version and rejects stale or
// deleted IDs. User-supplied topic text cannot silently replace a missing point.
func (s *Service) ResolveForGeneration(ctx context.Context, versionID, id, code string) (*domain.KnowledgePoint, error) {
	var p *domain.KnowledgePoint
	var err error
	if id != "" {
		p, err = s.store.GetPoint(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, storage.ErrKnowledgeNotFound
		}
		if versionID != "" && p.VersionID != versionID {
			return nil, fmt.Errorf("知识点不属于所选大纲版本")
		}
		versionID = p.VersionID
	}
	v, err := s.ResolveVersion(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if v.Status != "published" {
		return nil, fmt.Errorf("请先启用此大纲版本，再用于出题")
	}
	if p == nil {
		points, err := s.SearchFiltered(ctx, KPSearchOptions{VersionID: v.ID, OutlineCode: code})
		if err != nil {
			return nil, err
		}
		for _, candidate := range points {
			if code != "" && candidate.OutlineCode == code {
				p = &candidate
				break
			}
		}
	}
	if p == nil {
		return nil, storage.ErrKnowledgeNotFound
	}
	return p, nil
}

type TreeNode struct {
	ID       string      `json:"id"`
	Label    string      `json:"label"`
	Field    string      `json:"field"`
	Value    string      `json:"value"`
	Path     []string    `json:"path"`
	Count    int         `json:"count"`
	Children []*TreeNode `json:"children"`
}

func (s *Service) Tree(ctx context.Context, versionID string) ([]*TreeNode, error) {
	points, err := s.SearchFiltered(ctx, KPSearchOptions{VersionID: versionID})
	if err != nil {
		return nil, err
	}
	roots := []*TreeNode{}
	nodes := map[string]*TreeNode{}
	for _, p := range points {
		values := []string{p.Category, p.Subject, p.Unit, p.SubItem}
		fields := []string{"category", "subject", "unit", "sub_item"}
		path := []string{}
		children := &roots
		for i, value := range values {
			if i >= 2 && strings.Join(values[i:], "") == "" {
				break
			}
			path = append(path, value)
			if value == "" {
				value = "未分类"
			}
			key := strings.Join(path, "\x00")
			node := nodes[key]
			if node == nil {
				node = &TreeNode{ID: domain.ScopedKnowledgeID("tree", key), Label: value, Field: fields[i], Value: values[i], Path: append([]string{}, path...), Children: []*TreeNode{}}
				nodes[key] = node
				*children = append(*children, node)
			}
			node.Count++
			children = &node.Children
		}
	}
	// Input points follow outline code order, so children follow the exam outline.
	return roots, nil
}
func sortPoints(points []domain.KnowledgePoint) {
	sort.SliceStable(points, func(i, j int) bool {
		if points[i].OutlineCode != points[j].OutlineCode {
			return outlineCodeLess(points[i].OutlineCode, points[j].OutlineCode)
		}
		return points[i].ID < points[j].ID
	})
}

func outlineCodeLess(a, b string) bool {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == right[i] {
			continue
		}
		x, xe := strconv.ParseUint(left[i], 10, 64)
		y, ye := strconv.ParseUint(right[i], 10, 64)
		if xe == nil && ye == nil && x != y {
			return x < y
		}
		return left[i] < right[i]
	}
	return len(left) < len(right)
}

func (s *Service) DeleteVersion(ctx context.Context, id string) (int, error) {
	if id == "" {
		return 0, storage.ErrKnowledgeNotFound
	}
	return s.store.DeleteKnowledgeVersion(ctx, id)
}
