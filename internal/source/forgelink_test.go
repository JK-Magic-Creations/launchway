package source

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func forgeFixture(t *testing.T, layout forge.RefLayout) (*Manager, *engine.Engine, string) {
	t.Helper()
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "git", "init", "-q", "-b", "main")
	commit(t, repo, pl, "first")
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	m, err := NewManager(eng, dir, []Project{{Name: "app", URL: repo, PullRefs: layout}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m, eng, repo
}

// Every project becomes a repository on the plain connection, and what the
// watcher decides about each push is kept as an event; watching itself is
// the same as before.
func TestProjectsBecomeRepositoriesAndDecisionsBecomeEvents(t *testing.T) {
	m, eng, repo := forgeFixture(t, forge.RefLayout{})
	ctx := context.Background()
	m.syncForge(ctx)
	r, err := m.ForgeRepository(ctx, "app")
	if err != nil || r.Name != "app" || r.DefaultBranch != "main" {
		t.Fatalf("%+v %v", r, err)
	}
	p := m.c().projects["app"]
	if err := m.poll(ctx, p); err != nil { // first sight: nothing runs
		t.Fatal(err)
	}
	commit(t, repo, "", "second") // starts a run
	run(t, repo, "git", "checkout", "-q", "-b", "feature/x")
	commit(t, repo, "", "on a feature branch") // ignored
	m.poll(ctx, p)
	if runs, _ := eng.ListRuns(engine.RunFilter{}); len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	st := store.New(eng.Database())
	events, err := st.Events(ctx, r.Connection, r.ID, 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatalf("%+v %v", events, err)
	}
	outcomes := map[string]string{}
	for _, e := range events {
		outcomes[e.Ref] = e.Outcome + ": " + e.Detail
	}
	if !strings.HasPrefix(outcomes["refs/heads/main"], "started: run ") || !strings.HasPrefix(outcomes["refs/heads/feature/x"], "ignored: ") {
		t.Errorf("%v", outcomes)
	}
	if names := m.Projects(); len(names) != 1 || names[0] != "app" {
		t.Error(names)
	}
	// The mirror is a bare repository the provider can read.
	dir, err := m.Mirror(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil {
		t.Error(err)
	}
	if _, err := m.Mirror(ctx, "nope"); err == nil {
		t.Error("unknown project")
	}
	if _, err := m.ForgeRepository(ctx, "nope"); err == nil {
		t.Error("unknown project")
	}
}

// A project whose rows cannot be written is watched exactly as before.
func TestWatchingGoesOnWhenTheForgeTablesAreMissing(t *testing.T) {
	m, eng, repo := forgeFixture(t, forge.RefLayout{})
	ctx := context.Background()
	for _, tb := range []string{"forge_events", "forge_repositories", "forge_connections"} {
		if _, err := eng.Database().Exec(ctx, "DROP TABLE "+tb); err != nil {
			t.Fatal(err)
		}
	}
	m.syncForge(ctx) // logs, does not panic
	if _, err := m.ForgeRepository(ctx, "app"); err == nil {
		t.Error("a repository was found without a table")
	}
	p := m.c().projects["app"]
	m.poll(ctx, p)
	commit(t, repo, "", "second")
	m.poll(ctx, p)
	if runs, _ := eng.ListRuns(engine.RunFilter{}); len(runs) != 1 {
		t.Fatalf("%d runs; the push should still start one", len(runs))
	}
}

// Where a host publishes pull request heads comes from the provider's layout,
// not from the code.
func TestPullRequestRefsFollowTheHostsLayout(t *testing.T) {
	gl := forge.RefLayout{Prefix: "refs/merge-requests", Suffix: "/head"}
	m, _, repo := forgeFixture(t, gl)
	ctx := context.Background()
	head := strings.TrimSpace(runOut(t, repo, "git", "rev-parse", "HEAD"))
	run(t, repo, "git", "update-ref", "refs/merge-requests/7/head", head)
	run(t, repo, "git", "update-ref", "refs/merge-requests/7/merge", head)
	run(t, repo, "git", "update-ref", "refs/pull/9/head", head) // another host's shape: not this project's
	p := m.c().projects["app"]
	if err := p.fetch(ctx, 0); err != nil {
		t.Fatal(err)
	}
	pulls, err := p.pullRefs(ctx)
	if err != nil || len(pulls) != 1 || pulls[0].Name != "7" || pulls[0].Kind != kindPull || pulls[0].FullRef() != "refs/merge-requests/7/head" {
		t.Fatalf("%+v %v", pulls, err)
	}
	// The default layout still finds refs/pull/<n>/head.
	m2, _, repo2 := forgeFixture(t, forge.RefLayout{})
	head2 := strings.TrimSpace(runOut(t, repo2, "git", "rev-parse", "HEAD"))
	run(t, repo2, "git", "update-ref", "refs/pull/3/head", head2)
	run(t, repo2, "git", "update-ref", "refs/pull/3/merge", head2)
	p2 := m2.c().projects["app"]
	p2.fetch(ctx, 0)
	pulls, err = p2.pullRefs(ctx)
	if err != nil || len(pulls) != 1 || pulls[0].FullRef() != "refs/pull/3/head" {
		t.Fatalf("%+v %v", pulls, err)
	}
	// A Ref built by hand, as callers do, keeps the old shape.
	if got := (Ref{Kind: kindPull, Name: "12"}).FullRef(); got != "refs/pull/12/head" {
		t.Error(got)
	}
}

func runOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return string(out)
}
