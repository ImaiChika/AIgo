package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/storage"
	"context"
	"database/sql"
	"fmt"
	"github.com/lib/pq"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *Store) PreferenceSummary(ctx context.Context, uid, role string) (result domain.PreferenceSummary, err error) {
	err = s.WithKnowledgeBaseAccess(ctx, uid, role, func(ctx context.Context, p []string) error {
		if !slices.Contains(p, domain.PermQuestionGenerate) || !slices.Contains(p, domain.PermQuestionView) {
			return storage.ErrPreferenceDenied
		}
		statuses := []string{}
		for _, tier := range []domain.QuestionTier{domain.TierWorking, domain.TierFormal, domain.TierEliminated} {
			for _, status := range domain.TierStatuses(tier) {
				if preferenceAccess(p, status) {
					statuses = append(statuses, string(status))
				}
			}
		}
		tx := reviewTransactionFromContext(ctx)
		rows, e := tx.QueryContext(ctx, `WITH visible AS (SELECT id,version FROM questions WHERE owner_id=$1 AND status=ANY($2))
SELECT 'eligible', '', count(*) FROM visible
UNION ALL
SELECT CASE WHEN f.question_version=q.version THEN CASE WHEN q.version=1 THEN 'initial' ELSE 'revised' END ELSE 'historical' END, f.rating,count(*)
FROM visible q JOIN question_preference_feedback f ON f.question_id=q.id AND f.author_id=$1 AND f.question_version<=q.version
GROUP BY 1,2`, uid, pq.Array(statuses))
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var group, rating string
			var count int
			if e = rows.Scan(&group, &rating, &count); e != nil {
				return e
			}
			if group == "eligible" {
				result.Eligible = count
				continue
			}
			counts := &result.Historical
			if group == "initial" {
				counts = &result.Initial
			} else if group == "revised" {
				counts = &result.Revised
			}
			switch rating {
			case "met":
				counts.Met += count
			case "partial":
				counts.Partial += count
			case "not_met":
				counts.NotMet += count
			}
		}
		result.Current = domain.PreferenceCounts{Met: result.Initial.Met + result.Revised.Met, Partial: result.Initial.Partial + result.Revised.Partial, NotMet: result.Initial.NotMet + result.Revised.NotMet}
		result.Unrated = result.Eligible - result.Current.Total()
		return rows.Err()
	})
	return
}

func preferenceFeedbackMigrationStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS question_preference_feedback (
 question_id TEXT NOT NULL,question_version INTEGER NOT NULL CHECK(question_version>0),
 revision INTEGER NOT NULL CHECK(revision>0),author_id TEXT NOT NULL,
 rating TEXT NOT NULL CHECK(rating IN ('met','partial','not_met')),comment TEXT NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),PRIMARY KEY(question_id,question_version))`,
	}
}
func preferenceAccess(p []string, _ domain.QuestionStatus) bool {
	// All callers first enforce ownership. The personal bank's three tiers
	// consistently use question:view (scope.go), not global-bank grants.
	required := domain.PermQuestionView
	generate, view := false, false
	for _, permission := range p {
		generate = generate || permission == domain.PermQuestionGenerate
		view = view || permission == required
	}
	return generate && view
}
func readPreference(ctx context.Context, tx *sql.Tx, id string, version int) (*domain.PreferenceFeedback, error) {
	var f domain.PreferenceFeedback
	err := tx.QueryRowContext(ctx, `SELECT question_id,question_version,revision,author_id,rating,comment,updated_at FROM question_preference_feedback WHERE question_id=$1 AND question_version=$2`, id, version).Scan(&f.QuestionID, &f.QuestionVersion, &f.Revision, &f.AuthorID, &f.Rating, &f.Comment, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &f, err
}
func (s *Store) GetPreferenceFeedback(ctx context.Context, uid, role, id string) (feedback *domain.PreferenceFeedback, contentVersion int, writable bool, err error) {
	err = s.WithKnowledgeBaseAccess(ctx, uid, role, func(ctx context.Context, p []string) error {
		tx := reviewTransactionFromContext(ctx)
		var owner string
		var version int
		var status domain.QuestionStatus
		e := tx.QueryRowContext(ctx, `SELECT owner_id,version,status FROM questions WHERE id=$1`, id).Scan(&owner, &version, &status)
		if e == sql.ErrNoRows {
			return storage.ErrPreferenceDenied
		}
		if e != nil {
			return e
		}
		if owner != uid {
			return storage.ErrPreferenceDenied
		}
		if !preferenceAccess(p, status) || domain.IsStagingStatus(status) {
			return storage.ErrPreferenceDenied
		}
		contentVersion = version
		writable = status == domain.StatusAIReviewed
		feedback, e = readPreference(ctx, tx, id, version)
		if e == nil && feedback != nil && feedback.AuthorID != uid {
			return storage.ErrPreferenceDenied
		}
		return e
	})
	return
}
func (s *Store) SavePreferenceFeedback(ctx context.Context, uid, role, actor string, input domain.PreferenceFeedback) (result *domain.PreferenceFeedback, err error) {
	input.Comment = strings.TrimSpace(input.Comment)
	if strings.IndexByte(input.Comment, 0) >= 0 || input.QuestionID == "" || input.QuestionVersion < 1 || input.Revision < 0 || utf8.RuneCountInString(input.Comment) > 1000 || (input.Rating != "met" && input.Rating != "partial" && input.Rating != "not_met") {
		return nil, storage.ErrPreferenceInvalid
	}
	err = s.WithKnowledgeBaseAccess(ctx, uid, role, func(ctx context.Context, p []string) error {
		tx := reviewTransactionFromContext(ctx)
		var owner string
		var version int
		var status domain.QuestionStatus
		e := tx.QueryRowContext(ctx, `SELECT owner_id,version,status FROM questions WHERE id=$1 FOR UPDATE`, input.QuestionID).Scan(&owner, &version, &status)
		if e == sql.ErrNoRows {
			return storage.ErrPreferenceDenied
		}
		if e != nil {
			return e
		}
		if owner != uid {
			return storage.ErrPreferenceDenied
		}
		if !preferenceAccess(p, status) {
			return storage.ErrPreferenceDenied
		}
		if version != input.QuestionVersion {
			return storage.ErrPreferenceConflict
		}
		if status != domain.StatusAIReviewed {
			return storage.ErrPreferenceReadOnly
		}
		existing, e := readPreference(ctx, tx, input.QuestionID, version)
		if e != nil {
			return e
		}
		revision := 0
		if existing != nil {
			revision = existing.Revision
			if existing.AuthorID != uid {
				return storage.ErrPreferenceDenied
			}
		}
		if input.Revision != revision {
			return storage.ErrPreferenceConflict
		}
		input.AuthorID = uid
		input.Revision = revision + 1
		input.UpdatedAt = time.Now().UTC()
		_, e = tx.ExecContext(ctx, `INSERT INTO question_preference_feedback(question_id,question_version,revision,author_id,rating,comment,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(question_id,question_version) DO UPDATE SET revision=EXCLUDED.revision,rating=EXCLUDED.rating,comment=EXCLUDED.comment,updated_at=EXCLUDED.updated_at`, input.QuestionID, version, input.Revision, uid, input.Rating, input.Comment, input.UpdatedAt)
		if e != nil {
			return e
		}
		ratingLabel := map[string]string{"met": "基本满足", "partial": "部分满足", "not_met": "尚未满足"}[input.Rating]
		if e = s.SaveLog(ctx, domain.AuditLog{ID: fmt.Sprintf("preference-%s-%d-%d", input.QuestionID, version, input.Revision), QuestionID: input.QuestionID, Action: "preference_feedback", Actor: actor, Detail: fmt.Sprintf("题目第%d版，个人需求反馈：%s（不改变首检或审核）", version, ratingLabel), CreatedAt: input.UpdatedAt}); e != nil {
			return e
		}
		copy := input
		result = &copy
		return nil
	})
	return
}

var _ storage.PreferenceFeedbackStore = (*Store)(nil)
