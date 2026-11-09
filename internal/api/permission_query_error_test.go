package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPermissionQueryCancellationClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cancel   bool
		err      error
		wantCode int
	}{
		{"request and query cancelled", true, context.Canceled, 499},
		{"wrapped cancellation", true, fmt.Errorf("lookup: %w", context.Canceled), 499},
		{"real query failure despite cancellation", true, errors.New("database unavailable"), 500},
		{"live request and real query failure", false, errors.New("database unavailable"), 500},
		{"internal cancellation without cancelled request", false, context.Canceled, 500},
		{"deadline is not client cancellation", false, context.DeadlineExceeded, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			server := &Server{}
			handler := server.withRequestObservability(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Pattern = "GET /permission-probe"
				writePermissionQueryError(w, r, tc.err)
			}))
			request := httptest.NewRequest(http.MethodGet, "/permission-probe", nil).WithContext(ctx)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.wantCode || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatal(response.Code, response.Body)
			}
			metrics := server.httpMetrics.snapshot()
			if len(metrics.Routes) != 1 || metrics.Routes[0].Requests != 1 {
				t.Fatal(metrics)
			}
			if tc.wantCode == 499 {
				if metrics.Routes[0].ServerErrors != 0 || metrics.Routes[0].ClientErrors != 1 || !strings.Contains(response.Body.String(), "请求已取消") {
					t.Fatal("cancellation became server failure", response.Body, metrics)
				}
			} else if metrics.Routes[0].ServerErrors != 1 || !strings.Contains(response.Body.String(), "权限查询失败") {
				t.Fatal("real failure was hidden", response.Body, metrics)
			}
		})
	}
}
