package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIOverview(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/owners/acme/activity":
			if r.URL.Query().Get("before") == "" {
				_, _ = w.Write([]byte(`{"items":[{"kind":"push","at":1700000000000,"actor":"ada","repository":"acme/shop","title":"main"}],"next":"1700000000000.push-1"}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"kind":"pull_opened","at":1690000000000,"actor":"bob","repository":"acme/shop","title":"Fix","number":3}]}`))
		case "/api/v1/repositories/acme/shop/languages":
			_, _ = w.Write([]byte(`{"commit":"abc","languages":[{"name":"Go","bytes":600,"files":2,"share":60}],"excluded_files":4,"rules":"built-in"}`))
		case "/api/v1/repositories/acme/shop/contributors":
			polls++
			if polls < 2 {
				_, _ = w.Write([]byte(`{"state":"computing","period":"90d"}`))
				return
			}
			_, _ = w.Write([]byte(`{"state":"done","period":"90d","walked":5,"at_least":true,"contributors":[{"Name":"Ada","Email":"ada@x","Commits":5}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "t"}, &out)
	if err := cl.reposActivity([]string{"acme", "--all"}); err != nil || !strings.Contains(out.String(), "push") || !strings.Contains(out.String(), "#3 Fix") {
		t.Fatalf("activity over all pages: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cl.reposLanguages([]string{"acme/shop"}); err != nil || !strings.Contains(out.String(), "Go") || !strings.Contains(out.String(), "60.00%") {
		t.Fatalf("languages: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cl.reposContributors([]string{"acme/shop"}); err != nil || !strings.Contains(out.String(), "being made") {
		t.Fatalf("contributors while computing: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cl.reposContributors([]string{"acme/shop"}); err != nil || !strings.Contains(out.String(), "5+") || !strings.Contains(out.String(), "at least") {
		t.Fatalf("contributors cut: %q %v", out.String(), err)
	}
}
