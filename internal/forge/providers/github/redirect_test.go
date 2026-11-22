package github

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newRedirector is a host that answers every request with a redirect to target.
func newRedirector(t *testing.T, target string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(s.Close)
	return s.URL
}
