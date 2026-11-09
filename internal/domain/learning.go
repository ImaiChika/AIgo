package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const LearningProtocolVersion = "learning-compare-v1"
const ExperienceProtocolVersion = "personal-experience-v1"
const LearningDiverseRetrySampling = "failure-aware-retry-v1"
const LearningProgramOutput = "learning-program-fields-v1"

const LearningScopedReflectionPrompt = "reflection-scope-v2"

const (
	LearningAttemptTimeout  = 180 * time.Second
	LearningExecutionBudget = 420 * time.Second
	LearningLeaseBuffer     = 30 * time.Second
)

const MaxPersonalExperienceEntries = 5
const MaxPersonalExperienceContentSize = 3000

func (j *LearningJob) AnalysisTemperature() float64 {
	if j.SamplingPolicy == LearningConfiguredSampling && j.SamplingTemperature != nil {
		return *j.SamplingTemperature
	}
	if j.SamplingPolicy == LearningDiverseRetrySampling && j.Attempts > 1 && j.ErrorKind != "protocol_error" {
		return 0.6
	}
	return 0.2
}

// Zero timing fields identify tasks accepted under the original shared 120s
// policy. A new server must not silently extend those persisted tasks.
func (j *LearningJob) ExecutionBudget() time.Duration {
	if j.ExecutionBudgetSeconds <= 0 {
		return 120 * time.Second
	}
	return min(time.Duration(j.ExecutionBudgetSeconds)*time.Second, LearningExecutionBudget)
}

func (j *LearningJob) AttemptTimeout() time.Duration {
	if j.AttemptTimeoutSeconds <= 0 {
		return min(120*time.Second, j.ExecutionBudget())
	}
	return min(time.Duration(j.AttemptTimeoutSeconds)*time.Second, LearningAttemptTimeout, j.ExecutionBudget())
}

type LearningExpectation struct {
	QuestionID              string `json:"question_id"`
	ExpectedVersion         int    `json:"expected_version"`
	ExpectedLibraryRevision *int64 `json:"expected_library_revision"`
	ExpectedReviewRevision  *int64 `json:"expected_review_revision"`
}

type LearningFieldChange struct {
	Field  string          `json:"field"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// LearningSource is private persisted comparison evidence, never a public KB card.
type LearningSource struct {
	Original                *A2Question         `json:"original"`
	Current                 *A2Question         `json:"current"`
	InitialCheckPassed      bool                `json:"initial_check_passed"`
	ActiveReview            bool                `json:"active_review"`
	ShareStatus             QuestionShareStatus `json:"share_status"`
	SharedSnapshotAvailable bool                `json:"shared_snapshot_available"`
}

type LearningComparison struct {
	Original                 *A2Question           `json:"original"`
	Current                  *A2Question           `json:"current"`
	Changes                  []LearningFieldChange `json:"changes"`
	OriginalHash             string                `json:"original_hash"`
	CurrentHash              string                `json:"current_hash"`
	Expectation              LearningExpectation   `json:"expectation"`
	CanAnalyze               bool                  `json:"can_analyze"`
	ExpressionCardsSupported bool                  `json:"expression_cards_supported,omitempty"`
	BlockedReason            string                `json:"blocked_reason,omitempty"`
}

type ExperienceScope struct {
	Kind       string   `json:"kind"` // point / profession / all
	PointIDs   []string `json:"point_ids,omitempty"`
	Profession string   `json:"profession,omitempty"`
}

type LearningRule struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	Reason        string `json:"reason"`
	Field         string `json:"field"`
	Before        string `json:"before"`
	After         string `json:"after"`
	MedicalChange bool   `json:"medical_change"`
}

type LearningCandidate struct {
	ID    string          `json:"id"`
	Style string          `json:"style"` // concise / detailed / checklist
	Title string          `json:"title"`
	Scope ExperienceScope `json:"scope"`
	Rules []LearningRule  `json:"rules"`
}

type LearningJob struct {
	OutputPolicy        string   `json:"output_policy,omitempty"`
	SamplingTemperature *float64 `json:"sampling_temperature,omitempty"`
	PromptPolicy        string   `json:"prompt_policy,omitempty"`

	// Missing policy keeps the original 0.2 sampling on every attempt.
	SamplingPolicy string `json:"sampling_policy,omitempty"`

	// Timing is frozen at submission. Missing fields retain the original policy.
	ExecutionBudgetSeconds int `json:"execution_budget_seconds,omitempty"`
	AttemptTimeoutSeconds  int `json:"attempt_timeout_seconds,omitempty"`

	ID                  string                   `json:"id"`
	OwnerID             string                   `json:"-"`
	RoleID              string                   `json:"-"`
	RequestKey          string                   `json:"request_key"`
	RequestHash         string                   `json:"-"`
	QuestionID          string                   `json:"question_id"`
	OriginalVersion     int                      `json:"original_version"`
	CurrentVersion      int                      `json:"current_version"`
	OriginalHash        string                   `json:"original_hash"`
	CurrentHash         string                   `json:"current_hash"`
	LibraryRevision     int64                    `json:"library_revision"`
	ReviewRevision      int64                    `json:"review_revision"`
	ProtocolVersion     string                   `json:"protocol_version"`
	Note                string                   `json:"note,omitempty"`
	Source              *LearningSource          `json:"-"`
	Changes             []LearningFieldChange    `json:"changes"`
	Status              string                   `json:"status"` // pending/running/succeeded/failed
	Revision            int                      `json:"revision"`
	Attempts            int                      `json:"attempts"`
	MaxAttempts         int                      `json:"max_attempts"`
	Candidates          []LearningCandidate      `json:"candidates"`
	ImportedExperiences map[string]string        `json:"imported_experiences,omitempty"`
	AttemptHistory      []LearningAttemptSummary `json:"attempt_history,omitempty"`
	Warnings            []string                 `json:"warnings,omitempty"`
	ErrorKind           string                   `json:"error_kind,omitempty"`
	Error               string                   `json:"error,omitempty"`
	ModelCallIDs        []string                 `json:"model_call_ids,omitempty"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
	FirstStartedAt      *time.Time               `json:"first_started_at,omitempty"`
	DeadlineAt          *time.Time               `json:"deadline_at,omitempty"`
	NextAttemptAt       *time.Time               `json:"next_attempt_at,omitempty"`
	CompletedAt         *time.Time               `json:"completed_at,omitempty"`
	LeaseToken          string                   `json:"-"`
	LeasedUntil         *time.Time               `json:"-"`
	SourceCurrent       bool                     `json:"source_current"`
	CanImport           bool                     `json:"can_import"`
	BlockedReason       string                   `json:"blocked_reason,omitempty"`
}

type LearningSubmitRequest struct {
	LearningExpectation
	RequestKey string `json:"request_key"`
	Note       string `json:"note,omitempty"`
}

type LearningAttemptSummary struct {
	Attempt     int       `json:"attempt"`
	Status      string    `json:"status"`
	ErrorKind   string    `json:"error_kind,omitempty"`
	ModelCallID string    `json:"model_call_id,omitempty"`
	CompletedAt time.Time `json:"completed_at"`
}

type LearningCandidateEdit struct {
	LearningExpectation
	JobID            string              `json:"job_id"`
	ExpectedRevision int                 `json:"expected_revision"`
	Candidates       []LearningCandidate `json:"candidates"`
}

type LearningImportItem struct {
	CandidateID           string          `json:"candidate_id"`
	Title                 string          `json:"title"`
	Scope                 ExperienceScope `json:"scope"`
	Rules                 []LearningRule  `json:"rules"`  // selected stable rule IDs plus final teacher-edited text
	Status                string          `json:"status"` // draft / active
	ConfirmMedicalChanges bool            `json:"confirm_medical_changes"`
	AsExpression          bool            `json:"as_expression,omitempty"`
}

type LearningImportRequest struct {
	LearningExpectation
	JobID            string               `json:"job_id"`
	ExpectedRevision int                  `json:"expected_revision"`
	ImportKey        string               `json:"import_key"`
	Items            []LearningImportItem `json:"items"`
}

type ExperienceSource struct {
	Evidence          []LearningRule `json:"evidence,omitempty"`
	Kind              string         `json:"kind,omitempty"`
	OriginID          string         `json:"origin_id,omitempty"`
	OriginType        string         `json:"origin_type,omitempty"`
	OriginOrder       int            `json:"origin_order,omitempty"`
	OriginDeletedAt   *time.Time     `json:"origin_deleted_at,omitempty"`
	OriginRequestHash string         `json:"origin_request_hash,omitempty"`
	JobID             string         `json:"job_id"`
	CandidateID       string         `json:"candidate_id"`
	QuestionID        string         `json:"question_id"`
	OriginalVersion   int            `json:"original_version"`
	CurrentVersion    int            `json:"current_version"`
	OriginalHash      string         `json:"original_hash"`
	CurrentHash       string         `json:"current_hash"`
}

type PersonalKnowledgeCreateRequest struct {
	CreateKey             string          `json:"create_key"`
	Title                 string          `json:"title"`
	Scope                 ExperienceScope `json:"scope"`
	Rules                 []LearningRule  `json:"rules"`
	Status                string          `json:"status"`
	ConfirmMedicalChanges bool            `json:"confirm_medical_changes"`
}

type AuthoringExperience struct {
	MedicalConfirmed bool             `json:"medical_confirmed"`
	ID               string           `json:"id"`
	OwnerID          string           `json:"-"`
	Kind             string           `json:"kind"`
	ScopeType        string           `json:"scope_type"` // always personal; applicability is Scope
	Title            string           `json:"title"`
	Scope            ExperienceScope  `json:"scope"`
	Rules            []LearningRule   `json:"rules"`
	Status           string           `json:"status"` // draft/active/archived
	Revision         int              `json:"revision"`
	Source           ExperienceSource `json:"source"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// PersonalExperienceSnapshot freezes a bounded set once, before a generation
// run or batch is admitted. Later edits affect only future submissions.
type PersonalExperienceSnapshot struct {
	Version       string                  `json:"version"`
	OwnerID       string                  `json:"owner_id"`
	CapturedAt    time.Time               `json:"captured_at"`
	Entries       []PersonalExperienceUse `json:"entries"`
	ExcludedCount int                     `json:"excluded_count"`
}

type PersonalExperienceUse struct {
	ID              string          `json:"id"`
	Revision        int             `json:"revision"`
	Title           string          `json:"title"`
	Scope           ExperienceScope `json:"scope"`
	Rules           []LearningRule  `json:"rules"`
	MatchedPointIDs []string        `json:"matched_point_ids"`
	MatchReason     string          `json:"match_reason"`
}

// PersonalExperienceContentSize shares the freeze-time budget with resumed
// tasks. Keep the original conservative byte accounting for match reasons so
// existing snapshots retain their C5 meaning.
func PersonalExperienceContentSize(entry PersonalExperienceUse) int {
	size := utf8.RuneCountInString(entry.Title) + len(entry.MatchReason)
	for _, rule := range entry.Rules {
		size += utf8.RuneCountInString(rule.Text + rule.Reason + rule.Before + rule.After)
	}
	return size
}

// ValidatePersonalExperienceSnapshot rejects immutable corrupt input before a
// paid call. Nil is the pre-C5/no-adoption protocol and remains valid.
func ValidatePersonalExperienceSnapshot(snapshot *PersonalExperienceSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.Version != ExperienceProtocolVersion || strings.TrimSpace(snapshot.OwnerID) == "" {
		return fmt.Errorf("个人经验冻结协议无效")
	}
	if len(snapshot.Entries) > MaxPersonalExperienceEntries {
		return fmt.Errorf("个人经验快照超过%d条上限", MaxPersonalExperienceEntries)
	}
	used := 0
	for _, entry := range snapshot.Entries {
		used += PersonalExperienceContentSize(entry)
		if used > MaxPersonalExperienceContentSize {
			return fmt.Errorf("个人经验快照超过%d字预算", MaxPersonalExperienceContentSize)
		}
	}
	return nil
}
