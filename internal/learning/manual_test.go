package learning

import (
	"testing"

	"aigo/internal/domain"
)

func TestManualKnowledgePermissionsDoNotGrantAIHistory(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		permissions                []string
		view, manage, use, history bool
	}{
		{"manual_view", []string{"experience:view"}, true, false, false, false},
		{"latent_manage", []string{"experience:manage"}, false, false, false, false},
		{"manual_manage_without_questions", []string{"experience:view", "experience:manage"}, true, true, false, false},
		{"latent_use", []string{"experience:use", "question:generate"}, false, false, false, false},
		{"manual_use_without_questions", []string{"experience:view", "experience:use", "question:generate"}, true, false, true, false},
		{"manual_use_with_questions_not_AI_history", []string{"experience:view", "experience:use", "question:generate", "question:view"}, true, false, true, false},
		{"explicit_learning_use_history", []string{"learning:use", "question:generate", "question:view"}, true, false, true, true},
		{"explicit_learning_use_without_view", []string{"learning:use", "question:generate"}, false, false, true, false},
		{"existing_learning_save", []string{"learning:save", "question:view"}, true, true, false, true},
		{"shared_KB_separate", []string{"kb:view", "kb:manage", "kb:use"}, false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := domain.PersonalKnowledgeCapabilitiesFor(tc.permissions)
			if caps.View != tc.view || caps.Manage != tc.manage || caps.Use != tc.use || readJobs(tc.permissions) != tc.history {
				t.Fatal(caps, readJobs(tc.permissions))
			}
			if tc.name == "manual_manage_without_questions" && domain.LearningCapabilitiesFor(tc.permissions).Analyze {
				t.Fatal("manual management granted paid learning")
			}
		})
	}
}
