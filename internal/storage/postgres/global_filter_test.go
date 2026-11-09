package postgres

import (
	"strings"
	"testing"

	"aigo/internal/storage"
)

func TestExplicitGlobalFilterWithoutVisibleStatusesFailsClosed(t *testing.T) {
	where, args := questionFilterWhere(storage.QuestionFilter{GlobalOnly: true})
	if !strings.Contains(where, "FALSE") || len(args) != 0 {
		t.Fatalf("empty global permissions must not become an unfiltered query: where=%q args=%v", where, args)
	}
}
