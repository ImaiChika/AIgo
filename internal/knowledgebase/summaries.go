package knowledgebase

import (
	"aigo/internal/domain"
	"context"
)

type SummaryStore interface {
	ListKnowledgeSummaries(context.Context, Filter) ([]domain.KnowledgeCardSummary, int, error)
	ListPrivateKnowledgeSummaries(context.Context, string, Filter) ([]domain.KnowledgeCardSummary, int, error)
}

func (s *Service) Summaries(ctx context.Context, uid, role string, f Filter) (items []domain.KnowledgeCardSummary, total int, caps Capabilities, err error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if len(f.Query) > 600 {
		return nil, 0, caps, ErrInvalid
	}
	items = []domain.KnowledgeCardSummary{}
	err = s.Access(ctx, uid, role, func(tx context.Context, c Capabilities) error {
		caps = c
		if !c.View {
			return ErrDenied
		}
		var e error
		if st, ok := s.Store.(SummaryStore); ok {
			items, total, e = st.ListKnowledgeSummaries(tx, f)
			return e
		}
		cards, n, e := s.Store.ListKnowledgeCards(tx, f)
		total = n
		for _, card := range cards {
			items = append(items, domain.SummarizeKnowledgeCard(card, nil))
		}
		return e
	})
	return
}
func (s *Service) PrivateSummaries(ctx context.Context, uid, role string, f Filter) (items []domain.KnowledgeCardSummary, total int, err error) {
	items = []domain.KnowledgeCardSummary{}
	f = collectionFilter(f)
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return ErrDenied
		}
		var e error
		if projected, ok := s.Store.(SummaryStore); ok {
			items, total, e = projected.ListPrivateKnowledgeSummaries(tx, uid, f)
			return e
		}
		cards, n, e := st.ListPrivateKnowledge(tx, uid, f)
		total = n
		for _, card := range cards {
			items = append(items, domain.SummarizeKnowledgeCard(card.KnowledgeCard, &card.Selected))
		}
		return e
	})
	return
}
func (s *Service) PrivateDetail(ctx context.Context, uid, role, id string) (card *domain.PrivateKnowledgeCard, err error) {
	err = s.collectionAccess(ctx, uid, role, func(tx context.Context, st CollectionStore, p []string) error {
		if !domain.PersonalKnowledgeCapabilitiesFor(p).View {
			return ErrDenied
		}
		var e error
		card, e = st.GetPrivateKnowledge(tx, uid, id, false)
		if e == nil && card == nil {
			return ErrNotFound
		}
		return e
	})
	return
}
