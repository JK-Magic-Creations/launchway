package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// commitFiles commits files (path → text) with a message.
func relCommit(t *testing.T, repo, msg string, files map[string]string) {
	t.Helper()
	for p, text := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(repo, p)), 0o755)
		os.WriteFile(filepath.Join(repo, p), []byte(text), 0o644)
	}
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-q", "-m", msg)
}

// A fixture repository with a first release tag, a squash-merged pull request, a merge-commit pull
// request, a plain commit and a change to a manifest: the notes source reads them as changes.
func TestReleaseChangesFromMergedPullRequests(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	run(t, work, "git", "init", "-q", "-b", "main")
	commit(t, work, pl, "first")
	run(t, work, "git", "tag", "-a", "v1.0.0", "-m", "one")
	first := gitIn(t, work, "rev-parse", "HEAD")
	relCommit(t, work, "feat(ui): the matrix (#12)", map[string]string{"web/matrix.txt": "m"})
	run(t, work, "git", "checkout", "-q", "-b", "side")
	relCommit(t, work, "fix: a crash on start", map[string]string{"app.txt": "fixed"})
	run(t, work, "git", "checkout", "-q", "main")
	run(t, work, "git", "merge", "--no-ff", "-q", "-m", "Merge pull request #13 from ann/side\n\nfix: a crash on start", "side")
	relCommit(t, work, "tidy the readme", map[string]string{"README.md": "r"})
	relCommit(t, work, "chore(deps): bump x\n\nBREAKING CHANGE: x 2 needs go 1.30", map[string]string{"deploy/values.yaml": "a: 1"})
	tip := gitIn(t, work, "rev-parse", "HEAD")
	bare := filepath.Join(dir, "origin.git")
	run(t, dir, "git", "clone", "-q", "--bare", work, bare)

	eng := newTestEngine(t, testdb.New(t))
	m := manager(t, eng, filepath.Join(dir, "data"), bare, nil)
	ctx := context.Background()

	r, err := m.ResolveRef(ctx, "app", "main")
	if err != nil || r.SHA != tip {
		t.Fatalf("%+v %v", r, err)
	}
	commits, err := m.CommitsBetween(ctx, "app", first, tip)
	if err != nil || len(commits) < 4 {
		t.Fatalf("%d commits %v", len(commits), err)
	}
	changes := ChangesFromCommits(commits, func(n int) string { return "https://git.example/app/pulls/" + string(rune('0'+n%10)) })
	var pulls, plain int
	titles := map[int]string{}
	var breaking bool
	for _, c := range changes {
		switch c.Kind {
		case "pull":
			pulls++
			titles[c.Ref] = c.Title
			if c.URL == "" {
				t.Errorf("a pull request has a link: %+v", c)
			}
		case "commit":
			plain++
		}
		breaking = breaking || strings.Contains(c.Body, "BREAKING CHANGE")
	}
	if pulls != 2 || titles[12] != "feat(ui): the matrix" || titles[13] != "fix: a crash on start" {
		t.Fatalf("pull requests: %v from %+v", titles, changes)
	}
	if plain < 2 || !breaking {
		t.Fatalf("plain commits %d breaking %v", plain, breaking)
	}
	for _, c := range changes {
		if strings.HasPrefix(c.Title, "Merge ") {
			t.Errorf("a join of branches is not a change: %+v", c)
		}
	}
	d, err := m.Compare(ctx, "app", first, tip)
	if err != nil || len(d.Manifest) != 1 || d.Manifest[0] != "deploy/values.yaml" || !strings.Contains(d.Stat, "files changed") {
		t.Fatalf("compare: %+v %v", d, err)
	}
	// An existing tag is read, not changed.
	ti, err := m.ReleaseTag(ctx, "app", "v1.0.0")
	if err != nil || !ti.Exists || !ti.Annotated || ti.Commit != first {
		t.Fatalf("tag: %+v %v", ti, err)
	}
	if ti, _ := m.ReleaseTag(ctx, "app", "v9.9.9"); ti.Exists {
		t.Fatal("no such tag")
	}
	if _, err := m.ReleaseTag(ctx, "app", "bad name"); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("a bad tag name: %v", err)
	}
	// Writing needs push settings.
	if _, err := m.CreateTag(ctx, "app", "v1.1.0", tip, "x", Author{User: "ann"}, time.Now()); !errors.Is(err, ErrNotSavable) {
		t.Fatalf("a read-only project cannot be tagged: %v", err)
	}
}

func TestCreateAnnotatedTagAndProposeChangelog(t *testing.T) {
	dir := t.TempDir()
	origin := remote(t, dir, pl)
	eng := newTestEngine(t, testdb.New(t))
	m := manager(t, eng, filepath.Join(dir, "data"), origin, &PushSettings{})
	ctx := context.Background()
	tip := gitIn(t, origin, "rev-parse", "main")
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	ti, err := m.CreateTag(ctx, "app", "v1.1.0", tip, "Release 1.1.0", Author{User: "ann", Name: "Ann"}, now)
	if err != nil || !ti.Exists || !ti.Annotated || ti.Commit != tip {
		t.Fatalf("%+v %v", ti, err)
	}
	if got := gitIn(t, origin, "cat-file", "-t", "refs/tags/v1.1.0"); got != "tag" {
		t.Fatalf("the origin has %s, want an annotated tag", got)
	}
	if msg := gitIn(t, origin, "tag", "-n1", "v1.1.0"); !strings.Contains(msg, "Release 1.1.0") {
		t.Fatalf("message: %q", msg)
	}
	// A tag is never moved or made twice.
	if _, err := m.CreateTag(ctx, "app", "v1.1.0", tip, "again", Author{User: "ann"}, now); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("a second tag of that name: %v", err)
	}
	noTempLeft(t, filepath.Join(dir, "data"))

	// The changelog goes to a branch, never to main.
	p, err := m.ProposeChangelog(ctx, "app", "main", "CHANGELOG.md", "1.1.0", "## 1.1.0\n\n- a thing\n", Author{User: "ann"}, now)
	if err != nil || !strings.HasPrefix(p.Branch, DefaultBranchPrefix) || p.Base != "main" {
		t.Fatalf("%+v %v", p, err)
	}
	if gitIn(t, origin, "rev-parse", "main") != tip {
		t.Fatal("main moved: a changelog must be proposed, never pushed to the branch")
	}
	if txt := gitIn(t, origin, "show", p.Branch+":CHANGELOG.md"); !strings.Contains(txt, "## 1.1.0") || !strings.HasPrefix(txt, "# Changelog") {
		t.Fatalf("changelog: %q", txt)
	}
	if _, err := m.ProposeChangelog(ctx, "app", "main", "CHANGELOG.md", "1.1.0", "## 1.1.0\n", Author{User: "ann"}, now); !errors.Is(err, engine.ErrConflict) {
		t.Fatalf("proposed twice: %v", err)
	}
	noTempLeft(t, filepath.Join(dir, "data"))
}
