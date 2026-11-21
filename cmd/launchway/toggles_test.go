package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
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

// The command line makes a toggle, rolls it out to a percentage while keeping its rules, refuses without a reason,
// shows a change that waits and who approves it, reverts, and compares and applies a file.
func TestCLIToggles(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	no := false
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet, AllowSelfApproval: &no, Environments: []engine.Environment{{Name: "production", Target: "x", Projects: []string{"shop"}, Protected: true}, {Name: "dev", Target: "x", Projects: []string{"shop"}}}})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := source.NewManager(eng, filepath.Join(t.TempDir(), "data"), []source.Project{{Name: "shop", URL: t.TempDir(), DefaultBranch: "main"}}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	pol, _ := auth.NewPolicy([]auth.Binding{{Group: "ops", Role: auth.Operator}, {Group: "root", Role: auth.Admin}})
	s := &api.Server{Engine: eng, Projects: mgr, Log: quiet, RunnerToken: "r", Auth: &api.Auth{Policy: pol, IdleTimeout: time.Hour, Lifetime: time.Hour}}
	s.Toggles = newToggles(eng, nil)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	now := time.Now().UnixMilli()
	clientFor := func(user, group string, out *bytes.Buffer) *client {
		if err := eng.SignIn(user, []string{group}, engine.Profile{}, "session-"+user, now, now+3600_000, 0); err != nil {
			t.Fatal(err)
		}
		tok := "lw_" + "clitog" + user + strings.Repeat("0", 20)
		if err := eng.CreateToken(engine.APIToken{ID: "tok_" + user, Name: user, User: user, CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
			t.Fatal(err)
		}
		return newClient(credentials{Server: srv.URL, Token: tok}, out)
	}
	var oo, ao bytes.Buffer
	otto, ada := clientFor("otto", "ops", &oo), clientFor("ada", "root", &ao)

	if err := otto.togglesCreate([]string{"shop", "checkout-v2", "--type", "boolean", "--owner", "team-pay", "--reason", "new"}); err != nil {
		t.Fatal(err)
	}
	if err := otto.togglesSet([]string{"shop", "checkout-v2", "dev", "--on"}); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("a change with no reason: %v", err)
	}
	// Rules in a file are kept when the toggle is rolled out to a percentage.
	rules := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(rules, []byte("on: true\noff_value: false\nrules:\n  - id: staff\n    clauses: [{attribute: email, op: suffix, values: ['@corp.example']}]\n    serve: {value: true}\nfallthrough: {value: false}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := otto.togglesRules([]string{"shop", "checkout-v2", "dev", "-f", rules, "--reason", "staff first"}); err != nil {
		t.Fatal(err)
	}
	if err := otto.togglesSet([]string{"shop", "checkout-v2", "dev", "--percent", "25", "--reason", "widen"}); err != nil {
		t.Fatal(err)
	}
	oo.Reset()
	if err := otto.togglesGet([]string{"shop", "checkout-v2"}); err != nil {
		t.Fatal(err)
	}
	if got := oo.String(); !strings.Contains(got, "1 rule(s), then 25% true, 75% false") || !strings.Contains(got, "rule staff") {
		t.Fatalf("the rule was lost, or the rollout is not what was asked:\n%s", got)
	}
	// Production is protected: the change waits, and says who approves it.
	oo.Reset()
	if err := otto.togglesSet([]string{"shop", "checkout-v2", "production", "--on", "--reason", "launch"}); err != nil {
		t.Fatal(err)
	}
	if got := oo.String(); !strings.Contains(got, "Not made yet") || !strings.Contains(got, "launchway toggles approve shop") {
		t.Fatalf("%s", got)
	}
	var id string
	oo.Reset()
	if err := otto.togglesGeneric([]string{"shop"}, "waiting", "GET", "/toggle-changes"); err != nil {
		t.Fatal(err)
	}
	if i := strings.Index(oo.String(), `"id": `); i >= 0 {
		id = strings.Fields(strings.TrimLeft(oo.String()[i+6:], " "))[0]
		id = strings.TrimRight(id, ",")
	}
	if id == "" {
		t.Fatalf("no change waits:\n%s", oo.String())
	}
	if err := otto.togglesChange([]string{"shop", id}, "approve"); err == nil {
		t.Fatal("the person who asked approved their own change")
	}
	if err := ada.togglesChange([]string{"shop", id}, "approve"); err != nil {
		t.Fatal(err)
	}
	// The file: export, compare (nothing to do), change the file, compare, apply.
	exp := filepath.Join(t.TempDir(), "toggles.yaml")
	if err := otto.togglesExport([]string{"shop", "-o", exp}); err != nil {
		t.Fatal(err)
	}
	oo.Reset()
	if err := otto.togglesFile([]string{"shop", "-f", exp}, "plan"); err != nil || !strings.Contains(oo.String(), "agree") {
		t.Fatalf("%v %s", err, oo.String())
	}
	b, _ := os.ReadFile(exp) //nolint:gosec // the test's own file
	changed := strings.Replace(string(b), "owner: team-pay", "owner: team-checkout", 1)
	if err := os.WriteFile(exp, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	oo.Reset()
	if err := otto.togglesFile([]string{"shop", "-f", exp}, "plan"); err != nil || !strings.Contains(oo.String(), "differs in owner") {
		t.Fatalf("%v %s", err, oo.String())
	}
	if err := otto.togglesFile([]string{"shop", "-f", exp, "--reason", "from the repository"}, "apply"); err != nil {
		t.Fatal(err)
	}
	oo.Reset()
	if err := otto.togglesGet([]string{"shop", "checkout-v2"}); err != nil || !strings.Contains(oo.String(), "owner: team-checkout") {
		t.Fatalf("%v %s", err, oo.String())
	}
	// Keys: only an admin makes one, and the text is printed once.
	if err := otto.togglesKeys([]string{"create", "shop", "dev", "--kind", "server"}); err == nil {
		t.Fatal("an operator made a key")
	}
	ao.Reset()
	if err := ada.togglesKeys([]string{"create", "shop", "dev", "--kind", "server", "--name", "api"}); err != nil || !strings.HasPrefix(ao.String(), "lwt_srv_") {
		t.Fatalf("%v %s", err, ao.String())
	}
}
