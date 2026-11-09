package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestKnownHTTPFailureDoesNotWaitForHangingErrorBody(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tools  NetworkTools
		status int
	}{
		{"summary_chat_429", NetworkTools{}, 429},
		{"responses_503", NetworkTools{WebSearchEnabled: true}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"message":"controlled provider failure"}}`)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			client := NewQwenClient(QwenConfig{APIKey: "controlled", BaseURL: server.URL, Model: "qwen3.5-flash", HTTPTimeout: 3 * time.Second, NetworkTools: tc.tools})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			started := time.Now()
			_, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "controlled request"}}, GenerateOptions{StreamChat: true})
			var status *HTTPStatusError
			t.Logf("status=%d elapsed=%s Retry-After=%s", tc.status, time.Since(started), 2*time.Second)
			if !errors.As(err, &status) || status.Status != tc.status || status.RetryAfter != 2*time.Second || time.Since(started) > time.Second {
				t.Fatalf("known HTTP status lost or delayed: %v elapsed=%s", err, time.Since(started))
			}
		})
	}
}

func TestActualStreamEOFReturnsBeforeAttemptTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrame("partial"))
		w.(http.Flusher).Flush()
	}))
	defer server.Close()
	client := NewQwenClient(QwenConfig{APIKey: "controlled", BaseURL: server.URL, Model: "qwen3.5-flash"})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	got, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "closed stream"}}, GenerateOptions{StreamChat: true})
	if err == nil || got != "" || errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("EOF waited for timeout or exposed partial result: %q %v", got, err)
	}
}

func TestOpenStreamWithoutTerminalMarkerUsesCallBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrame("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := NewQwenClient(QwenConfig{APIKey: "controlled", BaseURL: server.URL, Model: "qwen3.5-flash"})
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	got, err := client.Complete(ctx, []Message{{Role: RoleUser, Content: "open stream"}}, GenerateOptions{StreamChat: true})
	if got != "" || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) < 140*time.Millisecond {
		t.Fatalf("open response was mistaken for EOF: %q %v", got, err)
	}
}

func TestHTTP2ErrorBodyCancellationDoesNotInterruptAnotherCall(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.Messages) > 0 && request.Messages[0].Content == "limited" {
			w.WriteHeader(429)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		time.Sleep(350 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"complete"},"finish_reason":"stop"}]}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := NewQwenClient(QwenConfig{APIKey: "controlled", BaseURL: server.URL, Model: "qwen3.5-flash"})
	client.httpClient = server.Client()
	client.httpClient.Timeout = 3 * time.Second
	failed := make(chan error, 1)
	go func() {
		_, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "limited"}}, GenerateOptions{})
		failed <- err
	}()
	answer, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "valid"}}, GenerateOptions{})
	var status *HTTPStatusError
	if failure := <-failed; !errors.As(failure, &status) || status.Status != 429 {
		t.Fatal("rate limit not retained", failure)
	}
	if err != nil || answer != "complete" {
		t.Fatal("closing one error response interrupted another request", answer, err)
	}
}

func TestProviderRetryAfterHeaderBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		want         time.Duration
	}{
		{"seconds", " 3 ", 3 * time.Second},
		{"http_date", now.Add(4 * time.Second).Format(http.TimeFormat), 4 * time.Second},
		{"past_date", now.Add(-time.Second).Format(http.TimeFormat), 0},
		{"negative", "-2", 0},
		{"overflow", "9223372036854775807", 0},
		{"invalid", "not-a-delay", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := providerRetryAfter(tc.header, now); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestSuccessfulHTTPBodyRetainsBudgetBeyondErrorDetailWindow(t *testing.T) {
	for _, tools := range []NetworkTools{{}, {WebSearchEnabled: true}} {
		name := "chat"
		if tools.WebSearchEnabled {
			name = "responses"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				var request map[string]any
				_ = json.NewDecoder(r.Body).Decode(&request)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				time.Sleep(400 * time.Millisecond)
				if name == "chat" {
					fmt.Fprint(w, `{"choices":[{"message":{"content":"complete answer"},"finish_reason":"stop"}]}`)
				} else {
					fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"complete answer"}]}]}`)
				}
			}))
			defer server.Close()
			client := NewQwenClient(QwenConfig{APIKey: "controlled", BaseURL: server.URL, Model: "qwen3.5-flash", HTTPTimeout: 2 * time.Second, NetworkTools: tools})
			got, err := client.Complete(t.Context(), []Message{{Role: RoleUser, Content: "controlled"}}, GenerateOptions{})
			if err != nil || got != "complete answer" {
				t.Fatal("error-body window interrupted successful response", got, err)
			}
		})
	}
}
