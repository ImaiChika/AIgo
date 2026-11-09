package storage

import (
	"context"
	"testing"
)

func TestQueueAttemptFenceSurvivesCancellationAndRejectsMismatchedWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	ctx = WithQueueAttempt(ctx, QueueGeneration, "run", 2)
	cancel()
	safe := context.WithoutCancel(ctx)
	if safe.Err() != nil || QueueAttemptFromContext(safe, QueueGeneration, "run") != 2 {
		t.Fatal("safe status write lost its claimed identity")
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		kind QueueKind
		id   string
		want int
	}{
		{"legacy", t.Context(), QueueGeneration, "run", 0},
		{"other_run", safe, QueueGeneration, "other", -1},
		{"other_queue", safe, QueueAICheck, "run", -1},
		{"invalid_attempt", WithQueueAttempt(t.Context(), QueueGeneration, "run", 0), QueueGeneration, "run", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := QueueAttemptFromContext(tc.ctx, tc.kind, tc.id); got != tc.want {
				t.Fatalf("fence=%d want %d", got, tc.want)
			}
		})
	}
}
