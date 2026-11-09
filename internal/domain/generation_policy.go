package domain

import (
	"errors"
	"strings"
)

const InitialA2GenerationPolicy = "a2-clinical-20261023-v1"
const ReferenceA2GenerationPolicy = "a2-clinical-20261023-v2"

// SemanticA2GenerationPolicy removed the lexical wording bans but still asked the
// model for the facts-array case_plan; real runs kept producing broken JSON and
// were retired. Accepted snapshots of that era keep their frozen prompt.
const SemanticA2GenerationPolicy = "a2-clinical-20261023-v3"
const FixedFieldA2GenerationPolicy = "a2-clinical-20261023-v4"
const CurrentA2GenerationPolicy = "a2-clinical-20261024-v5"

func SupportsA2GenerationPolicy(version string) bool {
	return version == InitialA2GenerationPolicy || version == ReferenceA2GenerationPolicy || version == SemanticA2GenerationPolicy || version == FixedFieldA2GenerationPolicy || version == CurrentA2GenerationPolicy
}

func UsesAnswerReferenceReminder(version string) bool {
	return version == ReferenceA2GenerationPolicy || version == SemanticA2GenerationPolicy || version == FixedFieldA2GenerationPolicy || version == CurrentA2GenerationPolicy
}

// UsesFixedFieldCasePlan gates the simplified direct transport: plain string
// history fields instead of the id/section fact array.
func UsesFixedFieldCasePlan(version string) bool {
	return version == FixedFieldA2GenerationPolicy || version == CurrentA2GenerationPolicy
}

// UsesContextualCheckFormat binds a policy era to the evidence-based first
// check instead of the historical lexical second-wave gates.
func UsesContextualCheckFormat(version string) bool {
	return version == SemanticA2GenerationPolicy || version == FixedFieldA2GenerationPolicy || version == CurrentA2GenerationPolicy
}

func UsesFocusedCheckFormat(version string) bool { return version == CurrentA2GenerationPolicy }

// Generation eligibility does not remove or change an outline point. A clinical
// point about pathogenesis is still clinical; topic words never classify it.
func IsBasicMedicalPoint(p KnowledgePoint) bool {
	return ClassifyBasicScope(p) == ScopeBasicMedical
}

// IsEligibleClinicalPoint is the generation/admission gate. Conflicted legacy
// labels require correction and must not be mistaken for an eligible point.
func IsEligibleClinicalPoint(p KnowledgePoint) bool {
	return ClassifyBasicScope(p) == ScopeClinical
}

// BasicMedicalScope 分类结果：基础医学按分类或学科判定；学科与分类互相矛盾
// 的条目进入待核对，不进入有效大纲。Topic 措辞永不参与判定。
type BasicMedicalScope int

const (
	ScopeClinical BasicMedicalScope = iota
	ScopeBasicMedical
	ScopeConflicted
)

// basicMedicalSubjects 基础医学科目名（含 2024 临床大纲实际使用的短名：生理、
// 生化、病生、解剖、微生物、免疫、药理、病理）。临床综合下的考点不会以这些
// 科目作为专业/系统；科目与分类不一致即为伪装或脏数据。
var basicMedicalSubjects = map[string]bool{
	"生理": true, "生理学": true, "生物化学": true, "生化": true, "生物化学与分子生物学": true,
	"病理": true, "病理学": true, "病生": true, "病理生理学": true,
	"药理": true, "药理学": true,
	"免疫": true, "医学免疫学": true, "微生物": true, "医学微生物学": true,
	"解剖": true, "人体解剖学": true, "组织学与胚胎学": true,
}

func (s BasicMedicalScope) String() string {
	switch s {
	case ScopeBasicMedical:
		return "basic_medical"
	case ScopeConflicted:
		return "conflicted"
	default:
		return "clinical"
	}
}

// ClassifyBasicScope 返回知识点的有效范围分类：
//   - 分类以“基础医学”开头 → 基础医学（无论科目，宁可保守剔除）；
//   - 分类为空/未分类且科目为基础科目 → 基础医学；
//   - 分类为非基础分类但科目为基础科目 → 矛盾（待核对，禁止入库）；
//   - 其余 → 临床（可用）。
func ClassifyBasicScope(p KnowledgePoint) BasicMedicalScope {
	category := strings.TrimSpace(p.Category)
	subject := strings.TrimSpace(p.Subject)
	if strings.HasPrefix(category, "基础医学") {
		return ScopeBasicMedical
	}
	subjectBasic := basicMedicalSubjects[subject]
	if subjectBasic {
		if category == "" || category == "未分类" {
			return ScopeBasicMedical
		}
		return ScopeConflicted
	}
	return ScopeClinical
}

// ErrBasicMedicalScope 标记冻结快照中的知识点属于基础医学范围。有效大纲已剔除
// 该范围；在途单元以此终态停止（不烧重试、不换考点补生），知识点历史保留。
var ErrBasicMedicalScope = errors.New("基础医学或分类矛盾知识点不在有效出题大纲范围内")

func IsBasicMedicalScopeError(err error) bool { return errors.Is(err, ErrBasicMedicalScope) }

func StandardPresentation() *PresentationRequirements {
	return &PresentationRequirements{SectionOrder: ClinicalSections()}
}

func IsStandardPresentation(p *PresentationRequirements) bool {
	if p == nil {
		return false
	}
	order := ClinicalSections()
	if len(p.SectionOrder) != len(order) {
		return false
	}
	for i, s := range order {
		if p.SectionOrder[i] != s {
			return false
		}
	}
	return true
}
