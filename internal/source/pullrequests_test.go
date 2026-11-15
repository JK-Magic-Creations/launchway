package source

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const prPipeline = `
pipeline: app
on:
  push: { branches: [main] }
  pull_request: {}
supersede: true
steps:
  test: {image: alpine, run: echo ok}
`

// prRepo makes a repository whose main branch has the given pipeline.
func prRepo(t *testing.T, mainPipeline string) (dir, repo string) {
	t.Helper()
	dir = t.TempDir()
	repo = filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "git", "init", "-q", "-b", "main")
	commit(t, repo, mainPipeline, "first")
	return dir, repo
}

// openPR points refs/pull/<n>/head at a new commit on a branch of its own,
// as a Git host does when a pull request is opened or pushed to. The
// pipeline is changed only if pipelineYAML is not empty.
func openPR(t *testing.T, repo, n, pipelineYAML, msg string) {
	t.Helper()
	base := "main"
	if _, err := os.Stat(filepath.Join(repo, ".git", "refs", "pull", n, "head")); err == nil {
		base = "refs/pull/" + n + "/head"
	}
	run(t, repo, "git", "checkout", "-q", "--detach", base)
	commit(t, repo, pipelineYAML, msg)
	run(t, repo, "git", "update-ref", "refs/pull/"+n+"/head", "HEAD")
	run(t, repo, "git", "checkout", "-q", "main")
}

func prManager(t *testing.T, addr, dir string, p Project) (*Manager, *engine.Engine) {
	t.Helper()
	eng, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	m, err := NewManager(eng, dir, []Project{p}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m, eng
}

func pollOnce(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.poll(context.Background(), m.projects["app"]); err != nil {
		t.Fatal(err)
	}
}

func runsOf(t *testing.T, eng *engine.Engine) []*engine.RunSummary {
	t.Helper()
	runs, err := eng.ListRuns(engine.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return runs
}

// Pull requests that exist when the project is first seen, open or closed,
// do not run; one opened or pushed to afterwards does, as a pull-request
// run with no secrets, and supersedes its own older run.
func TestPullRequestsStartRuns(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	openPR(t, repo, "1", "", "an old, closed pull request")
	openPR(t, repo, "2", "", "an open pull request")
	run(t, repo, "git", "update-ref", "refs/pull/2/merge", "main")
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})

	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("history started %d runs", len(runs))
	}

	// A new pull request runs.
	openPR(t, repo, "3", "", "a new pull request")
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	first := runs[0]
	if first.Event != "pull_request" || first.PullRequest != 3 || first.Ref != "refs/pull/3/head" || first.Branch != "" ||
		first.Message != "a new pull request" || first.Author != "Dana" {
		t.Fatalf("run %+v", first)
	}
	st, _ := eng.State(first.ID)
	if st.PullRequestSecrets || st.Trigger.PullRequest != 3 {
		t.Fatalf("state %+v", st.Trigger)
	}
	if s, _ := m.Get("app"); s.Decisions[0].Outcome != "started" || s.Decisions[0].Ref != "refs/pull/3/head" {
		t.Fatalf("decision %+v", s.Decisions[0])
	}

	// Polling again changes nothing; nor does the host moving a merge ref.
	run(t, repo, "git", "update-ref", "refs/pull/2/merge", "refs/pull/3/head")
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}

	// Pushing to the pull request runs it again and supersedes the first
	// run, which nobody has started on.
	openPR(t, repo, "3", "", "fix the pull request")
	pollOnce(t, m)
	runs = runsOf(t, eng)
	if len(runs) != 2 || runs[0].PullRequest != 3 || runs[0].Message != "fix the pull request" {
		t.Fatalf("runs %+v", runs)
	}
	old, _ := eng.Summary(first.ID)
	if old.Status != engine.RunCanceled || old.CancelReason != "superseded by run #2" || old.SupersededBy != runs[0].ID {
		t.Fatalf("first run %+v", old)
	}
	if s, _ := m.Get("app"); !strings.Contains(s.Decisions[0].Detail, "started run #2, superseding #1") {
		t.Fatalf("decision %+v", s.Decisions[0])
	}

	// A pull request whose own pipeline does not ask for pull requests,
	// and one whose pipeline is broken, are explained, not run.
	openPR(t, repo, "4", pl, "drop pull requests")
	pollOnce(t, m)
	s, _ := m.Get("app")
	if d := s.Decisions[0]; d.Outcome != "ignored" || d.Ref != "refs/pull/4/head" || !strings.Contains(d.Detail, "does not ask for pull requests") {
		t.Fatalf("decision %+v", d)
	}
	openPR(t, repo, "5", "pipeline: app\non: { pull_request: { branches: [main] } }\nsteps:\n  t: {image: x, run: y}\n", "filter by branch")
	pollOnce(t, m)
	s, _ = m.Get("app")
	if d := s.Decisions[0]; d.Outcome != "error" || !strings.Contains(d.Detail, "cannot be filtered by branch") {
		t.Fatalf("decision %+v", d)
	}
	if runs := runsOf(t, eng); len(runs) != 2 {
		t.Fatalf("%d runs", len(runs))
	}

	// Pushes still start push runs, and a push run does not supersede a
	// pull request's.
	commit(t, repo, "", "on main")
	pollOnce(t, m)
	runs = runsOf(t, eng)
	if len(runs) != 3 || runs[0].Event != "push" || runs[0].Branch != "main" {
		t.Fatalf("runs %+v", runs)
	}
	if r, _ := eng.Summary(runs[1].ID); r.CancelRequested {
		t.Fatalf("the pull request's run was superseded by a push: %+v", r)
	}
}

// Whether pull requests run is decided by the default branch: a pull
// request cannot turn them on for itself. Heads are recorded all the same,
// so turning them on later does not run the pull requests that exist.
func TestPullRequestsNeedTheDefaultBranch(t *testing.T) {
	dir, repo := prRepo(t, pl)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo, PullRequestSecrets: true})
	pollOnce(t, m)

	openPR(t, repo, "1", prPipeline, "turn pull requests on")
	openPR(t, repo, "2", "", "an ordinary change")
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("%d runs", len(runs))
	}
	s, _ := m.Get("app")
	if len(s.Decisions) != 1 {
		t.Fatalf("decisions %+v", s.Decisions)
	}
	if d := s.Decisions[0]; d.Ref != "refs/pull/1/head" || d.Outcome != "ignored" ||
		!strings.Contains(d.Detail, "the pipeline on the default branch, main, does not ask for them") {
		t.Fatalf("decision %+v", d)
	}
	seen, _ := eng.SeenRefs("app")
	if seen["refs/pull/2/head"] == "" {
		t.Fatalf("pull request 2's head was not recorded: %v", seen)
	}

	// The change is merged: main asks for pull requests. Neither existing
	// pull request runs until it moves.
	run(t, repo, "git", "merge", "-q", "--no-edit", "refs/pull/1/head")
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 || runs[0].Event != "push" {
		t.Fatalf("runs %+v", runs)
	}
	openPR(t, repo, "2", prPipeline, "rebase on main")
	pollOnce(t, m)
	runs = runsOf(t, eng)
	if len(runs) != 2 || runs[0].PullRequest != 2 {
		t.Fatalf("runs %+v", runs)
	}
	// This project lets pull requests read secrets.
	if st, _ := eng.State(runs[0].ID); !st.PullRequestSecrets {
		t.Fatal("the project's pull_requests.secrets was not applied")
	}

	// A default branch that does not exist turns pull requests off.
	m2, eng2 := prManager(t, testdb.New(t), filepath.Join(dir, "other"), Project{Name: "app", URL: repo, DefaultBranch: "trunk"})
	pollOnce(t, m2)
	openPR(t, repo, "2", "", "again")
	pollOnce(t, m2)
	s, _ = m2.Get("app")
	if len(runsOf(t, eng2)) != 0 || len(s.Decisions) != 1 || !strings.Contains(s.Decisions[0].Detail, "the default branch, trunk, does not exist") {
		t.Fatalf("decisions %+v", s.Decisions)
	}
}

// A project an older server was watching has its refs recorded but not its
// pull requests: the first poll by a server that knows pull requests
// records them without running any.
func TestPullRequestsSeededAfterAnUpgrade(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	openPR(t, repo, "1", "", "already there")
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	main := strings.TrimSpace(gitHead(t, repo))
	if err := eng.SeedRefs("app", map[string]string{"refs/heads/main": main}, 1); err != nil {
		t.Fatal(err)
	}
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("%d runs", len(runs))
	}
	seen, _ := eng.SeenRefs("app")
	if _, ok := seen[engine.PullRequestsSeeded]; !ok || seen["refs/pull/1/head"] == "" {
		t.Fatalf("seen %v", seen)
	}
	openPR(t, repo, "1", "", "moved")
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 1 || runs[0].PullRequest != 1 {
		t.Fatalf("runs %+v", runs)
	}
}

// A configured pipeline can ask for pull requests and supersede, and the
// text it amounts to says so.
func TestConfiguredPipelineForPullRequests(t *testing.T) {
	pp := &ProjectPipeline{Library: "lib", Version: "v1", Template: "check", Supersede: true,
		On: pipeline.Triggers{PullRequest: &pipeline.PullRequestTrigger{}}}
	text, err := configuredText("app", pp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "pull_request: {}") || !strings.Contains(text, "supersede: true") {
		t.Fatalf("text:\n%s", text)
	}
	p, err := pipeline.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if !p.On.MatchPullRequest() || !p.Supersede || p.On.Push != nil {
		t.Fatalf("parsed %+v", p)
	}
	pp.Supersede, pp.On.PullRequest = false, nil
	text, _ = configuredText("app", pp)
	if strings.Contains(text, "pull_request") || strings.Contains(text, "supersede") || strings.Contains(text, "\non:") {
		t.Fatalf("text:\n%s", text)
	}
}

// Two servers polling the same pull requests start one run per change.
func TestTwoServersStartOneRunPerPullRequestChange(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	dir, repo := prRepo(t, prPipeline)
	openPR(t, repo, "1", "", "old")
	addr := testdb.New(t)
	a, eng := prManager(t, addr, filepath.Join(dir, "a"), Project{Name: "app", URL: repo})
	b, _ := prManager(t, addr, filepath.Join(dir, "b"), Project{Name: "app", URL: repo})
	pollBoth := func() {
		t.Helper()
		var wg sync.WaitGroup
		for _, m := range []*Manager{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := m.poll(context.Background(), m.projects["app"]); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	pollBoth()
	for i, n := range []string{"2", "2", "3", "2"} {
		openPR(t, repo, n, "", "change "+string(rune('a'+i)))
		pollBoth()
	}
	runs := runsOf(t, eng)
	if len(runs) != 4 {
		t.Fatalf("%d runs for 4 changes", len(runs))
	}
	going := 0
	for _, r := range runs {
		if !r.CancelRequested {
			going++
		}
	}
	if going != 2 { // the newest of pull requests 2 and 3
		t.Fatalf("%d runs left going: %+v", going, runs)
	}
	sa, _ := a.Get("app")
	sb, _ := b.Get("app")
	if len(sa.Decisions) != 4 || len(sb.Decisions) != 4 {
		t.Fatalf("decisions: %d and %d", len(sa.Decisions), len(sb.Decisions))
	}
}
