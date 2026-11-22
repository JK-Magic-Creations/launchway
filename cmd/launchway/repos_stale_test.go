package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// repos branches stale lists, and deletes only the listed names, only with --delete --yes.
func TestCLIStaleBranches(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		bodies = append(bodies, in)
		if in["confirm"] == true {
			_, _ = w.Write([]byte(`{"stale_days":90,"candidates":["old/a"],"deleted":["old/a"],"skipped":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"stale_days":90,"candidates":["old/a"],"deleted":[],"skipped":{}}`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "t"}, &out)
	if err := cl.reposBranches([]string{"stale", "acme/shop", "--delete"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "old/a") || !strings.Contains(out.String(), "Add --yes") || len(bodies) != 1 {
		t.Fatalf("without --yes nothing is deleted: %q %v", out.String(), bodies)
	}
	out.Reset()
	bodies = nil
	if err := cl.reposBranches([]string{"stale", "acme/shop", "--delete", "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deleted old/a") || len(bodies) != 2 || bodies[1]["confirm"] != true {
		t.Fatalf("with --yes: %q %v", out.String(), bodies)
	}
}

// repos branches start says the name with --dry-run and makes the branch otherwise.
func TestCLIStartBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/repositories/acme/shop/branches/suggest" && r.URL.Query().Get("item") == "PAY-1":
			_, _ = w.Write([]byte(`{"item":"PAY-1","name":"pay-1-round","from":"main"}`))
		case r.Method == "POST" && r.URL.Path == "/api/v1/repositories/acme/shop/branches/from-item":
			_, _ = w.Write([]byte(`{"name":"pay-1-round","commit":"abcdef0123456789","item":"PAY-1","linked":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "t"}, &out)
	if err := cl.reposBranches([]string{"start", "acme/shop", "PAY-1", "--dry-run"}); err != nil || strings.TrimSpace(out.String()) != "pay-1-round (from main)" {
		t.Fatalf("dry run: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cl.reposBranches([]string{"start", "acme/shop", "PAY-1"}); err != nil || !strings.Contains(out.String(), "Made the branch pay-1-round at abcdef0123 for PAY-1.") {
		t.Fatalf("start: %q %v", out.String(), err)
	}
}
