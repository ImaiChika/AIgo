//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"aigo/internal/domain"
)

func TestReviewRevisionRejectsStaleWritesWithoutNewContentVersion(t *testing.T) {
	s := authoringTestStore(t)
	ctx := context.Background()
	q := domain.A2Question{ID: "restore-revision", ClinicalStem: "版本并发测试", Answer: "A", Version: 1, Status: domain.StatusAIReviewed, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE questions SET review_revision=review_revision+1 WHERE id=$1`, q.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.GetQuestion(ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ReviewRevision != 1 || fresh.Version != 1 {
		t.Fatalf("metadata not loaded: %+v", fresh)
	}
	if err := s.SaveQuestion(ctx, *fresh); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		revision int64
	}{{"stale", 0}, {"untrusted increment", 2}} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := *fresh
			candidate.ReviewRevision = tc.revision
			if err := s.SaveQuestion(ctx, candidate); !errors.Is(err, domain.ErrReviewConflict) {
				t.Fatalf("expected review conflict, got %v", err)
			}
		})
	}
	var versions int
	if err := s.DB().QueryRow(`SELECT count(*) FROM question_versions WHERE question_id=$1`, q.ID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Fatalf("workflow revision added content version: %d", versions)
	}
	stored, _ := s.GetQuestion(ctx, q.ID)
	if stored.ReviewRevision != 1 || stored.ClinicalStem != q.ClinicalStem {
		t.Fatalf("stale write changed question: %+v", stored)
	}
}

func seedRestoreCandidate(t *testing.T, s *Store, id string, status domain.QuestionStatus) domain.ReviewRestoreCandidate {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	if _, err := s.DB().Exec(`UPDATE roles SET permissions=ARRAY['question:edit','review:submit'] WHERE id='fixture-authoring'`); err != nil {
		t.Fatal(err)
	}
	q := domain.A2Question{ID: id, ClinicalStem: "待还原正文", Answer: "A", Version: 1, Status: domain.StatusAIReviewed, OwnerID: "owner", CreatedBy: "owner", CreatedAt: now, UpdatedAt: now}
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReviewResult(ctx, domain.AIReviewResult{ID: "ai-" + id, QuestionID: id, QuestionVersion: 1, Verdict: "pass", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{ID: "flow-" + id, Name: "流程" + id, Rounds: []domain.RoundConfig{{RoundNumber: 1, ExpertIDs: []string{"r1", "r2"}, RequiredCount: 2}}, CreatedAt: now}
	if err := s.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	q.Status = status
	if err := s.SaveQuestion(ctx, q); err != nil {
		t.Fatal(err)
	}
	task := domain.ReviewTask{ID: "task-" + id, QuestionID: id, FlowID: flow.ID, Status: status, CurrentRound: 1, AssignedTo: []string{"r1", "r2"}, QuestionVersion: 1, Attempt: 1, CreatedAt: now, UpdatedAt: now, RoundResults: []domain.RoundResult{{RoundNumber: 1, Reviews: []domain.ExpertReview{{ExpertID: "r1", Conclusion: domain.StatusApproved}}, ApprovedCount: 1}}}
	if err := s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRecord(ctx, domain.ReviewRecord{ID: "rec-" + id, TaskID: task.ID, QuestionID: id, RoundNumber: 1, Attempt: 1, ExpertID: "r1", Conclusion: domain.StatusApproved, Opinion: "待清理意见", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"create", "update", "generate", "ai_check", "submit", "review"} {
		if err := s.SaveLog(ctx, domain.AuditLog{ID: id + "-" + action, QuestionID: id, Action: action, Actor: "owner", Detail: action, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	freshQ, _ := s.GetQuestion(ctx, id)
	freshTask, _ := s.GetTask(ctx, task.ID)
	return domain.ReviewRestoreCandidate{Question: *freshQ, Task: *freshTask, Flow: flow}
}

func restoreTestTransaction(s *Store, cmd domain.ReviewRestoreCommand) (out *domain.ReviewRestoreResult, err error) {
	ctx := context.Background()
	release, err := s.AcquireReviewMutationLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	err = s.WithReviewTransaction(ctx, s, func(txctx context.Context) error { var e error; out, e = s.ApplyReviewRestore(txctx, cmd); return e })
	return
}

func TestReviewRestoreAtomicHistoryAndIdempotency(t *testing.T) {
	s := authoringTestStore(t)
	c := seedRestoreCandidate(t, s, "restore-atomic", domain.StatusReviewing)
	items, total, err := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{}, 1, 20, true)
	if err != nil || total != 1 || !items[0].Eligible {
		t.Fatalf("candidate: %d %+v %v", total, items, err)
	}
	c = items[0]
	before, _ := s.ListQuestionVersions(t.Context(), c.Question.ID)
	cmd := domain.ReviewRestoreCommand{OperationID: "op-restore-atomic", ActorID: "admin", ActorName: "管理员", Reason: "重新分配流程", RequestHash: "hash-one", Targets: []domain.ReviewRestoreTarget{c.Target()}}
	result, err := restoreTestTransaction(s, cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result.Restored != 1 || result.DeletedRecords != 1 || result.DeletedTasks != 1 || result.DeletedLogs != 2 {
		t.Fatalf("unexpected counts: %+v", result)
	}
	q, _ := s.GetQuestion(t.Context(), c.Question.ID)
	if q.Status != domain.StatusAIReviewed || q.ReviewRevision != 1 || q.Version != c.Question.Version || !domain.QuestionContentEqual(*q, c.Question) {
		t.Fatalf("unexpected question: %+v", q)
	}
	task, _ := s.GetTaskByQuestionID(t.Context(), q.ID)
	if task != nil {
		t.Fatal("restored question still has review task")
	}
	after, _ := s.ListQuestionVersions(t.Context(), q.ID)
	if len(before) != len(after) {
		t.Fatal("restoration created content version")
	}
	logs, _ := s.ListLogsByQuestion(t.Context(), q.ID)
	if len(logs) != 5 {
		t.Fatalf("want four preserved logs and restore event, got %d", len(logs))
	}
	replay, err := restoreTestTransaction(s, cmd)
	if err != nil || !replay.Replayed || replay.Restored != 1 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	cmd.RequestHash = "other"
	if _, err := restoreTestTransaction(s, cmd); !errors.Is(err, domain.ErrReviewConflict) {
		t.Fatalf("ID collision must conflict: %v", err)
	}
}

func TestReviewRestoreBatchConflictRollsBackEveryQuestion(t *testing.T) {
	s := authoringTestStore(t)
	a := seedRestoreCandidate(t, s, "batch-a", domain.StatusReviewing)
	b := seedRestoreCandidate(t, s, "batch-b", domain.StatusPublished)
	// Published objects must be rejected even if a stale client retained their task ID.
	cmd := domain.ReviewRestoreCommand{OperationID: "op-invalid-batch", ActorID: "admin", ActorName: "管理员", Reason: "批量还原", RequestHash: "hash", Targets: []domain.ReviewRestoreTarget{a.Target(), b.Target()}}
	if _, err := restoreTestTransaction(s, cmd); !errors.Is(err, domain.ErrReviewConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	q, _ := s.GetQuestion(t.Context(), a.Question.ID)
	if q.Status != domain.StatusReviewing || q.ReviewRevision != 0 {
		t.Fatal("batch partially changed a valid member")
	}
	records, _ := s.ListRecordsByTaskID(t.Context(), a.Task.ID)
	if len(records) != 1 {
		t.Fatal("batch lost records on rollback")
	}
}

func TestReviewRestoreAuditFailureRollsBackStateAndHistory(t *testing.T) {
	s := authoringTestStore(t)
	seedRestoreCandidate(t, s, "fault", domain.StatusConflict)
	items, _, _ := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{}, 1, 20, false)
	_, err := s.DB().Exec(`CREATE FUNCTION reject_restore_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='review_restore' THEN RAISE EXCEPTION 'injected restore audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_restore_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_restore_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	cmd := domain.ReviewRestoreCommand{OperationID: "op-fault", ActorID: "admin", ActorName: "管理员", Reason: "事务测试", RequestHash: "fault", Targets: []domain.ReviewRestoreTarget{items[0].Target()}}
	if _, err := restoreTestTransaction(s, cmd); err == nil {
		t.Fatal("expected injected failure")
	}
	q, _ := s.GetQuestion(t.Context(), "fault")
	task, _ := s.GetTaskByQuestionID(t.Context(), q.ID)
	records, _ := s.ListRecordsByTaskID(t.Context(), task.ID)
	logs, _ := s.ListLogsByQuestion(t.Context(), q.ID)
	if q.Status != domain.StatusConflict || q.ReviewRevision != 0 || len(records) != 1 || len(logs) != 6 {
		t.Fatalf("partial commit after audit failure: %+v records=%d logs=%d", q, len(records), len(logs))
	}
	op, err := s.GetReviewRestoreOperation(t.Context(), cmd.OperationID, cmd.ActorID, cmd.RequestHash)
	if err != nil || op != nil {
		t.Fatalf("failed operation persisted: %+v %v", op, err)
	}
}

func TestReviewRestoreEligibilityAndExactSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   domain.QuestionStatus
		change   string
		eligible bool
	}{
		{"reviewing", domain.StatusReviewing, "", true}, {"waiting final", domain.StatusConflict, "", true},
		{"published", domain.StatusPublished, "", false}, {"rejected", domain.StatusRejected, "", false},
		{"archived", domain.StatusArchived, "", false}, {"revision", domain.StatusAIReviewed, "revision", false},
		{"disabled owner", domain.StatusReviewing, "disabled", false}, {"no AI evidence", domain.StatusReviewing, "no-ai", false},
		{"share reference", domain.StatusReviewing, "share", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := authoringTestStore(t)
			c := seedRestoreCandidate(t, s, "eligible", tc.status)
			switch tc.change {
			case "disabled":
				_, _ = s.DB().Exec(`UPDATE users SET enabled=FALSE WHERE id='owner'`)
			case "no-ai":
				_, _ = s.DB().Exec(`DELETE FROM ai_review_results WHERE question_id='eligible'`)
			case "revision":
				_, _ = s.DB().Exec(`UPDATE review_tasks SET status='revision_required' WHERE id=$1`, c.Task.ID)
			case "share":
				_, _ = s.DB().Exec(`INSERT INTO question_share_requests(id,question_id,owner_id) VALUES('share','eligible','owner')`)
			}
			rows, _, err := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{TaskIDs: []string{c.Task.ID}}, 1, 20, true)
			if err != nil {
				t.Fatal(err)
			}
			got := len(rows) == 1 && rows[0].Eligible
			if got != tc.eligible {
				t.Fatalf("eligibility got %v want %v", got, tc.eligible)
			}
			if tc.eligible {
				stale := rows[0].Target()
				_, _ = s.DB().Exec(`UPDATE review_tasks SET updated_at=updated_at+interval '1 second' WHERE id=$1`, c.Task.ID)
				_, err := restoreTestTransaction(s, domain.ReviewRestoreCommand{OperationID: "op-stale", ActorID: "admin", Reason: "changed", RequestHash: "hash", Targets: []domain.ReviewRestoreTarget{stale}})
				if !errors.Is(err, domain.ErrReviewConflict) {
					t.Fatalf("changed snapshot accepted: %v", err)
				}
			}
		})
	}
}

func TestReviewRestoreClearsEveryHistoricalTaskAndDoesNotRestoreAgainOnReplay(t *testing.T) {
	s := authoringTestStore(t)
	c := seedRestoreCandidate(t, s, "history", domain.StatusReviewing)
	old := c.Task
	old.ID = "older-task"
	old.CreatedAt = old.CreatedAt.Add(-time.Hour)
	old.Status = domain.StatusArchived
	if err := s.SaveTask(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRecord(t.Context(), domain.ReviewRecord{ID: "older-record", TaskID: old.ID, QuestionID: c.Question.ID, RoundNumber: 1, Attempt: 1, ExpertID: "r2", Conclusion: domain.StatusRevisionRequired, Opinion: "旧退修原因", CreatedAt: old.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{}, 1, 20, true)
	if len(rows[0].Records) != 2 || len(rows[0].History) != 2 {
		t.Fatal("administrative detail omitted old attempts")
	}
	cmd := domain.ReviewRestoreCommand{OperationID: "op-history", ActorID: "admin", ActorName: "admin", Reason: "清空全部人工审核", RequestHash: "history", Targets: []domain.ReviewRestoreTarget{rows[0].Target()}}
	result, err := restoreTestTransaction(s, cmd)
	if err != nil || result.DeletedTasks != 2 || result.DeletedRecords != 2 {
		t.Fatalf("history cleanup %+v %v", result, err)
	}
	q, _ := s.GetQuestion(t.Context(), c.Question.ID)
	q.Status = domain.StatusReviewing
	if err := s.SaveQuestion(t.Context(), *q); err != nil {
		t.Fatal(err)
	}
	later := c.Task
	later.ID = "new-task-after-restore"
	later.CreatedAt = time.Now()
	later.UpdatedAt = time.Now()
	if err := s.SaveTask(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreTestTransaction(s, cmd); err != nil {
		t.Fatal(err)
	}
	still, _ := s.GetTask(t.Context(), later.ID)
	if still == nil {
		t.Fatal("retry removed a newly submitted task")
	}
}

func TestReviewRestoreDiagnosesMissingAndEndedLatestTasks(t *testing.T) {
	for _, kind := range []string{"missing", "ended"} {
		t.Run(kind, func(t *testing.T) {
			s := authoringTestStore(t)
			c := seedRestoreCandidate(t, s, "diagnostic", domain.StatusReviewing)
			if kind == "missing" {
				if err := s.DeleteTask(t.Context(), c.Task.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				c.Task.Status = domain.StatusPublished
				if err := s.UpdateTask(t.Context(), c.Task); err != nil {
					t.Fatal(err)
				}
			}
			items, total, err := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{}, 1, 20, false)
			if err != nil || total != 1 || items[0].Eligible || items[0].Reason == "" {
				t.Fatalf("diagnostic not visible/read-only: %+v %d %v", items, total, err)
			}
			_, err = restoreTestTransaction(s, domain.ReviewRestoreCommand{OperationID: "op-diagnostic", ActorID: "admin", Reason: "异常不强制还原", RequestHash: "diagnostic", Targets: []domain.ReviewRestoreTarget{c.Target()}})
			if !errors.Is(err, domain.ErrReviewConflict) {
				t.Fatalf("invalid task was mutated: %v", err)
			}
		})
	}
}

func TestReviewRestoreEveryWriteFailureRollsBackWholeBatch(t *testing.T) {
	for _, stage := range []struct{ name, table, event string }{
		{"delete_tasks", "review_tasks", "DELETE"}, {"cascade_records", "review_records", "DELETE"}, {"delete_old_audit", "audit_logs", "DELETE"}, {"update_question", "questions", "UPDATE"}, {"insert_restore_audit", "audit_logs", "INSERT"}, {"commit_operation_receipt", "review_restore_operations", "INSERT"},
	} {
		t.Run(stage.name, func(t *testing.T) {
			s := authoringTestStore(t)
			a := seedRestoreCandidate(t, s, "rollback-a", domain.StatusReviewing)
			b := seedRestoreCandidate(t, s, "rollback-b", domain.StatusReviewing)
			snapshot := func() string {
				t.Helper()
				var out string
				err := s.DB().QueryRow(`SELECT jsonb_build_object('questions',(SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM questions q),'tasks',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM review_tasks t),'records',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM review_records r),'logs',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM audit_logs l),'operations',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM review_restore_operations o))::text`).Scan(&out)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			before := snapshot()
			if _, err := s.DB().Exec(`CREATE FUNCTION fail_restore_stage() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'controlled restore stage failure'; END $$; CREATE TRIGGER fail_restore_stage BEFORE ` + stage.event + ` ON ` + stage.table + ` FOR EACH ROW EXECUTE FUNCTION fail_restore_stage()`); err != nil {
				t.Fatal(err)
			}
			_, err := restoreTestTransaction(s, domain.ReviewRestoreCommand{OperationID: "rollback-all-stages", ActorID: "manager", ActorName: "管理员", Reason: "调整任务", RequestHash: "test-hash", Targets: []domain.ReviewRestoreTarget{a.Target(), b.Target()}})
			if err == nil {
				t.Fatal("injected stage failure unexpectedly committed")
			}
			if after := snapshot(); before != after {
				t.Fatal("partial batch writes survived a failed restoration")
			}
		})
	}
}

func TestRestoreConnectionLossBeforeCommitRollsBackAndCanRetry(t *testing.T) {
	s := authoringTestStore(t)
	c := seedRestoreCandidate(t, s, "lost-connection", domain.StatusReviewing)
	cmd := domain.ReviewRestoreCommand{OperationID: "lost-connection-operation", ActorID: "manager", ActorName: "管理员", Reason: "调整审核", RequestHash: "same-request", Targets: []domain.ReviewRestoreTarget{c.Target()}}
	applied, resume, done := make(chan int, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		release, err := s.AcquireReviewMutationLock(t.Context())
		if err != nil {
			done <- err
			return
		}
		defer release()
		done <- s.WithReviewTransaction(t.Context(), s, func(ctx context.Context) error {
			if _, err := s.ApplyReviewRestore(ctx, cmd); err != nil {
				return err
			}
			var pid int
			if err := s.queryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			applied <- pid
			<-resume
			return nil
		})
	}()
	var pid int
	select {
	case pid = <-applied:
	case err := <-done:
		t.Fatalf("apply before disconnect: %v", err)
	}
	var terminated bool
	if err := s.DB().QueryRow(`SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		close(resume)
		<-done
		t.Fatalf("disconnect fixture: %v", err)
	}
	close(resume)
	if err := <-done; err == nil {
		t.Fatal("disconnected transaction unexpectedly committed")
	}
	q, _ := s.GetQuestion(t.Context(), c.Question.ID)
	task, _ := s.GetTask(t.Context(), c.Task.ID)
	records, _ := s.ListRecordsByTaskID(t.Context(), c.Task.ID)
	if q.Status != domain.StatusReviewing || q.ReviewRevision != 0 || task == nil || len(records) != 1 {
		t.Fatal("connection loss exposed partial restore")
	}
	if receipt, err := s.GetReviewRestoreOperation(t.Context(), cmd.OperationID, cmd.ActorID, cmd.RequestHash); err != nil || receipt != nil {
		t.Fatalf("uncommitted receipt survived: %+v %v", receipt, err)
	}
	if result, err := restoreTestTransaction(s, cmd); err != nil || result.Restored != 1 {
		t.Fatalf("retry after disconnect: %+v %v", result, err)
	}
}

func TestReviewRestoreClearsLegacyPersonalConfirmationWithoutChangingContent(t *testing.T) {
	s := authoringTestStore(t)
	c := seedRestoreCandidate(t, s, "restore-legacy-active", domain.StatusReviewing)
	// Emulate a C3 record submitted before optional review suspended confirmation.
	if _, err := s.DB().ExecContext(t.Context(), `UPDATE questions SET library_state='active',confirmed_version=version,confirmation_source='self',confirmed_by='owner',confirmed_at=NOW(),library_revision=4 WHERE id=$1`, c.Question.ID); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{TaskIDs: []string{c.Task.ID}}, 1, 20, true)
	if err != nil || total != 1 || !rows[0].Eligible {
		t.Fatal(total, rows, err)
	}
	cmd := domain.ReviewRestoreCommand{OperationID: "restore-library-active", ActorID: "admin", ActorName: "管理员", Reason: "结束原审核安排", RequestHash: "restore-library-active-hash", Targets: []domain.ReviewRestoreTarget{rows[0].Target()}}
	if _, err := restoreTestTransaction(s, cmd); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetQuestion(t.Context(), c.Question.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.StatusAIReviewed || after.LibraryState != domain.LibraryUnfiled || after.ConfirmedVersion != 0 || after.ConfirmationSource != "" || after.ConfirmedBy != "" || after.ConfirmedAt != nil || after.LibraryRevision != 5 || after.Version != c.Question.Version {
		t.Fatalf("restore did not return to unfiled with independent library revision: %+v", after)
	}
	if !domain.QuestionContentEqual(*after, c.Question) {
		t.Fatal("review restore changed clinical content")
	}
}
