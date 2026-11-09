package api

import (
	"context"
	"encoding/json"
	"sort"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

// Serialize preparation for one user/key inside the serving instance. Waiters
// hold no DB connection; the storage unique index remains the cross-process
// admission guard. Entries are removed after the last holder/waiter leaves.
type batchSubmissionGate struct {
	token chan struct{}
	users int
}

// C2's original fingerprint sorted every ID list and pinned an omitted version
// before hashing. Recover those accepted v1 jobs only when their stored plan
// proves the requested order/set; never reinterpret an ambiguous limited pool.
func batchRequestMatches(record *storage.BatchJobRecord, req *batchSubmitRequest, mode, hash string) bool {
	if record.RequestHash == hash {
		return true
	}
	if batchLegacyWireAliasMatches(record, req, mode, hash) {
		return true
	}
	var plan domain.BatchPlan
	if json.Unmarshal([]byte(record.PlanJSON), &plan) != nil || len(plan.Candidates) > 0 || req.Limit > 0 {
		return false
	}
	legacy := *req
	if legacy.VersionID == "" {
		legacy.VersionID = plan.VersionID
	}
	if mode == domain.BatchModePerPoint {
		points := plan.UniquePlanPoints()
		if len(points) != len(req.KnowledgePointIDs) {
			return false
		}
		for i, p := range points {
			if p.ID != req.KnowledgePointIDs[i] {
				return false
			}
		}
	}
	legacy.KnowledgePointIDs = append([]string(nil), req.KnowledgePointIDs...)
	sort.Strings(legacy.KnowledgePointIDs)
	return batchRequestHash(&legacy, mode) == record.RequestHash || batchLegacyWireAliasMatches(record, &legacy, mode, batchRequestHash(&legacy, mode))
}

func (s *Server) acquireBatchSubmission(ctx context.Context, owner, key string) (func(), error) {
	if key == "" {
		return func() {}, nil
	}
	raw, _ := json.Marshal([]string{owner, key})
	identity := string(raw)
	s.batchSubmissionMu.Lock()
	if s.batchSubmissions == nil {
		s.batchSubmissions = map[string]*batchSubmissionGate{}
	}
	g := s.batchSubmissions[identity]
	if g == nil {
		g = &batchSubmissionGate{token: make(chan struct{}, 1)}
		s.batchSubmissions[identity] = g
	}
	g.users++
	s.batchSubmissionMu.Unlock()
	leave := func() {
		s.batchSubmissionMu.Lock()
		g.users--
		if g.users == 0 {
			delete(s.batchSubmissions, identity)
		}
		s.batchSubmissionMu.Unlock()
	}
	select {
	case g.token <- struct{}{}:
		return func() { <-g.token; leave() }, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}

func (s *Server) batchRequestRecord(ctx context.Context, owner, key string) (*storage.BatchJobRecord, error) {
	if store, ok := s.questionStore.(storage.BatchRequestKeyStore); ok {
		return store.GetBatchJobByRequestKey(ctx, owner, key)
	}
	if s.batchSvc != nil {
		return s.batchSvc.GetBatchJobByRequestKey(ctx, owner, key)
	}
	return nil, nil
}

// Old hashes encoded wire defaults and duplicate IDs. Only try aliases which
// normalize to this exact intent; a changed count or ordered/limited
// pool never reuses the accepted plan. Frozen selection may prove old raw IDs.
func batchLegacyWireAliasMatches(record *storage.BatchJobRecord, req *batchSubmitRequest, mode, hash string) bool {
	ids := [][]string{req.KnowledgePointIDs}
	var plan domain.BatchPlan
	if json.Unmarshal([]byte(record.PlanJSON), &plan) == nil && plan.Selection != nil {
		ids = append(ids, plan.Selection.KnowledgePointIDs)
	}
	yes, no := true, false
	choices := []*bool{nil, &yes, &no}
	workflows := []string{req.WorkflowMode}
	if req.WorkflowMode == "" || req.WorkflowMode == "direct" {
		workflows = []string{"", "direct"}
	}
	for _, points := range ids {
		for _, workflow := range workflows {
			candidate := *req
			candidate.KnowledgePointIDs = points
			candidate.WorkflowMode = workflow
			if batchRequestHash(&candidate, mode) != hash {
				continue
			}
			for _, choice := range choices {
				old := legacyBatchIntentHash(&candidate, mode, choice)
				if record.RequestHash == legacyVersionedBatchIntentHash(old, req.ApplyPersonalExperiences) {
					return true
				}
				if legacyBatchExperienceIntentMatches(record, req) && record.RequestHash == old {
					return true
				}
			}
		}
	}
	return false
}

// Old fingerprints cannot distinguish experience flags. Preserve the original
// omitted default without consulting live permissions/cards. An explicit true
// can only replay when the persisted snapshot proves adoption was enabled; an
// explicit false has no historical intent evidence and must use a new key.
func legacyBatchExperienceIntentMatches(record *storage.BatchJobRecord, req *batchSubmitRequest) bool {
	if req.ApplyPersonalExperiences == nil {
		return true
	}
	if !*req.ApplyPersonalExperiences || record.ExperienceSnapshotJSON == "" {
		return false
	}
	var snapshot domain.PersonalExperienceSnapshot
	return json.Unmarshal([]byte(record.ExperienceSnapshotJSON), &snapshot) == nil &&
		snapshot.Version == domain.ExperienceProtocolVersion && snapshot.OwnerID != "" &&
		snapshot.OwnerID == record.OwnerID && len(snapshot.Entries) > 0
}
