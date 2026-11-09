//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"fmt"
	"testing"
	"time"
)

// 纯存储容量夹具不冒充真实生成验收；用户闭环另由浏览器通过正常生成/首检构造。
func TestReviewRestoreBatchSizesAndBoundedPages(t *testing.T) {
	for _, size := range []int{1, 20, 100, 500, 1000, 5000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := authoringTestStore(t)
			base := seedRestoreCandidate(t, s, "scale-base", domain.StatusReviewing)
			if size > 1 {
				statements := []string{
					`INSERT INTO questions(id,clinical_stem,answer,status,version,owner_id,created_by,created_at,updated_at) SELECT 'scale-q-'||n,'容量夹具正文'||n,'A','reviewing',1,'owner','owner',NOW(),NOW() FROM generate_series(1,$1) n`,
					`INSERT INTO review_tasks(id,question_id,flow_id,status,current_round,question_version,assigned_to,created_at,updated_at) SELECT 'scale-task-'||n,'scale-q-'||n,'flow-scale-base','reviewing',1,1,ARRAY['r1','r2'],NOW(),NOW() FROM generate_series(1,$1) n`,
					`INSERT INTO ai_review_results(id,question_id,question_version,verdict) SELECT 'scale-ai-'||n,'scale-q-'||n,1,'pass' FROM generate_series(1,$1) n`,
					`INSERT INTO review_records(id,task_id,question_id,round_number,expert_id,conclusion) SELECT 'scale-record-'||n,'scale-task-'||n,'scale-q-'||n,1,'r1','approved' FROM generate_series(1,$1) n`,
					`INSERT INTO audit_logs(id,question_id,action,actor) SELECT 'scale-log-'||n||'-'||action,'scale-q-'||n,action,'r1' FROM generate_series(1,$1) n CROSS JOIN unnest(ARRAY['submit','review']) action`,
				}
				for _, statement := range statements {
					if _, err := s.DB().Exec(statement, size-1); err != nil {
						t.Fatal(err)
					}
				}
			}
			started := time.Now()
			first, total, err := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{FlowID: base.Flow.ID}, 1, 20, false)
			if err != nil || total != size || len(first) != min(20, size) {
				t.Fatalf("bounded list: %d/%d %v", len(first), total, err)
			}
			listElapsed := time.Since(started)
			restored, batches := 0, 0
			var writeElapsed time.Duration
			for restored < size {
				items, total, err := s.SearchReviewRestoreCandidates(t.Context(), domain.ReviewRestoreFilter{FlowID: base.Flow.ID, EligibleOnly: true}, 1, 1000, false)
				if err != nil || total != size-restored {
					t.Fatalf("remaining: %d %v", total, err)
				}
				targets := make([]domain.ReviewRestoreTarget, 0, len(items))
				for _, item := range items {
					if !item.Eligible {
						t.Fatal(item.Reason)
					}
					targets = append(targets, item.Target())
				}
				start := time.Now()
				result, err := restoreTestTransaction(s, domain.ReviewRestoreCommand{OperationID: fmt.Sprintf("scale-%d-batch-%d", size, batches), ActorID: "manager", ActorName: "容量测试", Reason: "分批还原", RequestHash: fmt.Sprint(size, batches), Targets: targets})
				writeElapsed += time.Since(start)
				if err != nil || result.Restored != len(items) || result.DeletedTasks != len(items) || result.DeletedRecords != len(items) {
					t.Fatalf("restore: %+v %v", result, err)
				}
				restored += result.Restored
				batches++
			}
			var questions, checks, tasks, records int
			if err := s.DB().QueryRow(`SELECT (SELECT count(*) FROM questions WHERE status='ai_reviewed' AND review_revision=1 AND version=1),(SELECT count(*) FROM ai_review_results),(SELECT count(*) FROM review_tasks),(SELECT count(*) FROM review_records)`).Scan(&questions, &checks, &tasks, &records); err != nil {
				t.Fatal(err)
			}
			if questions != size || checks != size || tasks != 0 || records != 0 {
				t.Fatalf("wrong totals: q=%d AI=%d tasks=%d records=%d", questions, checks, tasks, records)
			}
			t.Logf("size=%d first_page=%s write_total=%s batches=%d", size, listElapsed.Round(time.Millisecond), writeElapsed.Round(time.Millisecond), batches)
		})
	}
}
