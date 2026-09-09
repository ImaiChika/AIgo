package auth_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"aigo/internal/auth"
	"aigo/internal/storage/postgres"

	"github.com/lib/pq"
)

func TestLoginLimitsPersistAndSeparateAccountIPBuckets(t *testing.T) {
	service, cleanup := loginLimitTestService(t)
	defer cleanup()
	ctx := context.Background()

	// 同一账号的并发失败必须全部计数，第 8 次触发短时指数阻断。
	var wg sync.WaitGroup
	delays := make(chan time.Duration, accountFailureAttempts)
	errs := make(chan error, accountFailureAttempts)
	for i := 0; i < accountFailureAttempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			delay, err := service.RecordLoginFailure(ctx, "target-user", fmt.Sprintf("192.0.2.%d", index+1))
			delays <- delay
			errs <- err
		}(i)
	}
	wg.Wait()
	close(delays)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var blocked int
	for delay := range delays {
		if delay > 0 {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("concurrent account threshold triggered %d times, want 1", blocked)
	}
	if delay, err := service.CheckLoginAllowed(ctx, "target-user", "198.51.100.1"); err != nil || delay <= 0 {
		t.Fatalf("account bucket should be blocked: delay=%v err=%v", delay, err)
	}
	if err := service.RecordLoginSuccess(ctx, "target-user"); err != nil {
		t.Fatal(err)
	}
	if delay, err := service.CheckLoginAllowed(ctx, "target-user", "198.51.100.1"); err != nil || delay != 0 {
		t.Fatalf("successful login should clear account bucket: delay=%v err=%v", delay, err)
	}

	// 同一 IP 对不同账号的扫描使用独立 IP 桶，第 30 次失败触发阻断。
	for i := 0; i < ipFailureAttempts-1; i++ {
		delay, err := service.RecordLoginFailure(ctx, fmt.Sprintf("spray-%d", i), "203.0.113.10")
		if err != nil || delay != 0 {
			t.Fatalf("IP failure %d delay=%v err=%v", i+1, delay, err)
		}
	}
	delay, err := service.RecordLoginFailure(ctx, "spray-final", "203.0.113.10")
	if err != nil || delay <= 0 {
		t.Fatalf("IP threshold should block: delay=%v err=%v", delay, err)
	}
	if delay, err := service.CheckLoginAllowed(ctx, "unrelated-user", "203.0.113.10"); err != nil || delay <= 0 {
		t.Fatalf("IP bucket should affect another username: delay=%v err=%v", delay, err)
	}

	// 公开注册允许 5 次业务尝试，第 6 次开始阻断。
	for i := 0; i < registrationAllowedAttempts; i++ {
		delay, err := service.RecordRegistrationAttempt(ctx, "203.0.113.20")
		if err != nil || delay != 0 {
			t.Fatalf("registration attempt %d delay=%v err=%v", i+1, delay, err)
		}
	}
	delay, err = service.RecordRegistrationAttempt(ctx, "203.0.113.20")
	if err != nil || delay < time.Minute {
		t.Fatalf("registration threshold should block: delay=%v err=%v", delay, err)
	}
}

const (
	accountFailureAttempts      = 8
	ipFailureAttempts           = 30
	registrationAllowedAttempts = 5
)

func loginLimitTestService(t *testing.T) (*auth.Service, func()) {
	t.Helper()
	dsn := os.Getenv("AIGO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set AIGO_TEST_POSTGRES_DSN to run login limiter integration tests")
	}
	ctx := context.Background()
	adminDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := fmt.Sprintf("aigo_login_limit_%d", time.Now().UnixNano())
	if _, err := adminDB.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schemaName)); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	store, err := postgres.New(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	schemaSQL, err := os.ReadFile("../storage/postgres/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitSchema(string(schemaSQL)); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = store.Close()
		_, _ = adminDB.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+pq.QuoteIdentifier(schemaName)+` CASCADE`)
		_ = adminDB.Close()
	}
	return auth.NewService(store.DB(), "test-jwt-secret", time.Hour), cleanup
}
