package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

const libPipeline = `pipeline: app
libraries:
  platform: { repo: ci-templates, version: v3 }
steps:
  ci:
    uses: platform/svc
    with: { name: web }
  deploy:
    image: alpine
    needs: ci
    secrets: { TOKEN: { store: vault, key: deploy } }
    run: echo "$LAUNCHWAY_NEEDS_CI_COVERAGE"
`

const svcTemplate = `template: svc
params:
  name: { type: string }
steps:
  lint: { image: alpine, run: "$LAUNCHWAY_LIB/scripts/lint.sh" }
  test: { image: alpine, needs: lint, run: "echo $PARAM_NAME", secrets: { KEY: { store: vault, key: test } } }
outputs:
  coverage: { from: test.coverage }
`

// expanded is libPipeline expanded with platform at commit.
func expanded(t *testing.T, commit string) *pipeline.Pipeline {
	t.Helper()
	p := parse(t, libPipeline)
	p.Libraries[0].Commit = commit
	x, err := pipeline.Expand(p, func(*pipeline.Library, string) ([]byte, error) { return []byte(svcTemplate), nil })
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func libSource(commit string) Source {
	return Source{Kind: "git", URL: "/srv/app.git", Libraries: []LibrarySource{{Name: "platform", URL: "/srv/lib.git", Commit: commit}}}
}

func TestRunsRecordTheirLibraries(t *testing.T) {
	e, _ := newEngine(t)
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: expanded(t, commitA), Trigger: Trigger{Event: "manual"}, Source: libSource(commitA)})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.GetRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Graph.Libraries) != 1 || d.Graph.Libraries[0].Commit != commitA || d.Graph.Libraries[0].Version != "v3" ||
		len(d.Source.Libraries) != 1 || d.Source.Libraries[0].Commit != commitA {
		t.Fatalf("graph %+v source %+v", d.Graph.Libraries, d.Source)
	}
	// The record is in the run's first event, which everything else is
	// worked out from.
	evs, _ := e.Events(r.ID)
	if c := evs[0].Data; evs[0].Type != RunCreated || c.Pipeline.Libraries[0].Commit != commitA || c.Source.Libraries[0].URL != "/srv/lib.git" {
		t.Fatalf("first event %+v", evs[0])
	}
	for _, sv := range d.StepList {
		if (sv.ID == "deploy") != (sv.Group == "") {
			t.Fatalf("step %s group %q", sv.ID, sv.Group)
		}
	}

	// Jobs carry what the runner needs: where every library is, and the
	// step's own. The project is the run's, whatever the template.
	j := take(t, e)
	if j.StepID != "ci/lint" || j.Library != "platform" || j.Project != "demo" ||
		len(j.Source.Libraries) != 1 || j.Source.Libraries[0].Commit != commitA || j.Env["PARAM_NAME"] != "web" {
		t.Fatalf("job %+v", j)
	}
	ok(t, e, j, map[string]string{"lint": "fine"})
	j = take(t, e)
	if j.StepID != "ci/test" || j.Env["LAUNCHWAY_NEEDS_LINT_LINT"] != "fine" || j.Secrets["KEY"].Key != "test" || j.Project != "demo" {
		t.Fatalf("job %+v", j)
	}
	ok(t, e, j, map[string]string{"coverage": "91%", "private": "x"})
	j = take(t, e)
	if j.StepID != "deploy" || j.Library != "" || j.Env["LAUNCHWAY_NEEDS_CI_COVERAGE"] != "91%" {
		t.Fatalf("job %+v", j)
	}
	// Only what the template exposes reaches steps that need it.
	for k := range j.Env {
		if strings.Contains(k, "PRIVATE") || strings.HasPrefix(k, "LAUNCHWAY_NEEDS_CI_TEST") || strings.HasPrefix(k, "PARAM_") {
			t.Fatalf("deploy got %s", k)
		}
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, r.ID), "run", "succeeded")
}

// A resumed run keeps the commit it was created with, however the library's
// tags have moved since: nothing is resolved again.
func TestResumeUsesTheRecordedCommit(t *testing.T) {
	e, _ := newEngine(t)
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: expanded(t, commitA), Trigger: Trigger{Event: "manual"}, Source: libSource(commitA)})
	if err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil)
	fail(t, e, take(t, e))
	want(t, statuses(t, e, r.ID), "run", "failed", "ci/test", "failed", "deploy", "skipped")

	// Meanwhile another run is created after v3 moved to commit B.
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: expanded(t, commitB), Trigger: Trigger{Event: "manual"}, Source: libSource(commitB)}); err != nil {
		t.Fatal(err)
	}
	j := take(t, e) // the new run's first step
	if j.RunID == r.ID || j.Source.Libraries[0].Commit != commitB {
		t.Fatalf("job %+v", j)
	}

	if err := e.Resume(r.ID, "dana"); err != nil {
		t.Fatal(err)
	}
	for {
		j := take(t, e)
		if j.RunID != r.ID {
			continue
		}
		if j.StepID != "ci/test" || j.Source.Libraries[0].Commit != commitA {
			t.Fatalf("resumed job %+v", j)
		}
		break
	}
}

func TestCreateRunRefusesWhatCannotRun(t *testing.T) {
	e, _ := newEngine(t)
	// Not expanded.
	_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, libPipeline), Source: libSource(commitA)})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "have not been expanded") {
		t.Fatalf("unexpanded: %v", err)
	}
	// Expanded, but the source does not say where the library is, or says
	// another commit.
	for _, src := range []Source{{Kind: "git", URL: "/srv/app.git"}, libSource(commitB), {Kind: "dir", Path: "/tmp",
		Libraries: []LibrarySource{{Name: "platform", URL: "/srv/lib.git", Commit: commitA}}}} {
		_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: expanded(t, commitA), Source: src})
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("%+v: %v", src, err)
		}
	}
	if runs, _ := e.ListRuns(RunFilter{}); len(runs) != 0 {
		t.Fatalf("%d runs", len(runs))
	}
}

// Draft runs that may not read secrets refuse a template's step that asks
// for one, as they do any step.
func TestDraftSecretsAndTemplates(t *testing.T) {
	e, _ := newEngine(t)
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: expanded(t, commitA), Source: libSource(commitA),
		Draft: &Draft{Text: libPipeline, Secrets: false}})
	if err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil)
	none(t, e)
	d, _ := e.GetRun(r.ID)
	for _, s := range d.StepList {
		if s.ID == "ci/test" && (s.Status != StepFailed || !strings.Contains(s.Error, "draft runs of demo may not read secrets")) {
			t.Fatalf("ci/test %s: %s", s.Status, s.Error)
		}
	}
}
