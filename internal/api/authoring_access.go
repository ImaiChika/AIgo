package api

import (
	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
	"context"
	"errors"
	"net/http"
)

func writeAuthoringError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, storage.ErrAuthoringDenied) {
		writeError(w, 403, err.Error())
		return true
	}
	return false
}
func (s *Server) withGenerationAccess(ctx context.Context, action string, fn func(context.Context, *domain.GenerationPromptSnapshot) error) error {
	original := fn
	fn = func(ctx context.Context, snapshot *domain.GenerationPromptSnapshot) error {
		frozen, err := storage.FreezeGenerationSampling(ctx, s.questionStore, snapshot)
		if err != nil {
			return err
		}
		return original(ctx, frozen)
	}
	store, ok := s.questionStore.(storage.AuthoringAccessStore)
	if !ok {
		return fn(ctx, nil)
	}
	return store.WithAuthoringAccess(ctx, auth.GetUserID(ctx), auth.GetRole(ctx), func(tx context.Context, p []string) error {
		for _, code := range p {
			if code == action {
				if s.knowledgeBaseSvc == nil {
					return fn(tx, nil)
				}
				return s.knowledgeBaseSvc.Access(tx, auth.GetUserID(ctx), auth.GetRole(ctx), func(scoped context.Context, _ knowledgebase.Capabilities) error {
					guidelines, err := s.knowledgeBaseSvc.ActiveGuidelines(scoped)
					if err != nil {
						return err
					}
					return fn(scoped, &domain.GenerationPromptSnapshot{Guidelines: guidelines})
				})
			}
		}
		return storage.ErrAuthoringDenied
	})
}
