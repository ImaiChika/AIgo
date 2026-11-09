package storage

import "context"

// A task-scoped, content-free guide. IDs are resolved from owned durable tasks.
type AuthoringTask struct {
	Kind             string   `json:"kind"`
	ID               string   `json:"id"`
	OwnerID          string   `json:"-"`
	Name             string   `json:"name"`
	Status           string   `json:"status"`
	Total            int      `json:"total"`
	GenerationFailed int      `json:"generation_failed"`
	QuestionIDs      []string `json:"-"`
	NeedsImport      bool     `json:"needs_import"`
}
type AuthoringCounts struct {
	Arrange     int `json:"arrange"`
	Confirmed   int `json:"confirmed"`
	Deleted     int `json:"deleted"`
	Checking    int `json:"checking"`
	Rejected    int `json:"rejected"`
	CheckFailed int `json:"check_failed"`
}
type AuthoringJourneyStore interface {
	ReadAuthoringTask(context.Context, string, string) (*AuthoringTask, error)
	CountAuthoringQuestions(context.Context, string, []string) (AuthoringCounts, error)
}
