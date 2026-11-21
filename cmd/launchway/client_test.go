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
	"github.com/jk-magic-creations/launchway/internal/policy"
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
func cliServer(t *testing.T, po ...engine.PolicyOptions) (credentials, *engine.Engine) {
	t.Helper()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := engine.Options{Database: testdb.New(t), Logger: quiet}
	if len(po) > 0 {
		opts.Policy = po[0]
	}
	eng, err := engine.Open(opts)
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
	operator := auth.Operator
	s := &api.Server{Engine: eng, Projects: mgr, Log: quiet, RunnerToken: "r", MCP: &api.MCPOptions{},
		Auth: &api.Auth{Policy: pol, IdleTimeout: time.Hour, Lifetime: time.Hour}, WaiveRole: &operator}
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

func TestCLIPolicyAndWaivers(t *testing.T) {
	ps, err := policy.ParseFile([]byte(`policies:
  - name: shape
    mode: enforce
    rules:
      max_timeout: 30m
`), "t.yaml", "config")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := cliServer(t, engine.PolicyOptions{Inline: ps})
	var out bytes.Buffer
	cl := newClient(c, &out)
	// Steps run up to an hour by default, so the run is refused.
	if err := cl.start([]string{"app", "--json"}); err != nil {
		t.Fatal(err)
	}
	var started struct{ ID string }
	json.Unmarshal(out.Bytes(), &started)
	out.Reset()
	if err := cl.policyRun([]string{started.ID}); err != nil || !strings.Contains(out.String(), "run start: BLOCK") ||
		!strings.Contains(out.String(), "max_timeout        fail   longer timeouts: build: 1h0m0s") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	out.Reset()
	if err := cl.policyList(nil); err != nil || !strings.Contains(out.String(), "shape") || !strings.Contains(out.String(), "fail") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	out.Reset()
	if err := cl.waiverGrant([]string{"app", "--policy", "shape", "--rule", "max_timeout", "--reason", "long builds this week", "--expires", "48h"}); err != nil ||
		!strings.Contains(out.String(), "Granted wvr_") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	id := strings.Fields(strings.TrimPrefix(out.String(), "Granted "))[0]
	if err := cl.waiverGrant([]string{"app", "--policy", "shape", "--rule", "tests", "--reason", "x", "--expires", "1h"}); exitCode(err) != exitAPI {
		t.Fatalf("%v", err)
	}
	if err := cl.waiverGrant([]string{"app", "--policy", "shape"}); exitCode(err) != exitUsage {
		t.Fatalf("%v", err)
	}
	out.Reset()
	if err := cl.waiverList([]string{"--active"}); err != nil || !strings.Contains(out.String(), id+"  active") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	out.Reset()
	if err := cl.waiverRevoke([]string{"app", id, "--reason", "done"}); err != nil || !strings.Contains(out.String(), "revoked") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

// The reports commands read a run's tests, coverage and HTML reports.
func TestCLIReports(t *testing.T) {
	c, eng := cliServer(t)
	pollEvery = 20 * time.Millisecond
	var out bytes.Buffer
	cl := newClient(c, &out)
	if err := cl.start([]string{"app", "--wait", "-i", "version=v7"}); exitCode(err) != 0 {
		t.Fatalf("a run: %v", err)
	}
	runs, _ := eng.ListRuns(engine.RunFilter{})
	id := runs[0].ID
	for cmd, want := range map[string]string{"tests": "No test results.", "coverage": "No coverage.", "reports": "No HTML reports."} {
		out.Reset()
		var err error
		switch cmd {
		case "tests":
			err = cl.tests([]string{id})
		case "coverage":
			err = cl.coverage([]string{id, "--files"})
		case "reports":
			err = cl.reports([]string{id})
		}
		if err != nil || strings.TrimSpace(out.String()) != want {
			t.Errorf("%s: %v %q", cmd, err, out.String())
		}
	}
	out.Reset()
	if err := cl.coverage([]string{id, "--json"}); err != nil || !strings.Contains(out.String(), `"coverage"`) {
		t.Errorf("coverage --json: %v %s", err, out.String())
	}
	if err := cl.tests(nil); exitCode(err) != exitUsage {
		t.Errorf("tests without a run: %v", err)
	}
	if err := cl.tests([]string{"run_nope"}); exitCode(err) != exitAPI {
		t.Errorf("tests of a run that does not exist: %v", err)
	}
}

// Against a server without sign-in (auth.mode: none) the CLI's changes are
// accepted: it says it is not a browser, as Launchway's own pages do.
func TestCLIWithoutSignIn(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&api.Server{Engine: eng, Log: quiet, RunnerToken: "r", Actor: "ada"}).Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	var out bytes.Buffer
	cl := newClient(credentials{Server: srv.URL}, &out)
	err = cl.call("POST", "/api/v1/runs/run_nope/cancel", map[string]string{}, nil)
	if err == nil || strings.Contains(err.Error(), "header is missing") || !strings.Contains(strings.ToLower(err.Error()), "not found") && !strings.Contains(err.Error(), "no run") {
		t.Fatalf("a change without sign-in: %v", err)
	}
}

// runs resume and runs rerun work against a server, with runs start's exit
// codes and --wait, and --plan says what would run without running it.
func TestCLIResumeAndRerun(t *testing.T) {
	c, eng := cliServer(t)
	pollEvery = 20 * time.Millisecond
	var out bytes.Buffer
	cl := newClient(c, &out)
	if err := cl.start([]string{"app", "-i", "outcome=fail", "--wait"}); exitCode(err) != exitRunFailed {
		t.Fatalf("%v", err)
	}
	runs, _ := eng.ListRuns(engine.RunFilter{})
	failed := runs[0].ID
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}

	out.Reset()
	if err := cl.rerun([]string{failed, "--failed", "--plan"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"from build would run again", "run    build", "run    test[os=debian]", "run    test[os=alpine]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("plan lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := cl.rerun([]string{failed, "--from", "build", "--plan", "--json"}); err != nil || !strings.Contains(out.String(), `"from": [`) {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if n := len(runsOfEngine(t, eng)); n != 1 {
		t.Fatalf("a plan made a run: %d", n)
	}
	// A plan that cannot be carried out says why, with the server's exit code.
	if err := cl.rerun([]string{failed, "--from", "test[os=alpine]", "--plan"}); exitCode(err) != exitAPI || !strings.Contains(err.Error(), "cannot re-run from") {
		t.Fatalf("%v", err)
	}
	if err := cl.rerun([]string{failed}); exitCode(err) != exitUsage {
		t.Fatalf("no steps named: %v", err)
	}
	if err := cl.resume(nil); exitCode(err) != exitUsage {
		t.Fatalf("resume without a run: %v", err)
	}

	// The re-run is a new run that fails the same way: the exit says so.
	out.Reset()
	if err := cl.rerun([]string{failed, "--failed", "--wait"}); exitCode(err) != exitRunFailed {
		t.Fatalf("%v", err)
	}
	if n := len(runsOfEngine(t, eng)); n != 2 {
		t.Fatalf("%d runs after a re-run", n)
	}
	// Resume goes on in the same run.
	if err := cl.resume([]string{failed, "--wait"}); exitCode(err) != exitRunFailed {
		t.Fatalf("%v", err)
	}
	if n := len(runsOfEngine(t, eng)); n != 2 {
		t.Fatalf("a resume made a run: %d", n)
	}
	if err := cl.resume([]string{"run_nope"}); exitCode(err) != exitAPI {
		t.Fatalf("%v", err)
	}
	// Without --wait, the command returns at once.
	out.Reset()
	if err := cl.resume([]string{failed}); exitCode(err) != 0 {
		t.Fatalf("%v", err)
	}
}

func runsOfEngine(t *testing.T, eng *engine.Engine) []*engine.RunSummary {
	t.Helper()
	runs, err := eng.ListRuns(engine.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return runs
}
