package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const filteredPipeline = `
pipeline: app
on:
  push: { branches: ["*"], paths: [src/**, go.mod] }
  pull_request: { paths_ignore: ["docs/**"] }
steps:
  test: {image: alpine, run: echo ok}
  image:
    image: alpine
    run: echo image
    when: { changed: [src/**] }
`

func commitFiles(t *testing.T, repo, msg string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(repo, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-q", "-m", msg)
}

func decisionsOf(t *testing.T, m *Manager) []Decision {
	t.Helper()
	s, err := m.Get("app")
	if err != nil {
		t.Fatal(err)
	}
	return s.Decisions
}

// A push that changes only documentation starts no run and says why; one
// that changes source starts a run; the first push of a branch, and a
// force-push, always run because the change cannot be told.
func TestPushPathFilters(t *testing.T) {
	dir, repo := prRepo(t, filteredPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	pollOnce(t, m) // seeds

	commitFiles(t, repo, "docs only", map[string]string{"docs/a.md": "x", "README.md": "y"})
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("a docs-only push started %d runs", len(runs))
	}
	d := decisionsOf(t, m)[0]
	if d.Outcome != engine.OutcomePathRule || !strings.Contains(d.Detail, "skipped by path rule: no changed file matches paths (src/**, go.mod); changed: README.md, docs/a.md") {
		t.Fatalf("decision %+v", d)
	}

	commitFiles(t, repo, "source", map[string]string{"src/a.go": "package a"})
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 {
		t.Fatalf("a source push started %d runs", len(runs))
	}
	got, _ := eng.GetRun(runs[0].ID)
	if got.TriggerDetail == nil || got.TriggerDetail.Changed == nil || !got.TriggerDetail.Changed.Known || len(got.TriggerDetail.Changed.Files) != 1 {
		t.Fatalf("the run does not know what changed: %+v", got.TriggerDetail)
	}

	// The first push of a branch cannot be compared: it runs.
	run(t, repo, "git", "branch", "feature", "main")
	pollOnce(t, m)
	if n := len(runsOf(t, eng)); n != 2 {
		t.Fatalf("the first push of a branch started %d runs in all, want 2", n)
	}

	// A force-push that drops the previous head also runs.
	run(t, repo, "git", "checkout", "-q", "feature")
	run(t, repo, "git", "reset", "-q", "--hard", "HEAD~2")
	commitFiles(t, repo, "rewritten docs", map[string]string{"docs/b.md": "z"})
	run(t, repo, "git", "checkout", "-q", "main")
	pollOnce(t, m)
	if n := len(runsOf(t, eng)); n != 3 {
		t.Fatalf("a force-push started %d runs in all, want 3", n)
	}
}

// A step with when: { changed } is skipped, with the reason and the skip
// kind paths, when the change touched nothing it names; the run still passes.
func TestStepChangedSkipsWithReason(t *testing.T) {
	dir, repo := prRepo(t, strings.Replace(filteredPipeline, "paths: [src/**, go.mod]", "paths: [src/**, go.mod, docs/**]", 1))
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	pollOnce(t, m)
	commitFiles(t, repo, "docs", map[string]string{"docs/a.md": "x"})
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	got, _ := eng.GetRun(runs[0].ID)
	var image engine.StepView
	for _, s := range got.StepList {
		if s.ID == "image" {
			image = s
		}
	}
	if image.Status != engine.StepSkippedS || image.SkipKind != engine.SkipPaths || !strings.Contains(image.Error, "skipped by path rule: no file under src/ changed") {
		t.Fatalf("%+v", image)
	}
}

// A pull request that changes only what paths_ignore names starts no run, and
// leaves a decision that satisfies its required checks.
func TestPullRequestPathFilter(t *testing.T) {
	dir, repo := prRepo(t, filteredPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	pollOnce(t, m)
	openPR(t, repo, "4", "", "docs")
	// openPR's commit writes change.txt; make the head change docs only.
	run(t, repo, "git", "checkout", "-q", "--detach", "main")
	commitFiles(t, repo, "docs only", map[string]string{"docs/x.md": "x"})
	run(t, repo, "git", "update-ref", "refs/pull/5/head", "HEAD")
	run(t, repo, "git", "checkout", "-q", "main")
	pollOnce(t, m)
	var five []*engine.RunSummary
	for _, r := range runsOf(t, eng) {
		if r.PullRequest == 5 {
			five = append(five, r)
		}
	}
	if len(five) != 0 {
		t.Fatalf("a docs-only pull request started a run")
	}
	if heads := eng.PathSkippedHeads("app", "refs/pull/5/head"); len(heads) != 1 {
		t.Fatalf("path-skipped heads %v", heads)
	}
}
