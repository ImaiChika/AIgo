package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// PresentationRequirements is an explicit per-submission choice. Reference
// cards and free-form personal rules cannot create or replace this choice.
type PresentationRequirements struct {
	SectionOrder []string `json:"section_order"`
}

func ClinicalSections() []string {
	return []string{"chief_complaint", "present_illness", "past_history", "vitals", "physical_findings", "lab_results", "imaging_results", "other"}
}

func (p *PresentationRequirements) Validate() error {
	if p == nil {
		return nil
	}
	valid := map[string]bool{}
	for _, section := range ClinicalSections() {
		valid[section] = true
	}
	if len(p.SectionOrder) != len(valid) {
		return fmt.Errorf("请选择完整且不重复的病例分组顺序")
	}
	for _, section := range p.SectionOrder {
		if !valid[section] {
			return fmt.Errorf("病例分组顺序包含未知或重复分组")
		}
		delete(valid, section)
	}
	return nil
}

func (p *PresentationRequirements) Clone() *PresentationRequirements {
	if p == nil {
		return nil
	}
	return &PresentationRequirements{SectionOrder: append([]string(nil), p.SectionOrder...)}
}

// Reject ignored/duplicate settings instead of silently accepting an option
// that the application does not implement. Existing request fields are unchanged.
func (p *PresentationRequirements) UnmarshalJSON(raw []byte) error {
	if len(raw) > 2048 || !json.Valid(raw) {
		return fmt.Errorf("病例分组设置无效或过大")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return fmt.Errorf("病例分组设置须为对象")
	}
	seen := false
	var order []string
	for d.More() {
		key, err := d.Token()
		if err != nil || key != "section_order" || seen {
			return fmt.Errorf("病例分组设置包含未知或重复字段")
		}
		seen = true
		if err = d.Decode(&order); err != nil {
			return fmt.Errorf("病例分组顺序格式错误")
		}
	}
	if _, err = d.Token(); err != nil {
		return err
	}
	p.SectionOrder = order
	return p.Validate()
}

type PresentationFeedback struct {
	State           string   `json:"state"`
	SectionOrder    []string `json:"section_order"`
	PresentSections []string `json:"present_sections"`
	UnusedSections  []string `json:"unused_sections"`
	FactIDs         []string `json:"fact_ids"`
}
