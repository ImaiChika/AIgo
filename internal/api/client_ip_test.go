package api

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPTrustsProxyHeaderOnlyWhenConfigured(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "10.0.0.5:4321"
	request.Header.Set("X-AIgo-Client-IP", "203.0.113.9")

	if got := (&Server{}).clientIP(request); got != "10.0.0.5" {
		t.Fatalf("untrusted proxy header used: %q", got)
	}
	if got := (&Server{trustProxyHeaders: true}).clientIP(request); got != "203.0.113.9" {
		t.Fatalf("trusted proxy header ignored: %q", got)
	}
}

func TestClientIPRejectsMalformedProxyHeader(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "[2001:db8::2]:1234"
	request.Header.Set("X-AIgo-Client-IP", "not-an-ip, 127.0.0.1")
	if got := (&Server{trustProxyHeaders: true}).clientIP(request); got != "2001:db8::2" {
		t.Fatalf("malformed proxy header should fall back to remote address, got %q", got)
	}
}
