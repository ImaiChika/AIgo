package postgres

import (
	"aigo/internal/workflowtrace"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Every read is scoped by an already-authorized exact run or batch namespace.
// Exact operation keys use the existing index and cannot extend access to a
// different batch whose ID happens to share a prefix.
func (s *Store) ReadWorkflowExecutions(ctx context.Context, q workflowtrace.Query) (workflowtrace.Page, error) {
	page := workflowtrace.Page{Rows: []workflowtrace.DiagnosticRow{}}
	if q.Operation == "" || q.Limit < 1 || q.Limit > 20 {
		return page, fmt.Errorf("invalid diagnostic query")
	}
	condition := "w.operation=$1"
	args := []any{q.Operation}
	if q.Batch {
		if !strings.HasSuffix(q.Operation, ":") {
			return page, fmt.Errorf("invalid batch namespace")
		}
		if q.UnitCount < 0 || q.UnitCount > 10000 {
			return page, fmt.Errorf("invalid batch size")
		}
		condition = "w.operation IN (SELECT $1 || n::text FROM generate_series(0,$2::integer-1) AS n UNION ALL SELECT $1 || 'submission')"
		args = append(args, q.UnitCount)
	}
	if q.After != nil {
		position := len(args) + 1
		condition += fmt.Sprintf(" AND (w.started_at,w.id)<($%d,$%d)", position, position+1)
		args = append(args, q.After.StartedAt, q.After.ID)
	}
	args = append(args, q.Limit+1)
	query := `SELECT w.id,w.started_at,w.record,CASE WHEN m.id IS NULL THEN NULL ELSE jsonb_build_object(
 'attempted',m.record->'http_attempted','outcome',m.record->'outcome','observed',m.record->'tool_events_observed',
 'search_calls',m.record->'search_calls','extractor_calls',m.record->'extractor_calls','extractor_failure_notices',m.record->'extractor_failure_notices') END
 FROM workflow_execution_records w LEFT JOIN model_call_records m
 ON m.id=w.record->'model_calls'->0->>'id' AND m.operation=w.operation AND (m.record->>'attempt')::integer=w.attempt
 WHERE ` + condition + fmt.Sprintf(" ORDER BY w.started_at DESC,w.id DESC LIMIT $%d", len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var last workflowtrace.Cursor
	for rows.Next() {
		var id string
		var stamp time.Time
		var raw, call []byte
		if err = rows.Scan(&id, &stamp, &raw, &call); err != nil {
			return page, err
		}
		if len(page.Rows) == q.Limit {
			page.Next = workflowtrace.EncodeCursor(last)
			break
		}
		var item workflowtrace.DiagnosticRow
		if err = json.Unmarshal(raw, &item.Execution); err != nil {
			return page, err
		}
		if len(call) > 0 {
			item.Call = &workflowtrace.CallSummary{}
			if err = json.Unmarshal(call, item.Call); err != nil {
				return page, err
			}
		}
		page.Rows = append(page.Rows, item)
		last = workflowtrace.Cursor{StartedAt: stamp, ID: id}
	}
	return page, rows.Err()
}

var _ workflowtrace.Reader = (*Store)(nil)
