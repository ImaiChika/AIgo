package knowledgebase

import (
	"strings"

	"aigo/internal/domain"
)

const ShortReferenceProtocol = "fact-alias-v1"

const ExpressionWorkflowVersion = "knowledge-v5"

// Only private cards or reviewed shared copies may carry selectable expression
// intent. Ordinary auto-applied base references never become instructions.
func IsExpressionCard(card domain.KnowledgeCard) bool {
	return card.Kind == "custom" && card.Content.Expression != nil &&
		(strings.HasPrefix(card.ID, "pkc-") || card.OriginSubmissionID != "")
}

func ExpressionNotes(bundle domain.KnowledgeBundle) []string {
	var notes []string
	seen := map[string]bool{}
	for _, card := range bundle.Cards {
		if !IsExpressionCard(card) {
			continue
		}
		note := strings.TrimSpace(card.Content.Expression.ExplanationNote)
		if note != "" && !seen[note] {
			notes = append(notes, note)
			seen[note] = true
		}
	}
	return notes
}

func HasExpressionCards(bundle domain.KnowledgeBundle) bool {
	for _, card := range bundle.Cards {
		if IsExpressionCard(card) {
			return true
		}
	}
	return false
}
