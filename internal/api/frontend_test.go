package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticFrontendServesAssetsAndSPAFallback(t *testing.T) {
	distDir := t.TempDir()
	mustWriteFrontendFile(t, distDir, "index.html", "<html>AIgo production shell</html>")
	mustWriteFrontendFile(t, distDir, "assets/app-a1b2.js", "console.log('aigo')")
	mustWriteFrontendFile(t, distDir, ".env", "JWT_SECRET=must-not-leak")

	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"backend":true}`))
	})
	handler, err := WithStaticFrontend(backend, distDir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		method      string
		path        string
		status      int
		body        string
		cacheHeader string
	}{
		{name: "root", method: http.MethodGet, path: "/", status: 200, body: "AIgo production shell", cacheHeader: "no-cache"},
		{name: "history route", method: http.MethodGet, path: "/review-results", status: 200, body: "AIgo production shell", cacheHeader: "no-cache"},
		{name: "personal dashboard route", method: http.MethodGet, path: "/my", status: 200, body: "AIgo production shell", cacheHeader: "no-cache"},
		{name: "personal settings route", method: http.MethodGet, path: "/settings", status: 200, body: "AIgo production shell", cacheHeader: "no-cache"},
		{name: "head history route", method: http.MethodHead, path: "/bank", status: 200, cacheHeader: "no-cache"},
		{name: "hashed asset", method: http.MethodGet, path: "/assets/app-a1b2.js", status: 200, body: "console.log", cacheHeader: "public, max-age=31536000, immutable"},
		{name: "missing asset", method: http.MethodGet, path: "/assets/missing.js", status: 404},
		{name: "missing extensionless asset", method: http.MethodGet, path: "/assets/missing", status: 404},
		{name: "hidden file", method: http.MethodGet, path: "/.env", status: 404},
		{name: "post frontend route", method: http.MethodPost, path: "/login", status: 405},
		{name: "api stays backend", method: http.MethodGet, path: "/api/not-found", status: http.StatusTeapot, body: "backend"},
		{name: "health stays backend", method: http.MethodGet, path: "/health/unknown", status: http.StatusTeapot, body: "backend"},
		{name: "removed legacy resource stays backend", method: http.MethodGet, path: "/images/item/legacy", status: http.StatusTeapot, body: "backend"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
			if recorder.Code != tc.status {
				t.Fatalf("status=%d, want %d; body=%s", recorder.Code, tc.status, recorder.Body.String())
			}
			if tc.body != "" && !strings.Contains(recorder.Body.String(), tc.body) {
				t.Fatalf("body=%q does not contain %q", recorder.Body.String(), tc.body)
			}
			if tc.cacheHeader != "" && recorder.Header().Get("Cache-Control") != tc.cacheHeader {
				t.Fatalf("Cache-Control=%q, want %q", recorder.Header().Get("Cache-Control"), tc.cacheHeader)
			}
		})
	}
}

func TestStaticFrontendRequiresValidBuild(t *testing.T) {
	backend := http.NotFoundHandler()
	if _, err := WithStaticFrontend(backend, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing static directory must fail startup")
	}
	emptyDir := t.TempDir()
	if _, err := WithStaticFrontend(backend, emptyDir); err == nil {
		t.Fatal("directory without index.html must fail startup")
	}
}

func mustWriteFrontendFile(t *testing.T, root, name, contents string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
