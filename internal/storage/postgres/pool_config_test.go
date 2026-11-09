package postgres

import (
	"database/sql"
	"testing"
	"time"
)

func TestConnectionPoolConfiguration(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://127.0.0.1:1/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Store{db: db}
	if err = s.ConfigureConnectionPool(3, 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	if db.Stats().MaxOpenConnections != 3 {
		t.Fatal("configured pool not applied")
	}
	for _, tc := range []struct {
		name       string
		open, idle int
		life       time.Duration
	}{{"unbounded", 0, 0, time.Minute}, {"no_reserve", 1, 0, time.Minute}, {"too_large", 129, 1, time.Minute}, {"idle_over_budget", 3, 4, time.Minute}, {"lifetime", 3, 1, time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			if s.ConfigureConnectionPool(tc.open, tc.idle, tc.life) == nil {
				t.Fatal("invalid pool accepted")
			}
			if db.Stats().MaxOpenConnections != 3 {
				t.Fatal("invalid config partially applied")
			}
		})
	}
}
