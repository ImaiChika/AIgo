package bank

import (
	"context"
	"strings"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage/testutil"
)

type testBankStore struct {
	banks map[string]domain.QuestionBank
}

func newTestBankStore() *testBankStore {
	return &testBankStore{banks: map[string]domain.QuestionBank{}}
}

func (s *testBankStore) SaveBank(_ context.Context, bank domain.QuestionBank) error {
	s.banks[bank.ID] = bank
	return nil
}

func (s *testBankStore) GetBank(_ context.Context, id string) (*domain.QuestionBank, error) {
	bank, ok := s.banks[id]
	if !ok {
		return nil, nil
	}
	return &bank, nil
}

func (s *testBankStore) ListBanks(_ context.Context) ([]domain.QuestionBank, error) {
	result := make([]domain.QuestionBank, 0, len(s.banks))
	for _, bank := range s.banks {
		result = append(result, bank)
	}
	return result, nil
}

func (s *testBankStore) DeleteBank(_ context.Context, id string) error {
	delete(s.banks, id)
	return nil
}

func (s *testBankStore) AddQuestionsToBank(_ context.Context, _ []string, _ string) (int, error) {
	return 0, nil
}

func TestDeleteBankProtectsSealedQuestionClassification(t *testing.T) {
	store := testutil.NewMemoryStore()
	svc := NewService(newTestBankStore(), store)
	ctx := context.Background()
	if _, err := svc.CreateBank(ctx, "bank-a", "内科分类库", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveQuestion(ctx, domain.A2Question{
		ID: "q-formal", Status: domain.StatusPublished, Version: 1, BankIDs: []string{"bank-a"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteBank(ctx, "bank-a"); err == nil || !strings.Contains(err.Error(), "正式题库") {
		t.Fatalf("含正式题目的分类子题库不应被物理删除，实际错误=%v", err)
	}
	if bank, _ := svc.GetBank(ctx, "bank-a"); bank == nil {
		t.Fatal("拒绝删除后分类子题库应保留")
	}
}

func TestReviewingQuestionClassificationIsLocked(t *testing.T) {
	store := testutil.NewMemoryStore()
	svc := NewService(newTestBankStore(), store)
	ctx := context.Background()
	for _, id := range []string{"bank-a", "bank-b"} {
		if _, err := svc.CreateBank(ctx, id, id, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveQuestion(ctx, domain.A2Question{
		ID: "q-reviewing", Status: domain.StatusReviewing, Version: 1, BankIDs: []string{"bank-a"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddQuestionToBank(ctx, "q-reviewing", "bank-b"); err == nil || !strings.Contains(err.Error(), "分类子题库") {
		t.Fatalf("审核中题目不应允许改变分类，实际错误=%v", err)
	}
}
