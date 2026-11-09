package domain

import (
	"encoding/json"
	"testing"
)

func validRandomBatchPlan() *BatchPlan {
	a := KnowledgePoint{ID: "a", VersionID: "v1", OutlineCode: "1", Revision: 1, Topic: "甲"}
	b := KnowledgePoint{ID: "b", VersionID: "v1", OutlineCode: "2", Revision: 1, Topic: "乙"}
	return &BatchPlan{Version: 1, Mode: BatchModeRandomTotal, RequestedTotal: 3, VersionID: "v1", CandidateCount: 2,
		Units: []BatchPlanUnit{{Index: 0, KnowledgePoint: a, PointSequence: 1}, {Index: 1, KnowledgePoint: b, PointSequence: 1}, {Index: 2, KnowledgePoint: a, PointSequence: 2}}}
}

func TestBatchPlanValidateRejectsInconsistentUnits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*BatchPlan)
	}{
		{name: "repeated occurrence number", edit: func(p *BatchPlan) { p.Units[2].PointSequence = 1 }},
		{name: "skipped occurrence number", edit: func(p *BatchPlan) { p.Units[2].PointSequence = 3 }},
		{name: "same identity changed snapshot", edit: func(p *BatchPlan) { p.Units[2].KnowledgePoint.Topic = "新内容" }},
		{name: "unit belongs to another version", edit: func(p *BatchPlan) { p.Units[1].KnowledgePoint.VersionID = "v2" }},
		{name: "more identities than candidates", edit: func(p *BatchPlan) { p.CandidateCount = 1 }},
		{name: "per point does not cover each candidate", edit: func(p *BatchPlan) {
			p.Mode, p.CountPerPoint, p.CandidateCount = BatchModePerPoint, 3, 1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := validRandomBatchPlan()
			tt.edit(plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("inconsistent persisted plan was accepted")
			}
		})
	}
}

func TestBatchPlanValidateAcceptsLegacyV1WithoutCandidateMetadata(t *testing.T) {
	t.Parallel()
	plan := validRandomBatchPlan()
	if err := plan.Validate(); err != nil {
		t.Fatalf("accepted v1 plans remain readable: %v", err)
	}
}

func TestBatchPlanUniquePointsUsesVersionAndCodeWhenIDMissing(t *testing.T) {
	t.Parallel()
	plan := &BatchPlan{Units: []BatchPlanUnit{
		{KnowledgePoint: KnowledgePoint{VersionID: "v1", OutlineCode: "1"}},
		{KnowledgePoint: KnowledgePoint{VersionID: "v1", OutlineCode: "2"}},
		{KnowledgePoint: KnowledgePoint{VersionID: "v2", OutlineCode: "1"}},
		{KnowledgePoint: KnowledgePoint{VersionID: "v1", OutlineCode: "1"}},
	}}
	if got := plan.UniquePlanPoints(); len(got) != 3 {
		t.Fatalf("code-only points must keep distinct version/code identities: %+v", got)
	}
}

func TestBatchPlanRoundTripPreservesCandidateSelection(t *testing.T) {
	t.Parallel()
	const raw = `{"version":1,"mode":"random_total","requested_total":1,"version_id":"v1","candidate_count":2,"candidates":[{"id":"a","version_id":"v1","outline_code":"1","revision":2},{"id":"b","version_id":"v1","outline_code":"2","revision":3}],"selection":{"version_id":"v1","knowledge_point_ids":["a","b"],"limit":2,"skip_existing":true,"from_code":"1","to_code":"2"},"units":[{"index":0,"knowledge_point":{"id":"a","version_id":"v1","outline_code":"1","revision":2},"point_sequence":1,"target_offset":0}]}`
	var plan BatchPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var preserved map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &preserved); err != nil {
		t.Fatal(err)
	}
	if len(preserved["candidates"]) == 0 || len(preserved["selection"]) == 0 {
		t.Fatalf("candidate pool and selection were lost on persisted plan roundtrip: %s", encoded)
	}
}

func TestBatchPlanValidateRejectsWrongFrozenTargetOffset(t *testing.T) {
	t.Parallel()
	const raw = `{"version":1,"mode":"random_total","requested_total":1,"version_id":"v1","candidate_count":1,"units":[{"index":0,"knowledge_point":{"id":"a","version_id":"v1"},"point_sequence":1,"target_offset":2}]}`
	var plan BatchPlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err == nil {
		t.Fatal("frozen offset must match the per-point occurrence sequence")
	}
}

func TestBatchPlanValidateRejectsCorruptCandidateMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		edit func(*BatchPlan)
	}{
		{name: "candidate removed", edit: func(p *BatchPlan) { p.Candidates = p.Candidates[:1] }},
		{name: "candidate identity duplicated", edit: func(p *BatchPlan) { p.Candidates[1] = p.Candidates[0] }},
		{name: "candidate belongs to another version", edit: func(p *BatchPlan) { p.Candidates[1].VersionID = "v2" }},
		{name: "unit revision no longer belongs to candidate", edit: func(p *BatchPlan) { p.Candidates[1].Revision++ }},
		{name: "unit not in candidate pool", edit: func(p *BatchPlan) { p.Candidates[1].ID = "c" }},
		{name: "new unit lost target offset", edit: func(p *BatchPlan) { p.Units[1].TargetOffset = nil }},
		{name: "selection version changed", edit: func(p *BatchPlan) { p.Selection.VersionID = "v2" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := validRandomBatchPlan()
			plan.Selection = &BatchPlanSelection{VersionID: "v1"}
			plan.Candidates = []BatchPlanCandidate{{ID: "a", VersionID: "v1", OutlineCode: "1", Revision: 1}, {ID: "b", VersionID: "v1", OutlineCode: "2", Revision: 1}}
			for i := range plan.Units {
				offset := plan.Units[i].PointSequence - 1
				plan.Units[i].TargetOffset = &offset
			}
			tt.edit(plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("corrupt candidate metadata was accepted")
			}
		})
	}
}

func TestBatchPlanValidateRejectsUnequalPerPointCounts(t *testing.T) {
	t.Parallel()
	plan := validRandomBatchPlan()
	plan.Mode, plan.CountPerPoint, plan.RequestedTotal = BatchModePerPoint, 2, 4
	plan.Units = append(plan.Units, BatchPlanUnit{Index: 3, KnowledgePoint: plan.Units[0].KnowledgePoint, PointSequence: 3})
	if err := plan.Validate(); err == nil {
		t.Fatal("per-point plan with 3 questions for A and 1 for B was accepted")
	}
}
