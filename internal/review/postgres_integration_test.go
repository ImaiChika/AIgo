package review

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	pgstore "aigo/internal/storage/postgres"

	"github.com/lib/pq"
)

func TestPostgresTwoInstancesKeepReviewMutationsAtomic(t *testing.T) {
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL review integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	schemaName := fmt.Sprintf("aigo_review_service_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}

	testDSN, err := reviewDSNWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatal(err)
	}
	store1, err := pgstore.New(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	store2, err := pgstore.New(testDSN)
	if err != nil {
		store1.Close()
		t.Fatal(err)
	}
	defer func() {
		store2.Close()
		store1.Close()
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)
	}()

	schemaSQL, err := os.ReadFile("../storage/postgres/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := store1.InitSchema(string(schemaSQL)); err != nil {
		t.Fatal(err)
	}
	resolver := &fakeResolver{
		reviewers:  map[string][]string{"bank-neike": {"r1", "r2"}},
		finalRight: map[string]bool{"admin1": true},
	}
	svc1 := NewService(store1, store1, store1, resolver)
	svc2 := NewService(store2, store2, store2, resolver)
	if err := store1.SaveBank(ctx, domain.QuestionBank{ID: "bank-neike", Name: "内科题库", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store1.SaveQuestion(ctx, *testQuestion("bank-neike")); err != nil {
		t.Fatal(err)
	}
	if err := store1.SaveFlowConfig(ctx, testFlow()); err != nil {
		t.Fatal(err)
	}

	startSubmit := make(chan struct{})
	var submitWG sync.WaitGroup
	var submitSuccess atomic.Int32
	for i := 0; i < 8; i++ {
		svc := svc1
		if i%2 == 1 {
			svc = svc2
		}
		submitWG.Add(1)
		go func(service *Service) {
			defer submitWG.Done()
			<-startSubmit
			if _, err := service.SubmitQuestion(ctx, "q1", "flow-test"); err == nil {
				submitSuccess.Add(1)
			}
		}(svc)
	}
	close(startSubmit)
	submitWG.Wait()
	if submitSuccess.Load() != 1 {
		t.Fatalf("two instances created %d successful submissions, want 1", submitSuccess.Load())
	}
	tasks, err := store1.ListAllTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("review tasks = %d, want 1", len(tasks))
	}
	taskID := tasks[0].ID

	startVote := make(chan struct{})
	voteErrors := make(chan error, 2)
	var voteWG sync.WaitGroup
	for _, vote := range []struct {
		service  *Service
		reviewer string
	}{{svc1, "r1"}, {svc2, "r2"}} {
		vote := vote
		voteWG.Add(1)
		go func() {
			defer voteWG.Done()
			<-startVote
			voteErrors <- vote.service.Review(ctx, ReviewRequest{TaskID: taskID, ExpertID: vote.reviewer, Action: domain.StatusApproved})
		}()
	}
	close(startVote)
	voteWG.Wait()
	close(voteErrors)
	for err := range voteErrors {
		if err != nil {
			t.Fatalf("concurrent vote failed: %v", err)
		}
	}
	storedTask, err := store1.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTask.Status != domain.StatusConflict || len(storedTask.RoundResults[0].Reviews) != 2 || storedTask.RoundResults[0].ApprovedCount != 2 {
		t.Fatalf("votes not preserved: status=%s reviews=%d approved=%d", storedTask.Status, len(storedTask.RoundResults[0].Reviews), storedTask.RoundResults[0].ApprovedCount)
	}
	records, err := store1.ListRecordsByTaskID(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("review records = %d, want 2", len(records))
	}

	if err := svc2.Finalize(ctx, FinalizeRequest{TaskID: taskID, ReviewerID: "admin1", Action: domain.StatusApproved}); err != nil {
		t.Fatal(err)
	}
	storedTask, _ = store1.GetTask(ctx, taskID)
	storedQuestion, _ := store1.GetQuestion(ctx, "q1")
	if storedTask.Status != domain.StatusPublished || storedQuestion.Status != domain.StatusPublished {
		t.Fatalf("final state mismatch: task=%s question=%s", storedTask.Status, storedQuestion.Status)
	}
}

func reviewDSNWithSearchPath(dsn, schemaName string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
