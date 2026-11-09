package api

import (
	"aigo/internal/domain"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Historical receipt hashing only; no live preference API or prompt input.
func legacyBatchIntentHash(req *batchSubmitRequest, mode string, choice *bool) string {
	if req == nil {
		return ""
	}
	ids := append([]string{}, req.KnowledgePointIDs...)
	// Random sampling over a full set ignores order; prefix limiting and per-point
	// execution do not. Keep their order in the intent fingerprint.
	if mode == domain.BatchModeRandomTotal && req.Limit <= 0 {
		sort.Strings(ids)
	}
	payload := struct {
		Mode          string                           `json:"mode"`
		VersionID     string                           `json:"version_id"`
		PointIDs      []string                         `json:"point_ids"`
		OutlineCodes  string                           `json:"outline_codes"`
		Count         int                              `json:"count"`
		TotalCount    int                              `json:"total_count"`
		Limit         int                              `json:"limit"`
		SkipExisting  bool                             `json:"skip_existing"`
		FromCode      string                           `json:"from_code"`
		ToCode        string                           `json:"to_code"`
		WorkflowMode  string                           `json:"workflow_mode"`
		RetiredChoice *bool                            `json:"apply_personal_rules"`
		Presentation  *domain.PresentationRequirements `json:"presentation"`
		JobName       string                           `json:"job_name"`
	}{mode, req.VersionID, ids, req.OutlineCodes, req.Count, req.TotalCount, req.Limit, req.SkipExisting, req.FromCode, req.ToCode, req.WorkflowMode, choice, req.Presentation, req.JobName}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func legacyRawBatchRequestHash(req *batchSubmitRequest, mode string) string {
	enabled := true
	return legacyBatchIntentHash(req, mode, &enabled)
}
func legacyVersionedBatchIntentHash(intent string, apply *bool) string {
	raw, _ := json.Marshal(struct {
		Intent string `json:"intent"`
		Apply  *bool  `json:"apply_personal_experiences"`
	}{intent, apply})
	sum := sha256.Sum256(raw)
	return "batch-intent-v2:" + hex.EncodeToString(sum[:])
}
