package domain

import (
	"fmt"
	"reflect"
	"strings"
)

// 批量任务模式：per_point 为“每点 K 题”（旧语义，省略 mode 时兼容）；
// random_total 为“所选范围随机，共 N 题”——对唯一候选做等概率有放回抽取。
const (
	BatchModePerPoint    = "per_point"
	BatchModeRandomTotal = "random_total"
)

// BatchPlanVersion 显式批量计划的结构版本；解码器只接受已知版本。
const BatchPlanVersion = 1

// BatchPlan 是批量任务的权威单元清单：提交时一次成型并持久化，恢复、失败
// 重跑与展示都按此清单执行，绝不重新摊平或重新抽样。
type BatchPlan struct {
	Version        int                  `json:"version"`
	Mode           string               `json:"mode"`
	RequestedTotal int                  `json:"requested_total"`
	CountPerPoint  int                  `json:"count_per_point,omitempty"`
	VersionID      string               `json:"version_id,omitempty"`
	CandidateCount int                  `json:"candidate_count"`
	Candidates     []BatchPlanCandidate `json:"candidates,omitempty"`
	Selection      *BatchPlanSelection  `json:"selection,omitempty"`
	Seed           string               `json:"seed,omitempty"`
	Units          []BatchPlanUnit      `json:"units"`
}

// BatchPlanCandidate 保留原候选池的身份与修订，包括本次未抽中的点；
// 不复制候选的完整医学正文，也不参与恢复时重新抽样。
type BatchPlanCandidate struct {
	ID          string `json:"id,omitempty"`
	VersionID   string `json:"version_id,omitempty"`
	OutlineCode string `json:"outline_code,omitempty"`
	Revision    int    `json:"revision"`
}

// BatchPlanSelection 记录提交时的范围条件。显式候选池仍以 Candidates 为准，
// 恢复不得重新执行这些条件查询。旧 v1 计划没有本字段。
type BatchPlanSelection struct {
	VersionID         string   `json:"version_id,omitempty"`
	KnowledgePointIDs []string `json:"knowledge_point_ids,omitempty"`
	OutlineCodes      string   `json:"outline_codes,omitempty"`
	FromCode          string   `json:"from_code,omitempty"`
	ToCode            string   `json:"to_code,omitempty"`
	Limit             int      `json:"limit,omitempty"`
	SkipExisting      bool     `json:"skip_existing"`
}

// BatchPlanUnit 单个生成单元：冻结的知识点快照与该点在本批中的出现序号。
// 出现序号从 1 起；执行按 Units 下标定位，知识目标沿冻结偏移轮转。
type BatchPlanUnit struct {
	Index          int            `json:"index"`
	KnowledgePoint KnowledgePoint `json:"knowledge_point"`
	PointSequence  int            `json:"point_sequence"`
	// nil 保留已接受 v1 计划原有的全局下标轮转；新计划显式冻结每点偏移。
	TargetOffset *int `json:"target_offset,omitempty"`
}

// BatchPointIdentity 以真实 ID 定位；历史代码型快照按大纲版本和代码定位。
func BatchPointIdentity(point KnowledgePoint) string {
	if strings.TrimSpace(point.ID) != "" {
		return "id:" + point.ID
	}
	if strings.TrimSpace(point.OutlineCode) != "" {
		return "code:" + point.VersionID + "\x00" + point.OutlineCode
	}
	return ""
}

// ValidateBatchMode 校验批量模式字符串。
func ValidateBatchMode(mode string) bool {
	return mode == BatchModePerPoint || mode == BatchModeRandomTotal
}

// Validate 校验计划与候选快照完整性、单元身份/版本、出现序号和冻结偏移。
// 已接受的旧 v1 没有候选清单与目标偏移，保留原执行语义；它们的已有
// 身份/计数仍须一致。内容合法性（临床范围等）由提交入口另行校验。
func (p *BatchPlan) Validate() error {
	if p == nil {
		return fmt.Errorf("批量计划为空")
	}
	if p.Version != BatchPlanVersion {
		return fmt.Errorf("批量计划版本不支持: %d", p.Version)
	}
	if !ValidateBatchMode(p.Mode) {
		return fmt.Errorf("批量模式无效: %s", p.Mode)
	}
	if len(p.Units) == 0 {
		return fmt.Errorf("批量计划没有生成单元")
	}
	if p.RequestedTotal != len(p.Units) {
		return fmt.Errorf("计划题数 %d 与单元数 %d 不一致", p.RequestedTotal, len(p.Units))
	}
	if p.CandidateCount < 1 {
		return fmt.Errorf("候选知识点为空")
	}
	if p.Selection != nil && p.Selection.VersionID != p.VersionID {
		return fmt.Errorf("候选选择条件与计划大纲版本不一致")
	}
	candidates := make(map[string]BatchPlanCandidate, len(p.Candidates))
	if len(p.Candidates) > 0 {
		if len(p.Candidates) != p.CandidateCount {
			return fmt.Errorf("候选快照数量与候选总数不一致")
		}
		for _, c := range p.Candidates {
			identity := BatchPointIdentity(KnowledgePoint{ID: c.ID, VersionID: c.VersionID, OutlineCode: c.OutlineCode})
			if identity == "" || c.Revision < 0 {
				return fmt.Errorf("候选快照身份或修订无效")
			}
			if _, exists := candidates[identity]; exists {
				return fmt.Errorf("候选快照身份重复")
			}
			if c.VersionID != p.VersionID {
				return fmt.Errorf("候选快照与计划大纲版本不一致")
			}
			candidates[identity] = c
		}
	}
	seen := make(map[string]KnowledgePoint)
	occurrences := make(map[string]int)
	unitVersion := p.Units[0].KnowledgePoint.VersionID
	for i := range p.Units {
		u := &p.Units[i]
		if u.Index != i {
			return fmt.Errorf("单元下标不连续: 第 %d 项标记为 %d", i, u.Index)
		}
		// 单元身份允许 ID 或大纲代码任一存在：历史/受控调用可能只有代码。
		identity := BatchPointIdentity(u.KnowledgePoint)
		if identity == "" {
			return fmt.Errorf("单元 %d 缺少知识点快照", i)
		}
		if u.KnowledgePoint.Revision < 0 || u.KnowledgePoint.VersionID != unitVersion || (p.VersionID != "" && u.KnowledgePoint.VersionID != p.VersionID) {
			return fmt.Errorf("单元 %d 的大纲版本或修订无效", i)
		}
		if prior, ok := seen[identity]; ok && !reflect.DeepEqual(prior, u.KnowledgePoint) {
			return fmt.Errorf("单元 %d 与同身份知识点快照不一致", i)
		}
		seen[identity] = u.KnowledgePoint
		occurrences[identity]++
		if u.PointSequence != occurrences[identity] {
			return fmt.Errorf("单元 %d 出现序号不连续", i)
		}
		if u.TargetOffset != nil && *u.TargetOffset != u.PointSequence-1 {
			return fmt.Errorf("单元 %d 的冻结目标偏移与出现序号不一致", i)
		}
		if len(candidates) > 0 {
			c, ok := candidates[identity]
			if !ok || c.ID != u.KnowledgePoint.ID || c.VersionID != u.KnowledgePoint.VersionID || c.OutlineCode != u.KnowledgePoint.OutlineCode || c.Revision != u.KnowledgePoint.Revision {
				return fmt.Errorf("单元 %d 不属于冻结候选池对应修订", i)
			}
			if u.TargetOffset == nil {
				return fmt.Errorf("单元 %d 缺少冻结目标偏移", i)
			}
		}
	}
	if len(seen) > p.CandidateCount {
		return fmt.Errorf("单元知识点种数超过候选总数")
	}
	switch p.Mode {
	case BatchModePerPoint:
		if p.CountPerPoint < 1 {
			return fmt.Errorf("每点题数无效: %d", p.CountPerPoint)
		}
		if len(p.Units)%p.CountPerPoint != 0 || p.CandidateCount != len(p.Units)/p.CountPerPoint || len(seen) != p.CandidateCount {
			return fmt.Errorf("每点模式单元数与 候选×每点 不一致")
		}
		for _, count := range occurrences {
			if count != p.CountPerPoint {
				return fmt.Errorf("每点模式各候选生成数量不一致")
			}
		}
	case BatchModeRandomTotal:
		if p.CountPerPoint != 0 {
			return fmt.Errorf("随机模式不能携带每点题数")
		}
	}
	return nil
}

// UniquePlanPoints 返回计划中出现过的知识点（按首次出现顺序去重）。
func (p *BatchPlan) UniquePlanPoints() []KnowledgePoint {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	points := make([]KnowledgePoint, 0, len(p.Units))
	for i := range p.Units {
		kp := p.Units[i].KnowledgePoint
		identity := BatchPointIdentity(kp)
		if !seen[identity] {
			seen[identity] = true
			points = append(points, kp)
		}
	}
	return points
}

// PlanUnitsAsPoints 按下标返回计划单元的知识点序列（与 output.Items 对齐）。
func (p *BatchPlan) PlanUnitsAsPoints() []KnowledgePoint {
	if p == nil {
		return nil
	}
	points := make([]KnowledgePoint, len(p.Units))
	for i := range p.Units {
		points[i] = p.Units[i].KnowledgePoint
	}
	return points
}
