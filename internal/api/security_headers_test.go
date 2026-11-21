package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func headersOf(s *Server, req *http.Request) http.Header {
	h := s.hsts(securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result().Header
}

// The web UI's response headers are a security property: tests, not a hope.
func TestUIResponseHeaders(t *testing.T) {
	h := headersOf(&Server{}, httptest.NewRequest(http.MethodGet, "/", nil))
	for k, want := range map[string]string{
		"X-Frame-Options":            "DENY",
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "same-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(h.Get("Permissions-Policy"), "camera=()") {
		t.Errorf("Permissions-Policy = %q", h.Get("Permissions-Policy"))
	}
	csp := h.Get("Content-Security-Policy")
	for _, must := range []string{"default-src 'self'", "script-src 'self'", "object-src 'none'", "base-uri 'none'", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, must) {
			t.Errorf("Content-Security-Policy lacks %q: %s", must, csp)
		}
	}
	// Scripts must never be allowed to run inline or from eval, or to come from anywhere else.
	script := ""
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
			script = d
		}
	}
	if script != "script-src 'self'" {
		t.Errorf("script-src = %q: only 'self' is allowed", script)
	}
	if strings.Contains(csp, "*") || strings.Contains(csp, "http:") {
		t.Errorf("Content-Security-Policy has a wildcard or plain-HTTP source: %s", csp)
	}
}

func TestStrictTransportSecurityOnlyOverHTTPS(t *testing.T) {
	plain := headersOf(&Server{}, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := plain.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("sent over plain HTTP: %q", got)
	}
	tls := httptest.NewRequest(http.MethodGet, "https://launchway.example/", nil)
	if got := headersOf(&Server{}, tls).Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=") {
		t.Errorf("not sent over HTTPS: %q", got)
	}
	// Behind a proxy it is sent only if the proxy is trusted: a client cannot claim HTTPS.
	fwd := httptest.NewRequest(http.MethodGet, "/", nil)
	fwd.Header.Set("X-Forwarded-Proto", "https")
	if got := headersOf(&Server{}, fwd).Get("Strict-Transport-Security"); got != "" {
		t.Errorf("an untrusted X-Forwarded-Proto turned it on: %q", got)
	}
	if got := headersOf(&Server{TrustProxy: true}, fwd).Get("Strict-Transport-Security"); got == "" {
		t.Error("a trusted proxy's X-Forwarded-Proto: https did not turn it on")
	}
}
