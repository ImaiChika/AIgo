package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aigo/internal/auth"
)

func TestReportedIdentityTransitionResponses(t *testing.T) {
	server, store, cleanup := authHandlerTestServer(t)
	defer cleanup()
	handler := server.Handler()
	token := loginForAuthTest(t, handler, "admin", "admin-password", "198.51.100.200")
	// Recreate the auth service with the same persisted database and signing
	// secret. This is not an OS/process restart or a browser logout simulation.
	server.authSvc = auth.NewService(store.DB(), "auth-handler-test-secret", time.Hour)
	for _, tc := range []struct {
		name, token string
		status      int
	}{{"valid after auth recreation", token, http.StatusOK}, {"missing credential", "", http.StatusUnauthorized}, {"invalid credential", "invalid-token", http.StatusUnauthorized}} {
		t.Run(tc.name, func(t *testing.T) {
			response := serveAuthJSON(t, handler, http.MethodGet, "/api/users", tc.token, "198.51.100.200", nil)
			if response.Code != tc.status {
				t.Fatal(response.Code, response.Body)
			}
		})
	}
	t.Run("valid low privilege login still returns forbidden", func(t *testing.T) {
		response := serveAuthJSON(t, handler, http.MethodPost, "/api/auth/register", "", "198.51.100.201", map[string]any{"username": "transition-low", "password": "transition-user-password"})
		if response.Code != http.StatusCreated {
			t.Fatal(response.Code, response.Body)
		}
		lowToken := loginForAuthTest(t, handler, "transition-low", "transition-user-password", "198.51.100.201")
		response = serveAuthJSON(t, handler, http.MethodGet, "/api/users", lowToken, "198.51.100.201", nil)
		if response.Code != http.StatusForbidden {
			t.Fatal(response.Code, response.Body)
		}
	})
	// Cancellation is a client-side request outcome, not an internal failure or
	// an invalid login. The cancelled query must never start the route handler.
	t.Run("cancelled permission lookup preserves login and is classified separately", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		request := httptest.NewRequest(http.MethodGet, "/api/users", nil).WithContext(ctx)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 499 || !strings.Contains(response.Body.String(), "请求已取消") {
			t.Fatal(response.Code, response.Body)
		}
		active := serveAuthJSON(t, handler, http.MethodGet, "/api/users", token, "198.51.100.200", nil)
		if active.Code != http.StatusOK {
			t.Fatal("cancellation invalidated an otherwise valid login", active.Code, active.Body)
		}
	})
}
