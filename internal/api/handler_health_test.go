package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type readinessCheckerFunc func(context.Context) error

func (f readinessCheckerFunc) CheckReadiness(ctx context.Context) error { return f(ctx) }

func TestLivenessIsPublicAndDoesNotCheckDependencies(t *testing.T) {
	called := false
	server := &Server{readinessChecker: readinessCheckerFunc(func(context.Context) error {
		called = true
		return errors.New("should not run")
	})}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if recorder.Code != http.StatusOK || called {
		t.Fatalf("liveness code=%d dependencyCalled=%v", recorder.Code, called)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected liveness body: %s", recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("liveness response must not be cached")
	}
}

func TestReadinessReportsHealthyDatabase(t *testing.T) {
	server := &Server{readinessChecker: readinessCheckerFunc(func(context.Context) error { return nil })}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"database":"ok"`) {
		t.Fatalf("readiness code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestReadinessHidesDependencyError(t *testing.T) {
	secretError := "postgres://user:secret@db.internal/aigo"
	server := &Server{readinessChecker: readinessCheckerFunc(func(context.Context) error { return errors.New(secretError) })}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness code=%d, want 503", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), secretError) || !strings.Contains(recorder.Body.String(), `"database":"failed"`) {
		t.Fatalf("readiness leaked dependency error or omitted check: %s", recorder.Body.String())
	}
}

func TestReadinessTimesOut(t *testing.T) {
	server := &Server{
		readinessTimeout: 10 * time.Millisecond,
		readinessChecker: readinessCheckerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	}
	recorder := httptest.NewRecorder()
	started := time.Now()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness code=%d, want 503", recorder.Code)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("readiness timeout took too long: %v", elapsed)
	}
}

func TestReadinessWithoutCheckerIsUnavailable(t *testing.T) {
	server := &Server{}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"database":"unavailable"`) {
		t.Fatalf("readiness code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
