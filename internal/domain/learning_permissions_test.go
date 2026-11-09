package domain

import "testing"

func TestLearningCapabilitiesRespectIndependentDependencies(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions []string
		want        LearningCapabilities
	}{
		{"analyze_without_view", []string{PermLearningAnalyze}, LearningCapabilities{}},
		{"analyze_own_view", []string{PermLearningAnalyze, PermQuestionView}, LearningCapabilities{Analyze: true}},
		{"save_without_own_view", []string{PermLearningSave}, LearningCapabilities{}},
		{"save_own_view", []string{PermLearningSave, PermQuestionView}, LearningCapabilities{Save: true}},
		{"use_without_generation", []string{PermLearningUse}, LearningCapabilities{}},
		{"single_generation", []string{PermLearningUse, PermQuestionGenerate}, LearningCapabilities{Use: true}},
		{"batch_generation", []string{PermLearningUse, PermBatchRun}, LearningCapabilities{Use: true}},
		{"shared_kb_does_not_learn", []string{PermKBView, PermKBManage, PermKBUse}, LearningCapabilities{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LearningCapabilitiesFor(tc.permissions); got != tc.want {
				t.Fatalf("got%+v want%+v", got, tc.want)
			}
		})
	}
}
