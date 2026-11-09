package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func seedKnowledgeBase(t *testing.T) (*Store, context.Context, *knowledgebase.Service, domain.KnowledgePoint) {
	t.Helper()
	s, ctx := newAICheckOutcomeTestStore(t)
	if _, e := s.db.Exec(`INSERT INTO roles(id,name,permissions) VALUES('kb-role','知识库岗位',ARRAY['kb:view','kb:manage','kb:use']);INSERT INTO users(id,username,password_hash,role,roles,enabled) VALUES('kb-user','kb-user','not-a-password','kb-role',ARRAY['kb-role'],true)`); e != nil {
		t.Fatal(e)
	}
	v := domain.KnowledgeVersion{ID: "kb-v", Name: "测试大纲", Year: 2026, CreatedAt: time.Now()}
	if e := s.CreateKnowledgeVersion(ctx, v); e != nil {
		t.Fatal(e)
	}
	p := domain.KnowledgePoint{ID: "kb-point", VersionID: v.ID, Topic: "仅测试", OutlineCode: "001"}
	if e := s.UpdatePoint(ctx, p); e != nil {
		t.Fatal(e)
	}
	return s, ctx, &knowledgebase.Service{Store: s}, p
}
func testKnowledgeInput(p domain.KnowledgePoint) domain.KnowledgeCard {
	return domain.KnowledgeCard{Kind: "medical", Title: "知识卡", Source: "测试教材", KnowledgePoints: []domain.KnowledgePoint{p}, Content: domain.KnowledgeContent{ExamTargets: []string{"目标"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "测试事实"}}}}
}
func TestKnowledgeCardsVersionConflictRevokeAndArchive(t *testing.T) {
	s, ctx, svc, p := seedKnowledgeBase(t)
	card, e := svc.Save(ctx, "kb-user", "kb-role", "", testKnowledgeInput(p))
	if e != nil {
		t.Fatal(e)
	}
	if card.Revision != 1 || card.Status != "draft" || card.KnowledgePoints[0].VersionName != "测试大纲" {
		t.Fatal(card)
	}
	active, e := svc.Transition(ctx, "kb-user", "kb-role", card.ID, "activate", 1)
	if e != nil || active.Revision != 2 || active.Status != "active" {
		t.Fatal(active, e)
	}
	if _, e = svc.Save(ctx, "kb-user", "kb-role", card.ID, card); !errors.Is(e, knowledgebase.ErrConflict) {
		t.Fatalf("stale update: %v", e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := active
			copy.Title = fmt.Sprintf("edit %d", i)
			_, err := svc.Save(ctx, "kb-user", "kb-role", card.ID, copy)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, knowledgebase.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatal(ok, conflicts)
	}
	current, _ := s.GetKnowledgeCard(ctx, card.ID)
	if current.Status != "draft" {
		t.Fatal("edit remained active")
	}
	history, e := svc.Detail(ctx, "kb-user", "kb-role", card.ID, true)
	if e != nil || len(history) != 3 || history[1].Title != "知识卡" {
		t.Fatal(history, e)
	}
	if _, e = s.db.Exec(`UPDATE roles SET permissions=ARRAY['kb:view','kb:use'] WHERE id='kb-role'`); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Save(ctx, "kb-user", "kb-role", card.ID, *current); !errors.Is(e, knowledgebase.ErrDenied) {
		t.Fatal("stale token write accepted", e)
	}
	if _, _, caps, e := svc.List(ctx, "kb-user", "kb-role", knowledgebase.Filter{}); e != nil || caps.Manage {
		t.Fatal(caps, e)
	}
	if _, e = s.db.Exec(`UPDATE users SET permissions=ARRAY['kb:manage'],roles=ARRAY[]::text[],role='' WHERE id='kb-user'`); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = svc.List(ctx, "kb-user", "kb-role", knowledgebase.Filter{}); !errors.Is(e, knowledgebase.ErrDenied) {
		t.Fatal("unmounted role accepted", e)
	}
	if _, _, _, e = svc.List(ctx, "kb-user", "", knowledgebase.Filter{}); !errors.Is(e, knowledgebase.ErrDenied) {
		t.Fatal("manage without view accepted", e)
	}
	if _, e = s.db.Exec(`UPDATE users SET role='kb-role',roles=ARRAY['kb-role'],permissions=ARRAY[]::text[] WHERE id='kb-user';UPDATE roles SET permissions=ARRAY['kb:view','kb:manage'] WHERE id='kb-role'`); e != nil {
		t.Fatal(e)
	}
	archived, e := svc.Transition(ctx, "kb-user", "kb-role", card.ID, "archive", current.Revision)
	if e != nil || archived.Status != "archived" {
		t.Fatal(archived, e)
	}
	if _, e = svc.Save(ctx, "kb-user", "kb-role", card.ID, archived); !errors.Is(e, knowledgebase.ErrInvalid) {
		t.Fatal(e)
	}
	var logs int
	if e = s.db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action LIKE 'knowledge_card_%'`).Scan(&logs); e != nil || logs != 4 {
		t.Fatal(logs, e)
	}
}
func TestKnowledgeCardsDeletedSyllabusCannotBeActivated(t *testing.T) {
	s, ctx, svc, p := seedKnowledgeBase(t)
	card, e := svc.Save(ctx, "kb-user", "kb-role", "", testKnowledgeInput(p))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`UPDATE knowledge_versions SET deleted_at=NOW() WHERE id=$1`, p.VersionID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Transition(ctx, "kb-user", "kb-role", card.ID, "activate", 1); !errors.Is(e, knowledgebase.ErrInvalid) {
		t.Fatal(e)
	}
	if history, e := svc.Detail(ctx, "kb-user", "kb-role", card.ID, true); e != nil || len(history) != 1 || history[0].KnowledgePoints[0].Topic != "仅测试" {
		t.Fatal(history, e)
	}
}
func TestKnowledgeCardFailedAuditRollsBack(t *testing.T) {
	s, ctx, svc, p := seedKnowledgeBase(t)
	if _, e := s.db.Exec(`CREATE FUNCTION reject_kb_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'knowledge_card_%' THEN RAISE EXCEPTION 'test audit fault'; END IF; RETURN NEW; END $$;CREATE TRIGGER reject_kb_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_kb_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := svc.Save(ctx, "kb-user", "kb-role", "", testKnowledgeInput(p)); e == nil {
		t.Fatal("audit error ignored")
	}
	var count int
	if e := s.db.QueryRow(`SELECT count(*) FROM knowledge_cards`).Scan(&count); e != nil || count != 0 {
		t.Fatal("unlogged write committed", count, e)
	}
}
