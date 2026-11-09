package domain

import "time"

// PrivateKnowledgeCard deliberately uses the same content schema as a shared
// card. Ownership and submitted evidence never enter Materials or public copies.
type PrivateKnowledgeCard struct {
	KnowledgeCard
	OwnerID  string            `json:"-"`
	Selected bool              `json:"selected"`
	Origin   *ExperienceSource `json:"origin,omitempty"`
}
type KnowledgeCardSaveRequest struct {
	Key   string          `json:"key"`
	Cards []KnowledgeCard `json:"cards"`
}
type KnowledgeCardRef struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}
type KnowledgeShareRequest struct {
	Key   string             `json:"key"`
	Cards []KnowledgeCardRef `json:"cards"`
}
type KnowledgeSubmission struct {
	SharedRevision  int            `json:"shared_revision,omitempty"`
	Evidence        []LearningRule `json:"evidence,omitempty"`
	OriginalVersion int            `json:"original_version,omitempty"`
	CurrentVersion  int            `json:"current_version,omitempty"`
	ID              string         `json:"id"`
	SubmitterID     string         `json:"submitter_id"`
	SubmitterName   string         `json:"submitter_name"`
	PrivateCardID   string         `json:"private_card_id"`
	PrivateRevision int            `json:"private_revision"`
	Card            KnowledgeCard  `json:"card"`
	Status          string         `json:"status"`
	ReviewNote      string         `json:"review_note"`
	ReviewedBy      string         `json:"reviewed_by"`
	SharedCardID    string         `json:"shared_card_id"`
	CreatedAt       time.Time      `json:"created_at"`
	ReviewedAt      *time.Time     `json:"reviewed_at,omitempty"`
}
type KnowledgeDecisionRequest struct {
	Key      string   `json:"key"`
	IDs      []string `json:"ids"`
	Decision string   `json:"decision"`
	Note     string   `json:"note"`
}
