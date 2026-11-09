package knowledgebase

import (
	"aigo/internal/domain"
	"context"
)

const GuidelineKind = "authoring_guideline"

// ActiveGuidelines is called within the submission's current authoring access
// transaction. Shared methods are independent of optional medical KB use.
// Bound the addition; never concatenate an entire training deck into a prompt.
func (s *Service) ActiveGuidelines(ctx context.Context) ([]domain.AuthoringGuideline, error) {
	cards, _, err := s.Store.ListKnowledgeCards(ctx, Filter{Kind: GuidelineKind, Status: "active", Limit: 3})
	if err != nil {
		return nil, err
	}
	result := []domain.AuthoringGuideline{}
	for _, card := range cards {
		if err := Validate(card, true); err != nil {
			return nil, err
		}
		result = append(result, domain.AuthoringGuideline{ID: card.ID, Revision: card.Revision, Title: card.Title, Text: card.Content.CustomText, Source: card.Source})
	}
	return result, nil
}
