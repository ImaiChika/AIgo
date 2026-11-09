//go:build integration

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestBatchReplayUsesOriginalRequestAndSurvivesCheckUnavailability(t *testing.T) {
	for _, name := range []string{"omitted_version", "checker_unavailable", "ordered_limit"} {
		t.Run(name, func(t *testing.T) {
			s, h, token, points, cleanup := setupRandomBatchAPI(t)
			defer cleanup()
			payload := map[string]any{"mode": "random_total", "total_count": 1, "workflow_mode": "direct", "knowledge_point_ids": []string{points[0].ID}, "request_key": "replay-contract-" + name}
			if name != "omitted_version" {
				payload["version_id"] = points[0].VersionID
			}
			if name == "ordered_limit" {
				payload["mode"] = "per_point"
				delete(payload, "total_count")
				payload["count"] = 1
				payload["limit"] = 1
				payload["knowledge_point_ids"] = []string{points[0].ID, points[1].ID}
			}
			code, body, _ := batchSubmitJSON(t, h, token, payload)
			if code != 200 {
				t.Fatal(code, body)
			}
			job := body["job_id"].(string)
			waitBatchTerminal(t, h, token, job)
			want := http.StatusOK
			if name == "checker_unavailable" {
				s.aiCheckSvc = nil
			}
			if name == "ordered_limit" {
				payload["knowledge_point_ids"] = []string{points[1].ID, points[0].ID}
				want = http.StatusConflict
			}
			code, body, _ = batchSubmitJSON(t, h, token, payload)
			if code != want || (want == 200 && (body["job_id"] != job || body["replayed"] != true)) {
				t.Fatalf("replay contract: got %d %v, want %d original job %s", code, body, want, job)
			}
		})
	}
}

func TestBatchPreviewChecksCrossPageCoverageAndUnavailableService(t *testing.T) {
	s, h, token, points, cleanup := setupRandomBatchAPI(t)
	defer cleanup()
	ids := []string{points[0].ID, points[1].ID, points[2].ID}
	for i := 3; i < 61; i++ {
		point := points[1]
		point.ID = ""
		point.OutlineCode = fmt.Sprintf("coverage.%03d", i)
		created, err := s.kpSvc.CreatePoint(t.Context(), point)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	payload := map[string]any{"mode": "random_total", "total_count": 2, "workflow_mode": "knowledge", "version_id": points[0].VersionID, "knowledge_point_ids": ids}
	r := serveGenerateJSON(t, h, "POST", "/api/batch/preview", token, payload)
	var body map[string]any
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &body) != nil {
		t.Fatal(r.Code, r.Body)
	}
	if body["coverage_checked"] != float64(61) || body["missing_card_count"] != float64(60) || body["coverage_complete"] != true || body["coverage_unchecked"] != float64(0) || body["coverage_error"] != nil {
		t.Fatal(body)
	}
	s.knowledgeBaseSvc = nil
	r = serveGenerateJSON(t, h, "POST", "/api/batch/preview", token, payload)
	body = map[string]any{}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &body) != nil {
		t.Fatal(r.Code, r.Body)
	}
	if body["coverage_complete"] != false || body["coverage_unchecked"] != float64(61) || body["coverage_error"] == nil {
		t.Fatal(body)
	}
}
