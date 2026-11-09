package batch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"slices"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func jsonUnmarshalString(raw string, v any) error {
	return json.Unmarshal([]byte(raw), v)
}

// randomIndex 是服务端随机抽样的可注入入口：返回 [0,n) 内的整数。
// 生产使用 crypto/rand；测试可替换为固定序列以断言抽样结果与恢复不重抽。
var randomIndex = cryptoRandomIndex

func cryptoRandomIndex(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("随机源不可用: " + err.Error())
	}
	return int(v.Int64())
}

func randomSeed() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// DrawRandomPlan 对唯一候选知识点做等概率、有放回的独立抽取，共 total 次，
// 生成显式批量计划。同一知识点可出现多次，部分候选可以一次不出现；
// total 大于候选数合法，小于候选数不保证覆盖。随机种子仅作诊断，
// 恢复与重跑以持久化的 Units 为准，绝不重新抽样。
func DrawRandomPlan(candidates []domain.KnowledgePoint, versionID string, total int) (*domain.BatchPlan, error) {
	if total < 1 {
		return nil, fmt.Errorf("随机批量题数必须是正整数")
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("候选知识点为空，不能随机抽题")
	}
	candidates, versionID, err := uniquePlanCandidates(candidates, versionID)
	if err != nil {
		return nil, err
	}
	plan := &domain.BatchPlan{
		Version:        domain.BatchPlanVersion,
		Mode:           domain.BatchModeRandomTotal,
		RequestedTotal: total,
		VersionID:      versionID,
		CandidateCount: len(candidates),
		Candidates:     candidateSummaries(candidates),
		Seed:           randomSeed(),
		Units:          make([]domain.BatchPlanUnit, 0, total),
	}
	sequence := make(map[string]int, len(candidates))
	for i := 0; i < total; i++ {
		kp := candidates[randomIndex(len(candidates))]
		identity := domain.BatchPointIdentity(kp)
		sequence[identity]++
		offset := sequence[identity] - 1
		plan.Units = append(plan.Units, domain.BatchPlanUnit{
			Index:          i,
			KnowledgePoint: kp,
			PointSequence:  sequence[identity],
			TargetOffset:   &offset,
		})
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

// PerPointPlan 把“每点 K 题”摊平成显式计划；与旧 flattenUnits 结果一致，
// 但计划持久化后恢复与重跑不再依赖 每点数=总数/点数 的推导。
func PerPointPlan(points []domain.KnowledgePoint, versionID string, countPerPoint int) (*domain.BatchPlan, error) {
	if len(points) == 0 {
		return nil, fmt.Errorf("知识点列表为空")
	}
	if countPerPoint < 1 {
		countPerPoint = 1
	}
	points, versionID, err := uniquePlanCandidates(points, versionID)
	if err != nil {
		return nil, err
	}
	if countPerPoint > int(^uint(0)>>1)/len(points) {
		return nil, fmt.Errorf("批量题数超过可支持范围")
	}
	plan := &domain.BatchPlan{
		Version:        domain.BatchPlanVersion,
		Mode:           domain.BatchModePerPoint,
		RequestedTotal: len(points) * countPerPoint,
		CountPerPoint:  countPerPoint,
		VersionID:      versionID,
		CandidateCount: len(points),
		Candidates:     candidateSummaries(points),
		Units:          make([]domain.BatchPlanUnit, 0, len(points)*countPerPoint),
	}
	index := 0
	for _, point := range points {
		for i := 1; i <= countPerPoint; i++ {
			offset := i - 1
			plan.Units = append(plan.Units, domain.BatchPlanUnit{
				Index:          index,
				KnowledgePoint: point,
				PointSequence:  i,
				TargetOffset:   &offset,
			})
			index++
		}
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

// uniquePlanCandidates 在抽样前去重，重复身份不能增加抽中权重；不同版本
// 或同身份的不同快照必须拒绝，不能在两个版本中任取其一。
func uniquePlanCandidates(points []domain.KnowledgePoint, versionID string) ([]domain.KnowledgePoint, string, error) {
	if versionID == "" && len(points) > 0 {
		versionID = points[0].VersionID
	}
	seen := make(map[string]domain.KnowledgePoint, len(points))
	unique := make([]domain.KnowledgePoint, 0, len(points))
	for _, point := range points {
		identity := domain.BatchPointIdentity(point)
		if identity == "" || point.Revision < 0 {
			return nil, "", fmt.Errorf("候选知识点身份或修订无效")
		}
		if point.VersionID != versionID {
			return nil, "", fmt.Errorf("候选知识点不属于同一大纲版本")
		}
		if previous, exists := seen[identity]; exists {
			if !reflect.DeepEqual(previous, point) {
				return nil, "", fmt.Errorf("相同候选知识点包含不同快照")
			}
			continue
		}
		// 脱离调用方的可变切片，防止提交前修改输入污染计划快照。
		point.Keywords = slices.Clone(point.Keywords)
		seen[identity] = point
		unique = append(unique, point)
	}
	return unique, versionID, nil
}

func candidateSummaries(points []domain.KnowledgePoint) []domain.BatchPlanCandidate {
	candidates := make([]domain.BatchPlanCandidate, len(points))
	for i, point := range points {
		candidates[i] = domain.BatchPlanCandidate{ID: point.ID, VersionID: point.VersionID, OutlineCode: point.OutlineCode, Revision: point.Revision}
	}
	return candidates
}

// BatchPlanFromRecord 解析任务记录中的显式计划；空计划返回 nil（旧任务）。
func BatchPlanFromRecord(record storage.BatchJobRecord) (*domain.BatchPlan, error) {
	if record.PlanJSON == "" {
		return nil, nil
	}
	var plan domain.BatchPlan
	if err := jsonUnmarshalString(record.PlanJSON, &plan); err != nil {
		return nil, fmt.Errorf("批量计划记录损坏: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return nil, fmt.Errorf("批量计划无效: %w", err)
	}
	if record.TotalCount != plan.RequestedTotal {
		return nil, fmt.Errorf("批量任务总数 %d 与冻结计划题数 %d 不一致", record.TotalCount, plan.RequestedTotal)
	}
	return &plan, nil
}

// BatchUnitsFromRecord 返回任务的权威单元序列：有显式计划按计划，否则按旧
// 规则由 PointsJSON × 每点数（总数/点数）摊平。计划与旧快照都不可用时报错，
// 由调用方按既有“记录损坏”路径收口。
func BatchUnitsFromRecord(record storage.BatchJobRecord) ([]domain.KnowledgePoint, error) {
	if plan, err := BatchPlanFromRecord(record); err != nil {
		return nil, err
	} else if plan != nil {
		return plan.PlanUnitsAsPoints(), nil
	}
	var points []domain.KnowledgePoint
	if err := jsonUnmarshalString(record.PointsJSON, &points); err != nil || len(points) == 0 {
		return nil, fmt.Errorf("任务知识点记录损坏")
	}
	total := len(points)
	if record.TotalCount > 0 {
		total = record.TotalCount
	}
	countPerPoint := total / len(points)
	if countPerPoint < 1 || countPerPoint*len(points) != total {
		return nil, fmt.Errorf("任务单元记录与知识点快照不一致")
	}
	return flattenUnits(points, countPerPoint), nil
}
