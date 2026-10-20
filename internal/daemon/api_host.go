package daemon

import (
	"encoding/json/v2"
	"net"
	"net/http"
	"strings"
)

// apiHostAllowed reports whether hostHeader may call /api on this daemon.
// DNS rebinding sends a foreign Host to a loopback listener. localhost,
// 127.0.0.1, ::1, and the configured bind address are the only names that
// may appear, with or without a port.
func apiHostAllowed(hostHeader, bindAddr string) bool {
	hostHeader = strings.TrimSpace(hostHeader)
	if hostHeader == "" {
		return false
	}
	if bindAddr != "" && strings.EqualFold(hostHeader, strings.TrimSpace(bindAddr)) {
		return true
	}
	name := requestHostName(hostHeader)
	if name == "" {
		return false
	}
	switch strings.ToLower(name) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if bindHost := requestHostName(bindAddr); bindHost != "" && strings.EqualFold(name, bindHost) {
		return true
	}
	return false
}

func requestHostName(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(hostport, "[]")
}

// withAPIHostAllowlist rejects /api requests whose Host header is not a
// loopback name or the daemon bind address. Other paths are unchanged.
func withAPIHostAllowlist(next http.Handler, bindAddr string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !apiHostAllowed(r.Host, bindAddr) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.MarshalWrite(w, ErrorResponse{Error: "invalid host"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
