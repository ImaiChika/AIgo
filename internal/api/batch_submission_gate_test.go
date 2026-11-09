package api

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestBatchSubmissionGateWaitCancellationAndCleanup(t *testing.T) {
	s := &Server{}
	release, err := s.acquireBatchSubmission(t.Context(), "owner", "key")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.acquireBatchSubmission(t.Context(), "other-owner", "key")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if done, err := s.acquireBatchSubmission(ctx, "owner", "key"); done != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: done=%v err=%v", done != nil, err)
	}
	s.batchSubmissionMu.Lock()
	n := len(s.batchSubmissions)
	s.batchSubmissionMu.Unlock()
	if n != 1 {
		t.Fatalf("entries after cancelled waiter=%d", n)
	}
	release()
	if len(s.batchSubmissions) != 0 {
		t.Fatal("gate entry leaked")
	}
	again, err := s.acquireBatchSubmission(t.Context(), "owner", "key")
	if err != nil {
		t.Fatal(err)
	}
	again()
	if len(s.batchSubmissions) != 0 {
		t.Fatal("reused gate entry leaked")
	}
}

func TestBatchRequestMatchesLegacyAcceptedPlanWithoutChangingOrder(t *testing.T) {
	for _, mode := range []string{domain.BatchModePerPoint, domain.BatchModeRandomTotal} {
		t.Run(mode, func(t *testing.T) {
			req := &batchSubmitRequest{Mode: mode, Count: 1, KnowledgePointIDs: []string{"b", "a"}}
			plan := &domain.BatchPlan{Version: 1, Mode: mode, VersionID: "v", CandidateCount: 2, RequestedTotal: 2, Units: []domain.BatchPlanUnit{{Index: 0, PointSequence: 1, KnowledgePoint: domain.KnowledgePoint{ID: "b", VersionID: "v"}}, {Index: 1, PointSequence: 1, KnowledgePoint: domain.KnowledgePoint{ID: "a", VersionID: "v"}}}}
			raw, _ := json.Marshal(plan)
			accepted := *req
			accepted.VersionID = "v"
			accepted.KnowledgePointIDs = []string{"a", "b"}
			record := &storage.BatchJobRecord{PlanJSON: string(raw), RequestHash: batchRequestHash(&accepted, mode)}
			if !batchRequestMatches(record, req, mode, batchRequestHash(req, mode)) {
				t.Fatal("same original intent did not replay legacy accepted plan")
			}
			req.KnowledgePointIDs = []string{"a", "b"}
			got := batchRequestMatches(record, req, mode, batchRequestHash(req, mode))
			if got != (mode == domain.BatchModeRandomTotal) {
				t.Fatal("legacy replay changed candidate order semantics", got)
			}
			req.KnowledgePointIDs = []string{"b", "a"}
			req.Limit = 1
			if batchRequestMatches(record, req, mode, batchRequestHash(req, mode)) {
				t.Fatal("ambiguous legacy limit replayed")
			}
		})
	}
}

func TestReportOriginalC2PlanCanReplayWithProvenOrder(t *testing.T) {
	req := &batchSubmitRequest{KnowledgePointIDs: []string{"b", "a"}, Count: 1}
	original := *req
	original.VersionID = "v"
	original.KnowledgePointIDs = []string{"a", "b"}
	plan := domain.BatchPlan{Version: 1, Mode: domain.BatchModePerPoint, VersionID: "v", RequestedTotal: 2, CandidateCount: 2, CountPerPoint: 1, Units: []domain.BatchPlanUnit{{Index: 0, PointSequence: 1, KnowledgePoint: domain.KnowledgePoint{ID: "b", VersionID: "v"}}, {Index: 1, PointSequence: 1, KnowledgePoint: domain.KnowledgePoint{ID: "a", VersionID: "v"}}}}
	raw, _ := json.Marshal(plan)
	record := &storage.BatchJobRecord{PlanJSON: string(raw), RequestHash: legacyRawBatchRequestHash(&original, domain.BatchModePerPoint)}
	if !batchRequestMatches(record, req, domain.BatchModePerPoint, batchRequestHash(req, domain.BatchModePerPoint)) {
		t.Fatal("proven original C2 plan could not replay")
	}
	req.KnowledgePointIDs = []string{"a", "b"}
	if batchRequestMatches(record, req, domain.BatchModePerPoint, batchRequestHash(req, domain.BatchModePerPoint)) {
		t.Fatal("changed per-point order replayed original plan")
	}
}

func TestBatchRequestFingerprintKeepsPersonalExperienceIntent(t *testing.T) {
	enabled, disabled := true, false
	base := batchSubmitRequest{Mode: domain.BatchModeRandomTotal, TotalCount: 2, Count: 1, KnowledgePointIDs: []string{"a"}, WorkflowMode: "direct"}
	hashes := map[string]string{}
	for _, tc := range []struct {
		name  string
		apply *bool
	}{{"omitted", nil}, {"enabled", &enabled}, {"disabled", &disabled}} {
		req := base
		req.ApplyPersonalExperiences = tc.apply
		hashes[tc.name] = batchRequestHash(&req, base.Mode)
	}
	for first, hash := range hashes {
		for second, other := range hashes {
			if first != second && hash == other {
				t.Errorf("different experience intent reused fingerprint: %s vs %s", first, second)
			}
		}
	}
	for _, tc := range []struct {
		name    string
		frozen  bool
		request *bool
		matches bool
	}{
		{"old_default_without_snapshot", false, nil, true},
		{"old_empty_cannot_prove_explicit_enabled", false, &enabled, false},
		{"old_empty_cannot_prove_explicit_disabled", false, &disabled, false},
		{"old_snapshot_default_replay", true, nil, true},
		{"old_snapshot_enabled_replay", true, &enabled, true},
		{"old_snapshot_reject_changed_disabled", true, &disabled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := base
			record := &storage.BatchJobRecord{OwnerID: "owner", RequestHash: legacyRawBatchRequestHash(&old, base.Mode)}
			if tc.frozen {
				raw, _ := json.Marshal(domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: "owner", Entries: []domain.PersonalExperienceUse{{ID: "card", Revision: 1}}})
				record.ExperienceSnapshotJSON = string(raw)
			}
			req := base
			req.ApplyPersonalExperiences = tc.request
			if got := batchRequestMatches(record, &req, base.Mode, batchRequestHash(&req, base.Mode)); got != tc.matches {
				t.Fatalf("legacy experience replay=%v want %v", got, tc.matches)
			}
		})
	}
}

func TestUnifiedKnowledgeReplaysOldV2ReceiptWithoutChangingExperienceIntent(t *testing.T) {
	for _, choice := range []bool{false, true} {
		t.Run(fmt.Sprint(choice), func(t *testing.T) {
			apply := false
			req := &batchSubmitRequest{Mode: domain.BatchModeRandomTotal, Count: 1, TotalCount: 2, WorkflowMode: "direct", VersionID: "v", KnowledgePointIDs: []string{"a", "b"}, ApplyPersonalExperiences: &apply}
			record := &storage.BatchJobRecord{RequestHash: legacyVersionedBatchIntentHash(legacyBatchIntentHash(req, req.Mode, &choice), &apply), PromptSnapshotJSON: `{"appendix":"already accepted frozen text"}`}
			if !batchRequestMatches(record, req, req.Mode, batchRequestHash(req, req.Mode)) {
				t.Fatal("accepted receipt did not replay after unification")
			}
			changed := true
			req.ApplyPersonalExperiences = &changed
			if batchRequestMatches(record, req, req.Mode, batchRequestHash(req, req.Mode)) {
				t.Fatal("experience choice changed while replaying old receipt")
			}
		})
	}
}
