//go:build integration

package api

import (
	"context"
	"fmt"
	"testing"

	"aigo/internal/batch"
	"aigo/internal/storage"
)

type alreadyActiveBatchRetry struct{ batch.Executor }

func (p alreadyActiveBatchRetry) RetryFailed(context.Context, string) (*batch.BatchJob, error) {
	return nil, storage.ErrBatchJobAlreadyActive
}
func TestBatchConcurrentRetryConflictReturns409(t *testing.T) {
	s, h, token, points, cleanup := setupRandomBatchAPI(t)
	defer cleanup()
	code, body, _ := batchSubmitJSON(t, h, token, map[string]any{"mode": "random_total", "total_count": 1, "workflow_mode": "direct", "knowledge_point_ids": []string{points[0].ID}, "version_id": points[0].VersionID})
	if code != 200 {
		t.Fatal(code, body)
	}
	id := body["job_id"].(string)
	waitBatchTerminal(t, h, token, id)
	s.batchSvc = alreadyActiveBatchRetry{s.batchSvc}
	response := serveGenerateJSON(t, h, "POST", "/api/batch/retry-failed/"+id, token, nil)
	if response.Code != 409 {
		t.Fatalf("concurrent retry must be an actionable conflict, got %d %s", response.Code, response.Body)
	}
}
func TestBatchEquivalentValidIntentReplaysSamePlan(t *testing.T) {
	for _, name := range []string{"duplicate_ids", "default_direct_mode"} {
		t.Run(name, func(t *testing.T) {
			_, h, token, points, cleanup := setupRandomBatchAPI(t)
			defer cleanup()
			req := map[string]any{"mode": "random_total", "total_count": 1, "workflow_mode": "direct", "knowledge_point_ids": []string{points[0].ID}, "version_id": points[0].VersionID, "request_key": "equivalent-" + name}
			if name == "duplicate_ids" {
				req["knowledge_point_ids"] = []string{points[0].ID, points[0].ID}
			}
			if name == "default_direct_mode" {
				delete(req, "workflow_mode")
			}
			code, body, _ := batchSubmitJSON(t, h, token, req)
			if code != 200 {
				t.Fatal(code, body)
			}
			id := body["job_id"].(string)
			waitBatchTerminal(t, h, token, id)
			switch name {
			case "duplicate_ids":
				req["knowledge_point_ids"] = []string{points[0].ID}
			case "default_direct_mode":
				req["workflow_mode"] = "direct"
			}
			code, body, _ = batchSubmitJSON(t, h, token, req)
			if code != 200 || body["job_id"] != id || body["replayed"] != true {
				t.Fatalf("equivalent intent did not replay: %d %+v", code, body)
			}
		})
	}
}
func TestBatchEmptyPresentationIsInvalidRatherThanDefaulted(t *testing.T) {
	_, h, token, points, cleanup := setupRandomBatchAPI(t)
	defer cleanup()
	code, body, _ := batchSubmitJSON(t, h, token, map[string]any{"mode": "random_total", "total_count": 1, "workflow_mode": "knowledge", "knowledge_point_ids": []string{points[0].ID}, "version_id": points[0].VersionID, "presentation": map[string]any{}})
	if code != 400 {
		t.Fatalf("invalid explicit presentation should be rejected: %d %+v", code, body)
	}
}

func TestBatchExperienceToggleConflictDoesNotSubmitReplacement(t *testing.T) {
	for _, initial := range []bool{false, true} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			_, h, token, points, cleanup := setupRandomBatchAPI(t)
			defer cleanup()
			req := map[string]any{"mode": "random_total", "total_count": 1, "knowledge_point_ids": []string{points[0].ID}, "version_id": points[0].VersionID, "request_key": "experience-toggle-contract", "apply_personal_experiences": initial}
			code, body, _ := batchSubmitJSON(t, h, token, req)
			if code != 200 {
				t.Fatal(code, body)
			}
			id := body["job_id"].(string)
			waitBatchTerminal(t, h, token, id)
			replayCode, replay, _ := batchSubmitJSON(t, h, token, req)
			if replayCode != 200 || replay["replayed"] != true || replay["job_id"] != id {
				t.Fatalf("unchanged intent failed replay: %d %+v", replayCode, replay)
			}
			req["apply_personal_experiences"] = !initial
			code, body, _ = batchSubmitJSON(t, h, token, req)
			if code != 409 {
				t.Fatalf("changed experience toggle reused old plan: %d %+v", code, body)
			}
		})
	}
}
