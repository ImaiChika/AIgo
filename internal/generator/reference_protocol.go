package generator

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"fmt"
)

// Names are derived solely from the ordered immutable bundle. The persisted
// evidence and first check always use canonical versioned references.
type referenceNames struct{ toShort, toCanonical map[string]string }

func newReferenceNames(snapshot *domain.KnowledgeWorkflowSnapshot, bundle domain.KnowledgeBundle) referenceNames {
	r := referenceNames{}
	if snapshot == nil || snapshot.ReferenceProtocol != knowledgebase.ShortReferenceProtocol {
		return r
	}
	r.toShort = map[string]string{}
	r.toCanonical = map[string]string{}
	for _, card := range bundle.Cards {
		if card.Kind != "medical" {
			continue
		}
		for _, fact := range card.Content.Facts {
			full := knowledgebase.FactRef(card, fact)
			if _, ok := r.toShort[full]; ok {
				continue
			}
			short := fmt.Sprintf("k%d", len(r.toShort)+1)
			r.toShort[full] = short
			r.toCanonical[short] = full
		}
	}
	return r
}
func (r referenceNames) name(full string) string {
	if short, ok := r.toShort[full]; ok {
		return short
	}
	return full
}
func (r referenceNames) materials(materials []knowledgebase.Material) []knowledgebase.Material {
	for i := range materials {
		if materials[i].Kind != "medical" {
			continue
		}
		for j := range materials[i].Content.Facts {
			materials[i].Content.Facts[j].ID = r.name(materials[i].Content.Facts[j].ID)
		}
	}
	return materials
}
func (r referenceNames) expand(basis *domain.KnowledgeBasis) {
	expand := func(refs []string) {
		for i, id := range refs {
			if full, ok := r.toCanonical[id]; ok {
				refs[i] = full
			}
		}
	}
	expand(basis.FactRefs)
	if basis.CasePlan != nil {
		for i := range basis.CasePlan.OptionEvidence {
			expand(basis.CasePlan.OptionEvidence[i].FactRefs)
		}
	}
}
func (r referenceNames) contract() string {
	if r.toShort == nil {
		return ""
	}
	return "\n【冻结医学事实引用协议 fact-alias-v1】医学事实只使用materials.content.facts.id给出的k编号，逐字复制，不自行编造或混用病例c编号。knowledge_basis.fact_refs先列齐本题实际采用的医学事实；每个option_evidence.fact_refs必须是该顶层列表的子集。若不同选项采用多条事实，须先在顶层列齐再逐项选取；正确项还须包含其answer_fact_candidates中的适用事实。程序只还原冻结编号，不推测医学引用；未知、重复、未选或不属于目标的引用仍拒绝。医学推理、完整资料、原首检和权限保持。\n"
}
