package domain

import "testing"

func TestPersonallyExportableRequiresCurrentConfirmationAndLifecycle(t *testing.T) {
	t.Parallel()
	base := A2Question{Status: StatusAIReviewed, Version: 2, LibraryState: LibraryActive, ConfirmedVersion: 2, ConfirmationSource: ConfirmSourceSelf}
	cases := []struct {
		name   string
		change func(*A2Question)
		want   bool
	}{
		{name: "self_confirmed", want: true},
		{name: "review_confirmed", change: func(q *A2Question) { q.Status = StatusPublished; q.ConfirmationSource = ConfirmSourceReview }, want: true},
		{name: "legacy_confirmed", change: func(q *A2Question) { q.Status = StatusPublished; q.ConfirmationSource = ConfirmSourceLegacy }, want: true},
		{name: "unfiled", change: func(q *A2Question) { q.LibraryState = LibraryUnfiled }},
		{name: "deleted", change: func(q *A2Question) { q.LibraryState = LibraryDeleted }},
		{name: "old_confirmation", change: func(q *A2Question) { q.ConfirmedVersion-- }},
		{name: "no_source", change: func(q *A2Question) { q.ConfirmationSource = "" }},
		{name: "unknown_source", change: func(q *A2Question) { q.ConfirmationSource = "forged" }},
		{name: "zero_version", change: func(q *A2Question) { q.Version = 0; q.ConfirmedVersion = 0 }},
	}
	for _, status := range []QuestionStatus{StatusAIDraft, StatusAutoChecked, StatusReviewing, StatusConflict, StatusRevisionRequired, StatusRejected, StatusArchived} {
		value := status
		cases = append(cases, struct {
			name   string
			change func(*A2Question)
			want   bool
		}{name: string(value), change: func(q *A2Question) { q.Status = value }})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			if tc.change != nil {
				tc.change(&q)
			}
			if got := q.PersonallyExportable(); got != tc.want {
				t.Fatalf("got %v want %v (%+v)", got, tc.want, q)
			}
		})
	}
	var absent *A2Question
	if absent.PersonallyExportable() {
		t.Fatal("nil question cannot be exported")
	}
}
