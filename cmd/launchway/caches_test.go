package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// caches list shows what is kept against the quota, delete ends an entry or
// a whole cache for an operator and is refused for a viewer, and the
// policy is changed by an administrator only; each is in the audit trail.
func TestCLICaches(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := source.NewManager(eng, filepath.Join(t.TempDir(), "data"), []source.Project{{Name: "shop", URL: t.TempDir(), DefaultBranch: "main"}}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := auth.NewPolicy([]auth.Binding{{Group: "eyes", Role: auth.Viewer}, {Group: "ops", Role: auth.Operator}, {Group: "root", Role: auth.Admin}})
	if err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Engine: eng, Projects: mgr, Log: quiet, RunnerToken: "r",
		Auth: &api.Auth{Policy: pol, IdleTimeout: time.Hour, Lifetime: time.Hour}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	now := time.Now().UnixMilli()
	clientFor := func(user, group string, out *bytes.Buffer) *client {
		if err := eng.SignIn(user, []string{group}, engine.Profile{}, "session-"+user, now, now+3600_000, 0); err != nil {
			t.Fatal(err)
		}
		tok := "lw_" + "clica" + user + strings.Repeat("0", 20)
		if err := eng.CreateToken(engine.APIToken{ID: "tok_" + user, Name: user, User: user, CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
			t.Fatal(err)
		}
		return newClient(credentials{Server: srv.URL, Token: tok}, out)
	}
	var vo, oo, ao bytes.Buffer
	vic, otto, ada := clientFor("vic", "eyes", &vo), clientFor("otto", "ops", &oo), clientFor("ada", "root", &ao)

	if err := vic.cachesList([]string{"shop"}); err != nil || !strings.Contains(vo.String(), "Nothing is cached.") || !strings.Contains(vo.String(), "0 entries, 0 bytes of 10.0 GiB") {
		t.Fatalf("list: %v %q", err, vo.String())
	}
	if err := vic.cachesList(nil); err == nil {
		t.Fatal("list without a project")
	}
	// Deleting needs operate; nothing is there, so an operator is told so.
	if err := vic.cachesDelete([]string{"shop", "--name", "pip"}); err == nil || !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "role") && !strings.Contains(err.Error(), "operate") {
		t.Fatalf("a viewer deleted, or the refusal says nothing: %v", err)
	}
	if err := otto.cachesDelete([]string{"shop", "--name", "pip"}); err == nil || !strings.Contains(err.Error(), "no cache called pip") {
		t.Fatalf("%v", err)
	}
	for _, args := range [][]string{{"shop"}, {"shop", "--name", "a", "--id", "b"}, {}} {
		if err := otto.cachesDelete(args); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}

	// The policy: an operator reads, an administrator changes.
	if err := otto.cachesPolicyGet([]string{"shop"}); err != nil || !strings.Contains(oo.String(), "Caching is on (default)") || !strings.Contains(oo.String(), "at most 10 GiB") {
		t.Fatalf("%v %q", err, oo.String())
	}
	if err := otto.cachesPolicySet([]string{"--idle-days", "3"}); err == nil {
		t.Fatal("an operator changed the policy")
	}
	if err := ada.cachesPolicySet([]string{"--idle-days", "3", "--max-project-gib", "20"}); err != nil || !strings.Contains(ao.String(), "at most 20 GiB") || !strings.Contains(ao.String(), "after 3 days") {
		t.Fatalf("%v %q", err, ao.String())
	}
	ao.Reset()
	if err := ada.cachesPolicySet([]string{"shop", "--enabled=false"}); err != nil || !strings.Contains(ao.String(), "Caching is OFF (project)") || !strings.Contains(ao.String(), "after 3 days") {
		t.Fatalf("a project's policy starts from what applies to it: %v %q", err, ao.String())
	}
	if err := vic.cachesList([]string{"shop"}); err != nil || !strings.Contains(vo.String(), "Caching is turned off for this project") {
		t.Fatalf("%v %q", err, vo.String())
	}
	if err := ada.cachesPolicySet([]string{"shop", "--max-entry-gib", "400"}); err == nil {
		t.Fatal("an entry limit above the quota was accepted")
	}
	if err := ada.cachesPolicySet([]string{"nope", "--idle-days", "2"}); err == nil {
		t.Fatal("a policy for a project that does not exist was accepted")
	}
	if err := ada.cachesPolicySet(nil); err == nil {
		t.Fatal("set with nothing to set was accepted")
	}
	ao.Reset()
	if err := ada.cachesPolicyClear([]string{"shop"}); err != nil || !strings.Contains(ao.String(), "Caching is on (installation)") {
		t.Fatalf("%v %q", err, ao.String())
	}
	for _, action := range []string{"cache_policy.set", "cache_policy.set_project", "cache_policy.clear_project"} {
		entries, err := eng.AuditLog(engine.AuditFilter{Action: action})
		found := false
		for _, e := range entries {
			found = found || e.Outcome == "ok" && strings.Contains(e.Detail, "->")
		}
		if err != nil || !found {
			t.Fatalf("audit %s: %v %+v", action, err, entries)
		}
	}
}

// runs show says what each step's caches did.
func TestRunsShowListsCaches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"run_1","project":"shop","number":7,"status":"succeeded","event":"push","branch":"main","sha":"0123456789abcdef","actor":"ann","created_at":1,
			"step_list":[{"id":"test","status":"succeeded","attempt":1,"caches":[
				{"name":"pip","result":"hit","bytes":125829120,"scope":"default"},
				{"name":"npm","result":"saved","restored":"fallback","bytes":2097152,"scope":"pull:3"},
				{"name":"models","result":"not_saved","reason":"a draft run saves no cache"}]}]}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: "t"}, &out)
	if err := cl.show([]string{"run_1"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cache pip: hit, 120.0 MiB, scope default", "cache npm: saved (restored: fallback), 2.0 MiB, scope pull:3", "cache models: not saved: a draft run saves no cache"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}
