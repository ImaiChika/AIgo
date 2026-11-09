package domain

import "testing"

func TestReviewRevisionIsNotAContentVersion(t *testing.T) {
	current := A2Question{ID: "q", ClinicalStem: "当前正文", Version: 3, ReviewRevision: 7}
	changed := current
	changed.ReviewRevision++
	changed.Status = StatusAIReviewed
	if !QuestionContentEqual(current, changed) {
		t.Fatal("review metadata must not create a content version")
	}
	old := A2Question{ClinicalStem: "旧正文", Version: 1}
	restored := RestoreQuestionContent(current, old)
	if restored.ReviewRevision != 7 || restored.Version != 3 || restored.ClinicalStem != old.ClinicalStem {
		t.Fatal("content copy must preserve live workflow revision and version")
	}
}
