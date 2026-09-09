package auth

import (
	"testing"
	"time"
)

func TestExponentialBlockIsProgressiveAndCapped(t *testing.T) {
	base := 5 * time.Second
	maximum := 40 * time.Second
	tests := []struct {
		steps int
		want  time.Duration
	}{
		{0, 5 * time.Second},
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{20, 40 * time.Second},
	}
	for _, test := range tests {
		if got := exponentialBlock(test.steps, base, maximum); got != test.want {
			t.Errorf("steps=%d got=%v want=%v", test.steps, got, test.want)
		}
	}
}

func TestAuditSubjectDoesNotExposeInput(t *testing.T) {
	first := AuditSubject("account", "SensitiveUser")
	second := AuditSubject("account", "SensitiveUser")
	if first != second || len(first) != 12 || first == "SensitiveUser" {
		t.Fatalf("unexpected audit subject: %q %q", first, second)
	}
	if first == AuditSubject("ip", "SensitiveUser") {
		t.Fatal("audit subject must be scoped")
	}
}
