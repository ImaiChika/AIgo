package llm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func TestEndpointRecoveryIsolatesPromptsCredentialsAndProviders(t *testing.T) {
	g := NewEndpointRecovery()
	cfg := QwenConfig{BaseURL: "http://mock/v1", APIKey: "one"}
	fail := &HTTPStatusError{Status: 503, Message: "outage"}
	one := sha256.Sum256([]byte("same prompt"))
	for i := 0; i < 5; i++ {
		g.observe(cfg, one, fail)
	}
	if g.remaining(cfg) > 0 {
		t.Fatal("one failing prompt paused others")
	}
	g.observe(cfg, sha256.Sum256([]byte("second")), fail)
	g.observe(cfg, sha256.Sum256([]byte("third")), fail)
	if g.remaining(cfg) <= 0 {
		t.Fatal("distinct failed requests did not pause")
	}
	other := cfg
	other.APIKey = "two"
	if g.remaining(other) > 0 {
		t.Fatal("credentials not isolated")
	}
	other = cfg
	other.Model = "another-model"
	if g.remaining(other) > 0 {
		t.Fatal("a different model was paused")
	}
	other = cfg
	other.BaseURL = "http://other/v1"
	if g.remaining(other) > 0 {
		t.Fatal("provider not isolated")
	}
	g.observe(cfg, one, nil)
	if g.remaining(cfg) > 0 {
		t.Fatal("successful in-flight call did not restore service")
	}
	for i := 0; i < 3; i++ {
		g.observe(cfg, sha256.Sum256([]byte{byte(i)}), errors.New("bad output JSON"))
	}
	if g.remaining(cfg) > 0 {
		t.Fatal("malformed output paused endpoint")
	}
}
func TestEndpointRecoveryWaitCanBeCancelledAndExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := NewEndpointRecovery()
		cfg := QwenConfig{BaseURL: "http://mock/v1"}
		for i := 0; i < 3; i++ {
			g.observe(cfg, sha256.Sum256([]byte{byte(i)}), &HTTPStatusError{Status: 429})
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if !errors.Is(g.wait(ctx, cfg), context.Canceled) {
			t.Fatal("wait ignores cancellation")
		}
		started := time.Now()
		if err := g.wait(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if time.Since(started) != 5*time.Second {
			t.Fatalf("unexpected recovery delay %s", time.Since(started))
		}
	})
}

func TestEndpointRecoveryDoesNotTreatTaskBudgetsAsProviderOutage(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, context.Canceled} {
		t.Run(err.Error(), func(t *testing.T) {
			g := NewEndpointRecovery()
			cfg := QwenConfig{BaseURL: "http://shared/v1", Model: "same-generation-and-learning"}
			for i := 0; i < 3; i++ {
				g.observe(cfg, sha256.Sum256([]byte{byte(i)}), fmt.Errorf("stream read: %w", err))
			}
			if g.remaining(cfg) > 0 {
				t.Fatal("a task reaching its own budget blocked unrelated requests")
			}
		})
	}
}
