package workflowtrace

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

var ErrInvalidCursor = errors.New("诊断分页参数无效")

type Cursor struct {
	StartedAt time.Time `json:"t"`
	ID        string    `json:"id"`
}

func DecodeCursor(value string) (*Cursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 512 {
		return nil, ErrInvalidCursor
	}
	raw, e := base64.RawURLEncoding.DecodeString(value)
	if e != nil {
		return nil, ErrInvalidCursor
	}
	var c Cursor
	if json.Unmarshal(raw, &c) != nil || c.StartedAt.IsZero() || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(c.ID) {
		return nil, ErrInvalidCursor
	}
	return &c, nil
}
func EncodeCursor(c Cursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

type Query struct {
	Operation string
	Batch     bool
	UnitCount int
	Limit     int
	After     *Cursor
}
type CallSummary struct {
	Attempted               bool   `json:"attempted"`
	Outcome                 string `json:"outcome"`
	Observed                bool   `json:"observed"`
	SearchCalls             int    `json:"search_calls"`
	ExtractorCalls          int    `json:"extractor_calls"`
	ExtractorFailureNotices int    `json:"extractor_failure_notices"`
}
type DiagnosticRow struct {
	Execution Record
	Call      *CallSummary
}
type Page struct {
	Rows []DiagnosticRow
	Next string
}
type Reader interface {
	ReadWorkflowExecutions(context.Context, Query) (Page, error)
}
