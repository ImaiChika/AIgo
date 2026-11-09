package batch

import (
	"context"
	"encoding/json"
	"testing"

	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
)

type snapshotJobStore struct{ job storage.BatchJobRecord }

func (s *snapshotJobStore) SaveBatchJob(_ context.Context, j storage.BatchJobRecord) error {
	s.job = j
	return nil
}
func (s *snapshotJobStore) UpdateBatchJob(_ context.Context, j storage.BatchJobRecord) error {
	s.job.Status = j.Status
	return nil
}
func (s *snapshotJobStore) GetBatchJob(context.Context, string) (*storage.BatchJobRecord, error) {
	copy := s.job
	return &copy, nil
}
func (s *snapshotJobStore) ListBatchJobs(context.Context, int) ([]storage.BatchJobRecord, error) {
	return nil, nil
}
func (s *snapshotJobStore) SearchBatchJobs(context.Context, string, int) ([]storage.BatchJobRecord, error) {
	return nil, nil
}
func (s *snapshotJobStore) ClaimBatchJobImport(context.Context, string) (bool, error) {
	if s.job.ImportedAt != "" {
		return false, nil
	}
	s.job.ImportedAt = "now"
	return true, nil
}
func (s *snapshotJobStore) SaveBatchJobImportResult(_ context.Context, _ string, resultJSON string) error {
	s.job.ImportResult = resultJSON
	return nil
}
func (s *snapshotJobStore) ReleaseBatchJobImport(context.Context, string) error {
	s.job.ImportedAt = ""
	return nil
}

func TestBatchImportUsesSubmissionSyllabusSnapshot(t *testing.T) {
	point := domain.KnowledgePoint{ID: "kp-original", VersionID: "2025", VersionName: "2025大纲", VersionYear: 2025, Revision: 1, OutlineCode: "001", Category: "临床综合", Subject: "消化系统", Topic: "提交时考点"}
	if batchPointID(point) != point.ID {
		t.Fatal("versioned knowledge point id must be globally unique")
	}
	question := domain.A2Question{
		ID:           "q-001-test",
		Version:      1,
		ClinicalStem: "男，45岁。反复上腹痛2年。该患者最可能的诊断是",
		Options: []domain.Option{
			{Label: "A", Text: "胃溃疡"},
			{Label: "B", Text: "十二指肠溃疡"},
		},
		Answer:         "A",
		Explanation:    "正确答案为 A。该患者反复上腹痛，结合疼痛特点考虑胃溃疡。",
		Difficulty:     "0.65",
		CognitiveLevel: "应用",
		KnowledgePoints: []domain.KnowledgePoint{point},
	}
	output, _ := json.Marshal(localBatchOutput{Questions: []domain.A2Question{question}})
	jobStore := &snapshotJobStore{job: storage.BatchJobRecord{
		ID: "job", Backend: "local_single_api", Status: "completed", OutputJSON: string(output),
	}}
	qs := testutil.NewMemoryStore()
	executor := &LocalExecutor{questionStore: qs, batchJobStore: jobStore}
	// 提交后大纲被修订：导入必须沿用提交时快照，而非当前版本数据
	latest := point
	latest.VersionID = "2026"
	latest.Topic = "另一年度的同码考点"
	result, err := executor.ImportResults(context.Background(), "job", []domain.KnowledgePoint{latest})
	if err != nil || result.Saved != 1 {
		t.Fatalf("import failed: %+v %v", result, err)
	}
	questions, _ := qs.ListQuestions(context.Background())
	got := questions[0].KnowledgePoints[0]
	if got.VersionID != "2025" || got.Topic != "提交时考点" || got.Revision != 1 {
		t.Fatalf("snapshot was replaced by live data: %+v", got)
	}
	// 重复导入：已导入任务直接重放上次结果，不重复入库
	replay, err := executor.ImportResults(context.Background(), "job", nil)
	if err != nil || replay.Saved != 1 {
		t.Fatalf("replay failed: %+v %v", replay, err)
	}
	questions2, _ := qs.ListQuestions(context.Background())
	if len(questions2) != 1 {
		t.Fatalf("re-import duplicated questions: %d", len(questions2))
	}
}
