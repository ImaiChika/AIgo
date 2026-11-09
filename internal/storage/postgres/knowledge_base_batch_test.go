//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
)

// Hide the optional batched-reader capability to exercise the established
// per-kind queries against the exact same rows and authorization transaction.
type legacyKnowledgeReader struct{ knowledgebase.Store }

func TestKnowledgeWorkflowBatchedSelectionMatchesLegacy(t *testing.T) {
	s, ctx, svc, first := seedKnowledgeBase(t)
	points := []domain.KnowledgePoint{}
	for i := 0; i < 10; i++ {
		p := first
		p.ID = fmt.Sprintf("batch-kp-%02d", i)
		p.OutlineCode = fmt.Sprintf("batch.%02d", i)
		if err := s.UpdatePoint(ctx, p); err != nil {
			t.Fatal(err)
		}
		current, err := s.GetPoint(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		p = *current
		points = append(points, p)
		for _, kind := range []string{"medical", "exam_reference", "custom"} {
			for j := 0; j < 6; j++ {
				c := testKnowledgeInput(p)
				c.ID = fmt.Sprintf("batch-%02d-%s-%d", i, kind, j)
				c.Kind = kind
				c.Revision = 1
				c.Status = "active"
				switch kind {
				case "exam_reference":
					c.Content = domain.KnowledgeContent{Reference: &domain.ExamReference{Stem: "人工合成参考"}}
				case "custom":
					c.Content = domain.KnowledgeContent{CustomText: "仅用于隔离对照"}
				}
				if j == 5 {
					c.Status = "draft"
				}
				// One stale active medical mapping must remain excluded after
				// taking the same top-four candidates, without hidden backfill.
				if kind == "medical" && j == 3 {
					c.KnowledgePoints[0].Revision = 0
				}
				if kind == "custom" && (j == 2 || j == 4) {
					c.Content.CustomText = strings.Repeat("测", 6000)
				}
				body, _ := json.Marshal(c)
				if _, err = s.DB().Exec(`INSERT INTO knowledge_cards(id,revision,kind,status,body,updated_at) VALUES($1,1,$2,$3,$4,$5)`, c.ID, c.Kind, c.Status, body, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(j/2)*time.Second)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	// Reverse order, duplicate a point and leave zero-card points in a lower-level
	// lookup: batching must neither sort the public result nor duplicate cards.
	requested := append([]domain.KnowledgePoint{points[8], points[0], points[8]}, points[1:8]...)
	legacy := knowledgebase.Service{Store: legacyKnowledgeReader{Store: s}}
	prepare := func(service *knowledgebase.Service) *domain.KnowledgeWorkflowSnapshot {
		t.Helper()
		var snapshot *domain.KnowledgeWorkflowSnapshot
		if err := service.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", requested, func(_ context.Context, value *domain.KnowledgeWorkflowSnapshot) error { snapshot = value; return nil }); err != nil {
			t.Fatal(err)
		}
		snapshot.CapturedAt = time.Time{}
		return snapshot
	}
	old, current := prepare(&legacy), prepare(svc)
	if !reflect.DeepEqual(old, current) || len(current.Bundles) != 9 || current.Bundles[0].KnowledgePointID != points[8].ID {
		t.Fatal("batch lookup changed the frozen selection or order")
	}
	if len(current.Bundles[0].Cards) != 6 || current.Bundles[0].ExcludedCards != 9 {
		t.Fatal("stale mapping or rune-budget exclusion changed")
	}
	if _, err := s.LoadKnowledgeWorkflowCandidates(ctx, []string{points[0].ID}); err == nil {
		t.Fatal("candidate reader escaped its access transaction")
	}
	if err := svc.Access(ctx, "kb-user", "kb-role", func(txctx context.Context, _ knowledgebase.Capabilities) error {
		for _, bad := range [][]string{nil, make([]string, knowledgebase.WorkflowLookupBatchSize+1)} {
			if _, err := s.LoadKnowledgeWorkflowCandidates(txctx, bad); err == nil {
				return fmt.Errorf("unbounded candidate request accepted")
			}
		}
		foreignID := "point' OR TRUE --"
		rows, err := s.LoadKnowledgeWorkflowCandidates(txctx, []string{points[0].ID, points[0].ID, "missing-point", foreignID})
		if err != nil {
			return err
		}
		if rows[points[0].ID].Total != 15 || len(rows[points[0].ID].Cards) != 8 || rows["missing-point"].Total != 0 || len(rows["missing-point"].Cards) != 0 {
			return fmt.Errorf("wrong counts, limits or duplicate handling")
		}
		if rows[foreignID].Total != 0 || len(rows[foreignID].Cards) != 0 {
			return fmt.Errorf("point identifier changed the query scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A real update is visible on the very next preparation, with no cache
	// invalidation period; the prior captured snapshot remains byte-identical.
	before, _ := json.Marshal(current)
	for _, c := range current.Bundles[0].Cards {
		if c.Kind != "custom" {
			continue
		}
		c.Content.CustomText = "随后修订的测试资料"
		draft, err := svc.Save(ctx, "kb-user", "kb-role", c.ID, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Transition(ctx, "kb-user", "kb-role", c.ID, "activate", draft.Revision); err != nil {
			t.Fatal(err)
		}
		break
	}
	updated := prepare(svc)
	after, _ := json.Marshal(current)
	if string(before) != string(after) || reflect.DeepEqual(updated, current) {
		t.Fatal("new cards were cached or old snapshot mutated")
	}
	if _, err := s.DB().Exec(`UPDATE roles SET permissions=ARRAY['kb:view'] WHERE id='kb-role'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.WithPreparedSnapshot(ctx, "kb-user", "kb-role", "knowledge", requested, func(context.Context, *domain.KnowledgeWorkflowSnapshot) error {
		t.Fatal("revoked preparation ran")
		return nil
	}); !errors.Is(err, knowledgebase.ErrDenied) {
		t.Fatal(err)
	}
}

func TestKnowledgePointBatchResolutionOrderAndVersions(t *testing.T) {
	s, ctx, svc, p := seedKnowledgeBase(t)
	v := domain.KnowledgeVersion{ID: "batch-version-2", Name: "另版同代码", Year: 2023, CreatedAt: time.Now()}
	if err := s.CreateKnowledgeVersion(ctx, v); err != nil {
		t.Fatal(err)
	}
	p2 := p
	p2.ID = "batch-point-2"
	p2.VersionID = v.ID
	if err := s.UpdatePoint(ctx, p2); err != nil {
		t.Fatal(err)
	}
	if err := svc.Access(ctx, "kb-user", "kb-role", func(txctx context.Context, _ knowledgebase.Capabilities) error {
		if empty, err := s.ResolveKnowledgeCardPoints(txctx, nil); err != nil || len(empty) != 0 {
			return fmt.Errorf("empty draft bindings changed: %v", err)
		}
		resolved, err := s.ResolveKnowledgeCardPoints(txctx, []domain.KnowledgePoint{p2, p, p2})
		if err != nil {
			return err
		}
		if len(resolved) != 3 || resolved[0].ID != p2.ID || resolved[1].ID != p.ID || resolved[2].ID != p2.ID || resolved[0].VersionName != v.Name {
			return fmt.Errorf("input order or version was lost")
		}
		wrong := p2
		wrong.VersionID = p.VersionID
		for _, input := range [][]domain.KnowledgePoint{{wrong}, {p, wrong}, {p, {ID: "missing", VersionID: p.VersionID}}} {
			if _, err = s.ResolveKnowledgeCardPoints(txctx, input); !errors.Is(err, knowledgebase.ErrInvalid) {
				return fmt.Errorf("invalid mapping accepted: %v", err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE knowledge_versions SET deleted_at=NOW() WHERE id=$1`, p2.VersionID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Access(ctx, "kb-user", "kb-role", func(txctx context.Context, _ knowledgebase.Capabilities) error {
		_, err := s.ResolveKnowledgeCardPoints(txctx, []domain.KnowledgePoint{p, p2})
		return err
	}); !errors.Is(err, knowledgebase.ErrInvalid) {
		t.Fatal("deleted version accepted", err)
	}
}
