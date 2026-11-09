package review

import (
	"aigo/internal/domain"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func signedRestoreManifest(t *testing.T, svc *Service, manifest restoreManifest) string {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, svc.restoreKey)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestRestorePreviewExpiryAndScopeBinding(t *testing.T) {
	svc := &Service{}
	svc.SetReviewRestoreSigningKey([]byte("restore-protocol-fixture-key-at-least-32"))
	valid := restoreManifest{ActorID: "manager", Identity: "admin", ExpiresAt: time.Now().Add(time.Minute), Targets: []domain.ReviewRestoreTarget{{TaskID: "task", QuestionID: "question", Version: 1}}}
	for _, tc := range []struct {
		name, actor, identity string
		expires               time.Time
		tamper                bool
		empty                 bool
	}{
		{"valid", "manager", "admin", valid.ExpiresAt, false, false}, {"expired", "manager", "admin", time.Now().Add(-time.Minute), false, false}, {"other_actor", "other", "admin", valid.ExpiresAt, false, false}, {"other_identity", "manager", "teacher", valid.ExpiresAt, false, false}, {"tampered", "manager", "admin", valid.ExpiresAt, true, false}, {"empty_manifest", "manager", "admin", valid.ExpiresAt, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := valid
			manifest.ExpiresAt = tc.expires
			if tc.empty {
				manifest.Targets = nil
			}
			token := signedRestoreManifest(t, svc, manifest)
			if tc.tamper {
				token += "x"
			}
			_, err := svc.decodeRestorePreview(token, tc.actor, tc.identity)
			if tc.name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("invalid preview accepted")
			}
		})
	}
}

func TestRestoreRejectsInvalidRequestBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request RestoreRequest
	}{
		{"empty_id", RestoreRequest{Reason: "调整"}}, {"invalid_id", RestoreRequest{OperationID: "bad/op/identifier", Reason: "调整"}}, {"empty_reason", RestoreRequest{OperationID: "valid-operation", Reason: "   "}}, {"long_reason", RestoreRequest{OperationID: "valid-operation", Reason: strings.Repeat("长", 501)}}, {"nul", RestoreRequest{OperationID: "valid-operation", Reason: "原因\x00注入"}}, {"invalid_utf8", RestoreRequest{OperationID: "valid-operation", Reason: string([]byte{255})}}, {"oversized_token", RestoreRequest{OperationID: "valid-operation", Reason: "调整", PreviewToken: strings.Repeat("x", maxRestorePreviewTokenBytes+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&Service{}).RestoreReview(context.Background(), "manager", "admin", tc.request)
			if !errors.Is(err, domain.ErrReviewBadRequest) {
				t.Fatalf("invalid request reached storage: %v", err)
			}
		})
	}
}
