// Package workflowtrace observes existing work; it never schedules work or
// returns diagnostic errors to business callers.
package workflowtrace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"
)

const MaxStages = 96

type Fingerprint struct {
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int    `json:"bytes"`
}
type Stage struct {
	Name       string      `json:"name"`
	Item       int         `json:"item"`
	Status     string      `json:"status"`
	ErrorKind  string      `json:"error_kind,omitempty"`
	StartedAt  time.Time   `json:"started_at"`
	DurationMS int64       `json:"duration_ms"`
	Input      Fingerprint `json:"input"`
	Output     Fingerprint `json:"output"`
}
type ModelReference struct {
	ID       string `json:"id,omitempty"`
	Recorded bool   `json:"recorded"`
}
type Record struct {
	PointCount      int              `json:"point_count"`
	ID              string           `json:"id"`
	Operation       string           `json:"operation"`
	Phase           string           `json:"phase"`
	Attempt         int              `json:"attempt"`
	WorkflowVersion string           `json:"workflow_version"`
	Outcome         string           `json:"outcome"`
	StartedAt       time.Time        `json:"started_at"`
	DurationMS      int64            `json:"duration_ms"`
	Stages          []Stage          `json:"stages"`
	StagesTruncated bool             `json:"stages_truncated"`
	ModelCalls      []ModelReference `json:"model_calls,omitempty"`
	QuestionIDs     []string         `json:"question_ids,omitempty"`
	CardCount       int              `json:"card_count"`
	TargetCount     int              `json:"target_count"`
}
type Recorder interface {
	SaveWorkflowExecution(context.Context, Record) error
}
type executionKey struct{}
type Execution struct {
	record   Record
	recorder Recorder
	ctx      context.Context
	started  time.Time
	closed   bool
}
type Span struct {
	execution *Execution
	index     int
	started   time.Time
	ended     bool
}

// Start opts in only when the existing store provides diagnostic persistence.
func Start(ctx context.Context, store any, operation, phase string, attempt int, version string) (context.Context, *Execution) {
	recorder, ok := store.(Recorder)
	if !ok {
		return ctx, nil
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		slog.WarnContext(ctx, "workflow_trace.skipped", "reason", "id_unavailable")
		return ctx, nil
	}
	if version == "" {
		version = "direct"
		if phase == "submission" {
			version = "unresolved"
		}
	}
	started := time.Now()
	e := &Execution{recorder: recorder, ctx: ctx, started: started, record: Record{ID: hex.EncodeToString(nonce), Operation: operation, Phase: phase, Attempt: attempt, WorkflowVersion: version, StartedAt: started.UTC(), Outcome: "incomplete", Stages: []Stage{}}}
	return context.WithValue(ctx, executionKey{}, e), e
}
func current(ctx context.Context) *Execution {
	e, _ := ctx.Value(executionKey{}).(*Execution)
	return e
}
func fingerprint(input any) Fingerprint {
	if input == nil {
		return Fingerprint{}
	}
	var raw []byte
	if value, ok := input.(string); ok {
		raw = []byte(value)
	} else {
		var err error
		raw, err = json.Marshal(input)
		if err != nil {
			return Fingerprint{}
		}
	}
	sum := sha256.Sum256(raw)
	return Fingerprint{SHA256: hex.EncodeToString(sum[:]), Bytes: len(raw)}
}
func Begin(ctx context.Context, name string, item int, input any) *Span {
	e := current(ctx)
	if e == nil || e.closed {
		return &Span{}
	}
	if len(e.record.Stages) >= MaxStages {
		e.record.StagesTruncated = true
		return &Span{}
	}
	started := time.Now()
	index := len(e.record.Stages)
	e.record.Stages = append(e.record.Stages, Stage{Name: name, Item: item, Status: "started", StartedAt: started.UTC(), Input: fingerprint(input)})
	return &Span{execution: e, index: index, started: started}
}
func (s *Span) End(status, kind string, output any) {
	if s == nil || s.execution == nil || s.ended || s.execution.closed {
		return
	}
	s.ended = true
	stage := &s.execution.record.Stages[s.index]
	stage.Status = status
	stage.ErrorKind = kind
	stage.Output = fingerprint(output)
	stage.DurationMS = time.Since(s.started).Milliseconds()
	if status == "failed" {
		s.execution.record.Outcome = "failed"
	}
}
func (s *Span) FailUnlessEnded(kind string) {
	if s != nil && !s.ended {
		s.End("failed", kind, nil)
	}
}
func (e *Execution) Complete(outcome string, ids []string) {
	if e == nil || e.closed {
		return
	}
	e.record.Outcome = outcome
	e.record.QuestionIDs = append([]string(nil), ids...)
}
func (e *Execution) Operation(value string) {
	if e != nil {
		e.record.Operation = value
	}
}
func (e *Execution) Version(value string) {
	if e != nil && value != "" {
		e.record.WorkflowVersion = value
	}
}
func LinkModel(ctx context.Context, id string, recorded bool) {
	if e := current(ctx); e != nil {
		e.record.ModelCalls = append(e.record.ModelCalls, ModelReference{id, recorded})
	}
}
func Materials(ctx context.Context, cards, targets int) {
	if e := current(ctx); e != nil {
		e.record.CardCount = cards
		e.record.TargetCount = targets
	}
}
func (e *Execution) Close() {
	if e == nil || e.closed {
		return
	}
	e.closed = true
	e.record.DurationMS = time.Since(e.started).Milliseconds()
	// Never turn a successful task into an extra retry for diagnostic persistence.
	if deadline, ok := e.ctx.Deadline(); ok && e.ctx.Err() == nil && time.Until(deadline) < 250*time.Millisecond {
		slog.WarnContext(e.ctx, "workflow_trace.skipped", "operation", e.record.Operation, "reason", "task_budget_low")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(e.ctx), 200*time.Millisecond)
	defer cancel()
	if err := e.recorder.SaveWorkflowExecution(ctx, e.record); err != nil {
		slog.WarnContext(ctx, "workflow_trace.write_failed", "operation", e.record.Operation, "execution_id", e.record.ID, "reason", "storage_unavailable")
		return
	}
	slog.InfoContext(ctx, "workflow_trace.recorded", "operation", e.record.Operation, "phase", e.record.Phase, "attempt", e.record.Attempt, "outcome", e.record.Outcome, "stage_count", len(e.record.Stages), "execution_id", e.record.ID)
}

func (e *Execution) Begin(name string, item int, input any) *Span {
	if e == nil {
		return &Span{}
	}
	return Begin(context.WithValue(e.ctx, executionKey{}, e), name, item, input)
}

func Points(ctx context.Context, count int) {
	if e := current(ctx); e != nil {
		e.record.PointCount = count
	}
}
