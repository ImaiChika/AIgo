package domain

import "time"

// KnowledgeCard stores versioned reference material and optional author intent.
type KnowledgeCard struct {
	OriginSubmissionID string           `json:"origin_submission_id,omitempty"`
	ID                 string           `json:"id"`
	Revision           int              `json:"revision"`
	Kind               string           `json:"kind"`
	Title              string           `json:"title"`
	Status             string           `json:"status"`
	KnowledgePoints    []KnowledgePoint `json:"knowledge_points"`
	Content            KnowledgeContent `json:"content"`
	Source             string           `json:"source"`
	Note               string           `json:"note"`
	CreatedBy          string           `json:"created_by"`
	UpdatedBy          string           `json:"updated_by"`
	CreatedAt          time.Time        `json:"created_at"`
	UpdatedAt          time.Time        `json:"updated_at"`
}

type KnowledgeContent struct {
	ExamTargets   []string                `json:"exam_targets"`
	Features      []string                `json:"features"`
	Preconditions []string                `json:"preconditions"`
	Facts         []KnowledgeFact         `json:"facts"`
	Pitfalls      []string                `json:"pitfalls"`
	CustomText    string                  `json:"custom_text"`
	Reference     *ExamReference          `json:"reference,omitempty"`
	Expression    *ExpressionRequirements `json:"expression,omitempty"`
}

// ExpressionRequirements is explicit author intent, separate from reference
// text and private maintenance notes. It never changes medical or access rules.
type ExpressionRequirements struct {
	Instructions    string `json:"instructions,omitempty"`
	ExplanationNote string `json:"explanation_note,omitempty"`
}

type KnowledgeFact struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	AppliesWhen string `json:"applies_when"`
	Source      string `json:"source"`
}

type ExamReference struct {
	Year        string   `json:"year"`
	Stem        string   `json:"stem"`
	Options     []Option `json:"options"`
	Answer      string   `json:"answer"`
	Explanation string   `json:"explanation"`
}

// KnowledgeWorkflowSnapshot is an immutable, bounded generation input.
type KnowledgeWorkflowSnapshot struct {
	MaterialPolicy    string                    `json:"material_policy,omitempty"`
	ReferenceProtocol string                    `json:"reference_protocol,omitempty"`
	Presentation      *PresentationRequirements `json:"presentation,omitempty"`
	Version           string                    `json:"version"`
	CapturedAt        time.Time                 `json:"captured_at"`
	Bundles           []KnowledgeBundle         `json:"bundles"`
}
type KnowledgeBundle struct {
	KnowledgePointID string          `json:"knowledge_point_id"`
	ExcludedCards    int             `json:"excluded_cards"`
	Cards            []KnowledgeCard `json:"cards"`
}

// QuestionGenerationContext is stored separately from public question content.
// It remains available to the first check even if cards are subsequently edited.
type QuestionGenerationContext struct {
	ReferenceProtocol string                    `json:"reference_protocol,omitempty"`
	Presentation      *PresentationRequirements `json:"presentation,omitempty"`
	WorkflowVersion   string                    `json:"workflow_version"`
	CapturedAt        time.Time                 `json:"captured_at"`
	Bundle            KnowledgeBundle           `json:"bundle"`
	Target            WorkflowTarget            `json:"target"`
	Basis             KnowledgeBasis            `json:"basis"`
}
type WorkflowTarget struct {
	ID           string `json:"id,omitempty"`
	CardID       string `json:"card_id"`
	CardRevision int    `json:"card_revision"`
	Target       string `json:"target"`
}
type KnowledgeBasis struct {
	TargetID       string        `json:"target_id,omitempty"`
	CasePlan       *ClinicalPlan `json:"case_plan,omitempty"`
	Target         string        `json:"target"`
	FactRefs       []string      `json:"fact_refs"`
	ConditionNotes string        `json:"condition_notes"`
	AnswerSupport  string        `json:"answer_support"`
}

// ClinicalPlan is the immutable first-generation artifact, not editable patient
// data or a new public question format. Ordering belongs to this generation.
type ClinicalPlan struct {
	Person          string           `json:"person"`
	Facts           []ClinicalFact   `json:"facts"`
	LeadIn          string           `json:"lead_in"`
	Answer          string           `json:"answer,omitempty"`
	OptionEvidence  []OptionEvidence `json:"option_evidence"`
	PreferenceNotes []string         `json:"preference_notes,omitempty"`
	// Populated from frozen, explicitly selected cards by the application, not
	// from the model's self-reported preference_notes.
	ExplanationNotes []string `json:"explanation_notes,omitempty"`
}

type ClinicalFact struct {
	ID      string `json:"id"`
	Section string `json:"section"`
	Text    string `json:"text"`
}

type OptionEvidence struct {
	Label       string   `json:"label"`
	CaseRefs    []string `json:"case_refs"`
	FactRefs    []string `json:"fact_refs,omitempty"`
	Reason      string   `json:"reason"`
	Limitations string   `json:"limitations,omitempty"`
}
