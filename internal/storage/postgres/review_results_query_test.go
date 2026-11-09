package postgres

import (
	"strings"
	"testing"

	"aigo/internal/storage"
)

func TestReviewResultWhereCombinesPermissionAndFinalStatus(t *testing.T) {
	where, args, err := reviewResultWhere(storage.QuestionFilter{
		Tiers: []string{"working"}, BankScope: []string{"bank-a"}, ScopeRestricted: true,
		Unclassified: false, Keyword: "胸痛",
	}, "revision_required")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"q.status = ANY", "question_bank_members", "q.search_text ILIKE", "revision_required"} {
		if !strings.Contains(where, fragment) && fragment != "revision_required" {
			t.Fatalf("where missing %q: %s", fragment, where)
		}
	}
	if len(args) != 4 || args[len(args)-1] != "revision_required" {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestReviewResultWhereRejectsUnknownFinalStatus(t *testing.T) {
	if _, _, err := reviewResultWhere(storage.QuestionFilter{}, "unknown"); err == nil {
		t.Fatal("unknown final status should be rejected")
	}
}
