//go:build integration

package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"aigo/internal/domain"
	pgstore "aigo/internal/storage/postgres"
	"github.com/lib/pq"
)

type restoreTestResolver struct {
	*fakeResolver
	allowed atomic.Bool
}

func (r *restoreTestResolver) AuthorizeReviewRestore(_ context.Context, id string) (string, bool, error) {
	return "维护管理员", id == "restorer" && r.allowed.Load(), nil
}

func newRestoreServiceFixture(t *testing.T) (*Service, *pgstore.Store, *restoreTestResolver, domain.ReviewTask) {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	schema := fmt.Sprintf("restore_service_%d", time.Now().UnixNano())
	if _, err := db.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`) })
	scoped, err := reviewDSNWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	store, err := pgstore.New(scoped)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`INSERT INTO roles(id,name,permissions) VALUES('owner-role','出题人',ARRAY['question:edit','review:submit']); INSERT INTO users(id,username,password_hash,display_name,role,roles,enabled) VALUES('owner','owner','unused','原出题人','owner-role',ARRAY['owner-role'],TRUE)`)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &restoreTestResolver{fakeResolver: &fakeResolver{reviewers: map[string][]string{"": {"r1", "r2"}}, finalRight: map[string]bool{"final": true}}}
	resolver.allowed.Store(true)
	svc := NewService(store, store, store, resolver)
	svc.SetReviewRestoreSigningKey([]byte("restore-fixture-signing-key-32-bytes"))
	flow := domain.ReviewFlowConfig{ID: "restore-flow", Name: "两轮全票", FinalReviewerIDs: []string{"final"}, VoteRule: "veto", Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "第一轮", ExpertIDs: []string{"r1", "r2"}, RequiredCount: 2}, {RoundNumber: 2, Name: "第二轮", ExpertIDs: []string{"r1", "r2"}, RequiredCount: 2}}}
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	q := testQuestion("")
	q.BankIDs = nil
	q.OwnerID = "owner"
	q.CreatedBy = "owner"
	if err := store.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "first-pass", QuestionID: q.ID, QuestionVersion: q.Version, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	task, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	return svc, store, resolver, *task
}

func TestRestoreServiceBindsPreviewActorIdentityAndCurrentAuthorization(t *testing.T) {
	svc, store, auth, task := newRestoreServiceFixture(t)
	if _, err := svc.PreviewReviewRestore(t.Context(), "other", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}}); !errors.Is(err, domain.ErrReviewRestoreForbidden) {
		t.Fatalf("unauthorized preview: %v", err)
	}
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	req := RestoreRequest{OperationID: "op-service-restore", PreviewToken: preview.Token, Reason: "更换审核安排"}
	if _, err := svc.RestoreReview(t.Context(), "restorer", "expert", req); !errors.Is(err, domain.ErrReviewConflict) {
		t.Fatalf("identity switch bypassed preview: %v", err)
	}
	tampered := req
	tampered.PreviewToken += "x"
	if _, err := svc.RestoreReview(t.Context(), "restorer", "admin", tampered); !errors.Is(err, domain.ErrReviewConflict) {
		t.Fatalf("tamper: %v", err)
	}
	auth.allowed.Store(false)
	if _, err := svc.RestoreReview(t.Context(), "restorer", "admin", req); !errors.Is(err, domain.ErrReviewRestoreForbidden) {
		t.Fatalf("old preview survived revocation: %v", err)
	}
	auth.allowed.Store(true)
	result, err := svc.RestoreReview(t.Context(), "restorer", "admin", req)
	if err != nil || result.Restored != 1 {
		t.Fatalf("restore: %+v %v", result, err)
	}
	replay, err := svc.RestoreReview(t.Context(), "restorer", "admin", req)
	if err != nil || !replay.Replayed {
		t.Fatalf("retry: %+v %v", replay, err)
	}
	q, _ := store.GetQuestion(t.Context(), task.QuestionID)
	if q.Version != 1 || q.ReviewRevision != 1 || q.Status != domain.StatusAIReviewed {
		t.Fatal("wrong restored question")
	}
}

func TestRestoreServiceFlowPreviewDoesNotIncludeLaterSubmissions(t *testing.T) {
	svc, store, _, task := newRestoreServiceFixture(t)
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{FlowID: task.FlowID})
	if err != nil || preview.Count != 1 {
		t.Fatalf("flow preview: %+v %v", preview, err)
	}
	q := testQuestion("")
	q.ID = "later"
	q.BankIDs = nil
	q.OwnerID = "owner"
	q.CreatedBy = "owner"
	if err := store.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "later-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SubmitQuestionForOwner(t.Context(), q.ID, task.FlowID); err != nil {
		t.Fatal(err)
	}
	result, err := svc.RestoreReview(t.Context(), "restorer", "admin", RestoreRequest{OperationID: "op-frozen-flow", PreviewToken: preview.Token, Reason: "仅还原确认过的题"})
	if err != nil || result.Restored != 1 {
		t.Fatalf("frozen restore: %+v %v", result, err)
	}
	later, _ := store.GetQuestion(t.Context(), q.ID)
	if later.Status != domain.StatusReviewing || later.ReviewRevision != 0 {
		t.Fatal("new submission was silently included")
	}
}

func TestRestoreServiceRejectsVotesAfterPreview(t *testing.T) {
	svc, store, _, task := newRestoreServiceFixture(t)
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: "r1", Action: domain.StatusApproved}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.RestoreReview(t.Context(), "restorer", "admin", RestoreRequest{OperationID: "op-preview-vote", PreviewToken: preview.Token, Reason: "过期确认"})
	if !errors.Is(err, domain.ErrReviewConflict) {
		t.Fatalf("changed votes accepted: %v", err)
	}
	records, _ := store.ListRecordsByTaskID(t.Context(), task.ID)
	if len(records) != 1 {
		t.Fatal("conflicting restore removed new vote")
	}
}

func TestConcurrentRestoreOperationsHaveOneWinner(t *testing.T) {
	svc, store, _, task := newRestoreServiceFixture(t)
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, id := range []string{"op-concurrent-a", "op-concurrent-b"} {
		go func(id string) {
			<-start
			_, err := svc.RestoreReview(context.Background(), "restorer", "admin", RestoreRequest{OperationID: id, PreviewToken: preview.Token, Reason: "并发还原"})
			errs <- err
		}(id)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		e := <-errs
		if e == nil {
			success++
		} else if errors.Is(e, domain.ErrReviewConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("winners=%d conflicts=%d", success, conflict)
	}
	var operations int
	if err := store.DB().QueryRow(`SELECT count(*) FROM review_restore_operations`).Scan(&operations); err != nil || operations != 1 {
		t.Fatalf("operation count %d %v", operations, err)
	}
}

func TestRevisionCanChangeFlowAndKeepOriginalReason(t *testing.T) {
	svc, store, _, task := newRestoreServiceFixture(t)
	for _, id := range []string{"r1", "r2"} {
		action := domain.StatusRevisionRequired
		opinion := "原退修原因"
		if id == "r1" {
			action = domain.StatusApproved
			opinion = "不应向作者泄漏的通过意见"
		}
		if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpertID: id, Action: action, Comment: &domain.ReviewComment{Stem: opinion}}); err != nil {
			t.Fatal(err)
		}
	}
	q, _ := store.GetQuestion(t.Context(), task.QuestionID)
	q.ClinicalStem += "（已修订）"
	q.Version++
	if err := store.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "new-revision-flow", Name: "新退修流程", Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{"r2"}, RequiredCount: 1}}}
	if err := svc.CreateFlow(t.Context(), flow); err != nil {
		t.Fatal(err)
	}
	ctx := WithSubmissionExpectation(t.Context(), domain.ReviewExpectation{QuestionID: q.ID, Version: q.Version, ReviewRevision: q.ReviewRevision, PreviousTaskID: task.ID})
	next, err := svc.SubmitQuestionForOwner(ctx, q.ID, flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID == task.ID || next.PreviousTaskID != task.ID || next.FlowID != flow.ID || next.Attempt != 2 || next.CurrentRound != 1 {
		t.Fatalf("new submission did not preserve lineage: %+v", next)
	}
	old, _ := store.GetTask(t.Context(), task.ID)
	records, _ := store.ListRecordsByTaskID(t.Context(), old.ID)
	if old.FlowID != task.FlowID || old.Status != domain.StatusArchived || len(records) != 2 || records[1].Comment.Stem != "原退修原因" {
		t.Fatalf("original revision history lost: %+v %+v", old, records)
	}
	history, e := svc.QuestionReviewHistory(t.Context(), q.ID, "owner", false)
	if e != nil || len(history) != 2 || len(history[0].Records) != 1 || history[0].Records[0].Comment.Stem != "原退修原因" || !history[0].Superseded {
		t.Fatalf("author revision reasons/privacy: %+v %v", history, e)
	}
	visible, e := svc.RecordsForViewer(t.Context(), task.ID, "r2", false)
	if e != nil || len(visible) != 1 || visible[0].ExpertID != "r2" {
		t.Fatal("superseded task leaked another expert's opinions")
	}
	revisions, err := svc.MyRevisions(t.Context(), "owner")
	if err != nil || len(revisions) != 0 {
		t.Fatalf("old revision resurrected: %+v %v", revisions, err)
	}
	if err := svc.DeleteFlow(t.Context(), task.FlowID); err != nil {
		t.Fatalf("old history should not hold active flow lock: %v", err)
	}
}

// 第一项变更持锁停在提交前，第二项明确发起后才放行，固定两种提交顺序。
func runOrderedReviewMutations(t *testing.T, svc *Service, first func(context.Context) error, second func() error) error {
	t.Helper()
	entered, release, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		firstDone <- svc.WithReviewMutation(t.Context(), func(ctx context.Context) error {
			if err := first(ctx); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-firstDone:
		t.Fatalf("first mutation failed: %v", err)
	}
	go func() { close(attempted); secondDone <- second() }()
	<-attempted
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	return <-secondDone
}

func TestRestoreAndVoteHaveDeterministicCommitOrders(t *testing.T) {
	for _, restoreFirst := range []bool{false, true} {
		name := "vote_first"
		if restoreFirst {
			name = "restore_first"
		}
		t.Run(name, func(t *testing.T) {
			svc, store, _, task := newRestoreServiceFixture(t)
			preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
			if err != nil {
				t.Fatal(err)
			}
			request := RestoreRequest{OperationID: "ordered-vote-restore", PreviewToken: preview.Token, Reason: "调整任务"}
			restore := func(ctx context.Context) error {
				_, err := svc.RestoreReview(ctx, "restorer", "admin", request)
				return err
			}
			vote := func(ctx context.Context) error {
				return svc.Review(ctx, ReviewRequest{TaskID: task.ID, ExpectedRound: 1, ExpertID: "r1", Action: domain.StatusApproved})
			}
			first, second := vote, restore
			if restoreFirst {
				first, second = restore, vote
			}
			err = runOrderedReviewMutations(t, svc, first, func() error { return second(t.Context()) })
			want := domain.ErrReviewConflict
			if restoreFirst {
				want = domain.ErrReviewTaskNotFound
			}
			if !errors.Is(err, want) {
				t.Fatalf("later mutation: %v want %v", err, want)
			}
			records, _ := store.ListRecordsByTaskID(t.Context(), task.ID)
			expected := 1
			if restoreFirst {
				expected = 0
			}
			if len(records) != expected {
				t.Fatalf("inconsistent records: %d", len(records))
			}
		})
	}
}

func TestRestoreAndFinalDecisionHaveDeterministicCommitOrders(t *testing.T) {
	for _, action := range []domain.QuestionStatus{domain.StatusApproved, domain.StatusRejected, domain.StatusRevisionRequired} {
		for _, restoreFirst := range []bool{false, true} {
			name := string(action) + "_final_first"
			if restoreFirst {
				name = string(action) + "_restore_first"
			}
			t.Run(name, func(t *testing.T) {
				svc, store, _, task := newRestoreServiceFixture(t)
				for round := 1; round <= 2; round++ {
					for _, id := range []string{"r1", "r2"} {
						if err := svc.Review(t.Context(), ReviewRequest{TaskID: task.ID, ExpectedRound: round, ExpertID: id, Action: domain.StatusApproved}); err != nil {
							t.Fatal(err)
						}
					}
				}
				preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
				if err != nil {
					t.Fatal(err)
				}
				restore := func(ctx context.Context) error {
					_, err := svc.RestoreReview(ctx, "restorer", "admin", RestoreRequest{OperationID: "ordered-final-restore", PreviewToken: preview.Token, Reason: "调整任务"})
					return err
				}
				final := func(ctx context.Context) error {
					return svc.Finalize(ctx, FinalizeRequest{TaskID: task.ID, ReviewerID: "final", Action: action, Opinion: "最终决断理由"})
				}
				first, second := final, restore
				if restoreFirst {
					first, second = restore, final
				}
				err = runOrderedReviewMutations(t, svc, first, func() error { return second(t.Context()) })
				want := domain.ErrReviewConflict
				if restoreFirst {
					want = domain.ErrReviewTaskNotFound
				}
				if !errors.Is(err, want) {
					t.Fatalf("later mutation: %v want %v", err, want)
				}
				q, _ := store.GetQuestion(t.Context(), task.QuestionID)
				if restoreFirst && (q.Status != domain.StatusAIReviewed || q.ReviewRevision != 1) {
					t.Fatalf("old decision revived restored task: %+v", q)
				}
				if !restoreFirst && q.ReviewRevision != 0 {
					t.Fatal("restoration overwrote a committed decision")
				}
			})
		}
	}
}

func TestRestoreCanceledWhileWaitingLeavesNoWritesAndReleasesResources(t *testing.T) {
	svc, store, _, task := newRestoreServiceFixture(t)
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.AcquireReviewMutationLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, err = svc.RestoreReview(ctx, "restorer", "admin", RestoreRequest{OperationID: "cancel-wait-operation", PreviewToken: preview.Token, Reason: "调整任务"})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected canceled lock wait, got %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	latest, _ := store.GetTask(t.Context(), task.ID)
	q, _ := store.GetQuestion(t.Context(), task.QuestionID)
	if latest == nil || q.ReviewRevision != 0 || q.Status != domain.StatusReviewing {
		t.Fatal("canceled operation changed task")
	}
	if _, err := svc.RestoreReview(t.Context(), "restorer", "admin", RestoreRequest{OperationID: "after-cancel-operation", PreviewToken: preview.Token, Reason: "调整任务"}); err != nil {
		t.Fatalf("canceled request leaked lock/connection: %v", err)
	}
}

func TestRestoreThousandQuestionsWithLongLegalChineseIDs(t *testing.T) {
	svc, store, _, task := newRestoreServiceFixture(t)
	for _, query := range []string{
		`INSERT INTO questions(id,clinical_stem,answer,status,version,owner_id,created_by,created_at,updated_at) SELECT 'q-'||repeat('中',250)||'-'||n,'容量夹具正文','A','reviewing',1,'owner','owner',NOW(),NOW() FROM generate_series(1,999) n`,
		`INSERT INTO review_tasks(id,question_id,flow_id,status,current_round,question_version,assigned_to,created_at,updated_at) SELECT 'task-q-'||repeat('中',250)||'-'||n,'q-'||repeat('中',250)||'-'||n,'restore-flow','reviewing',1,1,ARRAY['r1','r2'],NOW(),NOW() FROM generate_series(1,999) n`,
		`INSERT INTO ai_review_results(id,question_id,question_version,verdict) SELECT 'long-ai-'||n,'q-'||repeat('中',250)||'-'||n,1,'pass' FROM generate_series(1,999) n`,
	} {
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{FlowID: task.FlowID})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Token) <= 1024*1024 || preview.Count != 1000 {
		t.Fatalf("long legal IDs not exercised: token=%d count=%d", len(preview.Token), preview.Count)
	}
	result, err := svc.RestoreReview(t.Context(), "restorer", "admin", RestoreRequest{OperationID: "long-id-thousand-restore", PreviewToken: preview.Token, Reason: "长中文ID批次"})
	if err != nil || result.Restored != 1000 {
		t.Fatalf("1000 legal IDs cannot be restored: %+v %v", result, err)
	}
	t.Logf("1000-question signed token: %d bytes", len(preview.Token))
}

func TestCommittedRestoreReplaysAfterServiceAndConnectionRestart(t *testing.T) {
	svc, store, resolver, task := newRestoreServiceFixture(t)
	preview, err := svc.PreviewReviewRestore(t.Context(), "restorer", "admin", domain.ReviewRestoreFilter{TaskIDs: []string{task.ID}})
	if err != nil {
		t.Fatal(err)
	}
	request := RestoreRequest{OperationID: "service-restart-operation", PreviewToken: preview.Token, Reason: "重新安排"}
	if _, err := svc.RestoreReview(t.Context(), "restorer", "admin", request); err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.SubmitQuestionForOwner(WithSubmissionExpectation(t.Context(), domain.ReviewExpectation{QuestionID: task.QuestionID, Version: 1, ReviewRevision: 1}), task.QuestionID, task.FlowID)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := store.DB().QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	scoped, err := reviewDSNWithSearchPath(os.Getenv("AIGO_TEST_POSTGRES_DSN"), schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := pgstore.New(scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := NewService(reopened, reopened, reopened, resolver)
	// 已提交回执应先于过期/轮换预览签名检查；仍需同一账号、身份、请求和实时权限。
	restarted.SetReviewRestoreSigningKey([]byte("rotated-fixture-signing-key-32-bytes"))
	result, err := restarted.RestoreReview(t.Context(), "restorer", "admin", request)
	if err != nil || !result.Replayed {
		t.Fatalf("durable replay: %+v %v", result, err)
	}
	latest, _ := reopened.GetTaskByQuestionID(t.Context(), task.QuestionID)
	if latest == nil || latest.ID != fresh.ID {
		t.Fatal("retry after restart deleted the later submission")
	}
}
