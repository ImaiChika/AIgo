package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"github.com/lib/pq"
)

// PostgreSQL历史时区可能包含秒偏移；投影为UTC RFC3339，兼容零值/旧数据。
func restoreJSONRow(alias string, fields ...string) string {
	pairs := make([]string, 0, len(fields))
	for _, field := range fields {
		pairs = append(pairs, "'"+field+"',to_char("+alias+"."+field+" AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")
	}
	return "(to_jsonb(" + alias + ")||jsonb_build_object(" + strings.Join(pairs, ",") + "))"
}

var reviewRestoreAuditActions = []string{"submit", "resubmit", "submit_bank", "review", "final_approved", "final_rejected", "final_revision_required", "publish", "unpublish", "flow_revoke", "review_flow_migrate"}

func (s *Store) SearchReviewRestoreCandidates(ctx context.Context, f domain.ReviewRestoreFilter, page, size int, details bool) ([]domain.ReviewRestoreCandidate, int, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > domain.MaxReviewRestoreQuestions+1 {
		size = domain.MaxReviewRestoreQuestions + 1
	}
	where, args := questionFilterWhere(storage.QuestionFilter{OwnerID: f.OwnerID, Keyword: f.Keyword})
	if where == "" {
		where = "WHERE TRUE"
	}
	where += " AND q.status IN ('reviewing','conflict') AND q.library_state<>'deleted'"
	add := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	if f.Status != "" {
		where += " AND t.status=" + add(f.Status)
	}
	if f.FlowID != "" {
		where += " AND t.flow_id=" + add(f.FlowID)
	}
	if len(f.TaskIDs) > 0 {
		where += " AND t.id=ANY(" + add(pq.Array(f.TaskIDs)) + ")"
	}
	recordExpr := "'[]'::jsonb"
	historyExpr := "'[]'::jsonb"
	peopleExpr := "'{}'::jsonb"
	if details {
		recordExpr = `COALESCE((SELECT jsonb_agg(` + restoreJSONRow("r", "created_at") + `||jsonb_build_object('review_status',r.conclusion) ORDER BY r.created_at,r.id) FROM review_records r WHERE r.question_id=q.id),'[]'::jsonb)`
		historyExpr = `COALESCE((SELECT jsonb_agg(` + restoreJSONRow("h", "created_at", "updated_at") + `||jsonb_build_object('flow_name',COALESCE(hf.name,'')) ORDER BY h.created_at,h.id) FROM review_tasks h LEFT JOIN review_flows hf ON hf.id=h.flow_id WHERE h.question_id=q.id),'[]'::jsonb)`
		peopleExpr = `COALESCE((SELECT jsonb_object_agg(p.id,COALESCE(NULLIF(p.display_name,''),p.username)||' ('||p.username||')') FROM users p WHERE p.id=ANY(COALESCE(t.assigned_to,'{}')||COALESCE(t.final_reviewer_ids,'{}')||ARRAY(SELECT jsonb_array_elements_text(round_config->'expert_ids') FROM jsonb_array_elements(COALESCE(f.rounds,'[]'::jsonb)) round_config)) OR p.id IN (SELECT expert_id FROM review_records WHERE question_id=q.id)),'{}'::jsonb)`
	}
	eligible := add(f.EligibleOnly)
	limit := add(size)
	offset := add((page - 1) * size)
	query := `WITH latest AS (SELECT DISTINCT ON(question_id) * FROM review_tasks ORDER BY question_id,created_at DESC,id DESC), raw_candidates AS (
 SELECT COALESCE(t.updated_at,q.updated_at) AS updated_at,COALESCE(t.id,q.id) AS id,jsonb_build_object(
 'question',` + restoreJSONRow("q", "created_at", "updated_at") + `||jsonb_build_object('bank_ids',ARRAY(SELECT bank_id FROM question_bank_members WHERE question_id=q.id)),
 'task',` + restoreJSONRow("t", "created_at", "updated_at") + `,'flow',` + restoreJSONRow("f", "created_at") + `,
 'creator_username',COALESCE(u.username,q.created_by),'creator_name',COALESCE(u.display_name,q.created_by),
 'owner_ready',COALESCE(u.enabled AND
   ('question:edit'=ANY(COALESCE(u.permissions,'{}')||ARRAY(SELECT p FROM roles ro CROSS JOIN LATERAL unnest(ro.permissions) p WHERE ro.id=ANY(COALESCE(u.roles,'{}')||ARRAY[u.role])))) AND
   ('review:submit'=ANY(COALESCE(u.permissions,'{}')||ARRAY(SELECT p FROM roles ro CROSS JOIN LATERAL unnest(ro.permissions) p WHERE ro.id=ANY(COALESCE(u.roles,'{}')||ARRAY[u.role])))),FALSE),
 'ai_pass',EXISTS(SELECT 1 FROM ai_review_results ar WHERE ar.question_id=q.id AND ar.verdict='pass'),
 'has_share',EXISTS(SELECT 1 FROM question_share_requests sr WHERE sr.question_id=q.id),
 'record_count',(SELECT count(*) FROM review_records r WHERE r.question_id=q.id),
 'task_count',(SELECT count(*) FROM review_tasks h WHERE h.question_id=q.id),
 'records',` + recordExpr + `,'history',` + historyExpr + `,'people',` + peopleExpr + `) AS payload
 FROM questions q LEFT JOIN latest t ON q.id=t.question_id LEFT JOIN review_flows f ON f.id=t.flow_id LEFT JOIN users u ON u.id=q.owner_id ` + where + `
 ), candidates AS (SELECT * FROM raw_candidates WHERE NOT ` + eligible + ` OR ((payload->>'owner_ready')::boolean AND (payload->>'ai_pass')::boolean AND NOT (payload->>'has_share')::boolean AND payload->'task'->>'status' IN ('reviewing','conflict') AND (payload->'question'->>'status'='reviewing' OR payload->'task'->>'status'='conflict'))),
 paged AS (SELECT payload FROM candidates ORDER BY updated_at DESC,id DESC LIMIT ` + limit + ` OFFSET ` + offset + `)
 SELECT (SELECT count(*) FROM candidates),COALESCE((SELECT jsonb_agg(payload) FROM paged),'[]'::jsonb)`
	var total int
	var data []byte
	if err := s.queryRowContext(ctx, query, args...).Scan(&total, &data); err != nil {
		return nil, 0, err
	}
	var rows []struct {
		domain.ReviewRestoreCandidate
		QuestionJSON json.RawMessage `json:"question"`
		OwnerReady   bool            `json:"owner_ready"`
		AIPass       bool            `json:"ai_pass"`
		HasShare     bool            `json:"has_share"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, 0, err
	}
	out := make([]domain.ReviewRestoreCandidate, 0, len(rows))
	for _, row := range rows {
		c := row.ReviewRestoreCandidate
		var q struct {
			domain.A2Question
			CreatedBy  string `json:"created_by"`
			SystemName string `json:"system_name"`
		}
		if err := json.Unmarshal(row.QuestionJSON, &q); err != nil {
			return nil, 0, err
		}
		c.Question = q.A2Question
		c.Question.CreatedBy = q.CreatedBy
		c.Question.System = q.SystemName
		switch {
		case c.Task.ID == "":
			c.Reason = "缺少审核任务，请联系维护人员核对"
		case c.Task.Status != domain.StatusReviewing && c.Task.Status != domain.StatusConflict:
			c.Reason = "最新任务与题目当前审核状态不一致"
		case c.Task.Status == domain.StatusReviewing && c.Question.Status != domain.StatusReviewing:
			c.Reason = "题目与审核任务状态不一致"
		case !row.OwnerReady:
			c.Reason = "原出题账号不存在、未启用或已无编辑/送审权限"
		case !row.AIPass:
			c.Reason = "缺少首次AI检查通过记录"
		case row.HasShare:
			c.Reason = "存在全局分享关联，不能通过审核还原修改分享状态"
		default:
			c.Eligible = true
		}
		c.Task.FlowName = c.Flow.Name
		out = append(out, c)
	}
	return out, total, nil
}

func (s *Store) ReviewRestoreOptions(ctx context.Context) (*domain.ReviewRestoreOptions, error) {
	// 聚合元数据，避免为筛选器拉取全部题干、审核意见或整个用户库。
	var data []byte
	err := s.queryRowContext(ctx, `WITH latest AS (
 SELECT DISTINCT ON(question_id) question_id,flow_id FROM review_tasks ORDER BY question_id,created_at DESC,id DESC
), active AS (
 SELECT t.flow_id,q.owner_id,COALESCE(f.name,t.flow_id,'未关联流程') flow_name,
 COALESCE(NULLIF(u.display_name,''),u.username,q.created_by)||COALESCE(' ('||u.username||')','') owner_name
 FROM questions q LEFT JOIN latest t ON t.question_id=q.id LEFT JOIN review_flows f ON f.id=t.flow_id LEFT JOIN users u ON u.id=q.owner_id
 WHERE q.status IN ('reviewing','conflict')
), flows AS (SELECT flow_id AS id,flow_name AS name,count(*) AS count FROM active WHERE flow_id IS NOT NULL GROUP BY flow_id,flow_name),
owners AS (SELECT owner_id AS id,owner_name AS name,count(*) AS count FROM active GROUP BY owner_id,owner_name)
SELECT jsonb_build_object('flows',COALESCE((SELECT jsonb_agg(to_jsonb(flows) ORDER BY name,id) FROM flows),'[]'::jsonb),
'owners',COALESCE((SELECT jsonb_agg(to_jsonb(owners) ORDER BY name,id) FROM owners),'[]'::jsonb))`).Scan(&data)
	if err != nil {
		return nil, err
	}
	var options domain.ReviewRestoreOptions
	if err := json.Unmarshal(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}

func (s *Store) GetReviewRestoreOperation(ctx context.Context, id, actor, hash string) (*domain.ReviewRestoreResult, error) {
	var storedActor, storedHash string
	var data []byte
	err := s.queryRowContext(ctx, `SELECT actor_id,request_hash,result FROM review_restore_operations WHERE id=$1`, id).Scan(&storedActor, &storedHash, &data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if storedActor != actor || storedHash != hash {
		return nil, domain.ErrReviewConflict
	}
	var result domain.ReviewRestoreResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	result.Replayed = true
	return &result, nil
}

// ApplyReviewRestore 由review服务在数据库审核锁及事务中调用；这里不启动独立事务。
func (s *Store) ApplyReviewRestore(ctx context.Context, cmd domain.ReviewRestoreCommand) (*domain.ReviewRestoreResult, error) {
	if reviewTransactionFromContext(ctx) == nil {
		return nil, fmt.Errorf("审核还原必须在审核事务内执行")
	}
	if cmd.OperationID == "" || cmd.ActorID == "" || strings.TrimSpace(cmd.Reason) == "" || len(cmd.Targets) == 0 || len(cmd.Targets) > domain.MaxReviewRestoreQuestions {
		return nil, domain.ErrReviewBadRequest
	}
	if result, err := s.GetReviewRestoreOperation(ctx, cmd.OperationID, cmd.ActorID, cmd.RequestHash); err != nil || result != nil {
		return result, err
	}
	ids := make([]string, 0, len(cmd.Targets))
	qids := make([]string, 0, len(cmd.Targets))
	targets := map[string]domain.ReviewRestoreTarget{}
	seenQ := map[string]bool{}
	for _, target := range cmd.Targets {
		if target.TaskID == "" || target.QuestionID == "" || targets[target.TaskID].TaskID != "" || seenQ[target.QuestionID] {
			return nil, domain.ErrReviewBadRequest
		}
		targets[target.TaskID] = target
		seenQ[target.QuestionID] = true
		ids = append(ids, target.TaskID)
		qids = append(qids, target.QuestionID)
	}
	// 与编辑使用相同题目行锁；稳定顺序避免重叠批次的行锁死锁。
	for _, query := range []string{`SELECT id FROM questions WHERE id=ANY($1) ORDER BY id FOR UPDATE`, `SELECT id FROM review_tasks WHERE question_id=ANY($1) ORDER BY id FOR UPDATE`} {
		rows, err := s.queryContext(ctx, query, pq.Array(qids))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	candidates, total, err := s.SearchReviewRestoreCandidates(ctx, domain.ReviewRestoreFilter{TaskIDs: ids}, 1, len(ids), false)
	if err != nil {
		return nil, err
	}
	if total != len(ids) {
		return nil, domain.ErrReviewConflict
	}
	for _, c := range candidates {
		target, ok := targets[c.Task.ID]
		if !ok || !c.Eligible || target.QuestionID != c.Question.ID || target.Version != c.Question.Version || target.ReviewRevision != c.Question.ReviewRevision || !target.UpdatedAt.Equal(c.Task.UpdatedAt) {
			return nil, domain.ErrReviewConflict
		}
	}
	result := &domain.ReviewRestoreResult{OperationID: cmd.OperationID, QuestionIDs: qids, Restored: len(qids)}
	if err := s.queryRowContext(ctx, `SELECT count(*) FROM review_records WHERE question_id=ANY($1)`, pq.Array(qids)).Scan(&result.DeletedRecords); err != nil {
		return nil, err
	}
	var inconsistent bool
	if err := s.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM review_records r JOIN review_tasks t ON t.id=r.task_id WHERE (t.question_id=ANY($1) OR r.question_id=ANY($1)) AND r.question_id<>t.question_id)`, pq.Array(qids)).Scan(&inconsistent); err != nil {
		return nil, err
	}
	if inconsistent {
		return nil, domain.ErrReviewConflict
	}
	deleted, err := s.execContext(ctx, `DELETE FROM review_tasks WHERE question_id=ANY($1)`, pq.Array(qids))
	if err != nil {
		return nil, err
	}
	n, _ := deleted.RowsAffected()
	result.DeletedTasks = int(n)
	// 仅精确删除题目级审核过程日志，生成/编辑/AI及流程配置日志保留。
	deleted, err = s.execContext(ctx, `DELETE FROM audit_logs WHERE question_id=ANY($1) AND action=ANY($2)`, pq.Array(qids), pq.Array(reviewRestoreAuditActions))
	if err != nil {
		return nil, err
	}
	n, _ = deleted.RowsAffected()
	result.DeletedLogs = int(n)
	if _, err = s.execContext(ctx, `UPDATE questions SET status='ai_reviewed',review_revision=review_revision+1,
 library_revision=library_revision+CASE WHEN library_state<>'unfiled' OR confirmed_version<>0 OR confirmation_source<>'' OR confirmed_by<>'' OR confirmed_at IS NOT NULL THEN 1 ELSE 0 END,
 library_state='unfiled',confirmed_version=0,confirmation_source='',confirmed_by='',confirmed_at=NULL,updated_at=NOW() WHERE id=ANY($1)`, pq.Array(qids)); err != nil {
		return nil, err
	}
	now := time.Now()
	logs := make([]domain.AuditLog, 0, len(candidates))
	for _, c := range candidates {
		detail, _ := json.Marshal(map[string]any{"operation_id": cmd.OperationID, "reason": cmd.Reason, "previous_flow_id": c.Flow.ID, "previous_flow_name": c.Flow.Name, "previous_round": c.Task.CurrentRound, "deleted_tasks": c.TaskCount, "deleted_records": c.RecordCount, "review_revision": c.Question.ReviewRevision + 1})
		logs = append(logs, domain.AuditLog{ID: "restore-" + cmd.OperationID + "-" + c.Question.ID, QuestionID: c.Question.ID, Action: "review_restore", Actor: cmd.ActorName, Detail: string(detail), CreatedAt: now})
	}
	logData, _ := json.Marshal(logs)
	if _, err = s.execContext(ctx, `INSERT INTO audit_logs(id,question_id,action,actor,detail,created_at) SELECT id,question_id,action,actor,detail,created_at FROM jsonb_populate_recordset(NULL::audit_logs,$1::jsonb)`, string(logData)); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(result)
	if _, err = s.execContext(ctx, `INSERT INTO review_restore_operations(id,actor_id,request_hash,result) VALUES($1,$2,$3,$4::jsonb)`, cmd.OperationID, cmd.ActorID, cmd.RequestHash, string(data)); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) ListQuestionReviewTasks(ctx context.Context, qid string) ([]domain.ReviewTask, error) {
	rows, err := s.queryContext(ctx, `SELECT `+taskColumns+` FROM review_tasks WHERE question_id=$1 ORDER BY created_at,id`, qid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ReviewTask{}
	for rows.Next() {
		t, err := scanReviewTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *Store) CountReviewRecordsByExpert(ctx context.Context, expert string) (int, error) {
	var count int
	err := s.queryRowContext(ctx, `SELECT count(*) FROM review_records WHERE expert_id=$1`, expert).Scan(&count)
	return count, err
}
