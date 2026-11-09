package knowledgebase

import (
	"context"
	"fmt"

	"aigo/internal/domain"
)

// coverageCheckLimit 单次覆盖检查的知识点上限：预览只做有界候选查询，
// 超出部分明确告知未检查，不能以此冒充全量结论。
const coverageCheckLimit = 1000

// CardCoverage 返回候选中当前缺少可用考点知识卡（active medical 且含目标、
// 事实与来源）的知识点，供批量提交前预览提示；不冻结工作流快照、不写库。
// 传入点按 ID 去重，超过上限时只检查前 coverageCheckLimit 个并在 Checked 中
// 如实返回实际核对数量。
func (s *Service) CardCoverage(ctx context.Context, uid, role string, points []domain.KnowledgePoint) (missing []domain.KnowledgePoint, checked int, err error) {
	seen := map[string]bool{}
	unique := make([]domain.KnowledgePoint, 0, len(points))
	for _, p := range points {
		if !seen[p.ID] {
			seen[p.ID] = true
			unique = append(unique, p)
		}
	}
	if len(unique) > coverageCheckLimit {
		unique = unique[:coverageCheckLimit]
	}
	checked = len(unique)
	err = s.workflowAccess(ctx, uid, role, func(ctx context.Context, shared, personal bool) error {
		for start := 0; start < len(unique); start += WorkflowLookupBatchSize {
			group := unique[start:min(start+WorkflowLookupBatchSize, len(unique))]
			candidates, e := s.userWorkflowCandidates(ctx, uid, group, shared, personal)
			if e != nil {
				return e
			}
			for _, point := range group {
				selection, ok := candidates[point.ID]
				if !ok {
					return fmt.Errorf("知识资料查询结果不完整")
				}
				bundle, _, _, e := selectWorkflowBundle(point, selection)
				if e != nil {
					return e
				}
				if len(Targets(bundle)) == 0 {
					missing = append(missing, point)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return missing, checked, nil
}
