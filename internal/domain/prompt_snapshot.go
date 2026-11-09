package domain

import "time"

// AuthoringGuideline is an approved shared method, frozen independently of
// the medical material and of private author preferences.
type AuthoringGuideline struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	Source   string `json:"source"`
}

// GenerationPromptSnapshot freezes the exact baseline for accepted tasks.
// Appendix is opaque, immutable historical input; new preferences use private knowledge.
const DelegatedGenerationOutput = "program-fields-v1"

type GenerationPromptSnapshot struct {
	GenerationTemperature *float64             `json:"generation_temperature,omitempty"`
	OutputPolicy          string               `json:"output_policy,omitempty"`
	Guidelines            []AuthoringGuideline `json:"authoring_guidelines,omitempty"`
	PolicyVersion         string               `json:"policy_version,omitempty"`
	OwnerID               string               `json:"owner_id"`
	RoleID                string               `json:"role_id"`
	CapturedAt            time.Time            `json:"captured_at"`
	SystemPrompt          string               `json:"system_prompt"`
	UserPrompts           map[string]string    `json:"user_prompts"`
	Appendix              string               `json:"appendix,omitempty"`
}
