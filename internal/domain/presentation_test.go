package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPresentationInputIsExplicitAndStrict(t *testing.T) {
	valid, _ := json.Marshal(PresentationRequirements{SectionOrder: ClinicalSections()})
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"valid", string(valid), true}, {"empty object", `{}`, false}, {"partial", `{"section_order":["lab_results"]}`, false},
		{"unknown option", strings.TrimSuffix(string(valid), "}") + `,"hide_diagnosis":true}`, false},
		{"duplicate field", strings.TrimSuffix(string(valid), "}") + `,` + strings.TrimPrefix(string(valid), "{"), false},
		{"case alias", strings.Replace(string(valid), "section_order", "Section_Order", 1), false},
		{"array", `[]`, false}, {"oversize", `{"section_order":["` + strings.Repeat("x", 2100) + `"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p PresentationRequirements
			err := json.Unmarshal([]byte(tc.raw), &p)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	p := &PresentationRequirements{SectionOrder: ClinicalSections()}
	copy := p.Clone()
	p.SectionOrder[0] = "bad"
	if copy.Validate() != nil {
		t.Fatal("clone shares caller storage")
	}
	if p.Validate() == nil {
		t.Fatal("invalid group accepted")
	}
	if (*PresentationRequirements)(nil).Clone() != nil {
		t.Fatal("nil created a requirement")
	}
}
