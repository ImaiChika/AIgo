package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeWorkflowSnapshotSelectionFreezeAndPermission(t *testing.T) {
	s, ctx, svc, p := seedKnowledgeBase(t)
	medical, e := svc.Save(ctx, "kb-user", "kb-role", "", testKnowledgeInput(p))
	if e != nil {
		t.Fatal(e)
	}
	active, e := svc.Transition(ctx, "kb-user", "kb-role", medical.ID, "activate", medical.Revision)
	if e != nil {
		t.Fatal(e)
	}
	draft := testKnowledgeInput(p)
	draft.Title = "不能选入的草稿"
	if _, e = svc.Save(ctx, "kb-user", "kb-role", "", draft); e != nil {
		t.Fatal(e)
	}
	custom := domain.KnowledgeCard{Kind: "custom", Title: "用户自定义", KnowledgePoints: []domain.KnowledgePoint{p}, Content: domain.KnowledgeContent{CustomText: "自定义参考，不作为医学事实"}}
	c, e := svc.Save(ctx, "kb-user", "kb-role", "", custom)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Transition(ctx, "kb-user", "kb-role", c.ID, "activate", c.Revision); e != nil {
		t.Fatal(e)
	}
	var frozen *domain.KnowledgeWorkflowSnapshot
	e = svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", []domain.KnowledgePoint{p}, func(_ context.Context, value *domain.KnowledgeWorkflowSnapshot) error { frozen = value; return nil })
	if e != nil {
		t.Fatal(e)
	}
	if len(frozen.Bundles) != 1 || len(frozen.Bundles[0].Cards) != 2 {
		t.Fatal(frozen)
	}
	raw, _ := json.Marshal(frozen)
	if strings.Contains(string(raw), draft.Title) {
		t.Fatal("draft selected")
	}
	active.Content.Facts[0].Text = "后来修改的事实"
	if _, e = svc.Save(ctx, "kb-user", "kb-role", active.ID, active); e != nil {
		t.Fatal(e)
	}
	rawAfter, _ := json.Marshal(frozen)
	if string(rawAfter) != string(raw) {
		t.Fatal("queued snapshot was mutated")
	}
	if e = svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", []domain.KnowledgePoint{p}, func(_ context.Context, value *domain.KnowledgeWorkflowSnapshot) error {
		if len(value.Bundles) != 1 || len(value.Bundles[0].Cards) != 1 || value.Bundles[0].Cards[0].ID != c.ID {
			t.Fatal("custom-only references lost", value)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`UPDATE roles SET permissions=ARRAY['kb:view','kb:manage'] WHERE id='kb-role'`); e != nil {
		t.Fatal(e)
	}
	if e = svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", []domain.KnowledgePoint{p}, func(context.Context, *domain.KnowledgeWorkflowSnapshot) error {
		t.Fatal("revoked use proceeded")
		return nil
	}); !errors.Is(e, knowledgebase.ErrDenied) {
		t.Fatal(e)
	}
}
func TestQuestionKnowledgeContextAtomicAndSurvivesHumanEdit(t *testing.T) {
	s, ctx, _, p := seedKnowledgeBase(t)
	q := aiCheckTestQuestion(t, s, ctx, "context-fixture")
	if e := s.DeleteQuestion(ctx, q.ID); e != nil {
		t.Fatal(e)
	}
	q.ID = "context-question"
	card := testKnowledgeInput(p)
	card.ID = "frozen-card"
	card.Revision = 2
	card.Status = "active"
	target := knowledgebase.Targets(domain.KnowledgeBundle{Cards: []domain.KnowledgeCard{card}})[0]
	q.GenerationContext = &domain.QuestionGenerationContext{WorkflowVersion: knowledgebase.LegacyWorkflowVersion, CapturedAt: time.Now(), Bundle: domain.KnowledgeBundle{KnowledgePointID: p.ID, Cards: []domain.KnowledgeCard{card}}, Target: target, Basis: domain.KnowledgeBasis{Target: target.Target, FactRefs: []string{knowledgebase.FactRef(card, card.Content.Facts[0])}, ConditionNotes: "仅测试", AnswerSupport: "测试依据"}}
	if _, e := s.db.Exec(`CREATE FUNCTION reject_generation_context() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test context persistence failure'; END $$;CREATE TRIGGER reject_generation_context BEFORE INSERT ON question_generation_context FOR EACH ROW EXECUTE FUNCTION reject_generation_context()`); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveQuestion(ctx, q); e == nil {
		t.Fatal("context failure ignored")
	}
	if saved, e := s.GetQuestion(ctx, q.ID); e != nil || saved != nil {
		t.Fatal("question committed without context", saved, e)
	}
	if _, e := s.db.Exec(`DROP TRIGGER reject_generation_context ON question_generation_context`); e != nil {
		t.Fatal(e)
	}
	if e := s.SaveQuestion(ctx, q); e != nil {
		t.Fatal(e)
	}
	saved, e := s.GetQuestion(ctx, q.ID)
	if e != nil || saved.WorkflowVersion != knowledgebase.LegacyWorkflowVersion {
		t.Fatal(saved, e)
	}
	before, e := s.GetQuestionGenerationContext(ctx, q.ID)
	if e != nil {
		t.Fatal(e)
	}
	beforeJSON, _ := json.Marshal(before)
	saved.ClinicalStem += " "
	saved.Version++
	saved.WorkflowVersion = "forged"
	if e = s.SaveQuestion(ctx, *saved); e != nil {
		t.Fatal(e)
	}
	after, e := s.GetQuestionGenerationContext(ctx, q.ID)
	if e != nil {
		t.Fatal(e)
	}
	afterJSON, _ := json.Marshal(after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("human edit replaced provenance")
	}
	saved, e = s.GetQuestion(ctx, q.ID)
	if e != nil || saved.WorkflowVersion != knowledgebase.LegacyWorkflowVersion {
		t.Fatal("workflow marker changed", saved, e)
	}
	if e = s.DeleteQuestion(ctx, q.ID); e != nil {
		t.Fatal(e)
	}
	if archive, e := s.GetQuestionGenerationContext(ctx, q.ID); e != nil || archive == nil {
		t.Fatal("discard erased provenance", e)
	}
}

func TestKnowledgeWorkflowBoundedSelectionAndDeletedVersionIsolation(t *testing.T) {
	s, ctx, svc, p := seedKnowledgeBase(t)
	for i := 0; i < 5; i++ {
		card := testKnowledgeInput(p)
		card.Title = fmt.Sprintf("目标卡%d", i)
		saved, e := svc.Save(ctx, "kb-user", "kb-role", "", card)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = svc.Transition(ctx, "kb-user", "kb-role", saved.ID, "activate", saved.Revision); e != nil {
			t.Fatal(e)
		}
	}
	var snapshot *domain.KnowledgeWorkflowSnapshot
	e := svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", []domain.KnowledgePoint{p}, func(_ context.Context, x *domain.KnowledgeWorkflowSnapshot) error { snapshot = x; return nil })
	if e != nil {
		t.Fatal(e)
	}
	if len(snapshot.Bundles[0].Cards) != 4 || snapshot.Bundles[0].ExcludedCards != 1 {
		t.Fatal(snapshot)
	}

	updated, err := s.GetPoint(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated.Topic = "修改后的考点"
	if err = s.UpdatePoint(ctx, *updated); err != nil {
		t.Fatal(err)
	}
	if e = svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", []domain.KnowledgePoint{p}, func(_ context.Context, value *domain.KnowledgeWorkflowSnapshot) error {
		if len(value.Bundles) != 1 || len(value.Bundles[0].Cards) != 0 {
			t.Fatal("stale mapping reused", value)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`UPDATE knowledge_versions SET deleted_at=NOW() WHERE id=$1`, p.VersionID); e != nil {
		t.Fatal(e)
	}
	if e = svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", []domain.KnowledgePoint{p}, func(context.Context, *domain.KnowledgeWorkflowSnapshot) error {
		t.Fatal("deleted syllabus reused")
		return nil
	}); !errors.Is(e, knowledgebase.ErrInvalid) {
		t.Fatal(e)
	}
	if _, e = knowledgebase.BundleFor(snapshot, p.ID); e != nil {
		t.Fatal("already submitted snapshot lost after syllabus deletion", e)
	}
}
func TestKnowledgeWorkflowSkipsOversizedPrimaryMaterialWithoutBlockingGeneration(t *testing.T) {
	_, ctx, svc, p := seedKnowledgeBase(t)
	card := testKnowledgeInput(p)
	card.Content.Facts = nil
	for i := 0; i < 15; i++ {
		card.Content.Facts = append(card.Content.Facts, domain.KnowledgeFact{ID: fmt.Sprint(i), Text: strings.Repeat("测", 900)})
	}
	saved, e := svc.Save(ctx, "kb-user", "kb-role", "", card)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Transition(ctx, "kb-user", "kb-role", saved.ID, "activate", saved.Revision); e != nil {
		t.Fatal(e)
	}
	if e = svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", []domain.KnowledgePoint{p}, func(_ context.Context, value *domain.KnowledgeWorkflowSnapshot) error {
		if len(value.Bundles) != 1 || len(value.Bundles[0].Cards) != 0 || value.Bundles[0].ExcludedCards != 1 {
			t.Fatal("over-budget material entered prompt", value)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
