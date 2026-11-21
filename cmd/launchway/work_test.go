package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// run is one work command: its output, or its error.
func runWork(t *testing.T, cl *client, out *bytes.Buffer, args ...string) (string, error) {
	t.Helper()
	out.Reset()
	err := cl.work(args)
	return out.String(), err
}

func mustWork(t *testing.T, cl *client, out *bytes.Buffer, args ...string) string {
	t.Helper()
	s, err := runWork(t, cl, out, args...)
	if err != nil {
		t.Fatalf("work %v: %v\n%s", args, err, s)
	}
	return s
}

// A person plans work from the command line, registers an agent, and the
// agent works an item through its own commands; the history says the changes
// came from the command line.
func TestWorkCommandsRoundTrip(t *testing.T) {
	hc := newHostedCLI(t)
	hc.repo("acme/app")
	var out, aout bytes.Buffer
	ada := hc.client("ada", "root", &out)
	const proj = "project:acme/app"

	if s := mustWork(t, ada, &out, "items", "create", "Ring the bell", "--scope", proj, "--type", "bug", "--priority", "high", "--label", "agent:ok", "--criterion", "it rings"); !strings.Contains(s, "Made ") {
		t.Fatalf("create: %q", s)
	}
	var made map[string]any
	if err := json.Unmarshal([]byte(mustWork(t, ada, &out, "items", "list", "--scope", proj, "--json")), &made); err != nil {
		t.Fatal(err)
	}
	ref := wstr(wlist(made, "items")[0], "ref")
	if ref == "" {
		t.Fatalf("no ref in %v", made)
	}
	if s := mustWork(t, ada, &out, "items", "list", "--repo", "acme/app", "--query", "bell"); !strings.Contains(s, ref) || !strings.Contains(s, "Ring the bell") {
		t.Fatalf("list: %q", s)
	}
	mustWork(t, ada, &out, "items", "update", ref, "--estimate", "3", "--add-label", "audio")
	mustWork(t, ada, &out, "items", "comment", ref, "Looks easy")
	if s := mustWork(t, ada, &out, "items", "show", ref); !strings.Contains(s, "Ring the bell") || !strings.Contains(s, "Looks easy") || !strings.Contains(s, "[ ] it rings") || !strings.Contains(s, "audio") {
		t.Fatalf("show: %q", s)
	}
	if _, err := runWork(t, ada, &out, "items", "update", ref); err == nil {
		t.Error("an update that changes nothing was accepted")
	}
	if _, err := runWork(t, ada, &out, "items", "list"); err != nil {
		t.Errorf("list with no scope reads everything: %v", err)
	}
	if _, err := runWork(t, ada, &out, "items", "create", "x"); err == nil || !strings.Contains(err.Error(), "say where") {
		t.Errorf("create with no scope: %v", err)
	}

	// Planning.
	mustWork(t, ada, &out, "milestones", "create", "v1", "--scope", proj, "--due", "2027-01-31")
	if s := mustWork(t, ada, &out, "milestones", "list", "--scope", proj); !strings.Contains(s, "v1") {
		t.Fatalf("milestones: %q", s)
	}
	mustWork(t, ada, &out, "sprints", "create", "Sprint 1", "--scope", proj, "--start", "2026-12-01", "--end", "2026-12-14", "--capacity", "ada=20")
	mustWork(t, ada, &out, "items", "update", ref, "--sprint", "Sprint 1", "--milestone", "v1")
	if s := mustWork(t, ada, &out, "sprints", "show", "Sprint 1", "--scope", proj); !strings.Contains(s, "Sprint 1") {
		t.Fatalf("sprint show by name: %q", s)
	}
	mustWork(t, ada, &out, "sprints", "start", "Sprint 1", "--scope", proj)
	if s := mustWork(t, ada, &out, "board", "--scope", proj); !strings.Contains(s, ref) {
		t.Fatalf("board: %q", s)
	}
	mustWork(t, ada, &out, "items", "move", ref, "in_progress")
	mustWork(t, ada, &out, "charts", "velocity", "--scope", proj, "--json")
	mustWork(t, ada, &out, "roadmap", "--scope", proj)
	mustWork(t, ada, &out, "release-notes", "--scope", proj, "--since", "2026-01-01")
	mustWork(t, ada, &out, "views", "save", "mine", "--scope", proj, "--query", "assignee:me")
	if s := mustWork(t, ada, &out, "views", "list", "--scope", proj); !strings.Contains(s, "mine") {
		t.Fatalf("views: %q", s)
	}
	mustWork(t, ada, &out, "settings", "show", "--scope", proj)
	mustWork(t, ada, &out, "inbox")
	mustWork(t, ada, &out, "home")
	if s := mustWork(t, ada, &out, "items", "history", ref); !strings.Contains(s, "cli") {
		t.Fatalf("history does not say the changes came from the command line: %q", s)
	}

	// A file of stories, previewed.
	stories := filepath.Join(t.TempDir(), "stories.json")
	if err := os.WriteFile(stories, []byte(`[{"key":"S-1","title":"First story"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := mustWork(t, ada, &out, "import", "--format", "stories-json", stories, "--scope", proj, "--preview"); !strings.Contains(s, "Would make 1") {
		t.Fatalf("import: %q", s)
	}

	// An agent: registered by a person, works through its own commands.
	s := mustWork(t, ada, &out, "agents", "create", "bellringer", "--scope", proj, "--project", "acme/app", "--concurrency", "1")
	tok := regexp.MustCompile(`\n(\S{20,})\n`).FindStringSubmatch(s)
	if tok == nil {
		t.Fatalf("agent create printed no token: %q", s)
	}
	if s := mustWork(t, ada, &out, "agents", "list"); !strings.Contains(s, "bellringer") || strings.Contains(s, tok[1]) {
		t.Fatalf("agents list: %q", s)
	}
	bot := newClient(credentials{Server: hc.srv.URL, Token: tok[1]}, &aout)
	if s := mustWork(t, bot, &aout, "agent", "me"); !strings.Contains(s, "bellringer") {
		t.Fatalf("agent me: %q", s)
	}
	mustWork(t, bot, &aout, "agent", "queue")
	if _, err := runWork(t, bot, &aout, "agent", "hand-back", ref); err == nil {
		t.Error("handing back without a reason")
	}
	if _, err := runWork(t, ada, &out, "agent", "me"); err == nil {
		t.Error("a person called an agent command")
	}
	mustWork(t, ada, &out, "agents", "disable", "bellringer")
	if _, err := runWork(t, bot, &aout, "agent", "me"); err == nil {
		t.Error("the kill switch left the agent's token working")
	}
	mustWork(t, ada, &out, "agents", "enable", "bellringer")
	mustWork(t, ada, &out, "agents", "rotate-token", "bellringer")
	mustWork(t, ada, &out, "agents", "delete", "bellringer")

	// Others may read what they may see and nothing more.
	otto := hc.client("otto", "ops", &bytes.Buffer{})
	if err := otto.workDo("x", []string{"--json"}, func(w *wcmd) func([]string) (*workReq, error) {
		return func([]string) (*workReq, error) {
			return &workReq{method: "PUT", path: "/api/v1/work/settings", body: map[string]any{"scope": proj, "settings": map[string]any{}}}, nil
		}
	}); err == nil {
		t.Error("an operator replaced the settings")
	}
	if _, err := runWork(t, ada, &out, "nonsense"); err == nil || !strings.Contains(err.Error(), "unknown command work nonsense") {
		t.Errorf("unknown group: %v", err)
	}
}

// The scope comes from the environment when no flag gives it.
func TestWorkScopeFromEnvironment(t *testing.T) {
	hc := newHostedCLI(t)
	hc.repo("acme/app")
	var out bytes.Buffer
	ada := hc.client("ada", "root", &out)
	t.Setenv("LAUNCHWAY_WORK_SCOPE", "project:acme/app")
	mustWork(t, ada, &out, "items", "create", "From the environment")
	if s := mustWork(t, ada, &out, "items", "list"); !strings.Contains(s, "From the environment") {
		t.Fatalf("list: %q", s)
	}
}
