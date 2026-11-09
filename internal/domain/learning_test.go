package domain

import (
	"strings"
	"testing"
)

func TestPersonalExperienceSnapshotBudgetUsesWholeFrozenContent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot *PersonalExperienceSnapshot
		valid    bool
	}{
		{"legacy_nil", nil, true},
		{"empty", &PersonalExperienceSnapshot{Version: ExperienceProtocolVersion, OwnerID: "owner"}, true},
		{"exact_boundary", &PersonalExperienceSnapshot{Version: ExperienceProtocolVersion, OwnerID: "owner", Entries: []PersonalExperienceUse{{Title: strings.Repeat("字", MaxPersonalExperienceContentSize)}}}, true},
		{"whole_example_budget", &PersonalExperienceSnapshot{Version: ExperienceProtocolVersion, OwnerID: "owner", Entries: []PersonalExperienceUse{{Rules: []LearningRule{{Text: strings.Repeat("字", 1500), Before: strings.Repeat("例", 1501)}}}}}, false},
		{"all_entries_aggregate", &PersonalExperienceSnapshot{Version: ExperienceProtocolVersion, OwnerID: "owner", Entries: []PersonalExperienceUse{{Title: strings.Repeat("字", 1500)}, {Title: strings.Repeat("字", 1501)}}}, false},
		{"too_many_entries", &PersonalExperienceSnapshot{Version: ExperienceProtocolVersion, OwnerID: "owner", Entries: make([]PersonalExperienceUse, MaxPersonalExperienceEntries+1)}, false},
		{"unknown_protocol", &PersonalExperienceSnapshot{Version: "future", OwnerID: "owner"}, false},
		{"missing_owner", &PersonalExperienceSnapshot{Version: ExperienceProtocolVersion, OwnerID: " "}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePersonalExperienceSnapshot(tc.snapshot); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}
