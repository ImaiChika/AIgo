package api

import (
	"net"
	"net/http"
	"strings"
)

func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxyHeaders {
		forwarded := strings.TrimSpace(r.Header.Get("X-AIgo-Client-IP"))
		if parsed := net.ParseIP(forwarded); parsed != nil {
			return parsed.String()
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		if parsed := net.ParseIP(host); parsed != nil {
			return parsed.String()
		}
	}
	if parsed := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); parsed != nil {
		return parsed.String()
	}
	return "unknown"
}
