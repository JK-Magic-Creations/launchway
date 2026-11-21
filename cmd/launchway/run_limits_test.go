package main

import (
	"bytes"
	"io"
	"log/slog"
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

// run-limits get shows the limit to anyone; set changes it for an admin and
// is refused for an operator.
func TestCLIRunLimits(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := source.NewManager(eng, filepath.Join(t.TempDir(), "data"), nil, quiet)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := auth.NewPolicy([]auth.Binding{{Group: "ops", Role: auth.Operator}, {Group: "root", Role: auth.Admin}})
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
		tok := "lw_" + "clirl" + user + strings.Repeat("0", 20)
		if err := eng.CreateToken(engine.APIToken{ID: "tok_" + user, Name: user, User: user, CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
			t.Fatal(err)
		}
		return newClient(credentials{Server: srv.URL, Token: tok}, out)
	}
	var oo, ao bytes.Buffer
	otto, ada := clientFor("otto", "ops", &oo), clientFor("ada", "root", &ao)
	if err := otto.runLimitsGet(nil); err != nil || !strings.Contains(oo.String(), "max run timeout: not set") {
		t.Fatalf("get: %v %q", err, oo.String())
	}
	if err := otto.runLimitsSet([]string{"--max-run-timeout", "1h"}); err == nil {
		t.Fatal("an operator changed the limit")
	}
	if err := ada.runLimitsSet([]string{"--max-run-timeout", "48h"}); err != nil || !strings.Contains(ao.String(), "48h") {
		t.Fatalf("set: %v %q", err, ao.String())
	}
	oo.Reset()
	if err := otto.runLimitsGet([]string{"--json"}); err != nil || !strings.Contains(oo.String(), `"max_run_timeout": "48h"`) {
		t.Fatalf("get after: %v %q", err, oo.String())
	}
	if err := ada.runLimitsSet([]string{"--max-run-timeout", "soon"}); err == nil {
		t.Fatal("a duration that does not read was accepted")
	}
	if err := ada.runLimitsSet(nil); exitCode(err) != exitUsage {
		t.Fatalf("set with nothing: %v", err)
	}
	if err := ada.runLimitsSet([]string{"--max-run-timeout", ""}); err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if entries, err := eng.AuditLog(engine.AuditFilter{Action: "run_limits.set"}); err != nil || len(entries) < 3 {
		t.Fatalf("audit: %v %+v", err, entries)
	}
}
