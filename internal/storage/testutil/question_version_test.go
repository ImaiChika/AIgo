package testutil

import (
	"context"
	"errors"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func versionedQuestion() domain.A2Question {
	return domain.A2Question{
		ID: "q-version", ClinicalStem: "第一版题干",
		Options: []domain.Option{{Label: "A", Text: "甲"}, {Label: "B", Text: "乙"}, {Label: "C", Text: "丙"}, {Label: "D", Text: "丁"}},
		Answer:  "A", Difficulty: domain.DifficultyMedium, Status: domain.StatusAIDraft, Version: 1,
	}
}

func TestMemoryStoreCreatesImmutableQuestionVersions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	q := versionedQuestion()
	createCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: "teacher1", ChangeType: "manual_create"})
	if err := store.SaveQuestion(createCtx, q); err != nil {
		t.Fatal(err)
	}

	q.Status = domain.StatusReviewing
	q.BankIDs = []string{"bank-a"}
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	versions, _ := store.ListQuestionVersions(ctx, q.ID)
	if len(versions) != 1 {
		t.Fatalf("状态/题库变化不应新增版本，实际 %d", len(versions))
	}
	if versions[0].Actor != "teacher1" || versions[0].ChangeType != "manual_create" {
		t.Fatalf("创建版本元数据错误: %+v", versions[0])
	}

	sameVersionEdit := q
	sameVersionEdit.ClinicalStem = "未递增版本的篡改"
	if err := store.SaveQuestion(ctx, sameVersionEdit); !errors.Is(err, domain.ErrQuestionVersionConflict) {
		t.Fatalf("同版本内容变化应冲突，实际 %v", err)
	}

	q.Status = domain.StatusRevisionRequired
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	q.ClinicalStem = "第二版题干"
	q.Version = 2
	editCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: "teacher2", ChangeType: "edit", ChangeNote: "补充信息"})
	if err := store.SaveQuestion(editCtx, q); err != nil {
		t.Fatal(err)
	}
	versions, _ = store.ListQuestionVersions(ctx, q.ID)
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("版本历史错误: %+v", versions)
	}
	if versions[1].Snapshot.ClinicalStem != "第一版题干" {
		t.Fatalf("历史快照被覆盖: %s", versions[1].Snapshot.ClinicalStem)
	}

	stale := q
	stale.ClinicalStem = "并发旧写入"
	if err := store.SaveQuestion(ctx, stale); !errors.Is(err, domain.ErrQuestionVersionConflict) {
		t.Fatalf("并发旧版本写入应冲突，实际 %v", err)
	}
}

func TestMemoryStoreImportCreatesNewDraftVersionAndPreservesBank(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	q := versionedQuestion()
	q.Status = domain.StatusPublished
	q.BankIDs = []string{"bank-a"}
	if err := store.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	imported := versionedQuestion()
	imported.ClinicalStem = "Excel更新后的题干"
	count, err := store.SaveQuestions(storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: "admin", ChangeType: "excel_import"}), []domain.A2Question{imported})
	if err != nil || count != 1 {
		t.Fatalf("import failed: count=%d err=%v", count, err)
	}
	stored, _ := store.GetQuestion(ctx, q.ID)
	if stored.Version != 2 || stored.Status != domain.StatusAIDraft || len(stored.BankIDs) != 1 || stored.BankIDs[0] != "bank-a" {
		t.Fatalf("导入更新应生成草稿新版本并保留题库: %+v", stored)
	}
	versions, _ := store.ListQuestionVersions(ctx, q.ID)
	if len(versions) != 2 || versions[0].ChangeType != "excel_import" {
		t.Fatalf("导入版本记录错误: %+v", versions)
	}
}

func TestMemoryStoresNormalizeLegacyApprovedLifecycleState(t *testing.T) {
	ctx := context.Background()
	questions := NewMemoryStore()
	q := versionedQuestion()
	q.Status = domain.StatusApproved
	if err := questions.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	stored, _ := questions.GetQuestion(ctx, q.ID)
	version, _ := questions.GetQuestionVersion(ctx, q.ID, 1)
	if stored.Status != domain.StatusPublished || version.Snapshot.Status != domain.StatusPublished {
		t.Fatalf("legacy approved question was not normalized: question=%s snapshot=%s", stored.Status, version.Snapshot.Status)
	}

	reviews := NewMemoryReviewStore()
	task := domain.ReviewTask{ID: "task-legacy", QuestionID: q.ID, FlowID: "flow", Status: domain.StatusApproved, QuestionVersion: 1}
	if err := reviews.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	storedTask, _ := reviews.GetTask(ctx, task.ID)
	if storedTask.Status != domain.StatusPublished {
		t.Fatalf("legacy approved task was not normalized: %s", storedTask.Status)
	}
}
