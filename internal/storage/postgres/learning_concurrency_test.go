//go:build integration

package postgres

import (
	"aigo/internal/storage"
	"encoding/json"
	"fmt"
	"github.com/lib/pq"
	"sync"
	"testing"
	"time"
)

func TestLearningConfiguredGlobalSlotsAcrossIndependentStores(t *testing.T) {
	s, _ := learningStoreFixture(t)
	// Slot-only fixtures: no source execution, model request, or published data.
	if _, err := s.db.Exec(`INSERT INTO learning_analysis_jobs(id,owner_id,role_id,request_key,request_hash,status,body) SELECT 'slot-'||i,'slot-owner-'||i,'learner','slot-request-'||i,'hash','pending',body FROM learning_analysis_jobs CROSS JOIN generate_series(1,7)i WHERE id='learning-job'`); err != nil {
		t.Fatal(err)
	}
	q, _, err := s.GetGenerationQuota(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	q.LearningConcurrency = 2
	if err = s.SaveGenerationQuotaVersion(t.Context(), q, q.Revision); err != nil {
		t.Fatal(err)
	}
	stores := []*Store{s, {db: s.db}, {db: s.db}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := []string{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			job, e := stores[i%len(stores)].ClaimLearningJob(t.Context(), time.Minute)
			if e != nil {
				t.Error(e)
				return
			}
			if job != nil {
				mu.Lock()
				claimed = append(claimed, job.ID)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(claimed) != 2 {
		t.Fatalf("global concurrency=2 admitted %d jobs", len(claimed))
	}
	if claimed[0] == claimed[1] {
		t.Fatal("same job claimed twice")
	}
	// Legacy callers must preserve the configured execution bound.
	raw, _ := json.Marshal(q)
	var input map[string]any
	json.Unmarshal(raw, &input)
	delete(input, "learning_concurrency")
	raw, _ = json.Marshal(input)
	q = storage.GenerationQuota{}
	json.Unmarshal(raw, &q)
	if err = s.SaveGenerationQuota(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	q, _, err = s.GetGenerationQuota(t.Context())
	if err != nil || q.LearningConcurrency != 2 {
		t.Fatal("legacy save reset global slots", q.LearningConcurrency, err)
	}
	q.LearningConcurrency = 1
	if err = s.SaveGenerationQuotaVersion(t.Context(), q, q.Revision); err != nil {
		t.Fatal(err)
	}
	if job, e := s.ClaimLearningJob(t.Context(), time.Minute); e != nil || job != nil {
		t.Fatal("lower limit must drain existing work", job, e)
	}

	var statuses []string
	rows, err := s.db.Query(`SELECT status FROM learning_analysis_jobs WHERE id=ANY($1)`, pq.Array(claimed))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			t.Fatal(err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(statuses) != 2 || statuses[0] != "running" || statuses[1] != "running" {
		t.Fatal("lowering cancelled existing jobs", statuses)
	}
}

func TestLearningConcurrencyUpgradePreservesActiveJob(t *testing.T) {
	s, _ := learningStoreFixture(t)
	job, err := s.ClaimLearningJob(t.Context(), time.Minute)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	before, _ := json.Marshal(job)
	// Reconstruct the actual schema65 boundary, including its original singleton index.
	if _, err = s.db.Exec(`DROP INDEX learning_running_capacity; ALTER TABLE generation_quota DROP COLUMN learning_concurrency; CREATE UNIQUE INDEX learning_one_running ON learning_analysis_jobs ((true)) WHERE status='running'; DELETE FROM schema_migrations WHERE version>=66`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	n, err := s.LearningConcurrencyLimit(t.Context())
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	afterJob, err := scanLearningJob(s.db.QueryRow(`SELECT `+learningJobColumns+` FROM learning_analysis_jobs WHERE id=$1`, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(afterJob)
	if string(before) != string(after) {
		t.Fatal("migration changed active snapshot or lease")
	}
	if another, err := s.ClaimLearningJob(t.Context(), time.Minute); err != nil || another != nil {
		t.Fatal(another, err)
	}
}

func TestLearningParallelDefaultUpgradeAndAdministratorOverride(t *testing.T) {
	for _, limit := range []int{1, 2, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			_, dsn, cleanup := migrationTestSchema(t)
			defer cleanup()
			s, err := New(dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			applyMigrationPrefix(t, t.Context(), s, configuredMigrations(baselineSchemaSQL)[:66])
			if _, err = s.db.Exec(`UPDATE generation_quota SET learning_concurrency=$1 WHERE id=1`, limit); err != nil {
				t.Fatal(err)
			}
			before, _, err := s.GetGenerationQuota(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			want := limit
			if want == 1 {
				want = 2
			}
			got, err := s.LearningConcurrencyLimit(t.Context())
			if err != nil || got != want {
				t.Fatal(got, want, err)
			}
			q, _, err := s.GetGenerationQuota(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			wantRevision := before.Revision
			if limit == 1 {
				wantRevision++
			}
			if q.Revision != wantRevision {
				t.Fatal("wrong migration revision", q.Revision, wantRevision)
			}
			q.LearningConcurrency = 1
			if err = s.SaveGenerationQuotaVersion(t.Context(), q, q.Revision); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			got, err = s.LearningConcurrencyLimit(t.Context())
			if err != nil || got != 1 {
				t.Fatal("restart overwrote administrator choice", got, err)
			}
		})
	}
}
