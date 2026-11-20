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

	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// TestCLIVerifyListings: health sources and models are listed by name and
// limits, never by where they are; verifications list and mark talk to
// the API and report its refusals.
func TestCLIVerifyListings(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	models, err := ai.NewRegistry([]ai.Model{{Name: "local", Provider: "openai", Endpoint: "http://127.0.0.1:1/v1", Model: "qwen",
		Projects: []string{"app"}, TokensPerDay: 5000}})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet, Models: models,
		HealthSources: []engine.HealthSource{{Name: "prometheus", Type: "prometheus", Projects: []string{"app"}}}})
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := source.NewManager(eng, filepath.Join(t.TempDir(), "data"), nil, quiet)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := auth.NewPolicy([]auth.Binding{{Group: "ops", Role: auth.Operator}})
	if err != nil {
		t.Fatal(err)
	}
	s := &api.Server{Engine: eng, Projects: mgr, Log: quiet, RunnerToken: "r",
		Auth: &api.Auth{Policy: pol, IdleTimeout: time.Hour, Lifetime: time.Hour}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	now := time.Now().UnixMilli()
	if err := eng.SignIn("ci", []string{"ops"}, engine.Profile{}, "session-hash", now, now+3600_000, 0); err != nil {
		t.Fatal(err)
	}
	tok := "lw_" + "cliverify" + strings.Repeat("0", 20) // built, not token-shaped in source
	if err := eng.CreateToken(engine.APIToken{ID: "tok_cli", Name: "cli", User: "ci", CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL, Token: tok}, &out)

	if err := cl.healthSources(nil); err != nil || !strings.Contains(out.String(), "prometheus") || strings.Contains(out.String(), "http") {
		t.Fatalf("health-sources: %v %q", err, out.String())
	}
	out.Reset()
	if err := cl.models(nil); err != nil || !strings.Contains(out.String(), "local") || !strings.Contains(out.String(), "5000 tokens a day") ||
		strings.Contains(out.String(), "127.0.0.1") {
		t.Fatalf("models: %v %q", err, out.String())
	}
	out.Reset()
	if err := cl.verifications([]string{"--environment", "prod", "--json"}); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("verifications list: %v %q", err, out.String())
	}
	if err := cl.markVerification([]string{"run_x", "verify", "passed"}); exitCode(err) != exitUsage {
		t.Fatalf("mark without a reason: %v", err)
	}
	if err := cl.markVerification([]string{"run_x", "verify", "maybe", "--reason", "x"}); exitCode(err) != exitUsage {
		t.Fatalf("mark maybe: %v", err)
	}
	if err := cl.markVerification([]string{"run_x", "verify", "passed", "--reason", "checked"}); exitCode(err) != exitAPI {
		t.Fatalf("mark on no run: %v", err)
	}
	out.Reset()
	if err := cl.showVerifications([]string{"run_x"}); exitCode(err) != exitAPI {
		t.Fatalf("show on no run: %v %q", err, out.String())
	}
}
