package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aigo/internal/domain"
	"aigo/internal/importer"
	"aigo/internal/storage"
)

type ImportIssue struct {
	File   string `json:"file"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason"`
}
type ImportPreview struct {
	Mode               string        `json:"mode"`
	Accepted           int           `json:"accepted"`
	ExcludedBasic      int           `json:"excluded_basic"`
	ExcludedBasicCodes []string      `json:"excluded_basic_codes"`
	Conflicted         []ImportIssue `json:"conflicted"`
	Invalid            []ImportIssue `json:"invalid"`
	ConflictedCount    int           `json:"conflicted_count"`
	InvalidCount       int           `json:"invalid_count"`
	DuplicateRows      int           `json:"duplicate_rows"`
	TotalCurrent       int           `json:"total_current"`
	ReplaceWouldRemove int           `json:"replace_would_remove"`
	PreviewToken       string        `json:"preview_token,omitempty"`
	DocumentHash       string        `json:"-"`
	StateHash          string        `json:"-"`
}

const (
	previewIssueLimit    = 20
	previewSampleLimit   = 10
	importMaxBatchPoints = 50000
)

type preparedOutlineImport struct {
	preview *ImportPreview
	points  []domain.KnowledgePoint
}

// A single parser/classifier supplies both preview and confirmation. Diagnostic
// samples are bounded; their counts always describe the entire input.
func prepareOutlineImport(paths []string, replace bool) (*preparedOutlineImport, error) {
	p := &preparedOutlineImport{preview: &ImportPreview{Mode: "merge", ExcludedBasicCodes: []string{}, Conflicted: []ImportIssue{}, Invalid: []ImportIssue{}}}
	if replace {
		p.preview.Mode = "replace"
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("请选择包含知识点的文件")
	}
	seen := map[string]domain.KnowledgePoint{}
	total := 0
	files := []struct{ Name, Hash string }{}
	invalid := func(issue ImportIssue) {
		p.preview.InvalidCount++
		if len(p.preview.Invalid) < previewIssueLimit {
			p.preview.Invalid = append(p.preview.Invalid, issue)
		}
	}
	for _, path := range paths {
		name := filepath.Base(path)
		raw, err := os.ReadFile(path)
		if err != nil {
			invalid(ImportIssue{File: name, Reason: "无法读取文件"})
			continue
		}
		hash := sha256.Sum256(raw)
		files = append(files, struct{ Name, Hash string }{name, hex.EncodeToString(hash[:])})
		points, err := importer.ReadOutlineDocument(path)
		if err != nil {
			invalid(ImportIssue{File: name, Reason: strings.ReplaceAll(err.Error(), path, name)})
			continue
		}
		for _, point := range points {
			total++
			if total > importMaxBatchPoints {
				return nil, fmt.Errorf("单次最多导入 %d 个知识点", importMaxBatchPoints)
			}
			if err := normalizePoint(&point); err != nil {
				invalid(ImportIssue{File: name, Code: point.OutlineCode, Reason: err.Error()})
				continue
			}
			old, duplicate := seen[point.OutlineCode]
			duplicate = duplicate && point.OutlineCode != ""
			if duplicate && !sameImportPoint(old, point) {
				invalid(ImportIssue{File: name, Code: point.OutlineCode, Reason: "同一大纲代码在本批文件中存在不同内容或分类，请先消除冲突"})
			} else if !duplicate && point.OutlineCode != "" {
				seen[point.OutlineCode] = point
			}
			switch domain.ClassifyBasicScope(point) {
			case domain.ScopeBasicMedical:
				p.preview.ExcludedBasic++
				if len(p.preview.ExcludedBasicCodes) < previewSampleLimit && point.OutlineCode != "" {
					p.preview.ExcludedBasicCodes = append(p.preview.ExcludedBasicCodes, point.OutlineCode)
				}
			case domain.ScopeConflicted:
				p.preview.ConflictedCount++
				if len(p.preview.Conflicted) < previewIssueLimit {
					p.preview.Conflicted = append(p.preview.Conflicted, ImportIssue{File: name, Code: point.OutlineCode, Reason: "基础医学科目（" + point.Subject + "）挂在非基础医学分类下，请核对分类"})
				}
			default:
				// Empty codes have no identity and must remain separate rows.
				if duplicate {
					if sameImportPoint(old, point) {
						p.preview.DuplicateRows++
					}
					continue
				}
				p.points = append(p.points, point)
			}
		}
	}
	if total == 0 && p.preview.InvalidCount == 0 {
		return nil, fmt.Errorf("请选择包含知识点的文件")
	}
	p.preview.Accepted = len(p.points)
	raw, _ := json.Marshal(files)
	digest := sha256.Sum256(raw)
	p.preview.DocumentHash = hex.EncodeToString(digest[:])
	return p, nil
}

func sameImportPoint(a, b domain.KnowledgePoint) bool {
	return a.Category == b.Category && a.Subject == b.Subject && a.Unit == b.Unit && a.SubItem == b.SubItem && a.Topic == b.Topic && strings.Join(a.Keywords, "\x00") == strings.Join(b.Keywords, "\x00")
}

func (s *Service) ImportDocuments(ctx context.Context, versionID string, paths []string, replace bool) (int, int, int, int, error) {
	if versionID == "" {
		return 0, 0, 0, 0, fmt.Errorf("请选择目标大纲版本")
	}
	if _, err := s.ResolveVersion(ctx, versionID); err != nil {
		return 0, 0, 0, 0, err
	}
	prepared, err := prepareOutlineImport(paths, replace)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	p := prepared.preview
	if p.InvalidCount > 0 {
		issue := p.Invalid[0]
		return 0, 0, 0, 0, fmt.Errorf("%s 大纲代码 %s：%s（共%d条无效记录）", issue.File, issue.Code, issue.Reason, p.InvalidCount)
	}
	if p.ConflictedCount > 0 {
		issue := p.Conflicted[0]
		return 0, 0, 0, 0, fmt.Errorf("大纲代码 %s：%s；本批共 %d 条分类矛盾待核对，请修正文件后重试", issue.Code, issue.Reason, p.ConflictedCount)
	}
	if p.Accepted == 0 {
		return 0, 0, 0, 0, fmt.Errorf("本批文件的全部 %d 条大纲要点均属于基础医学，已从有效大纲剔除，未写入任何内容", p.ExcludedBasic)
	}
	if expected, ok := storage.KnowledgeImportExpected(ctx); ok && expected.DocumentHash != p.DocumentHash {
		return 0, 0, 0, 0, fmt.Errorf("%w: 文件已变化，请重新预览", storage.ErrKnowledgeConflict)
	}
	for i := range prepared.points {
		prepared.points[i].VersionID = versionID
		prepared.points[i].ID = newID("kp")
	}
	inserted, updated, duplicated, err := s.store.SaveVersionPoints(ctx, versionID, prepared.points, replace)
	if err == nil {
		s.invalidate()
	}
	return inserted, updated, duplicated + p.DuplicateRows, p.ExcludedBasic, err
}

func (s *Service) PreviewDocuments(ctx context.Context, versionID string, paths []string, replace bool) (*ImportPreview, error) {
	if versionID == "" {
		return nil, fmt.Errorf("请选择目标大纲版本")
	}
	if _, err := s.ResolveVersion(ctx, versionID); err != nil {
		return nil, err
	}
	prepared, err := prepareOutlineImport(paths, replace)
	if err != nil {
		return nil, err
	}
	// Do not use the TTL cache for destructive-import expectations.
	current, err := s.store.ListVersionPoints(ctx, versionID)
	if err != nil {
		return nil, err
	}
	p := prepared.preview
	p.TotalCurrent = len(current)
	p.StateHash = storage.KnowledgeStateHash(current)
	if guard, ok := s.store.(interface {
		RemovedKnowledgeScopeCodes(context.Context, string, []string) ([]string, error)
	}); ok {
		codes := []string{}
		for _, point := range prepared.points {
			if point.OutlineCode != "" {
				codes = append(codes, point.OutlineCode)
			}
		}
		removed, e := guard.RemovedKnowledgeScopeCodes(ctx, versionID, codes)
		if e != nil {
			return nil, e
		}
		for _, code := range removed {
			p.InvalidCount++
			if len(p.Invalid) < previewIssueLimit {
				p.Invalid = append(p.Invalid, ImportIssue{Code: code, Reason: "该代码已从本版本有效范围剔除，不能换分类或新ID重录"})
			}
		}
	}
	if replace {
		keep := map[string]bool{}
		for _, point := range prepared.points {
			if point.OutlineCode != "" {
				keep[point.OutlineCode] = true
			}
		}
		for _, point := range current {
			if point.OutlineCode == "" || !keep[point.OutlineCode] {
				p.ReplaceWouldRemove++
			}
		}
	}
	return p, nil
}
