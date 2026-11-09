//go:build integration

package postgres

import (
	"aigo/internal/domain"
	"aigo/internal/knowledgebase"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAuthoringAdmissionCannotStarveQueueConnection(t *testing.T) {
	for _, pool := range []int{2, 10} {
		t.Run(fmt.Sprint("pool_", pool), func(t *testing.T) {
			s, _, _, _ := seedKnowledgeBase(t)
			if err := s.ConfigureConnectionPool(pool, pool, time.Minute); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < pool; i++ {
				if _, e := s.DB().Exec(`INSERT INTO users(id,username,password_hash,display_name,role,roles,permissions,enabled) SELECT $1,$1,password_hash,'quota',role,roles,permissions,true FROM users WHERE id='kb-user'`, fmt.Sprint("quota-", i)); e != nil {
					t.Fatal(e)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			arrived := make(chan struct{}, pool)
			release := make(chan struct{})
			results := make(chan error, pool)
			for i := 0; i < pool; i++ {
				go func(i int) {
					uid := fmt.Sprint("quota-", i)
					results <- s.WithKnowledgeBaseAccess(ctx, uid, "kb-role", func(tx context.Context, _ []string) error {
						arrived <- struct{}{}
						<-release
						created, e := s.CreateGenerationRun(tx, domain.GenerationRun{ID: fmt.Sprint("quota-run-", i), OwnerID: uid, Status: domain.GenerationRunPending, RequestedCount: 1})
						if e == nil && !created {
							return fmt.Errorf("new run not created")
						}
						return e
					})
				}(i)
			}
			// The old path fills every connection before trying a nested transaction.
			// A bounded admission gate is allowed to hold callers outside the pool.
			timer := time.NewTimer(150 * time.Millisecond)
			count := 0
		wait:
			for count < pool {
				select {
				case <-arrived:
					count++
				case <-timer.C:
					break wait
				case <-ctx.Done():
					break wait
				}
			}
			timer.Stop()
			close(release)
			for i := 0; i < pool; i++ {
				if e := <-results; e != nil {
					t.Errorf("submission starved with pool %d: %v", pool, e)
				}
			}
			var runs int
			if e := s.DB().QueryRow(`SELECT count(*) FROM generation_runs WHERE id LIKE 'quota-run-%'`).Scan(&runs); e != nil {
				t.Fatal(e)
			}
			if runs != pool {
				t.Fatalf("admitted %d of %d", runs, pool)
			}
		})
	}
}

func TestAuthoringAdmissionWaiterCancellationAndFreshGrants(t *testing.T) {
	for _, mode := range []string{"cancelled", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			s, _, _, _ := seedKnowledgeBase(t)
			if e := s.ConfigureConnectionPool(2, 2, time.Minute); e != nil {
				t.Fatal(e)
			}
			if _, e := s.DB().Exec(`INSERT INTO users(id,username,password_hash,display_name,role,roles,permissions,enabled) SELECT 'waiting-user','waiting-user',password_hash,'waiting',role,roles,permissions,true FROM users WHERE id='kb-user'`); e != nil {
				t.Fatal(e)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			leader := make(chan error, 1)
			go func() {
				leader <- s.WithKnowledgeBaseAccess(t.Context(), "kb-user", "kb-role", func(ctx context.Context, _ []string) error {
					close(entered)
					<-release
					return s.WithAuthoringAccess(ctx, "kb-user", "kb-role", func(context.Context, []string) error { return nil })
				})
			}()
			<-entered
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			waiter := make(chan error, 1)
			called := false
			go func() {
				waiter <- s.WithKnowledgeBaseAccess(ctx, "waiting-user", "kb-role", func(context.Context, []string) error { called = true; return nil })
			}()
			deadline := time.Now().Add(time.Second)
			for s.AuthoringAdmissionSnapshot().Waiting != 1 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if s.AuthoringAdmissionSnapshot().Waiting != 1 || s.DB().Stats().InUse != 1 {
				t.Fatal("queued caller borrowed a database connection")
			}
			if mode == "cancelled" {
				cancel()
			} else {
				if _, e := s.DB().Exec(`UPDATE users SET enabled=false WHERE id='waiting-user'`); e != nil {
					t.Fatal(e)
				}
			}
			close(release)
			if e := <-leader; e != nil {
				t.Fatal(e)
			}
			e := <-waiter
			if mode == "cancelled" {
				if !errors.Is(e, context.Canceled) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, knowledgebase.ErrDenied) {
				t.Fatal(e)
			}
			if called || s.AuthoringAdmissionSnapshot().Waiting != 0 {
				t.Fatal("cancelled/revoked queued request was admitted")
			}
		})
	}
}
