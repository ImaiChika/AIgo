package domain

import "testing"

func TestQuestionContentEqualIgnoresWorkflowFields(t *testing.T) {
	base := A2Question{
		ID: "q1", ClinicalStem: "题干", Options: []Option{{Label: "A", Text: "甲"}}, Answer: "A",
		Status: StatusAIDraft, Version: 1, BankIDs: []string{"bank-a"},
	}
	changedWorkflow := base
	changedWorkflow.Status = StatusReviewing
	changedWorkflow.BankIDs = []string{"bank-b"}
	if !QuestionContentEqual(base, changedWorkflow) {
		t.Fatal("状态和题库归属变化不应产生内容版本")
	}
	nilCollections := base
	nilCollections.SourceRefs = nil
	emptyCollections := base
	emptyCollections.SourceRefs = []SourceRef{}
	if !QuestionContentEqual(nilCollections, emptyCollections) {
		t.Fatal("nil 与空集合应视为相同内容")
	}
	changedContent := base
	changedContent.ClinicalStem = "新题干"
	if QuestionContentEqual(base, changedContent) {
		t.Fatal("题干变化必须被识别为内容版本变化")
	}
}

func TestRestoreQuestionContentKeepsIdentityAndBanks(t *testing.T) {
	current := A2Question{ID: "q1", ClinicalStem: "当前", BankIDs: []string{"bank-current"}, Version: 3, Status: StatusPublished}
	snapshot := A2Question{ID: "old-id", ClinicalStem: "历史", Options: []Option{{Label: "A", Text: "旧选项"}}, Answer: "A", BankIDs: []string{"bank-old"}}
	restored := RestoreQuestionContent(current, snapshot)
	if restored.ID != "q1" || restored.ClinicalStem != "历史" || restored.BankIDs[0] != "bank-current" || restored.Version != 3 || restored.Status != StatusPublished {
		t.Fatalf("恢复内容不应覆盖身份、题库、版本和状态: %+v", restored)
	}
}

func TestCanonicalLifecycleStatusUsesPublishedAsOnlyPassedState(t *testing.T) {
	if got := CanonicalLifecycleStatus(StatusApproved); got != StatusPublished {
		t.Fatalf("legacy approved should normalize to published, got %s", got)
	}
	if !IsPassedLifecycleStatus(StatusPublished) || !IsPassedLifecycleStatus(StatusApproved) {
		t.Fatal("published and legacy approved input should both resolve to the single passed lifecycle state")
	}
	if IsPassedLifecycleStatus(StatusReviewing) {
		t.Fatal("reviewing must not be treated as passed")
	}
}
