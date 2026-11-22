package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Secure is the rule; plain HTTP from this machine to a loopback name is the
// one documented exception.
func TestCookieSecureDefaultsOn(t *testing.T) {
	s := &Server{}
	for _, c := range []struct {
		name, host, remote string
		tls                bool
		want               bool
	}{
		{"plain http from another machine", "launchway.example", "192.0.2.1:5555", false, true},
		{"plain http, loopback name but remote peer", "localhost:8080", "192.0.2.1:5555", false, true},
		{"plain http, loopback peer but public name", "launchway.example", "127.0.0.1:5555", false, true},
		{"localhost over plain http", "localhost:8080", "127.0.0.1:5555", false, false},
		{"loopback IPv6 over plain http", "[::1]:8080", "[::1]:5555", false, false},
		{"https", "launchway.example", "192.0.2.1:5555", true, true},
	} {
		r := httptest.NewRequest("GET", "http://"+c.host+"/", nil)
		r.RemoteAddr = c.remote
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := s.cookie(r, "x", "v", "/", 60e9).Secure; got != c.want {
			t.Errorf("%s: Secure = %v, want %v", c.name, got, c.want)
		}
	}
	r := httptest.NewRequest("GET", "http://launchway.example/", nil)
	c := s.cookie(r, "x", "v", "/", 60e9)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie lacks HttpOnly or SameSite: %+v", c)
	}
}
