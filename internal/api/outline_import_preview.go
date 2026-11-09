package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/knowledge"
	"aigo/internal/storage"
)

type outlineImportClaims struct {
	User, Role, Version, Mode, DocumentHash, StateHash string
	Expires                                            int64
}

func (s *Server) signOutlinePreview(r *http.Request, version string, p *knowledge.ImportPreview) string {
	c := outlineImportClaims{User: auth.GetUserID(r.Context()), Role: auth.GetRole(r.Context()), Version: version, Mode: p.Mode, DocumentHash: p.DocumentHash, StateHash: p.StateHash, Expires: time.Now().Add(10 * time.Minute).Unix()}
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.authSvc.OutlineImportSigningKey())
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifyOutlinePreview(r *http.Request, token, version, mode string) (storage.KnowledgeImportExpectation, error) {
	invalid := func() (storage.KnowledgeImportExpectation, error) {
		return storage.KnowledgeImportExpectation{}, fmt.Errorf("%w: 预览凭证无效、已过期或目标已变化，请重新预览", storage.ErrKnowledgeConflict)
	}
	if len(token) > 4096 {
		return invalid()
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return invalid()
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return invalid()
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return invalid()
	}
	mac := hmac.New(sha256.New, s.authSvc.OutlineImportSigningKey())
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return invalid()
	}
	var c outlineImportClaims
	if json.Unmarshal(raw, &c) != nil || c.Expires <= time.Now().Unix() || c.User != auth.GetUserID(r.Context()) || c.Role != auth.GetRole(r.Context()) || c.Version != version || c.Mode != mode || c.DocumentHash == "" || c.StateHash == "" {
		return invalid()
	}
	return storage.KnowledgeImportExpectation{DocumentHash: c.DocumentHash, StateHash: c.StateHash}, nil
}

// Preserve status classifications while redacting temporary filesystem paths.
type outlinePublicError struct {
	message string
	cause   error
}

func (e outlinePublicError) Error() string { return e.message }
func (e outlinePublicError) Unwrap() error { return e.cause }
