package testutil

import "context"

type libraryTransactionKey struct{}

func (s *MemoryStore) inLibraryTransaction(ctx context.Context) bool {
	store, _ := ctx.Value(libraryTransactionKey{}).(*MemoryStore)
	return store == s
}

// WithLibraryTransaction holds the memory store lock for the complete callback,
// mirrors row-transaction isolation and restores all versions after any failure.
func (s *MemoryStore) WithLibraryTransaction(ctx context.Context, _ []string, fn func(context.Context) error) (err error) {
	if s.inLibraryTransaction(ctx) {
		return fn(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	questions, err := cloneQuestionsForRollback(s.questions)
	if err != nil {
		return err
	}
	versions, err := cloneJSON(s.versions)
	if err != nil {
		return err
	}
	index, err := cloneJSON(s.index)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			s.questions, s.versions, s.index = questions, versions, index
		}
	}()
	if err = fn(context.WithValue(ctx, libraryTransactionKey{}, s)); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	succeeded = true
	return nil
}
