//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/learning"
	"aigo/internal/storage"
	"github.com/lib/pq"
)

func legacyPriorityFixture(t *testing.T) (*Server, *sql.DB, string, string, domain.KnowledgePoint) {
	t.Helper()
	s, _, _, _, cleanup := libraryTestEnv(t)
	t.Cleanup(cleanup)
	owner, err := s.authSvc.GetUserByUsername("lib-teacher")
	if err != nil {
		t.Fatal(err)
	}
	db := s.questionStore.(interface{ DB() *sql.DB }).DB()
	if _, err := db.ExecContext(t.Context(), `UPDATE users SET permissions=$2 WHERE id=$1`, owner.ID, pq.Array([]string{domain.PermLearningUse, domain.PermQuestionGenerate})); err != nil {
		t.Fatal(err)
	}
	return s, db, owner.ID, owner.Role, domain.KnowledgePoint{ID: "legacy-priority-clinical", Category: "临床综合", Subject: "消化", Topic: "消化性溃疡"}
}

func insertLegacyPriorityCard(t *testing.T, db *sql.DB, owner string, card domain.AuthoringExperience) {
	t.Helper()
	card.Kind, card.ScopeType, card.Revision = "authoring_experience", "personal", 1
	if card.CreatedAt.IsZero() {
		card.CreatedAt = card.UpdatedAt
	}
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	// Trusted fixture represents the forward migration's persisted output. The
	// public manual endpoint is deliberately unable to create this source kind.
	if _, err = db.ExecContext(t.Context(), `INSERT INTO personal_experiences(id,owner_id,revision,status,source_job_id,source_candidate_id,body,created_at,updated_at)VALUES($1,$2,1,$3,'priority-fixture',$1,$4,$5,$6)`, card.ID, owner, card.Status, raw, card.CreatedAt, card.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyPreferencePrioritySurvivesSQLLimitAndWholeEntryBudget(t *testing.T) {
	s, db, owner, role, point := legacyPriorityFixture(t)
	now := time.Now().UTC()
	for i := 0; i < 204; i++ {
		insertLegacyPriorityCard(t, db, owner, domain.AuthoringExperience{ID: fmt.Sprintf("recent-point-ai-%03d", i), Title: "近期考点经验", Scope: domain.ExperienceScope{Kind: "point", PointIDs: []string{point.ID}}, Rules: []domain.LearningRule{{ID: "r", Text: strings.Repeat("新", 600), Field: "clinical_stem"}}, Status: "active", Source: domain.ExperienceSource{Kind: "learning"}, UpdatedAt: now.Add(time.Duration(i) * time.Second)})
	}
	legacyText := strings.Repeat("偏", 2000)
	insertLegacyPriorityCard(t, db, owner, domain.AuthoringExperience{ID: "old-active-preference", Title: "已迁入的启用偏好", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "legacy", Text: legacyText[:1500], Field: "clinical_stem"}, {ID: "legacy-2", Text: legacyText[1500:3000], Field: "clinical_stem"}, {ID: "legacy-3", Text: legacyText[3000:4500], Field: "clinical_stem"}, {ID: "legacy-4", Text: legacyText[4500:], Field: "clinical_stem"}}, Status: "active", Source: domain.ExperienceSource{Kind: "legacy_personal_preference", OriginID: "original-enabled"}, UpdatedAt: now.Add(-time.Hour)})
	query := s.questionStore.(storage.ExperienceQueryStore)
	store := s.questionStore.(storage.LearningStore)
	err := store.WithLearningAccess(t.Context(), owner, role, func(ctx context.Context, _ []string) error {
		rows, total, e := query.MatchAuthoringExperiences(ctx, owner, []string{point.ID}, []string{point.Subject}, 200)
		if e != nil {
			return e
		}
		if total != 205 || len(rows) != 200 || rows[0].ID != "old-active-preference" {
			t.Errorf("SQL limit starved enabled migrated preference: total=%d first=%s rows=%d", total, rows[0].ID, len(rows))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := learning.NewService(store, nil).FreezeExperiences(t.Context(), owner, role, []domain.KnowledgePoint{point})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) == 0 || snapshot.Entries[0].ID != "old-active-preference" {
		t.Errorf("freeze budget starved enabled migrated preference: %+v", snapshot)
	} else {
		var text string
		for _, r := range snapshot.Entries[0].Rules {
			text += r.Text
		}
		if text != legacyText {
			t.Fatal("legacy preference was truncated or rewritten")
		}
	}
	used := 0
	for _, entry := range snapshot.Entries {
		used += domain.PersonalExperienceContentSize(entry)
	}
	if used > domain.MaxPersonalExperienceContentSize || len(snapshot.Entries) > domain.MaxPersonalExperienceEntries || snapshot.ExcludedCount != 205-len(snapshot.Entries) {
		t.Fatal("priority changed snapshot budget/count semantics", used, snapshot)
	}
	if _, err = db.ExecContext(t.Context(), `UPDATE personal_experiences SET status='draft',body=jsonb_set(body,'{status}','"draft"') WHERE id='old-active-preference'`); err != nil {
		t.Fatal(err)
	}
	snapshot, err = learning.NewService(store, nil).FreezeExperiences(t.Context(), owner, role, []domain.KnowledgePoint{point})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.Entries {
		if entry.ID == "old-active-preference" {
			t.Fatal("priority admitted an inactive preference")
		}
	}
	if snapshot.ExcludedCount != 204-len(snapshot.Entries) {
		t.Fatal("inactive preference leaked into candidate count", snapshot.ExcludedCount)
	}
}

func TestLegacyPreferencePriorityKeepsOtherSourceScopeAndRecencyOrder(t *testing.T) {
	s, db, owner, role, point := legacyPriorityFixture(t)
	now := time.Now().UTC()
	for _, tc := range []struct {
		id, origin, scope string
		at                time.Time
	}{
		{"point-ai-new", "learning", "point", now},
		{"point-manual-old", "manual", "point", now.Add(-time.Hour)},
		{"profession-manual-newest", "manual", "profession", now.Add(time.Hour)},
		{"all-ai-newest", "learning", "all", now.Add(2 * time.Hour)},
	} {
		scope := domain.ExperienceScope{Kind: tc.scope}
		if tc.scope == "point" {
			scope.PointIDs = []string{point.ID}
		}
		if tc.scope == "profession" {
			scope.Profession = point.Subject
		}
		insertLegacyPriorityCard(t, db, owner, domain.AuthoringExperience{ID: tc.id, Title: tc.id, Scope: scope, Rules: []domain.LearningRule{{ID: "r", Text: "保留临床线索", Field: "clinical_stem"}}, Status: "active", Source: domain.ExperienceSource{Kind: tc.origin}, UpdatedAt: tc.at})
	}
	want := []string{"point-ai-new", "point-manual-old", "profession-manual-newest", "all-ai-newest"}
	store := s.questionStore.(storage.LearningStore)
	query := s.questionStore.(storage.ExperienceQueryStore)
	if err := store.WithLearningAccess(t.Context(), owner, role, func(ctx context.Context, _ []string) error {
		rows, total, e := query.MatchAuthoringExperiences(ctx, owner, []string{point.ID}, []string{point.Subject}, 200)
		if e != nil {
			return e
		}
		ids := []string{}
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		if total != 4 || !reflect.DeepEqual(ids, want) {
			t.Fatal("nonlegacy SQL order changed", ids, total)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := learning.NewService(store, nil).FreezeExperiences(t.Context(), owner, role, []domain.KnowledgePoint{point})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, entry := range snapshot.Entries {
		ids = append(ids, entry.ID)
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatal("nonlegacy freeze order changed", ids)
	}
}
