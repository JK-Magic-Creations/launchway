package api

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// gatedTemplate waits for an approval, so runs need no runner.
const gatedTemplate = `template: gated
params:
  target: { type: string, default: dev, options: [dev, prod] }
steps:
  gate: { kind: approval, message: "Ship it?" }
  ship: { image: none, needs: gate, run: "$LAUNCHWAY_LIB/scripts/check.sh" }
`

const gatedApp = `pipeline: app
libraries:
  platform: { repo: ci-templates, version: v1 }
steps:
  ci:
    uses: platform/gated
    with: { target: prod }
`

// libAuthEnv is a signed-in server with a library, pay-lib (whose pipeline
// uses it) and pay-conf (whose repository has no pipeline: the server
// configures one from the library).
func libAuthEnv(t *testing.T) (*authEnv, *testlib.Repo) {
	t.Helper()
	dir := t.TempDir()
	lib := testlib.NewLibrary(t, filepath.Join(dir, "lib"))
	lib.Commit(map[string]string{"templates/gated.yaml": gatedTemplate}, "gated")
	lib.Tag("v1", lib.Head())
	app := testlib.New(t, filepath.Join(dir, "app"), map[string]string{".launchway/pipeline.yaml": gatedApp})
	conf := testlib.New(t, filepath.Join(dir, "conf"), map[string]string{"README": "no pipeline here"})
	e := setupAuth(t, envOpts{
		libraries: []source.Library{{Name: "ci-templates", URL: lib.Bare}},
		projects: []source.Project{
			{Name: "pay-lib", URL: app.Bare, Poll: time.Hour},
			{Name: "pay-conf", URL: conf.Bare, Poll: time.Hour, Pipeline: &source.ProjectPipeline{
				Library: "ci-templates", Version: "v1", Template: "gated", With: map[string]string{"target": "dev"}}},
		},
	})
	return e, lib
}

func TestLibraryOverridesNeedTheRightToStartAndAreAudited(t *testing.T) {
	e, lib := libAuthEnv(t)
	v1 := lib.Head()
	tip := lib.Commit(map[string]string{"scripts/check.sh": testlib.CheckScript("2")}, "next")
	override := `{"ref": "main", "libraries": {"platform": "main"}}`
	runs := e.srv.URL + "/api/v1/projects/pay-lib/runs"

	// A viewer may not; someone who cannot see the project is told it does
	// not exist.
	if r := e.signIn(t, "vic").do(http.MethodPost, runs, override); r.status != http.StatusForbidden {
		t.Fatalf("viewer: %d %s", r.status, r.body)
	}
	if r := e.signIn(t, "dana").do(http.MethodPost, runs, override); r.status != http.StatusNotFound {
		t.Fatalf("no access: %d %s", r.status, r.body)
	}

	// An operator may, and the run records the override and its commit.
	otto := e.signIn(t, "otto")
	r := otto.do(http.MethodPost, runs, override)
	if r.status != http.StatusCreated {
		t.Fatalf("operator: %d %s", r.status, r.body)
	}
	var run engine.RunSummary
	r.json(t, &run)
	var d engine.RunDetail
	otto.get(e.srv.URL+"/api/v1/runs/"+run.ID).json(t, &d)
	if l := d.Graph.Libraries[0]; l.Override != "main" || l.Commit != tip || l.Version != "v1" {
		t.Fatalf("library %+v", l)
	}
	if g := d.Graph.Group("ci"); g == nil || g.Params["target"] != "prod" {
		t.Fatalf("group %+v", g)
	}

	// Without an override, the tag.
	r = otto.do(http.MethodPost, runs, `{"ref": "main"}`)
	r.json(t, &run)
	var d2 engine.RunDetail // a fresh one: JSON leaves out an empty override
	otto.get(e.srv.URL+"/api/v1/runs/"+run.ID).json(t, &d2)
	if l := d2.Graph.Libraries[0]; l.Override != "" || l.Commit != v1 {
		t.Fatalf("library %+v", l)
	}

	// A template's steps have a / in their IDs, which URLs carry escaped.
	ada := e.signIn(t, "ada")
	stepURL := e.srv.URL + "/api/v1/runs/" + run.ID + "/steps/" + url.PathEscape("ci/gate")
	if !strings.Contains(stepURL, "ci%2Fgate") {
		t.Fatalf("not escaped: %s", stepURL)
	}
	if r := ada.get(stepURL + "/logs"); r.status != http.StatusOK {
		t.Fatalf("logs: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, stepURL+"/approve", `{}`); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	var after engine.RunDetail
	ada.get(e.srv.URL+"/api/v1/runs/"+run.ID).json(t, &after)
	for _, s := range after.StepList {
		if s.ID == "ci/gate" && s.Status != engine.StepSucceeded {
			t.Fatalf("ci/gate is %s", s.Status)
		}
	}

	// A bad override is refused before anything is fetched.
	if r := otto.do(http.MethodPost, runs, `{"ref": "main", "libraries": {"platform": "--upload-pack=touch /tmp/x"}}`); r.status != http.StatusBadRequest {
		t.Fatalf("bad override: %d %s", r.status, r.body)
	}
	// A draft run with an override needs the right to start drafts, which
	// operators have by default.
	draft := jsonBody(t, map[string]any{"ref": "main", "pipeline": gatedApp, "libraries": map[string]string{"platform": tip}})
	if r := e.signIn(t, "vic").do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-lib/draft-runs", draft); r.status != http.StatusForbidden {
		t.Fatalf("viewer draft: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-lib/draft-runs", draft); r.status != http.StatusCreated {
		t.Fatalf("operator draft: %d %s", r.status, r.body)
	}

	// The audit trail names the override, the commit it resolved to and
	// the template, for manual and draft starts alike.
	var starts []engine.AuditEntry
	for _, a := range e.auditEntries(t) {
		if a.Project == "pay-lib" && (a.Action == "run.start" || a.Action == "run.draft") {
			starts = append(starts, a)
		}
	}
	if len(starts) != 7 {
		t.Fatalf("entries %+v", starts)
	}
	for i, want := range []struct{ user, outcome string }{{"vic", "denied"}, {"dana", "denied"}, {"otto", "ok"}, {"otto", "ok"},
		{"otto", "failed"}, {"vic", "denied"}, {"otto", "ok"}} {
		if starts[i].User != want.user || starts[i].Outcome != want.outcome {
			t.Fatalf("entry %d: %+v", i, starts[i])
		}
	}
	for _, c := range []struct {
		i    int
		want []string
	}{
		{2, []string{"platform=ci-templates@v1 overridden by main at " + tip, "templates platform/gated"}},
		{3, []string{"platform=ci-templates@v1 at " + v1, "templates platform/gated"}},
		{4, []string{"--upload-pack"}}, // what was refused, as asked
		{6, []string{"pipeline sha256:", "platform=ci-templates@v1 overridden by " + tip + " at " + tip, "templates platform/gated"}},
	} {
		for _, w := range c.want {
			if !strings.Contains(starts[c.i].Detail, w) {
				t.Fatalf("entry %d detail %q does not say %q", c.i, starts[c.i].Detail, w)
			}
		}
	}
}

type validateResult struct {
	OK       bool               `json:"ok"`
	Expanded bool               `json:"expanded"`
	Problems pipeline.Problems  `json:"problems"`
	Graph    *pipeline.Pipeline `json:"graph"`
}

func TestValidateWithAProject(t *testing.T) {
	e, lib := libAuthEnv(t)
	ada := e.signIn(t, "ada")
	validate := func(b *browser, query, text string) (int, validateResult) {
		t.Helper()
		r := b.do(http.MethodPost, e.srv.URL+"/api/v1/validate"+query, text, "Content-Type", "text/yaml")
		var out validateResult
		if r.status == http.StatusOK {
			r.json(t, &out)
		}
		return r.status, out
	}

	// Without a project, a uses step is one node, not expanded.
	code, res := validate(ada, "", gatedApp)
	if code != 200 || !res.OK || res.Expanded || res.Graph.Step("ci") == nil || res.Graph.Step("ci").Uses != "platform/gated" {
		t.Fatalf("%d %+v", code, res)
	}
	// With one, it is expanded from the library as a run would be.
	for _, q := range []string{"?project=pay-lib", "?expand=1"} {
		code, res = validate(ada, q, gatedApp)
		if code != 200 || !res.OK || !res.Expanded || res.Graph.Step("ci/gate") == nil || res.Graph.Libraries[0].Commit != lib.Head() {
			t.Fatalf("%s: %d %+v", q, code, res)
		}
	}
	// A parameter mistake is reported at its line.
	code, res = validate(ada, "?project=pay-lib", strings.Replace(gatedApp, "target: prod", "target: staging", 1))
	if code != 200 || res.OK || len(res.Problems) != 1 || res.Problems[0].Line != 7 || !strings.Contains(res.Problems[0].Message, `target must be one of "dev", "prod"`) {
		t.Fatalf("%d %+v", code, res)
	}
	// A project the caller cannot see is not there for them.
	dana := e.signIn(t, "dana")
	if code, _ := validate(dana, "?project=pay-lib", gatedApp); code != http.StatusNotFound {
		t.Fatalf("dana: %d", code)
	}
	if code, _ := validate(ada, "?project=nope", gatedApp); code != http.StatusNotFound {
		t.Fatalf("no such project: %d", code)
	}
	if code, res := validate(dana, "?project=web", gatedApp); code != 200 || !res.OK || !res.Expanded {
		t.Fatalf("dana on web: %d %+v", code, res)
	}
}

func TestProjectPipelineFromALibraryOverTheAPI(t *testing.T) {
	e, _ := libAuthEnv(t)
	otto := e.signIn(t, "otto")
	var v projectView
	otto.get(e.srv.URL+"/api/v1/projects/pay-conf").json(t, &v)
	if v.Pipeline == nil || v.Pipeline.Library != "ci-templates" || v.Pipeline.Template != "gated" {
		t.Fatalf("project %+v", v.Pipeline)
	}
	r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-conf/runs", `{"ref": "main"}`)
	if r.status != http.StatusCreated {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var run engine.RunSummary
	r.json(t, &run)
	if run.Pipeline != "pay-conf" {
		t.Fatalf("pipeline %q", run.Pipeline)
	}
	var file struct {
		Text   string `json:"text"`
		Origin string `json:"origin"`
	}
	otto.get(e.srv.URL+"/api/v1/runs/"+run.ID+"/pipeline-file").json(t, &file)
	if file.Origin != "server" || !strings.Contains(file.Text, "pipeline configured for it on the server") || !strings.Contains(file.Text, "uses: ci-templates/gated") {
		t.Fatalf("file %+v", file)
	}
	var pf source.PipelineFile
	otto.get(e.srv.URL+"/api/v1/projects/pay-conf/pipeline?ref=main").json(t, &pf)
	if pf.Exists || !pf.Configured || pf.Text != file.Text {
		t.Fatalf("editor file %+v", pf)
	}
}
