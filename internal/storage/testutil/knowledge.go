package testutil

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

func (s *MemoryKnowledgeStore) ListKnowledgeVersions(_ context.Context) ([]domain.KnowledgeVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.KnowledgeVersion{}
	for _, v := range s.versions {
		if !s.deletedVersions[v.ID] {
			out = append(out, v)
		}
	}
	for i := range out {
		out[i].PointCount = 0
		for _, p := range s.points {
			if p.VersionID == out[i].ID && !s.deleted[p.ID] {
				out[i].PointCount++
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Year != out[j].Year {
			return out[i].Year > out[j].Year
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
func (s *MemoryKnowledgeStore) CreateKnowledgeVersion(_ context.Context, v domain.KnowledgeVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.versions {
		if (old.Name == v.Name && !s.deletedVersions[old.ID]) || old.ID == v.ID {
			return storage.ErrKnowledgeConflict
		}
	}
	v.Status = "draft"
	s.versions = append(s.versions, v)
	return nil
}
func (s *MemoryKnowledgeStore) PublishKnowledgeVersion(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deletedVersions[id] {
		return storage.ErrKnowledgeNotFound
	}
	count := 0
	for _, p := range s.points {
		if p.VersionID == id && !s.deleted[p.ID] {
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("版本没有知识点，请先导入或新增")
	}
	for i, v := range s.versions {
		if v.ID == id {
			now := time.Now()
			s.versions[i].Status = "published"
			s.versions[i].PublishedAt = &now
			return nil
		}
	}
	return storage.ErrKnowledgeNotFound
}
func memoryKPFingerprint(p domain.KnowledgePoint) string {
	p.ID = ""
	p.VersionID = ""
	p.VersionName = ""
	p.VersionYear = 0
	p.Revision = 0
	if len(p.Keywords) == 0 {
		p.Keywords = nil
	}
	b, _ := json.Marshal(p)
	return string(b)
}
func (s *MemoryKnowledgeStore) SaveVersionPoints(_ context.Context, version string, points []domain.KnowledgePoint, replace bool) (int, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v *domain.KnowledgeVersion
	for _, item := range s.versions {
		if item.ID == version && !s.deletedVersions[version] {
			copy := item
			v = &copy
			break
		}
	}
	if v == nil {
		return 0, 0, 0, storage.ErrKnowledgeNotFound
	}
	if replace && len(points) == 0 {
		return 0, 0, 0, fmt.Errorf("不能用空文档替换大纲")
	}
	// Stage changes so any later validation failure rolls the whole import back.
	staged := append([]domain.KnowledgePoint{}, s.points...)
	idx := map[string]int{}
	gone := map[string]bool{}
	for k, n := range s.index {
		idx[k] = n
	}
	for k, b := range s.deleted {
		gone[k] = b
	}
	seen := map[string]bool{}
	inserted, updated, duplicated := 0, 0, 0
	for _, p := range points {
		if p.VersionID != "" && p.VersionID != version {
			return 0, 0, 0, storage.ErrKnowledgeConflict
		}
		p.VersionID = version
		p.VersionName = v.Name
		p.VersionYear = v.Year
		if p.OutlineCode != "" {
			for _, old := range staged {
				if old.VersionID == version && old.OutlineCode == p.OutlineCode && !gone[old.ID] {
					p.ID = old.ID
					break
				}
			}
		}
		if p.ID == "" {
			p.ID = domain.ScopedKnowledgeID(version, p.OutlineCode)
		}
		seen[p.ID] = true
		if n, ok := idx[p.ID]; ok {
			old := staged[n]
			if old.VersionID != version {
				return 0, 0, 0, storage.ErrKnowledgeConflict
			}
			if !gone[p.ID] && memoryKPFingerprint(old) == memoryKPFingerprint(p) {
				duplicated++
				continue
			}
			p.Revision = old.Revision + 1
			staged[n] = p
			if gone[p.ID] {
				inserted++
			} else {
				updated++
			}
		} else {
			p.Revision = 1
			idx[p.ID] = len(staged)
			staged = append(staged, p)
			inserted++
		}
		gone[p.ID] = false
	}
	if replace {
		for i, p := range staged {
			if p.VersionID == version && !seen[p.ID] && !gone[p.ID] {
				gone[p.ID] = true
				staged[i].Revision++
			}
		}
	}
	s.points = staged
	s.index = idx
	s.deleted = gone
	return inserted, updated, duplicated, nil
}
func (s *MemoryKnowledgeStore) UpdatePoint(_ context.Context, p domain.KnowledgePoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	exists := false
	for _, v := range s.versions {
		if v.ID == p.VersionID && !s.deletedVersions[v.ID] {
			exists = true
			p.VersionName = v.Name
			p.VersionYear = v.Year
		}
	}
	if !exists {
		return storage.ErrKnowledgeNotFound
	}
	for _, old := range s.points {
		if old.ID != p.ID && old.VersionID == p.VersionID && old.OutlineCode == p.OutlineCode && p.OutlineCode != "" && !s.deleted[old.ID] {
			return storage.ErrKnowledgeConflict
		}
	}
	n, ok := s.index[p.ID]
	if p.Revision == 0 {
		if ok {
			return storage.ErrKnowledgeConflict
		}
		p.Revision = 1
		s.index[p.ID] = len(s.points)
		s.points = append(s.points, p)
		return nil
	}
	if !ok || s.deleted[p.ID] || s.points[n].Revision != p.Revision || s.points[n].VersionID != p.VersionID {
		return storage.ErrKnowledgeConflict
	}
	p.Revision++
	s.points[n] = p
	return nil
}

func (s *MemoryKnowledgeStore) ListVersionPoints(ctx context.Context, version string) ([]domain.KnowledgePoint, error) {
	points, err := s.ListPoints(ctx)
	if err != nil {
		return nil, err
	}
	result := []domain.KnowledgePoint{}
	for _, p := range points {
		if p.VersionID == version {
			result = append(result, p)
		}
	}
	return result, nil
}

func (s *MemoryKnowledgeStore) DeleteKnowledgeVersion(_ context.Context, id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	exists := false
	for _, v := range s.versions {
		if v.ID == id && !s.deletedVersions[id] {
			exists = true
			break
		}
	}
	if !exists {
		return 0, storage.ErrKnowledgeNotFound
	}
	count := 0
	for i, p := range s.points {
		if p.VersionID == id && !s.deleted[p.ID] {
			s.deleted[p.ID] = true
			s.points[i].Revision++
			count++
		}
	}
	s.deletedVersions[id] = true
	return count, nil
}
