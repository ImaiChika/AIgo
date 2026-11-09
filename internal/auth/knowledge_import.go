package auth

import "crypto/sha256"

// KnowledgeImportSigningKey is isolated from login and review confirmations.
func (s *Service) KnowledgeImportSigningKey() []byte {
	sum := sha256.Sum256(append([]byte("aigo-knowledge-import-preview-v1:"), s.jwtSecret...))
	return append([]byte(nil), sum[:]...)
}

func (s *Service) OutlineImportSigningKey() []byte {
	sum := sha256.Sum256(append([]byte("aigo-outline-import-preview-v1:"), s.jwtSecret...))
	return append([]byte(nil), sum[:]...)
}
