//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Baseline query retained only as a test oracle for ordering/count/exclusion.
func legacyChosenCards(ctx context.Context, s *Store, uid string, ids []string, private, shared bool) (map[string]knowledgebase.WorkflowCandidates, error) {
	result := map[string]knowledgebase.WorkflowCandidates{}
	if !private && !shared {
		return result, nil
	}
	var anyChoice bool
	if e := s.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_card_choices WHERE user_id=$1 AND (($2 AND scope='private') OR ($3 AND scope='shared')))`, uid, private, shared).Scan(&anyChoice); e != nil {
		return nil, e
	}
	if !anyChoice {
		return result, nil
	}
	for _, id := range ids {
		raw, _ := json.Marshal([]map[string]string{{"id": id}})
		rows, e := s.queryContext(ctx, `SELECT body,count(*) OVER() FROM (
   SELECT c.body,c.updated_at,c.id FROM personal_knowledge_cards c JOIN knowledge_card_choices x ON x.card_id=c.id AND x.scope='private' AND x.user_id=$1 WHERE $4 AND c.owner_id=$1 AND c.status='active' AND c.body->'knowledge_points' @> $2::jsonb
   UNION ALL
   SELECT c.body,c.updated_at,c.id FROM knowledge_cards c JOIN knowledge_card_choices x ON x.card_id=c.id AND x.scope='shared' AND x.user_id=$1 WHERE $3 AND c.status='active' AND COALESCE(c.body->>'origin_submission_id','')<>'' AND c.body->'knowledge_points' @> $2::jsonb
  ) chosen ORDER BY updated_at DESC,id LIMIT 8`, uid, string(raw), shared, private)
		if e != nil {
			return nil, e
		}
		entry := knowledgebase.WorkflowCandidates{Cards: []domain.KnowledgeCard{}}
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b, &entry.Total); e != nil {
				rows.Close()
				return nil, e
			}
			var c domain.KnowledgeCard
			if e = json.Unmarshal(b, &c); e != nil {
				rows.Close()
				return nil, e
			}
			entry.Cards = append(entry.Cards, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		result[id] = entry
	}
	return result, nil
}

func chosenBatchFixture(t *testing.T) (*Store, context.Context, []domain.KnowledgePoint) {
	t.Helper()
	s, ctx, _, first := seedKnowledgeBase(t)
	points := []domain.KnowledgePoint{}
	if _, e := s.DB().ExecContext(ctx, `INSERT INTO users(id,username,password_hash,display_name,role,roles,permissions,enabled) SELECT 'chosen-other','chosen-other',password_hash,'other',role,roles,permissions,true FROM users WHERE id='kb-user'`); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 8; i++ {
		p := first
		p.ID = fmt.Sprintf("chosen-point-%02d", i)
		p.OutlineCode = fmt.Sprintf("chosen.%02d", i)
		if e := s.UpdatePoint(ctx, p); e != nil {
			t.Fatal(e)
		}
		live, e := s.GetPoint(ctx, p.ID)
		if e != nil {
			t.Fatal(e)
		}
		p = *live
		points = append(points, p)
		for _, scope := range []string{"private", "shared"} {
			for j := 0; j < 7; j++ {
				c := testKnowledgeInput(p)
				c.ID = fmt.Sprintf("chosen-%s-%02d-%02d", scope, i, j)
				c.Revision = 1
				c.Status = "active"
				if j == 4 {
					c.Status = "draft"
				}
				if j == 5 {
					c.KnowledgePoints[0].Revision = 0
				}
				if scope == "shared" {
					c.OriginSubmissionID = "submission-" + c.ID
				}
				raw, _ := json.Marshal(c)
				at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(j/2) * time.Second)
				if scope == "private" {
					_, e = s.DB().ExecContext(ctx, `INSERT INTO personal_knowledge_cards(id,owner_id,revision,status,body,updated_at) VALUES($1,'kb-user',1,$2,$3,$4)`, c.ID, c.Status, raw, at)
				} else {
					_, e = s.DB().ExecContext(ctx, `INSERT INTO knowledge_cards(id,revision,kind,status,body,updated_at) VALUES($1,1,$2,$3,$4,$5)`, c.ID, c.Kind, c.Status, raw, at)
				}
				if e != nil {
					t.Fatal(e)
				}
				if j < 6 {
					if _, e = s.DB().ExecContext(ctx, `INSERT INTO knowledge_card_choices(user_id,scope,card_id) VALUES('kb-user',$1,$2)`, scope, c.ID); e != nil {
						t.Fatal(e)
					}
				}
			}
		}
	}
	// A malformed historical selection must never expose somebody else's private card.
	c := testKnowledgeInput(points[0])
	c.ID = "foreign-selected-private"
	c.Revision = 1
	c.Status = "active"
	raw, _ := json.Marshal(c)
	if _, e := s.DB().ExecContext(ctx, `INSERT INTO personal_knowledge_cards(id,owner_id,revision,status,body,updated_at) VALUES($1,'chosen-other',1,'active',$2,NOW());`, c.ID, raw); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB().ExecContext(ctx, `INSERT INTO knowledge_card_choices(user_id,scope,card_id) VALUES('kb-user','private',$1)`, c.ID); e != nil {
		t.Fatal(e)
	}
	return s, ctx, points
}
func TestChosenKnowledgeBatchMatchesBaseline(t *testing.T) {
	s, ctx, points := chosenBatchFixture(t)
	ids := []string{}
	for _, p := range points {
		ids = append(ids, p.ID)
	}
	for _, tc := range []struct {
		name            string
		private, shared bool
	}{{"both", true, true}, {"private", true, false}, {"shared", false, true}, {"neither", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			if e := s.WithKnowledgeBaseAccess(ctx, "kb-user", "kb-role", func(tx context.Context, _ []string) error {
				old, e := legacyChosenCards(tx, s, "kb-user", ids, tc.private, tc.shared)
				if e != nil {
					return e
				}
				now, e := s.LoadChosenKnowledgeCards(tx, "kb-user", ids, tc.private, tc.shared)
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(old, now) {
					return fmt.Errorf("batch changed baseline material/order/counts")
				}
				repeated := []string{ids[0], ids[0], "missing", "point' OR TRUE --"}
				old, e = legacyChosenCards(tx, s, "kb-user", repeated, tc.private, tc.shared)
				if e != nil {
					return e
				}
				now, e = s.LoadChosenKnowledgeCards(tx, "kb-user", repeated, tc.private, tc.shared)
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(old, now) {
					return fmt.Errorf("duplicate/missing identifier changed semantics")
				}
				empty, e := s.LoadChosenKnowledgeCards(tx, "chosen-other", ids, tc.private, tc.shared)
				if e != nil {
					return e
				}
				if len(empty) != 0 {
					return fmt.Errorf("unselected account received cards")
				}
				return nil
			}); e != nil {
				t.Fatal(e)
			}
		})
	}
	// Native timing is descriptive, never a CI latency threshold.
	if e := s.WithKnowledgeBaseAccess(ctx, "kb-user", "kb-role", func(tx context.Context, _ []string) error {
		for _, tc := range []struct {
			name string
			read func(context.Context, string, []string, bool, bool) (map[string]knowledgebase.WorkflowCandidates, error)
		}{{"baseline", func(ctx context.Context, uid string, ids []string, pub, priv bool) (map[string]knowledgebase.WorkflowCandidates, error) {
			return legacyChosenCards(ctx, s, uid, ids, pub, priv)
		}}, {"batched", s.LoadChosenKnowledgeCards}} {
			start := time.Now()
			for i := 0; i < 20; i++ {
				if _, e := tc.read(tx, "kb-user", ids, true, true); e != nil {
					return e
				}
			}
			t.Logf("chosen_query %s points=8 iterations=20 elapsed=%v logical_roundtrips_per_group=%d", tc.name, time.Since(start), map[string]int{"baseline": 9, "batched": 2}[tc.name])
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	// Scale the lookup without changing selected-card cardinality (97 including
	// one malformed foreign reference). Unselected background cards must not enter.
	background := testKnowledgeInput(points[0])
	background.ID = "template"
	background.Revision = 1
	background.Status = "active"
	background.Kind = "custom"
	background.OriginSubmissionID = "template"
	background.Content = domain.KnowledgeContent{CustomText: "Unselected synthetic background"}
	body, _ := json.Marshal(background)
	if _, e := s.DB().ExecContext(ctx, `INSERT INTO knowledge_cards(id,revision,kind,status,body) SELECT 'chosen-bg-'||n,1,'custom','active',$1::jsonb||jsonb_build_object('id','chosen-bg-'||n,'origin_submission_id','chosen-bg-submission-'||n) FROM generate_series(1,10000)n`, body); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB().ExecContext(ctx, `INSERT INTO knowledge_points(id,version_id,category,subject,topic,outline_code,keywords,revision) SELECT 'chosen-probe-'||n,$1,'临床综合','合成','未绑定探测点','probe.'||n,'{}',1 FROM generate_series(1,1000)n`, points[0].VersionID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.DB().ExecContext(ctx, `ANALYZE knowledge_cards;ANALYZE personal_knowledge_cards;ANALYZE knowledge_card_choices`); e != nil {
		t.Fatal(e)
	}
	for _, n := range []int{100, 1000} {
		t.Run(fmt.Sprint("scale_", n), func(t *testing.T) {
			probes := append([]string{}, ids...)
			for i := len(ids); i < n; i++ {
				probes = append(probes, fmt.Sprintf("chosen-probe-%d", i))
			}
			for _, mode := range []string{"baseline", "batched"} {
				start := time.Now()
				lookups := 0
				e := s.WithKnowledgeBaseAccess(ctx, "kb-user", "kb-role", func(tx context.Context, _ []string) error {
					for repeat := 0; repeat < 3; repeat++ {
						for first := 0; first < n; first += 8 {
							group := probes[first:min(first+8, n)]
							var result map[string]knowledgebase.WorkflowCandidates
							var err error
							if mode == "baseline" {
								result, err = legacyChosenCards(tx, s, "kb-user", group, true, true)
								lookups += 1 + len(group)
							} else {
								result, err = s.LoadChosenKnowledgeCards(tx, "kb-user", group, true, true)
								lookups += 2
							}
							if err != nil {
								return err
							}
							for _, entry := range result {
								if len(entry.Cards) > 8 {
									return fmt.Errorf("unbounded material")
								}
								for _, card := range entry.Cards {
									if strings.HasPrefix(card.ID, "chosen-bg-") || card.ID == "foreign-selected-private" {
										return fmt.Errorf("unselected/foreign card adopted")
									}
								}
							}
						}
					}
					return nil
				})
				if e != nil {
					t.Fatal(e)
				}
				t.Logf("chosen_scale mode=%s points=%d background_cards=10000 iterations=3 elapsed=%v logical_roundtrips=%d", mode, n, time.Since(start), lookups)
			}
		})
	}

}
