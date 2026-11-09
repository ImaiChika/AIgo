package postgres

import (
	"aigo/internal/storage"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/lib/pq"
)

func (s *Store) ReadAuthoringTask(ctx context.Context, kind, id string) (*storage.AuthoringTask, error) {
	task := &storage.AuthoringTask{Kind: kind, ID: id}
	var err error
	if kind == "single" {
		err = s.queryRowContext(ctx, `SELECT owner_id,status,requested_count,question_ids,COALESCE(request_json#>>'{request,knowledge_points,0,topic}','单题命题') FROM generation_runs WHERE id=$1`, id).Scan(&task.OwnerID, &task.Status, &task.Total, pq.Array(&task.QuestionIDs), &task.Name)
		if task.Status == "failed" {
			task.GenerationFailed = max(0, task.Total-len(task.QuestionIDs))
		}
	}
	if kind == "batch" {
		var raw []byte
		var imported bool
		err = s.queryRowContext(ctx, `SELECT owner_id,job_name,status,total_count,failed,imported_at IS NOT NULL,
   COALESCE((SELECT jsonb_agg(DISTINCT qid) FROM (
    SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(import_result->'question_ids')='array' THEN import_result->'question_ids' ELSE '[]'::jsonb END) AS qid
    UNION ALL SELECT item->>'question_id' FROM jsonb_array_elements(CASE WHEN jsonb_typeof(output_json->'items')='array' THEN output_json->'items' ELSE '[]'::jsonb END)item
   ) ids WHERE COALESCE(qid,'')<>''),'[]'::jsonb)
   FROM batch_jobs WHERE id=$1`, id).Scan(&task.OwnerID, &task.Name, &task.Status, &task.Total, &task.GenerationFailed, &imported, &raw)
		if err == nil {
			err = json.Unmarshal(raw, &task.QuestionIDs)
			task.NeedsImport = !imported && len(task.QuestionIDs) == 0 && (task.Status == "completed" || task.Status == "complete") && task.GenerationFailed < task.Total
		}
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}
func (s *Store) CountAuthoringQuestions(ctx context.Context, uid string, ids []string) (storage.AuthoringCounts, error) {
	var result storage.AuthoringCounts
	if len(ids) == 0 {
		return result, nil
	}
	err := s.queryRowContext(ctx, `SELECT
 COUNT(*) FILTER(WHERE d.verdict IS NULL AND q.owner_id=$1 AND q.status='ai_reviewed' AND q.library_state='unfiled' AND NOT EXISTS(SELECT 1 FROM review_tasks rt WHERE rt.question_id=q.id AND rt.status NOT IN ('published','approved','rejected','archived'))),
 COUNT(*) FILTER(WHERE d.verdict IS NULL AND q.owner_id=$1 AND q.status IN ('ai_reviewed','published') AND q.library_state='active' AND q.confirmed_version=q.version AND NOT EXISTS(SELECT 1 FROM review_tasks rt WHERE rt.question_id=q.id AND rt.status NOT IN ('published','approved','rejected','archived'))),
 COUNT(*) FILTER(WHERE d.verdict IS NULL AND q.owner_id=$1 AND q.library_state='deleted'),
 COUNT(*) FILTER(WHERE d.verdict IS NULL AND q.owner_id=$1 AND q.status IN ('ai_draft','auto_checked') AND COALESCE(t.status,'pending')<>'failed'),
 COUNT(*) FILTER(WHERE d.verdict IS NOT NULL AND d.verdict<>'check_failed'),
 COUNT(*) FILTER(WHERE d.verdict='check_failed' OR (d.verdict IS NULL AND q.owner_id=$1 AND q.status IN ('ai_draft','auto_checked') AND t.status='failed'))
 FROM (SELECT DISTINCT unnest($2::text[]) AS id) ids LEFT JOIN questions q ON q.id=ids.id
 LEFT JOIN LATERAL(SELECT status FROM ai_check_tasks WHERE question_id=ids.id ORDER BY created_at DESC,id DESC LIMIT 1)t ON q.owner_id=$1
 LEFT JOIN LATERAL(SELECT verdict FROM ai_check_discards WHERE question_id=ids.id AND owner_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1)d ON true`, uid, pq.Array(ids)).Scan(&result.Arrange, &result.Confirmed, &result.Deleted, &result.Checking, &result.Rejected, &result.CheckFailed)
	return result, err
}
