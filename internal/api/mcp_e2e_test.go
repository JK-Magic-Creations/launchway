package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// agentRepo is a repository with no pipeline yet: a script that checks a
// sum, which is wrong at first, and writes a JUnit report.
func agentRepo(t *testing.T, dir string) string {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "check.sh"), []byte(`#!/bin/sh
mkdir -p reports
got=$(( 2 + ${ADDEND:-3} ))
if [ "$got" -eq 4 ]; then
  echo '<testsuite name="sums" tests="1" failures="0"><testcase classname="sums" name="two plus two"/></testsuite>' > reports/junit.xml
  echo "sum ok"
else
  echo '<testsuite name="sums" tests="1" failures="1"><testcase classname="sums" name="two plus two"><failure message="expected 4, got '$got'">assertion failed</failure></testcase></testsuite>' > reports/junit.xml
  echo "checking the sum"
  echo "assertion failed: expected 4, got $got" >&2
  exit 1
fi
`), 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "a sum to check"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=d@example.com",
			"GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=d@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// An agent that knows nothing about Launchway connects with a token and,
// using only what the server tells it, finds a project, learns the pipeline
// format, writes a pipeline (getting one wrong first and fixing it from the
// validator's line numbers), tries it in a draft run, watches the run with
// progress notifications, finds out why it failed from the run, its log and
// its test results, fixes it and sees it pass. Every change it made is in
// the audit trail as made through MCP with its token's name.
func TestMCPColdStartAgentFlow(t *testing.T) {
	dir := t.TempDir()
	repo := agentRepo(t, filepath.Join(dir, "agent-repo"))
	e := mcpEnv(t, envOpts{projects: []source.Project{{Name: "pay-agent", URL: repo, Poll: time.Hour}}})
	e.hostRunner(t, "host-1", nil)
	token := e.tokenFor(t, "otto", "coding agent")

	var progMu sync.Mutex
	var progress []string
	cs := mcpSession(t, e.mcpURL(), token, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			progMu.Lock()
			progress = append(progress, r.Params.Message)
			progMu.Unlock()
		},
	}, nil)

	// 1. What is here, and how is it used?
	if !strings.Contains(cs.InitializeResult().Instructions, "validate_pipeline") {
		t.Fatal("the instructions do not say how to write a pipeline")
	}
	projects := mustCall(t, cs, "list_projects", nil)
	if !strings.Contains(asJSON(projects), `"name":"pay-agent"`) || !strings.Contains(asJSON(projects), `"may_start_draft_runs":true`) {
		t.Fatalf("projects: %s", asJSON(projects))
	}
	project := mustCall(t, cs, "get_project", map[string]any{"project": "pay-agent"})
	if project["pipeline_file_exists"] != false || str(project, "ref") != "main" {
		t.Fatalf("project: %s", asJSON(project))
	}
	ref := mustCall(t, cs, "get_pipeline_reference", map[string]any{"part": "all"})
	if !strings.Contains(str(ref, "reference"), "LAUNCHWAY_OUTPUTS") || len(list(ref, "examples")) < 4 {
		t.Fatalf("reference: %.300s", asJSON(ref))
	}

	// 2. A first attempt with a mistake: the validator names the line.
	broken := `pipeline: pay-agent
steps:
  check:
    image: docker.io/library/alpine:3.22
    needs: build
    run: ./check.sh
    reports:
      tests: reports/junit.xml
`
	v := mustCall(t, cs, "validate_pipeline", map[string]any{"text": broken, "project": "pay-agent"})
	if v["ok"] != false || len(list(v, "problems")) == 0 {
		t.Fatalf("validating the broken pipeline: %s", asJSON(v))
	}
	p0 := list(v, "problems")[0].(map[string]any)
	if p0["line"] != float64(5) || !strings.Contains(str(p0, "source_line"), "needs: build") || !strings.Contains(str(p0, "message"), "build") {
		t.Fatalf("the problem: %s", asJSON(p0))
	}
	fixed := strings.Replace(broken, "    needs: build\n", "", 1)
	v = mustCall(t, cs, "validate_pipeline", map[string]any{"text": fixed, "project": "pay-agent"})
	if v["ok"] != true || !strings.Contains(asJSON(v["steps"]), `"id":"check"`) {
		t.Fatalf("validating the fixed pipeline: %s", asJSON(v))
	}

	// 3. Try it in a draft run, and watch it.
	started := mustCall(t, cs, "start_run", map[string]any{"project": "pay-agent", "pipeline_text": fixed})
	runID := str(started, "run_id")
	if runID == "" || started["draft"] != true || !strings.Contains(str(started, "url"), "/runs/"+runID) {
		t.Fatalf("start_run: %s", asJSON(started))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait_for_run", Arguments: map[string]any{"run_id": runID, "timeout_seconds": 60},
		Meta: mcp.Meta{"progressToken": "watch-1"}})
	if err != nil || res.IsError {
		t.Fatalf("wait_for_run: %v %+v", err, res)
	}
	waited := res.StructuredContent.(map[string]any)
	if str(waited, "run", "status") != "failed" || waited["timed_out"] == true {
		t.Fatalf("the run: %s", asJSON(waited))
	}
	// The client handles a notification on its own goroutine, so it may land just after the call returns.
	for i := 0; i < 100; i++ {
		progMu.Lock()
		n := len(progress)
		progMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	progMu.Lock()
	if len(progress) == 0 || !strings.Contains(strings.Join(progress, "\n"), "run #") {
		t.Errorf("no progress notifications: %v", progress)
	}
	progMu.Unlock()

	// 4. Why did it fail?
	run := mustCall(t, cs, "get_run", map[string]any{"run_id": runID})
	if !strings.Contains(str(run, "summary"), "step check failed") || !strings.Contains(asJSON(run["next_actions"]), "get_step_log") {
		t.Fatalf("get_run: %s", asJSON(run))
	}
	step := list(run, "steps")[0].(map[string]any)
	if !strings.Contains(str(step, "why"), "exit code 1") || !strings.Contains(str(step, "untrusted", "last_output"), "expected 4, got 5") {
		t.Fatalf("the failed step: %s", asJSON(step))
	}
	tail := mustCall(t, cs, "get_step_log", map[string]any{"run_id": runID, "step": "check"})
	if !strings.Contains(asJSON(tail["untrusted"]), "assertion failed: expected 4, got 5") {
		t.Fatalf("the log's tail: %s", asJSON(tail))
	}
	found := mustCall(t, cs, "get_step_log", map[string]any{"run_id": runID, "step": "check", "mode": "search", "query": "ASSERTION"})
	if lines := found["untrusted"].(map[string]any)["lines"].([]any); len(lines) != 1 {
		t.Fatalf("searching the log: %s", asJSON(found))
	}
	tests := mustCall(t, cs, "get_test_results", map[string]any{"run_id": runID})
	if !strings.Contains(str(tests, "summary"), "1 failed") || !strings.Contains(asJSON(tests["results"]), "expected 4, got 5") {
		t.Fatalf("test results: %s", asJSON(tests))
	}

	// 5. Fix it and try again: it passes.
	passing := strings.Replace(fixed, "    run: ./check.sh\n", "    env: { ADDEND: \"2\" }\n    run: ./check.sh\n", 1)
	if v := mustCall(t, cs, "validate_pipeline", map[string]any{"text": passing}); v["ok"] != true {
		t.Fatalf("validating: %s", asJSON(v))
	}
	again := mustCall(t, cs, "start_run", map[string]any{"project": "pay-agent", "ref": "main", "pipeline_text": passing})
	done := mustCall(t, cs, "wait_for_run", map[string]any{"run_id": str(again, "run_id"), "timeout_seconds": 60})
	if str(done, "run", "status") != "succeeded" || !strings.Contains(str(done, "summary"), "succeeded") {
		t.Fatalf("the second run: %s", asJSON(done))
	}

	// Both starts are in the audit trail, as made through MCP with the
	// token's name.
	entries, err := e.eng.AuditLog(engine.AuditFilter{Action: "run.draft", Limit: 10})
	if err != nil || len(entries) != 2 {
		t.Fatalf("audit: %v %+v", err, entries)
	}
	for _, en := range entries {
		want := `via MCP, token "coding agent", tool start_run, client "launchway-test-agent"`
		if en.User != "otto" || en.Outcome != "ok" || !strings.HasPrefix(en.Detail, want) {
			t.Errorf("audit entry %+v, want detail starting %s", en, want)
		}
	}
}
