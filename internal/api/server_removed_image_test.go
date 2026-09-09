package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"aigo/internal/domain"
)

func TestRemovedQuestionImageSurfaceIsUnavailable(t *testing.T) {
	handler := (&Server{}).Handler()
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/images/prompt"},
		{http.MethodPost, "/api/images/generate"},
		{http.MethodGet, "/api/images/question-1"},
		{http.MethodPost, "/api/images/review"},
		{http.MethodGet, "/images/item/image-1"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s status=%d, want 404", tc.method, tc.path, recorder.Code)
		}
	}

	for _, permission := range domain.AllPermissions() {
		if permission.Code == "image:generate" || permission.Code == "image:review" {
			t.Errorf("removed permission remains assignable: %s", permission.Code)
		}
	}
}
