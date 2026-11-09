package storage

import (
	"context"
	"encoding/json"
)

// LegacyBatchImportStore completes an old non-pipeline batch using its durable
// frozen output. Question saves, first-check enqueue, quota handoff and the
// replayable import result commit together. A stale exact import claim can be
// resumed without scanning or regenerating unrelated questions.
type LegacyBatchImportStore interface {
	CommitLegacyBatchImport(context.Context, string, QuestionChange, int) (json.RawMessage, error)
}
