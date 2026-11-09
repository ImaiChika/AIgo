package domain

const (
	PermLearningAnalyze  = "learning:analyze"
	PermLearningSave     = "learning:save"
	PermLearningUse      = "learning:use"
	PermExperienceView   = "experience:view"
	PermExperienceManage = "experience:manage"
	PermExperienceUse    = "experience:use"
)

type LearningCapabilities struct {
	Analyze bool `json:"analyze"`
	Save    bool `json:"save"`
	Use     bool `json:"use"`
}

func LearningCapabilitiesFor(permissions []string) LearningCapabilities {
	have := map[string]bool{}
	for _, p := range permissions {
		have[p] = true
	}
	generate := have[PermQuestionGenerate] || have[PermBatchRun]
	return LearningCapabilities{Analyze: have[PermLearningAnalyze] && have[PermQuestionView], Save: have[PermLearningSave] && have[PermQuestionView], Use: (have[PermLearningUse] || have[PermExperienceView] && have[PermExperienceUse]) && generate}
}

type PersonalKnowledgeCapabilities struct {
	View   bool `json:"view"`
	Manage bool `json:"manage"`
	Use    bool `json:"use"`
}

func PersonalKnowledgeCapabilitiesFor(permissions []string) PersonalKnowledgeCapabilities {
	have := map[string]bool{}
	for _, p := range permissions {
		have[p] = true
	}
	c := LearningCapabilitiesFor(permissions)
	view := have[PermExperienceView] || c.Analyze || c.Save || have[PermQuestionView] && have[PermLearningUse] && (have[PermQuestionGenerate] || have[PermBatchRun])
	return PersonalKnowledgeCapabilities{View: view, Manage: have[PermExperienceView] && have[PermExperienceManage] || c.Save, Use: c.Use}
}

func IsPersonalKnowledgePermission(code string) bool {
	return code == PermExperienceView || code == PermExperienceManage || code == PermExperienceUse
}
func IsLearningPermission(code string) bool {
	return code == PermLearningAnalyze || code == PermLearningSave || code == PermLearningUse
}
