package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
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

const cliPipeline = `
pipeline: app
inputs:
  outcome: { type: choice, options: [pass, fail, slow], default: pass }
  version: { pattern: "^v[0-9]+$", default: v1 }
steps:
  build:
    image: none
    run: echo build
  test:
    needs: build
    for_each: { os: [debian, alpine] }
    image: none
    run: echo test
`

// cliServer is a real server with sign-in, one project, a token for ci (an
// operator), and a stand-in runner that ends each step as the run's
// outcome input says.
func cliServer(t *testing.T) (credentials, *engine.Engine) {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".launchway"), 0o755)
	os.WriteFile(filepath.Join(repo, ".launchway", "pipeline.yaml"), []byte(cliPipeline), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	mgr, err := source.NewManager(eng, filepath.Join(dir, "data"), []source.Project{{Name: "app", URL: repo, Poll: time.Hour}}, quiet)
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
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); srv.Close(); eng.Close() })
	now := time.Now().UnixMilli()
	if err := eng.SignIn("ci", []string{"ops"}, engine.Profile{}, "session-hash", now, now+3600_000, 0); err != nil {
		t.Fatal(err)
	}
	tok := "lw_clitesttoken0123456789"
	if err := eng.CreateToken(engine.APIToken{ID: "tok_cli", Name: "cli", User: "ci", CreatedAt: now, ExpiresAt: now + 86400_000}, auth.Hash(tok)); err != nil {
		t.Fatal(err)
	}
	go func() {
		for ctx.Err() == nil {
			j, err := eng.Lease(ctx, engine.RunnerInfo{ID: "stand-in", Name: "stand-in"}, time.Second)
			if err != nil || j == nil {
				continue
			}
			eng.Start(j.LeaseID)
			eng.AppendLogs(j.LeaseID, []engine.LogLine{{Text: "hello from " + j.StepID}})
			res := engine.Result{Outcome: "succeeded"}
			switch j.Env["LAUNCHWAY_INPUT_OUTCOME"] {
			case "fail":
				if j.StepID == "build" {
					res = engine.Result{Outcome: "failed", Error: "exit status 1"}
				}
			case "slow":
				time.Sleep(300 * time.Millisecond)
			}
			eng.Finish(j.LeaseID, res)
		}
	}()
	return credentials{Server: srv.URL, Token: tok}, eng
}

func exitCode(err error) int {
	var ce cliErr
	if errors.As(err, &ce) {
		return ce.code
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestCLIRuns(t *testing.T) {
	c, eng := cliServer(t)
	pollEvery = 20 * time.Millisecond
	var out bytes.Buffer
	cl := newClient(c, &out)

	// --wait exits with the run's outcome.
	if err := cl.start([]string{"app", "--wait", "-i", "version=v7"}); exitCode(err) != 0 {
		t.Fatalf("a passing run: %v", err)
	}
	if !strings.Contains(out.String(), "succeeded") {
		t.Fatalf("output: %s", out.String())
	}
	if err := cl.start([]string{"app", "-i", "outcome=fail", "--wait"}); exitCode(err) != exitRunFailed {
		t.Fatalf("a failing run: %v", err)
	}
	// Values that do not fit are refused, by name, before anything runs.
	out.Reset()
	err := cl.start([]string{"app", "-i", "version=latest", "-i", "colour=red"})
	if exitCode(err) != exitAPI || !strings.Contains(err.Error(), "version: ") || !strings.Contains(err.Error(), "colour: ") {
		t.Fatalf("bad inputs: %v", err)
	}
	if err := cl.start([]string{"app", "-i", "novalue"}); exitCode(err) != exitUsage {
		t.Fatalf("-i without =: %v", err)
	}
	// --timeout gives up waiting, not the run.
	if err := cl.start([]string{"app", "-i", "outcome=slow", "--wait", "--timeout", "50ms"}); exitCode(err) != exitTimeout {
		t.Fatalf("timeout: %v", err)
	}

	// list, show and logs.
	runs, _ := eng.ListRuns(engine.RunFilter{})
	out.Reset()
	if err := cl.list([]string{"--project", "app"}); err != nil || strings.Count(out.String(), "\n") != len(runs) {
		t.Fatalf("list: %v\n%s", err, out.String())
	}
	var passed string
	for _, r := range runs {
		if r.Status == "succeeded" && r.Number == 1 {
			passed = r.ID
		}
	}
	out.Reset()
	if err := cl.show([]string{passed}); err != nil || !strings.Contains(out.String(), "input version = v7") || !strings.Contains(out.String(), "test[os=alpine]") {
		t.Fatalf("show: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := cl.logs([]string{passed}); err != nil || !strings.Contains(out.String(), "hello from test[os=debian]") {
		t.Fatalf("logs: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := cl.logs([]string{passed, "--step", "test[os=alpine]"}); err != nil || strings.TrimSpace(out.String()) != "hello from test[os=alpine]" {
		t.Fatalf("one step's logs: %v\n%q", err, out.String())
	}
	if err := cl.logs([]string{passed, "--step", "nope"}); exitCode(err) != exitAPI {
		t.Fatalf("logs of a step that does not exist: %v", err)
	}
	// --follow prints lines as they come, until the run ends.
	var slow string
	for _, r := range runs {
		if r.Status != "succeeded" && r.Status != "failed" {
			slow = r.ID
		}
	}
	if slow != "" {
		out.Reset()
		if err := cl.logs([]string{slow, "--follow"}); err != nil || !strings.Contains(out.String(), "hello from test[os=alpine]") {
			t.Fatalf("follow: %v\n%s", err, out.String())
		}
	}
	// cancel: a slow run, asked to stop.
	out.Reset()
	if err := cl.start([]string{"app", "-i", "outcome=slow", "--json"}); err != nil {
		t.Fatal(err)
	}
	var started struct{ ID string }
	json.Unmarshal(out.Bytes(), &started)
	if err := cl.cancel([]string{started.ID}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := cl.waitFor(started.ID, 10*time.Second, false); err != nil {
		t.Fatal(err)
	}
	if err := cl.cancel([]string{"run_nope"}); exitCode(err) != exitAPI {
		t.Fatalf("cancel of an unknown run: %v", err)
	}

	// A bad token: refused, with a hint.
	bad := newClient(credentials{Server: c.Server, Token: "lw_nope"}, &out)
	if err := bad.list(nil); exitCode(err) != exitAPI || !strings.Contains(err.Error(), "launchway login") {
		t.Fatalf("bad token: %v", err)
	}
	// An unreachable server.
	gone := newClient(credentials{Server: "http://127.0.0.1:1", Token: c.Token}, &out)
	if err := gone.list(nil); exitCode(err) != exitAPI {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestCLILogin(t *testing.T) {
	c, _ := cliServer(t)
	dir := filepath.Join(t.TempDir(), "conf")
	t.Setenv("LAUNCHWAY_CONFIG_DIR", dir)
	t.Setenv("LAUNCHWAY_SERVER", "")
	t.Setenv("LAUNCHWAY_TOKEN", c.Token)
	if err := cmdLogin([]string{"--server", c.Server}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v %v", fi, err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Fatalf("credentials directory: %v", di.Mode())
	}
	t.Setenv("LAUNCHWAY_TOKEN", "")
	got, err := loadCredentials()
	if err != nil || got.Server != c.Server || got.Token != c.Token {
		t.Fatalf("stored: %+v %v", got, err)
	}
	// A wrong token is not stored.
	t.Setenv("LAUNCHWAY_TOKEN", "lw_wrong")
	if err := cmdLogin([]string{"--server", c.Server}); err == nil {
		t.Fatal("login with a wrong token")
	}
	t.Setenv("LAUNCHWAY_TOKEN", "")
	if got, _ := loadCredentials(); got.Token != c.Token {
		t.Fatal("a failed login replaced the stored token")
	}
	// Plain HTTP to another machine is refused.
	t.Setenv("LAUNCHWAY_TOKEN", c.Token)
	if err := cmdLogin([]string{"--server", "http://ci.example.com"}); !errors.Is(err, exitErr(exitUsage)) {
		t.Fatalf("plain http: %v", err)
	}
}
