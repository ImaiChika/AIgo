package domain

import "time"

type PreferenceCounts struct {
	Met     int `json:"met"`
	Partial int `json:"partial"`
	NotMet  int `json:"not_met"`
}

func (c PreferenceCounts) Total() int { return c.Met + c.Partial + c.NotMet }

type PreferenceSummary struct {
	Eligible   int              `json:"eligible_current_versions"`
	Unrated    int              `json:"unrated_current_versions"`
	Current    PreferenceCounts `json:"current"`
	Initial    PreferenceCounts `json:"initial_content"`
	Revised    PreferenceCounts `json:"revised_content"`
	Historical PreferenceCounts `json:"historical"`
}

// PreferenceFeedback is the author's optional assessment of a specific content
// version. It is not an AI check, question edit, expert vote or approval.
type PreferenceFeedback struct {
	QuestionID      string    `json:"question_id"`
	QuestionVersion int       `json:"question_version"`
	Revision        int       `json:"revision"`
	Rating          string    `json:"rating"`
	Comment         string    `json:"comment"`
	AuthorID        string    `json:"author_id"`
	UpdatedAt       time.Time `json:"updated_at"`
}
