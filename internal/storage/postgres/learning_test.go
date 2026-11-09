//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"aigo/internal/domain"
	"aigo/internal/storage"
)

func learningStoreFixture(t *testing.T) (*Store, domain.LearningJob) {
	t.Helper()
	s := libraryTestStore(t)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO roles(id,name,permissions)VALUES('learner','learner',ARRAY['question:view','question:generate','learning:analyze','learning:save','learning:use']);INSERT INTO users(id,username,password_hash,role,roles,enabled)VALUES('owner','owner','unused','learner',ARRAY['learner'],TRUE),('other','other','unused','learner',ARRAY['learner'],TRUE)`); err != nil {
		t.Fatal(err)
	}
	q := libraryTestQuestion(t, s, "learning-question")
	q.ClinicalStem += "。老师补充临床体征"
	q.Version++
	if err := s.SaveQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveReviewResult(t.Context(), domain.AIReviewResult{ID: "learning-pass", QuestionID: q.ID, QuestionVersion: 1, Verdict: "pass", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.GetQuestion(t.Context(), q.ID)
	job := domain.LearningJob{ID: "learning-job", OwnerID: "owner", RoleID: "learner", RequestKey: "request", RequestHash: "request-hash", QuestionID: q.ID, OriginalVersion: 1, CurrentVersion: 2, OriginalHash: "original-hash", CurrentHash: "current-hash", LibraryRevision: current.LibraryRevision, ReviewRevision: current.ReviewRevision, ProtocolVersion: domain.LearningProtocolVersion, Note: "仅个人修改说明"}
	if err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error { return s.CreateLearningJob(ctx, job) }); err != nil {
		t.Fatal(err)
	}
	return s, job
}
func readLearningJob(t *testing.T, s *Store, id string) *domain.LearningJob {
	t.Helper()
	var job *domain.LearningJob
	err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
		var e error
		job, e = s.GetLearningJob(ctx, "owner", id)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	return job
}
func successfulLearningFixture(t *testing.T, s *Store) domain.LearningJob {
	t.Helper()
	job, err := s.ClaimLearningJob(t.Context(), time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	candidates := []domain.LearningCandidate{{ID: "c1", Style: "concise", Title: "保留鉴别证据", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "r1", Text: "临床情境需包含足够的鉴别线索"}}}, {ID: "c2", Style: "detailed", Title: "详细经验", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "r2", Text: "保留必要证据"}}}}
	if err = s.FinishLearningJob(t.Context(), job.ID, job.Attempts, job.LeaseToken, storage.LearningOutcome{Status: "succeeded", Candidates: candidates, ModelCallIDs: []string{"model-evidence"}}); err != nil {
		t.Fatal(err)
	}
	return *readLearningJob(t, s, job.ID)
}
func experienceFixture(job domain.LearningJob, id, candidate string) domain.AuthoringExperience {
	return domain.AuthoringExperience{ID: id, OwnerID: job.OwnerID, Kind: "authoring_experience", ScopeType: "personal", Title: "临床命题经验", Scope: domain.ExperienceScope{Kind: "all"}, Rules: []domain.LearningRule{{ID: "rule", Text: "保留鉴别线索"}}, Status: "draft", Revision: 1, Source: domain.ExperienceSource{JobID: job.ID, CandidateID: candidate, QuestionID: job.QuestionID, OriginalVersion: 1, CurrentVersion: job.CurrentVersion, OriginalHash: job.OriginalHash, CurrentHash: job.CurrentHash}}
}

func TestLearningJobPersistsPrivateEvidenceAndRestrictsAllOwnerReads(t *testing.T) {
	s, job := learningStoreFixture(t)
	persisted := readLearningJob(t, s, job.ID)
	if persisted.Source == nil || persisted.Source.Original == nil || persisted.Source.Original.Version != 1 || persisted.Source.Current.Version != 2 || persisted.OwnerID != "owner" || persisted.RoleID != "learner" || persisted.RequestHash != job.RequestHash || persisted.Note != job.Note {
		t.Fatalf("private frozen evidence lost %+v", persisted)
	}
	if _, err := s.GetLearningJob(t.Context(), "owner", job.ID); !errors.Is(err, storage.ErrLearningDenied) {
		t.Fatal("unprotected owner read accepted", err)
	}
	err := s.WithLearningAccess(t.Context(), "other", "learner", func(ctx context.Context, _ []string) error {
		if _, e := s.GetLearningJob(ctx, "owner", job.ID); !errors.Is(e, storage.ErrLearningDenied) {
			t.Fatal("forged owner accepted", e)
		}
		got, e := s.GetLearningJob(ctx, "other", job.ID)
		if e != nil || got != nil {
			t.Fatal("cross-owner job leaked", got, e)
		}
		source, e := s.LoadLearningSource(ctx, "other", job.QuestionID)
		if !errors.Is(e, storage.ErrLearningNotFound) || source != nil {
			t.Fatal("cross-owner versions leaked", source, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.WithLearningAccess(t.Context(), "owner", "missing-role", func(context.Context, []string) error { return nil }); !errors.Is(err, storage.ErrLearningDenied) {
		t.Fatal("revoked identity accepted", err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE users SET enabled=FALSE WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	if err = s.WithLearningAccess(t.Context(), "owner", "learner", func(context.Context, []string) error { return nil }); !errors.Is(err, storage.ErrLearningDenied) {
		t.Fatal("disabled owner accepted", err)
	}
}
func TestLearningClaimSerializesGloballyAndFencesExpiredAttempts(t *testing.T) {
	s, _ := learningStoreFixture(t)
	var wg sync.WaitGroup
	results := make(chan *domain.LearningJob, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := s.ClaimLearningJob(t.Context(), time.Minute)
			results <- job
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *domain.LearningJob
	claims := 0
	for job := range results {
		if job != nil {
			first = job
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("global claims %d", claims)
	}
	if first.Attempts != 1 || first.DeadlineAt == nil || first.LeaseToken == "" {
		t.Fatal(first)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET leased_until=NOW()-INTERVAL '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimLearningJob(t.Context(), time.Minute)
	if err != nil || second == nil || second.Attempts != 2 || second.LeaseToken == first.LeaseToken || !second.DeadlineAt.Equal(*first.DeadlineAt) {
		t.Fatal("restart reset budget or fencing", second, err)
	}
	if err = s.FinishLearningJob(t.Context(), first.ID, first.Attempts, first.LeaseToken, storage.LearningOutcome{Status: "succeeded"}); !errors.Is(err, storage.ErrLearningLeaseLost) {
		t.Fatal("old worker committed", err)
	}
	if err = s.FinishLearningJob(t.Context(), second.ID, second.Attempts, second.LeaseToken, storage.LearningOutcome{Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimLearningJob(t.Context(), time.Minute); err != nil || job != nil {
		t.Fatal("third model attempt allowed", job, err)
	}
	if got := readLearningJob(t, s, first.ID); got.Status != "failed" || got.Attempts != 2 {
		t.Fatal(got)
	}
}
func TestPersonalExperienceHistoryAtomicImportAndSharedKBIsolation(t *testing.T) {
	s, _ := learningStoreFixture(t)
	job := successfulLearningFixture(t, s)
	card := experienceFixture(job, "experience-one", "c1")
	failure := errors.New("confirmation failed")
	err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
		if err := s.SaveAuthoringExperience(ctx, card, 0); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM personal_experiences`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial import card persisted", count, err)
	}
	err = s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
		if err := s.SaveAuthoringExperience(ctx, card, 0); err != nil {
			return err
		}
		operation := storage.LearningImportOperation{OwnerID: "owner", ImportKey: "import-one", RequestHash: "payload-hash", JobID: job.ID, ExperienceIDs: []string{card.ID}}
		if err := s.SaveLearningImportOperation(ctx, operation); err != nil {
			return err
		}
		saved, e := s.GetLearningImportOperation(ctx, "owner", "import-one")
		if e != nil || saved.RequestHash != "payload-hash" || len(saved.ExperienceIDs) != 1 {
			t.Fatal(saved, e)
		}
		card.Revision = 2
		card.Status = "active"
		if e = s.SaveAuthoringExperience(ctx, card, 1); e != nil {
			return e
		}
		if e = s.SaveAuthoringExperience(ctx, card, 1); !errors.Is(e, storage.ErrLearningInvalid) && !errors.Is(e, storage.ErrLearningConflict) {
			t.Fatal("stale experience overwrite", e)
		}
		history, e := s.ListAuthoringExperienceVersions(ctx, "owner", card.ID)
		if e != nil || len(history) != 2 || history[0].Status != "active" || history[1].Status != "draft" {
			t.Fatal("missing immutable experience versions", history, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM knowledge_cards`).Scan(&count); err != nil || count != 0 {
		t.Fatal("private experience entered shared KB", count, err)
	}
	err = s.WithLearningAccess(t.Context(), "other", "learner", func(ctx context.Context, _ []string) error {
		got, e := s.GetAuthoringExperience(ctx, "other", card.ID)
		if e != nil || got != nil {
			t.Fatal("cross-owner card leaked", got, e)
		}
		versions, e := s.ListAuthoringExperienceVersions(ctx, "other", card.ID)
		if e != nil || len(versions) != 0 {
			t.Fatal("cross-owner history leaked", versions, e)
		}
		operation, e := s.GetLearningImportOperation(ctx, "other", "import-one")
		if e != nil || operation != nil {
			t.Fatal("cross-owner import leaked", operation, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestLearningImportRejectsChangedSourceButRetainsConfirmedExperience(t *testing.T) {
	s, _ := learningStoreFixture(t)
	job := successfulLearningFixture(t, s)
	err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
		return s.SaveAuthoringExperience(ctx, experienceFixture(job, "retained", "c1"), 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE questions SET library_state='deleted',library_revision=library_revision+1 WHERE id=$1`, job.QuestionID); err != nil {
		t.Fatal(err)
	}
	err = s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
		return s.SaveAuthoringExperience(ctx, experienceFixture(job, "stale", "c2"), 0)
	})
	if !errors.Is(err, storage.ErrLearningConflict) {
		t.Fatal("deleted source imported", err)
	}
	err = s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error {
		card, e := s.GetAuthoringExperience(ctx, "owner", "retained")
		if e != nil {
			return e
		}
		if card == nil {
			t.Fatal("source deletion destroyed confirmed experience")
		}
		card.Status = "archived"
		card.Revision++
		return s.SaveAuthoringExperience(ctx, *card, 1)
	})
	if err != nil {
		t.Fatal("existing experience cannot be deactivated after source deletion", err)
	}
}

func TestLearningAdmissionUsesSeparateOwnerAndGlobalBounds(t *testing.T) {
	s, job := learningStoreFixture(t)
	job.ID = "second-job"
	job.RequestKey = "second-key"
	err := s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error { return s.CreateLearningJob(ctx, job) })
	if !errors.Is(err, storage.ErrLearningBusy) {
		t.Fatal("owner queue was unbounded", err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE learning_analysis_jobs SET status='failed';INSERT INTO learning_analysis_jobs(id,owner_id,role_id,request_key,request_hash,status,body) SELECT 'queued-'||i,'owner-'||i,'learner','key-'||i,'hash','pending','{}'::jsonb FROM generate_series(1,20) i`); err != nil {
		t.Fatal(err)
	}
	err = s.WithLearningAccess(t.Context(), "owner", "learner", func(ctx context.Context, _ []string) error { return s.CreateLearningJob(ctx, job) })
	if !errors.Is(err, storage.ErrLearningBusy) {
		t.Fatal("global queue was unbounded", err)
	}
	var n int
	if err = s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM learning_analysis_jobs WHERE id='second-job'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("rejected job persisted", n, err)
	}
}

func TestLearningLaterPreflightFailureRetainsEarlierBilledCallReferences(t *testing.T) {
	s, _ := learningStoreFixture(t)
	first, err := s.ClaimLearningJob(t.Context(), time.Minute)
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	retry := time.Now().Add(-time.Second)
	if err = s.FinishLearningJob(t.Context(), first.ID, first.Attempts, first.LeaseToken, storage.LearningOutcome{Status: "pending", ErrorKind: "model_error", ModelCallIDs: []string{"billed-attempt-one"}, RetryAt: &retry}); err != nil {
		t.Fatal(err)
	}
	second, err := s.ClaimLearningJob(t.Context(), time.Minute)
	if err != nil || second == nil {
		t.Fatal(second, err)
	}
	if err = s.FinishLearningJob(t.Context(), second.ID, second.Attempts, second.LeaseToken, storage.LearningOutcome{Status: "failed", ErrorKind: "permission_revoked"}); err != nil {
		t.Fatal(err)
	}
	final := readLearningJob(t, s, first.ID)
	if len(final.ModelCallIDs) != 1 || final.ModelCallIDs[0] != "billed-attempt-one" || len(final.AttemptHistory) != 2 || final.AttemptHistory[0].ModelCallID != "billed-attempt-one" || final.AttemptHistory[1].ErrorKind != "permission_revoked" {
		t.Fatal(final)
	}
}
