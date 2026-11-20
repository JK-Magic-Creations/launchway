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

// step-resources get shows the configuration to anyone, set-limits and
// set-default change it for an admin and are refused for an operator, a
// quantity that does not read is refused, and flags left out keep what is.
func TestCLIStepResources(t *testing.T) {
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
		tok := "lw_" + "clisr" + user + strings.Repeat("0", 20)
		if err := eng.CreateToken(engine.APIToken{ID: "tok_" + user, Name: user, User: user, CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
			t.Fatal(err)
		}
		return newClient(credentials{Server: srv.URL, Token: tok}, out)
	}
	var oo, ao bytes.Buffer
	otto, ada := clientFor("otto", "ops", &oo), clientFor("ada", "root", &ao)

	if err := otto.stepResourcesGet(nil); err != nil || !strings.Contains(oo.String(), "default for step") || !strings.Contains(oo.String(), "not set") {
		t.Fatalf("get: %v %q", err, oo.String())
	}
	if err := otto.stepResourcesSetLimits([]string{"--max-memory", "8Gi"}); err == nil || !strings.Contains(err.Error(), "admin") && !strings.Contains(err.Error(), "403") && !strings.Contains(err.Error(), "role") {
		t.Fatalf("an operator changed the limits, or the refusal says nothing: %v", err)
	}
	if err := ada.stepResourcesSetLimits([]string{"--max-memory", "8Gi", "--max-cpu", "4"}); err != nil || !strings.Contains(ao.String(), "8Gi") {
		t.Fatalf("set-limits: %v %q", err, ao.String())
	}
	// Only the flag given changes; the rest stays.
	ao.Reset()
	if err := ada.stepResourcesSetLimits([]string{"--memory-per-thread", "512Mi", "--json"}); err != nil ||
		!strings.Contains(ao.String(), `"max_memory": "8Gi"`) && !strings.Contains(ao.String(), `"max_memory":"8Gi"`) {
		t.Fatalf("set-limits again: %v %q", err, ao.String())
	}
	ao.Reset()
	if err := ada.stepResourcesSetDefault([]string{"step", "--memory", "1Gi", "--memory-limit", "3Gi"}); err != nil || !strings.Contains(ao.String(), "memory 1Gi (up to 3Gi)") ||
		!strings.Contains(ao.String(), "cpu 1 (up to 4)") {
		t.Fatalf("set-default: %v %q", err, ao.String())
	}
	if err := ada.stepResourcesSetDefault([]string{"step", "--memory", "lots"}); err == nil {
		t.Fatal("a quantity that does not read was accepted")
	}
	if err := ada.stepResourcesSetDefault([]string{"step", "--memory", "64Gi"}); err == nil {
		t.Fatal("a default above the limit was accepted")
	}
	if err := ada.stepResourcesSetDefault([]string{"step"}); err == nil {
		t.Fatal("set-default with nothing to set was accepted")
	}
	entries, err := eng.AuditLog(engine.AuditFilter{Action: "step_resources.set_default"})
	if err != nil || len(entries) < 2 {
		t.Fatalf("audit: %v %+v", err, entries)
	}
}
