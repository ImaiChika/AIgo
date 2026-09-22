package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestObservabilityAddsIDAndRecordsBoundedRoute(t *testing.T) {
	server := &Server{}
	handler := server.Handler()

	request := httptest.NewRequest(http.MethodGet, "/health/live?ignored=secret", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if requestID := recorder.Header().Get("X-Request-ID"); len(requestID) != 32 {
		t.Fatalf("request id=%q", requestID)
	}
	snapshot := server.httpMetrics.snapshot()
	if snapshot.Requests != 1 || len(snapshot.Routes) != 1 {
		t.Fatalf("unexpected metrics: %#v", snapshot)
	}
	if snapshot.Routes[0].Route != "GET /health/live" {
		t.Fatalf("route must use bounded mux pattern, got %q", snapshot.Routes[0].Route)
	}
}

func TestRuntimeMetricsContainsNoConfigurationSecrets(t *testing.T) {
	server := &Server{}
	server.ensureObservability()
	server.httpMetrics.observe("GET /test", http.StatusServiceUnavailable, 12, 0)

	request := httptest.NewRequest(http.MethodGet, "/api/system/runtime-metrics", nil)
	recorder := httptest.NewRecorder()
	server.handleRuntimeMetrics(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"generated_at", "http", "process", "queues"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing %s in %#v", key, body)
		}
	}
	serialized := strings.ToLower(recorder.Body.String())
	for _, forbidden := range []string{"dsn", "api_key", "jwt_secret", "password"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("runtime metrics exposed forbidden text %q", forbidden)
		}
	}
}

func TestRuntimeMetricsRouteRequiresAuthentication(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, "/api/system/runtime-metrics", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
