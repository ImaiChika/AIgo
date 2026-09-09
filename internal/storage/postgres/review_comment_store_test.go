package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"aigo/internal/domain"

	"github.com/lib/pq"
)

// TestReviewRecordStructuredCommentRoundTrip 验证结构化评语在真实 PostgreSQL 中的存取：
// 新记录的 comment JSONB 与 expert_name 快照完整回读；历史风格记录（comment 为 NULL）回读为空。
func TestReviewRecordStructuredCommentRoundTrip(t *testing.T) {
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run PostgreSQL review comment integration test")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()

	schemaName := fmt.Sprintf("aigo_review_comment_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		t.Fatal(err)
	}
	defer adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)

	testDSN, err := dsnWithSearchPath(dsn, schemaName)
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if err := store.CheckSchemaVersion(ctx); err != nil {
		t.Fatalf("schema version check failed: %v", err)
	}

	now := time.Now()
	question := domain.A2Question{
		ID: "q-comment", ClinicalStem: "患者，男，60岁，进行性吞咽困难3个月……",
		Options: []domain.Option{{Label: "A", Text: "食管癌"}, {Label: "B", Text: "贲门失弛缓症"}, {Label: "C", Text: "食管良性狭窄"}, {Label: "D", Text: "食管静脉曲张"}},
		Answer:  "A", Difficulty: domain.DifficultyMedium, Status: domain.StatusAIDraft,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveQuestion(ctx, question); err != nil {
		t.Fatal(err)
	}
	flow := domain.ReviewFlowConfig{
		ID: "flow-comment", Name: "评语回环流程",
		Rounds: []domain.RoundConfig{{RoundNumber: 1, Name: "初审", ExpertIDs: []string{"r1"}}}, CreatedAt: now,
	}
	if err := store.SaveFlowConfig(ctx, flow); err != nil {
		t.Fatal(err)
	}
	task := domain.ReviewTask{
		ID: "task-comment", QuestionID: question.ID, FlowID: flow.ID, CurrentRound: 1,
		Status: domain.StatusReviewing, AssignedTo: []string{"r1", "r2"}, QuestionPrevStatus: domain.StatusAIDraft,
		QuestionVersion: 1, RoundResults: []domain.RoundResult{{RoundNumber: 1}}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	// 新风格记录：结构化评语 + 专家名快照
	comment := &domain.ReviewComment{
		Stem:   "题干信息完整",
		Answer: "答案 A 正确，但解析缺少分级依据",
	}
	recordNew := domain.ReviewRecord{
		ID: "rec-comment-new", TaskID: task.ID, QuestionID: question.ID, RoundNumber: 1,
		ExpertID: "r1", ExpertName: "张老师", Conclusion: domain.StatusRevisionRequired,
		Opinion:   comment.Flatten(),
		Comment:   comment,
		CreatedAt: now,
	}
	if err := store.SaveRecord(ctx, recordNew); err != nil {
		t.Fatal(err)
	}

	// 旧风格记录：无结构化评语（模拟迁移前历史数据）
	recordLegacy := domain.ReviewRecord{
		ID: "rec-comment-legacy", TaskID: task.ID, QuestionID: question.ID, RoundNumber: 1,
		ExpertID: "r2", Conclusion: domain.StatusApproved, Opinion: "同意", CreatedAt: now.Add(time.Second),
	}
	if err := store.SaveRecord(ctx, recordLegacy); err != nil {
		t.Fatal(err)
	}

	records, err := store.ListRecordsByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	gotNew := records[0]
	if gotNew.Comment == nil || gotNew.Comment.Stem != "题干信息完整" || gotNew.Comment.Options != "" || gotNew.Comment.Answer != "答案 A 正确，但解析缺少分级依据" {
		t.Fatalf("structured comment round-trip failed: %+v", gotNew)
	}
	if gotNew.ExpertName != "张老师" {
		t.Fatalf("expert_name = %q, want 张老师", gotNew.ExpertName)
	}
	gotLegacy := records[1]
	if gotLegacy.Comment != nil {
		t.Fatalf("legacy record should have nil comment, got %+v", gotLegacy.Comment)
	}
	// 汇总接口按任务批量读回
	batch, err := store.ListRecordsByTaskIDs(ctx, []string{task.ID})
	if err != nil || len(batch) != 2 {
		t.Fatalf("ListRecordsByTaskIDs = %d, %v", len(batch), err)
	}
}
