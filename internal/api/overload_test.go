package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestRequestLimiterRejectsAboveGlobalLimitAndKeepsHealthExempt(t *testing.T) {
	limiter := newRequestLimiter(RequestLimitConfig{MaxInFlight: 1, ExpensiveMaxInFlight: 1, HeavyMaxInFlight: 1, RetryAfterSeconds: 2})
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/live" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}))

	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/questions", nil))
	}()
	<-entered

	overloaded := httptest.NewRecorder()
	handler.ServeHTTP(overloaded, httptest.NewRequest(http.MethodGet, "/api/questions", nil))
	if overloaded.Code != http.StatusServiceUnavailable || overloaded.Header().Get("Retry-After") != "2" || overloaded.Header().Get("X-AIgo-Overload-Class") != "global" {
		t.Fatalf("overload status=%d retry=%q body=%s", overloaded.Code, overloaded.Header().Get("Retry-After"), overloaded.Body.String())
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health must bypass overload protection, got %d", health.Code)
	}
	close(release)
	wait.Wait()
	if limiter.snapshot()["global"].Rejected != 1 {
		t.Fatalf("rejected metrics=%#v", limiter.snapshot())
	}
}

func TestRequestLimiterSeparatesHeavyAndNormalTraffic(t *testing.T) {
	limiter := newRequestLimiter(RequestLimitConfig{MaxInFlight: 4, ExpensiveMaxInFlight: 1, HeavyMaxInFlight: 1})
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/export/docx" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}))

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/export/docx", nil))
		close(done)
	}()
	<-entered

	secondHeavy := httptest.NewRecorder()
	handler.ServeHTTP(secondHeavy, httptest.NewRequest(http.MethodPost, "/api/export/docx", nil))
	if secondHeavy.Code != http.StatusServiceUnavailable {
		t.Fatalf("second heavy status=%d", secondHeavy.Code)
	}
	normal := httptest.NewRecorder()
	handler.ServeHTTP(normal, httptest.NewRequest(http.MethodGet, "/api/questions", nil))
	if normal.Code != http.StatusOK {
		t.Fatalf("normal request must remain available, got %d", normal.Code)
	}
	close(release)
	<-done
	if limiter.snapshot()[requestClassHeavy].Rejected != 1 {
		t.Fatalf("heavy metrics=%#v", limiter.snapshot())
	}
}

func TestRequestClassDoesNotTreatAsyncGenerationSubmitAsHeavy(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   string
	}{
		{http.MethodPost, "/api/questions/generate", requestClassNormal},
		{http.MethodPost, "/api/knowledge-points/import", requestClassHeavy},
		{http.MethodPost, "/api/ai-check/progress", requestClassExpensive},
		{http.MethodGet, "/api/questions/search", requestClassExpensive},
		{http.MethodGet, "/api/export/download/result.docx", requestClassHeavy},
	}
	for _, test := range cases {
		request := httptest.NewRequest(test.method, test.path, nil)
		if got := requestClass(request); got != test.want {
			t.Errorf("%s %s class=%q want=%q", test.method, test.path, got, test.want)
		}
	}
}
