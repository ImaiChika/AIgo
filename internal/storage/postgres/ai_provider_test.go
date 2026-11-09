package postgres

import "testing"

func TestAIProviderKeyEncryptionRoundTrip(t *testing.T) {
	store := &Store{}
	store.SetLLMEncryptionKey("test-jwt-secret")

	ciphertext, err := store.encryptAIProviderKey("sk-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if ciphertext == "" || ciphertext == "sk-test-secret" {
		t.Fatalf("API Key should be stored as ciphertext, got %q", ciphertext)
	}
	plaintext, err := store.decryptAIProviderKey(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "sk-test-secret" {
		t.Fatalf("round-trip plaintext=%q", plaintext)
	}
}

func TestAIProviderKeyEncryptionRequiresSecret(t *testing.T) {
	store := &Store{}
	if _, err := store.encryptAIProviderKey("sk-test-secret"); err == nil {
		t.Fatal("expected missing encryption key error")
	}
}
