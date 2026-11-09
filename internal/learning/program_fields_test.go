package learning

import (
	"aigo/internal/domain"
	"encoding/json"
	"reflect"
	"testing"
)

func TestProgramSummaryIdentifiersPreservePromptsAndAdoptionBudget(t *testing.T) {
	_, job := budgetJob()
	originalMessages, _ := json.Marshal(analysisMessages(job))
	job.OutputPolicy = domain.LearningProgramOutput
	newMessages, _ := json.Marshal(analysisMessages(job))
	if string(originalMessages) != string(newMessages) {
		t.Fatal("baseline summary prompt changed")
	}
	expected := candidateFixture()
	items := candidateFixture()
	for i := range items {
		items[i].ID = ""
		for j := range items[i].Rules {
			items[i].Rules[j].ID = ""
		}
	}
	raw, _ := json.Marshal(map[string]any{"candidates": items})
	parsed, err := parseCandidatesForJob(string(raw), job)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range parsed {
		if c.ID != c.Style || c.Rules[0].ID != c.Style+"-r1" {
			t.Fatal("unstable or ambiguous identifiers", c)
		}
		before := domain.PersonalExperienceContentSize(domain.PersonalExperienceUse{Title: expected[i].Title, Rules: expected[i].Rules})
		after := domain.PersonalExperienceContentSize(domain.PersonalExperienceUse{Title: c.Title, Rules: c.Rules})
		if before != after {
			t.Fatal("identifier repair changed personal adoption budget", before, after)
		}
		c.ID = expected[i].ID
		for j := range c.Rules {
			c.Rules[j].ID = expected[i].Rules[j].ID
		}
		if !reflect.DeepEqual(c, expected[i]) {
			t.Fatal("non-identifier candidate data changed", c)
		}
	}
	job.OutputPolicy = ""
	if _, err := parseCandidatesForJob(string(raw), job); err == nil {
		t.Fatal("legacy contract silently relaxed")
	}
}
func TestProgramSummaryIdentifiersDoNotRepairSemanticFailures(t *testing.T) {
	for _, name := range []string{"duplicate_styles", "empty_rule", "unknown_field", "invalid_scope"} {
		t.Run(name, func(t *testing.T) {
			_, job := budgetJob()
			job.OutputPolicy = domain.LearningProgramOutput
			items := candidateFixture()
			switch name {
			case "duplicate_styles":
				items[1].Style = items[0].Style
			case "empty_rule":
				items[0].Rules[0].Text = ""
			case "unknown_field":
				items[0].Rules[0].Field = "password"
			case "invalid_scope":
				items[0].Scope.Kind = "shared"
			}
			raw, _ := json.Marshal(map[string]any{"candidates": items})
			if _, err := parseCandidatesForJob(string(raw), job); err == nil {
				t.Fatal("semantic candidate contract relaxed")
			}
		})
	}
}

func TestProgramOwnedIdentifiersIgnoreModelTypesOnlyForNewJobs(t *testing.T) {
	_, job := budgetJob()
	job.OutputPolicy = domain.LearningProgramOutput
	raw, _ := json.Marshal(map[string]any{"candidates": candidateFixture()})
	var object map[string]any
	json.Unmarshal(raw, &object)
	item := object["candidates"].([]any)[0].(map[string]any)
	item["id"] = 123
	item["rules"].([]any)[0].(map[string]any)["id"] = map[string]bool{"irrelevant": true}
	raw, _ = json.Marshal(object)
	parsed, err := parseCandidatesForJob(string(raw), job)
	if err != nil || parsed[0].ID != "concise" || parsed[0].Rules[0].ID != "concise-r1" {
		t.Fatal("model ID types still controlled program-owned identifiers", err)
	}
	job.OutputPolicy = ""
	if _, err := parseCandidatesForJob(string(raw), job); err == nil {
		t.Fatal("old typed contract changed")
	}
}

func TestProgramIdentifiersRemainUniqueAcrossMultipleCandidatesAndRules(t *testing.T) {
	_, job := budgetJob()
	job.OutputPolicy = domain.LearningProgramOutput
	items := candidateFixture()
	for i := range items {
		items[i].ID = "repeated"
		second := items[i].Rules[0]
		second.ID, second.Text = "repeated", "保留明确的时间顺序，避免重复交代相同时间信息"
		items[i].Rules[0].ID = "repeated"
		items[i].Rules = append(items[i].Rules, second)
	}
	raw, _ := json.Marshal(map[string]any{"candidates": items})
	got, err := parseCandidatesForJob(string(raw), job)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, candidate := range got {
		if seen[candidate.ID] {
			t.Fatal("duplicate candidate ID")
		}
		seen[candidate.ID] = true
		for j, rule := range candidate.Rules {
			if seen[rule.ID] {
				t.Fatal("rule IDs collide across candidates", rule.ID)
			}
			seen[rule.ID] = true
			rule.ID = items[i].Rules[j].ID
			if !reflect.DeepEqual(rule, items[i].Rules[j]) {
				t.Fatal("semantic rule data changed")
			}
		}
	}
}
