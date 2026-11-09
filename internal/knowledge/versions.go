package knowledge

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"crypto/rand"
	"errors"
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
	s.invalidate()
	return &v, nil
}
func (s *Service) PublishVersion(ctx context.Context, id string) error {
	if err := s.store.PublishKnowledgeVersion(ctx, id); err != nil {
		return err
	}
	s.invalidate()
	return nil
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

// basicScopeError 有效大纲范围拒绝原因。基础医学整段剔除但历史保留；科目与
// 分类矛盾的条目需人工核对后才能录入。
func basicScopeError(scope domain.BasicMedicalScope) error {
	switch scope {
	case domain.ScopeBasicMedical:
		return fmt.Errorf("基础医学知识点已从有效出题大纲剔除，不再支持新增或修改保留；历史题目与原大纲文件不受影响")
	case domain.ScopeConflicted:
		return fmt.Errorf("分类与专业/系统矛盾（基础医学科目挂在非基础医学分类下），请先核对分类")
	}
	return nil
}

func (s *Service) CreatePoint(ctx context.Context, p domain.KnowledgePoint) (*domain.KnowledgePoint, error) {
	if err := normalizePoint(&p); err != nil {
		return nil, err
	}
	if scope := domain.ClassifyBasicScope(p); scope != domain.ScopeClinical {
		return nil, basicScopeError(scope)
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
	s.invalidate()
	return s.store.GetPoint(ctx, p.ID)
}
func (s *Service) UpdatePoint(ctx context.Context, id string, p domain.KnowledgePoint) (*domain.KnowledgePoint, error) {
	if err := normalizePoint(&p); err != nil {
		return nil, err
	}
	if scope := domain.ClassifyBasicScope(p); scope != domain.ScopeClinical {
		return nil, basicScopeError(scope)
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
	s.invalidate()
	return s.store.GetPoint(ctx, id)
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
	v, err := s.ResolveVersion(ctx, versionID)
	if versionID == "" && errors.Is(err, storage.ErrKnowledgeNoDefault) {
		return []*TreeNode{}, nil
	}
	if err != nil {
		return nil, err
	}
	entry, err := s.snapshotEntry(ctx, v.ID)
	if err != nil {
		return nil, err
	}
	entry.treeOnce.Do(func() { entry.tree = buildVersionTree(entry.points) })
	return cloneVersionTree(entry.tree), nil
}

func cloneVersionTree(tree []*TreeNode) []*TreeNode {
	result := make([]*TreeNode, 0, len(tree))
	for _, node := range tree {
		copy := *node
		copy.Path = append([]string{}, node.Path...)
		copy.Children = cloneVersionTree(node.Children)
		result = append(result, &copy)
	}
	return result
}
func buildVersionTree(points []domain.KnowledgePoint) []*TreeNode {
	roots := []*TreeNode{}
	nodes := map[string]*TreeNode{}
	for i := range points {
		p := &points[i]
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
				value = "未设置目录"
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
	return roots
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
	count, err := s.store.DeleteKnowledgeVersion(ctx, id)
	if err != nil {
		return 0, err
	}
	s.invalidate()
	return count, nil
}
