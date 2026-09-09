package batch

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"aigo/internal/storage/testutil"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
		t.Fatal("versioned custom_id must be globally unique")
	}
	snapshot, _ := json.Marshal([]domain.KnowledgePoint{point})
	jobStore := &snapshotJobStore{job: storage.BatchJobRecord{ID: "job", PointsJSON: string(snapshot)}}
	question := `[{"clinical_stem":"男，45岁。反复上腹痛2年，加重1天。查体：T 36.8℃，P 80次/分，R 18次/分，BP 120/80mmHg，腹软，上腹部压痛。该患者最可能的诊断是","options":[{"label":"A","text":"胃溃疡"},{"label":"B","text":"十二指肠溃疡"},{"label":"C","text":"胃癌"},{"label":"D","text":"慢性胃炎"},{"label":"E","text":"功能性消化不良"}],"answer":"A","explanation":"正确答案为 A。该患者反复上腹痛，结合疼痛特点考虑胃溃疡。B 项疼痛规律更符合十二指肠溃疡；C 项缺乏消瘦等警示表现；D 项不能完整解释典型节律性疼痛；E 项应在排除器质性疾病后考虑。故选 A。","difficulty":"0.65","cognitive_level":"简单应用","exam_points":"诊断与鉴别诊断，临床表现"}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/batches/job" {
			json.NewEncoder(w).Encode(map[string]any{"id": "job", "status": "completed", "output_file_id": "output"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"custom_id": point.ID, "response": map[string]any{"status_code": 200, "body": map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": question}}}}}})
	}))
	defer server.Close()
	qs := testutil.NewMemoryStore()
	service := NewDashScopeService(DashScopeConfig{APIKey: "test", BaseURL: server.URL, Model: "test-model"}, qs, jobStore)
	latest := point
	latest.VersionID = "2026"
	latest.Topic = "另一年度的同码考点"
	result, err := service.ImportResults(context.Background(), "job", []domain.KnowledgePoint{latest})
	if err != nil || result.Saved != 1 {
		t.Fatalf("import failed: %+v %v", result, err)
	}
	questions, _ := qs.ListQuestions(context.Background())
	got := questions[0].KnowledgePoints[0]
	if got.VersionID != "2025" || got.Topic != "提交时考点" || got.Revision != 1 {
		t.Fatalf("snapshot was replaced by live data: %+v", got)
	}
	jobStore.job.PointsJSON = "[]"
	// 重复导入：已导入任务直接重放上次结果，不重复入库
	replay, err := service.ImportResults(context.Background(), "job", nil)
	if err != nil || replay.Saved != 1 {
		t.Fatalf("replay failed: %+v %v", replay, err)
	}
	questions2, _ := qs.ListQuestions(context.Background())
	if len(questions2) != 1 {
		t.Fatalf("re-import duplicated questions: %d", len(questions2))
	}
	// 从未导入且无知识点快照的任务：报错并释放导入标记（允许修复后重试）
	jobStore.job = storage.BatchJobRecord{ID: "job"}
	if _, err := service.ImportResults(context.Background(), "job", nil); err == nil {
		t.Fatal("missing snapshot silently accepted")
	}
	if jobStore.job.ImportedAt != "" {
		t.Fatal("failed import must release the import claim for retry")
	}
}
