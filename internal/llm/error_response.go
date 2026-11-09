package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This limit applies only after a non-success HTTP status is already known.
// Successful answers and active reasoning retain their original full budget.
const providerErrorReadTimeout = 250 * time.Millisecond
const maxProviderErrorBytes = 4096

func readProviderHTTPError(ctx context.Context, response *http.Response, fallback string) error {
	retryAfter := providerRetryAfter(response.Header.Get("Retry-After"), time.Now())
	timer := time.AfterFunc(providerErrorReadTimeout, func() { _ = response.Body.Close() })
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxProviderErrorBytes))
	timer.Stop()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	markModelResponseBody(ctx, body)
	var packet struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage modelUsage `json:"usage"`
	}
	_ = json.Unmarshal(body, &packet)
	recordModelResponse(ctx, response.StatusCode, packet.Usage)
	message := strings.TrimSpace(packet.Error.Message)
	if message == "" {
		message = fallback
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return &HTTPStatusError{Status: response.StatusCode, Message: message, RetryAfter: retryAfter}
}

func providerRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= int64((1<<63-1)/int64(time.Second)) {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}
