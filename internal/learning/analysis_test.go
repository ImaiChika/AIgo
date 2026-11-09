package learning

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func TestProfessionalExperienceMatchesCanonicalClinicalProfession(t *testing.T) {
	ids, _, _ := matchExperience(domain.ExperienceScope{Kind: "profession", Profession: "消化"}, []domain.KnowledgePoint{{ID: "digestive-point", Category: "临床综合", Subject: "三、消化系统"}})
	if len(ids) != 1 || ids[0] != "digestive-point" {
		t.Fatal("canonical profession did not match syllabus system", ids)
	}
}

func candidateFixture() []domain.LearningCandidate {
	items := []domain.LearningCandidate{}
	for _, style := range []string{"concise", "detailed", "checklist"} {
		items = append(items, domain.LearningCandidate{ID: "candidate-" + style, Style: style, Title: "表达经验", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: style + "-1", Text: "保留完成本题决策所必需的病例信息", Reason: "避免无关背景", Field: "clinical_stem", Before: "旧表达", After: "新表达"}}})
	}
	return items
}

func TestParseCandidatesRequiresThreeBoundedDistinctStyles(t *testing.T) {
	items := candidateFixture()
	raw, _ := json.Marshal(map[string]any{"candidates": items})
	parsed, err := ParseCandidates(string(raw))
	if err != nil || len(parsed) != 3 || parsed[0].Rules[0].Text != items[0].Rules[0].Text {
		t.Fatalf("one response must provide three usable variants: %v %v", parsed, err)
	}
	for _, name := range []string{"missing_style", "duplicate_id", "empty_rules", "invalid_scope", "unknown_field"} {
		t.Run(name, func(t *testing.T) {
			copy := candidateFixture()
			switch name {
			case "missing_style":
				copy = copy[:2]
			case "duplicate_id":
				copy[1].ID = copy[0].ID
			case "empty_rules":
				copy[0].Rules = nil
			case "invalid_scope":
				copy[0].Scope = domain.ExperienceScope{Kind: "shared"}
			case "unknown_field":
				copy[0].Rules[0].Field = "password"
			}
			raw, _ := json.Marshal(map[string]any{"candidates": copy})
			if _, err := ParseCandidates(string(raw)); err == nil {
				t.Fatal("invalid candidate contract accepted")
			}
		})
	}
}

func TestCompareContentIgnoresLifecycleAndBindsActualContent(t *testing.T) {
	original := domain.A2Question{ID: "question", Version: 1, ClinicalStem: "旧题干", Answer: "A", Options: []domain.Option{{Label: "A", Text: "甲"}}}
	current := original
	current.Version = 2
	current.ClinicalStem = "新题干"
	current.Status = domain.StatusPublished
	current.LibraryRevision = 20
	changes, first, last := CompareContent(original, current)
	if len(changes) != 1 || changes[0].Field != "clinical_stem" || first == "" || first == last {
		t.Fatal("comparison is not bound to saved content", changes, first, last)
	}
	current.ClinicalStem = original.ClinicalStem
	changes, first, last = CompareContent(original, current)
	if len(changes) != 0 || first != last {
		t.Fatal("lifecycle was mistaken for teacher content edit", changes, first, last)
	}
}

func TestCandidateDecodeDiagnosticsAreTypedBoundedAndPrivate(t *testing.T) {
	for _, tc := range []struct{ name, raw, kind string }{{"syntax", `{"candidates":[}`, "json_syntax"}, {"type", `[]`, "json_type"}, {"unknown", `{"PRIVATE_FIELD":"PRIVATE_VALUE","candidates":[]}`, "unknown_field"}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCandidates(tc.raw)
			var decode *CandidateJSONError
			if !errors.As(err, &decode) || decode.Kind != tc.kind || !errors.Is(err, storage.ErrLearningInvalid) {
				t.Fatal("decode category lost", err)
			}
			if strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal("response data leaked through error")
			}
			if tc.kind == "json_syntax" && decode.Offset <= 0 {
				t.Fatal("syntax offset lost")
			}
		})
	}
}
