//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func seedCollection(t *testing.T) (*Store, context.Context, *knowledgebase.Service, domain.KnowledgePoint) {
	t.Helper()
	s, ctx, svc, p := seedKnowledgeBase(t)
	_, err := s.db.Exec(`UPDATE roles SET permissions=permissions||ARRAY['experience:view','experience:manage','experience:use','question:generate','kb:submit','kb:review'] WHERE id='kb-role';
 INSERT INTO users(id,username,password_hash,role,roles,enabled) VALUES('peer','peer','unused','kb-role',ARRAY['kb-role'],true),('reviewer','reviewer','unused','kb-role',ARRAY['kb-role'],true)`)
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, svc, p
}
func privateInput(p domain.KnowledgePoint) domain.KnowledgeCard {
	return domain.KnowledgeCard{Title: "本人医学卡", Kind: "medical", Status: "active", Source: "独立测试来源", KnowledgePoints: []domain.KnowledgePoint{p}, Content: domain.KnowledgeContent{ExamTargets: []string{"测试目标"}, Facts: []domain.KnowledgeFact{{ID: "f1", Text: "合成事实，仅测传输"}}, CustomText: "本人知识标记"}}
}
func TestKnowledgeCollectionPrivateOwnershipVersionsAndReceipts(t *testing.T) {
	s, ctx, svc, p := seedCollection(t)
	req := domain.KnowledgeCardSaveRequest{Key: "create-1", Cards: []domain.KnowledgeCard{privateInput(p)}}
	cards, e := svc.SavePrivate(ctx, "kb-user", "kb-role", req, nil)
	if e != nil || len(cards) != 1 {
		t.Fatal(cards, e)
	}
	card := cards[0]
	again, e := svc.SavePrivate(ctx, "kb-user", "kb-role", req, nil)
	if e != nil || again[0].ID != card.ID {
		t.Fatal(again, e)
	}
	req.Cards[0].Title = "不同载荷"
	if _, e = svc.SavePrivate(ctx, "kb-user", "kb-role", req, nil); !errors.Is(e, knowledgebase.ErrConflict) {
		t.Fatal(e)
	}
	if _, e = svc.PrivateHistory(ctx, "peer", "kb-role", card.ID); !errors.Is(e, knowledgebase.ErrNotFound) {
		t.Fatal("peer private read", e)
	}
	if _, e = svc.PrivateHistory(ctx, "reviewer", "kb-role", card.ID); !errors.Is(e, knowledgebase.ErrNotFound) {
		t.Fatal("review does not grant owner access", e)
	}
	current := card.KnowledgeCard
	current.Content.CustomText = "本地新修订"
	current.Status = "draft"
	updated, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "edit", Cards: []domain.KnowledgeCard{current}}, nil)
	if e != nil || updated[0].Revision != 2 {
		t.Fatal(updated, e)
	}
	if _, e = svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "stale", Cards: []domain.KnowledgeCard{current}}, nil); !errors.Is(e, knowledgebase.ErrConflict) {
		t.Fatal(e)
	}
	history, e := svc.PrivateHistory(ctx, "kb-user", "kb-role", card.ID)
	if e != nil || len(history) != 2 || history[1].Content.CustomText != "本人知识标记" {
		t.Fatal(history, e)
	}
	_, e = s.db.Exec(`UPDATE roles SET permissions=array_remove(array_remove(permissions,'experience:manage'),'learning:save') WHERE id='kb-role'`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "revoked", Cards: []domain.KnowledgeCard{privateInput(p)}}, nil); !errors.Is(e, knowledgebase.ErrDenied) {
		t.Fatal(e)
	}
}
func TestKnowledgeCollectionReviewSnapshotDecisionsAndAtomicBatch(t *testing.T) {
	s, ctx, svc, p := seedCollection(t)
	input := privateInput(p)
	input.Note = "不能公开的维护备注"
	cards, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "create", Cards: []domain.KnowledgeCard{input, input}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	refs := []domain.KnowledgeCardRef{{ID: cards[0].ID, Revision: 1}, {ID: cards[1].ID, Revision: 1}}
	items, e := svc.SubmitCards(ctx, "kb-user", "kb-role", domain.KnowledgeShareRequest{Key: "submit", Cards: refs})
	if e != nil || len(items) != 2 {
		t.Fatal(items, e)
	}
	if items[0].Card.Note != "" || items[0].Card.Content.CustomText != "本人知识标记" {
		t.Fatal(items[0])
	}
	one := items[0]
	if _, e = svc.DecideCards(ctx, "kb-user", "kb-role", domain.KnowledgeDecisionRequest{Key: "self", IDs: []string{one.ID}, Decision: "approved"}); !errors.Is(e, knowledgebase.ErrDenied) {
		t.Fatal("self approval", e)
	}
	// Independent private revision after submission never mutates the reviewed payload.
	edited := cards[0].KnowledgeCard
	edited.Content.CustomText = "后来修改，不得污染共享"
	if _, e = svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "edit", Cards: []domain.KnowledgeCard{edited}}, nil); e != nil {
		t.Fatal(e)
	}
	decision := domain.KnowledgeDecisionRequest{Key: "approve", IDs: []string{one.ID}, Decision: "approved"}
	accepted, e := svc.DecideCards(ctx, "reviewer", "kb-role", decision)
	if e != nil || accepted[0].SharedCardID == "" {
		t.Fatal(accepted, e)
	}
	replay, e := svc.DecideCards(ctx, "reviewer", "kb-role", decision)
	if e != nil || replay[0].SharedCardID != accepted[0].SharedCardID {
		t.Fatal(replay, e)
	}
	public, e := s.GetKnowledgeCard(ctx, accepted[0].SharedCardID)
	if e != nil || public.Content.CustomText != "本人知识标记" || public.OriginSubmissionID != one.ID || public.Note != "" {
		t.Fatal(public, e)
	}
	if _, e = svc.DecideCards(ctx, "reviewer", "kb-role", domain.KnowledgeDecisionRequest{Key: "bad-batch", IDs: []string{items[1].ID, one.ID}, Decision: "rejected", Note: "批内包含已完成，整体应回滚"}); !errors.Is(e, knowledgebase.ErrConflict) {
		t.Fatal(e)
	}
	pending, _, e := svc.Submissions(ctx, "reviewer", "kb-role", true, knowledgebase.Filter{Status: "pending"})
	if e != nil || len(pending) != 1 {
		t.Fatal(pending, e)
	}
	if _, e = svc.DecideCards(ctx, "reviewer", "kb-role", domain.KnowledgeDecisionRequest{Key: "empty-reason", IDs: []string{items[1].ID}, Decision: "rejected"}); !errors.Is(e, knowledgebase.ErrInvalid) {
		t.Fatal(e)
	}
	rejected, e := svc.DecideCards(ctx, "reviewer", "kb-role", domain.KnowledgeDecisionRequest{Key: "reject", IDs: []string{items[1].ID}, Decision: "rejected", Note: "来源不足"})
	if e != nil || rejected[0].SharedCardID != "" {
		t.Fatal(rejected, e)
	}
	peer, n, e := svc.Submissions(ctx, "peer", "kb-role", false, knowledgebase.Filter{})
	if e != nil || n != 0 || len(peer) != 0 {
		t.Fatal(peer, n, e)
	}
	mine, n, e := svc.Submissions(ctx, "kb-user", "kb-role", false, knowledgebase.Filter{})
	if e != nil || n != 2 {
		t.Fatal(mine, n, e)
	}
	var count int
	if e = s.db.QueryRow(`SELECT count(*) FROM knowledge_cards WHERE body->>'origin_submission_id' IS NOT NULL`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
func TestKnowledgeCollectionConcurrentApprovalOnlyPublishesOnce(t *testing.T) {
	s, ctx, svc, p := seedCollection(t)
	cards, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "create", Cards: []domain.KnowledgeCard{privateInput(p)}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	items, e := svc.SubmitCards(ctx, "kb-user", "kb-role", domain.KnowledgeShareRequest{Key: "submit", Cards: []domain.KnowledgeCardRef{{ID: cards[0].ID, Revision: 1}}})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, actor := range []string{"peer", "reviewer"} {
		wg.Add(1)
		go func(i int, actor string) {
			defer wg.Done()
			_, e := svc.DecideCards(ctx, actor, "kb-role", domain.KnowledgeDecisionRequest{Key: fmt.Sprint("decision-", i), IDs: []string{items[0].ID}, Decision: "approved"})
			results <- e
		}(i, actor)
	}
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, knowledgebase.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	var count int
	if e = s.db.QueryRow(`SELECT count(*) FROM knowledge_cards WHERE body->>'origin_submission_id'=$1`, items[0].ID).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}

func TestKnowledgeCollectionReviewedRevisionsUpdateOnePublication(t *testing.T) {
	s, ctx, svc, p := seedCollection(t)
	cards, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "create", Cards: []domain.KnowledgeCard{privateInput(p)}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	submit := func(c domain.PrivateKnowledgeCard, key string) domain.KnowledgeSubmission {
		t.Helper()
		items, e := svc.SubmitCards(ctx, "kb-user", "kb-role", domain.KnowledgeShareRequest{Key: key, Cards: []domain.KnowledgeCardRef{{ID: c.ID, Revision: c.Revision}}})
		if e != nil {
			t.Fatal(e)
		}
		return items[0]
	}
	approve := func(item domain.KnowledgeSubmission, key string) domain.KnowledgeSubmission {
		t.Helper()
		items, e := svc.DecideCards(ctx, "reviewer", "kb-role", domain.KnowledgeDecisionRequest{Key: key, IDs: []string{item.ID}, Decision: "approved"})
		if e != nil {
			t.Fatal(e)
		}
		return items[0]
	}
	old := approve(submit(cards[0], "s1"), "a1")
	input := cards[0].KnowledgeCard
	input.Content.CustomText = "第二版来源内容"
	updated, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "v2", Cards: []domain.KnowledgeCard{input}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	pending := submit(updated[0], "s2")
	before, e := s.GetKnowledgeCard(ctx, old.SharedCardID)
	if e != nil || before.Revision != 1 {
		t.Fatal(before, e)
	}
	latest := approve(pending, "a2")
	if latest.SharedCardID != old.SharedCardID || latest.SharedRevision != 2 {
		t.Fatal(old, latest)
	}
	public, e := s.GetKnowledgeCard(ctx, latest.SharedCardID)
	if e != nil || public.Content.CustomText != "第二版来源内容" || public.OriginSubmissionID != pending.ID {
		t.Fatal(public, e)
	}
	history, e := svc.Detail(ctx, "reviewer", "kb-role", public.ID, true)
	if e != nil || len(history) != 2 || history[1].Content.CustomText != "本人知识标记" {
		t.Fatal(history, e)
	}
	// A newer private revision wins even if an older pending application is
	// processed later; reviewers can only reject that stale application.
	input = updated[0].KnowledgeCard
	input.Content.CustomText = "第三版"
	v3, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "v3", Cards: []domain.KnowledgeCard{input}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	older := submit(v3[0], "s3")
	input = v3[0].KnowledgeCard
	input.Content.CustomText = "第四版"
	v4, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "v4", Cards: []domain.KnowledgeCard{input}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	approve(submit(v4[0], "s4"), "a4")
	if _, e = svc.DecideCards(ctx, "peer", "kb-role", domain.KnowledgeDecisionRequest{Key: "old-a3", IDs: []string{older.ID}, Decision: "approved"}); !errors.Is(e, knowledgebase.ErrConflict) {
		t.Fatal("older approval overwrote latest", e)
	}
	var count int
	if e = s.db.QueryRow(`SELECT count(*) FROM knowledge_cards WHERE body->>'origin_submission_id' IS NOT NULL`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}

func TestKnowledgeCollectionConcurrentDifferentSourceVersionsShareOneCard(t *testing.T) {
	s, ctx, svc, p := seedCollection(t)
	v1, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "create", Cards: []domain.KnowledgeCard{privateInput(p)}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	submit := func(card domain.PrivateKnowledgeCard, key string) domain.KnowledgeSubmission {
		t.Helper()
		items, e := svc.SubmitCards(ctx, "kb-user", "kb-role", domain.KnowledgeShareRequest{Key: key, Cards: []domain.KnowledgeCardRef{{ID: card.ID, Revision: card.Revision}}})
		if e != nil {
			t.Fatal(e)
		}
		return items[0]
	}
	older := submit(v1[0], "older")
	input := v1[0].KnowledgeCard
	input.Content.CustomText = "latest-source-version"
	v2, e := svc.SavePrivate(ctx, "kb-user", "kb-role", domain.KnowledgeCardSaveRequest{Key: "v2", Cards: []domain.KnowledgeCard{input}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	latest := submit(v2[0], "latest")
	start := make(chan struct{})
	type result struct {
		revision int
		err      error
	}
	done := make(chan result, 2)
	for i, item := range []domain.KnowledgeSubmission{older, latest} {
		go func(i int, item domain.KnowledgeSubmission) {
			<-start
			actor := []string{"peer", "reviewer"}[i]
			_, e := svc.DecideCards(ctx, actor, "kb-role", domain.KnowledgeDecisionRequest{Key: fmt.Sprint("approve-", i), IDs: []string{item.ID}, Decision: "approved"})
			done <- result{item.PrivateRevision, e}
		}(i, item)
	}
	close(start)
	for range 2 {
		r := <-done
		if r.revision == 2 && r.err != nil || r.err != nil && !errors.Is(r.err, knowledgebase.ErrConflict) {
			t.Fatal(r)
		}
	}
	var count int
	var text string
	if e = s.db.QueryRow(`SELECT count(*),max(body->'content'->>'custom_text') FROM knowledge_cards WHERE body->>'origin_submission_id' IS NOT NULL`).Scan(&count, &text); e != nil || count != 1 || text != "latest-source-version" {
		t.Fatal(count, text, e)
	}
}
