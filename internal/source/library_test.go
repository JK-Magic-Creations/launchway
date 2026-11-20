package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// libSetup is a library, an application whose pipeline uses it, and a
// manager that knows both.
type libSetup struct {
	lib, app *testlib.Repo
	eng      *engine.Engine
	m        *Manager
	dir      string
}

func newLibSetup(t *testing.T, appPipeline string, pp *ProjectPipeline) *libSetup {
	t.Helper()
	dir := t.TempDir()
	s := &libSetup{dir: dir, lib: testlib.NewLibrary(t, filepath.Join(dir, "lib"))}
	files := map[string]string{"README": "app"}
	if appPipeline != "" {
		files[".launchway/pipeline.yaml"] = appPipeline
	}
	s.app = testlib.New(t, filepath.Join(dir, "app"), files)
	s.eng = newTestEngine(t, testdb.New(t))
	m, err := NewManagerWithLibraries(s.eng, filepath.Join(dir, "data"),
		[]Project{{Name: "app", URL: s.app.Bare, Pipeline: pp}},
		[]Library{{Name: "ci-templates", URL: s.lib.Bare}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.m = m
	return s
}

// changed makes the next StartRun fetch the application's repository,
// which it otherwise skips if it fetched in the last few seconds.
func (s *libSetup) changed() {
	p := s.m.c().projects["app"]
	p.stateMu.Lock()
	p.lastFetch = time.Time{}
	p.stateMu.Unlock()
}

// graph returns the pipeline a run keeps.
func (s *libSetup) graph(t *testing.T, runID string) *engine.RunDetail {
	t.Helper()
	d, err := s.eng.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func onlyLibrary(t *testing.T, d *engine.RunDetail) *pipeline.Library {
	t.Helper()
	if len(d.Graph.Libraries) != 1 {
		t.Fatalf("libraries %+v", d.Graph.Libraries)
	}
	l := d.Graph.Libraries[0]
	if len(d.Source.Libraries) != 1 || d.Source.Libraries[0].Commit != l.Commit || d.Source.Libraries[0].Name != l.Name {
		t.Fatalf("source libraries %+v, graph %+v", d.Source.Libraries, l)
	}
	return l
}

func TestLibraryVersionsResolveToCommits(t *testing.T) {
	s := newLibSetup(t, testlib.AppPipeline("ci-templates", "v1"), nil)
	ctx := context.Background()
	v1 := s.lib.Head()

	// A tag.
	r, err := s.m.StartRun(ctx, "app", "main", "dana", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := s.graph(t, r.ID)
	l := onlyLibrary(t, d)
	if l.Name != "platform" || l.Repo != "ci-templates" || l.Version != "v1" || l.Commit != v1 || l.RefKind != "tag" || l.Override != "" {
		t.Fatalf("library %+v", l)
	}
	if d.Source.Libraries[0].URL != s.lib.Bare {
		t.Fatalf("source %+v", d.Source.Libraries)
	}
	if g := d.Graph.Group("ci"); g == nil || len(g.Steps) != 1 || g.Steps[0] != "ci/run" {
		t.Fatalf("groups %+v", d.Graph.Groups)
	}

	// The tag moves; the run keeps the commit it was created with, and a new
	// run gets the new one.
	v2 := s.lib.Commit(map[string]string{"scripts/check.sh": testlib.CheckScript("2")}, "version 2")
	s.lib.Tag("v1", v2)
	r2, err := s.m.StartRun(ctx, "app", "main", "dana", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := onlyLibrary(t, s.graph(t, r2.ID)).Commit; got != v2 {
		t.Fatalf("after the tag moved, a new run got %s, want %s", got, v2)
	}
	if got := onlyLibrary(t, s.graph(t, r.ID)).Commit; got != v1 {
		t.Fatalf("the first run now says %s", got)
	}

	// A branch, and a full commit id.
	for _, c := range []struct{ version, commit, kind string }{{"main", v2, "branch"}, {v1, v1, "commit"}} {
		s.app.Commit(map[string]string{".launchway/pipeline.yaml": testlib.AppPipeline("ci-templates", c.version)}, "use "+c.version)
		s.changed()
		r, err := s.m.StartRun(ctx, "app", "main", "dana", nil)
		if err != nil {
			t.Fatal(err)
		}
		if l := onlyLibrary(t, s.graph(t, r.ID)); l.Commit != c.commit || l.RefKind != c.kind {
			t.Fatalf("%s: %+v", c.version, l)
		}
	}

	// A tag and a branch with the same name: the tag wins.
	s.lib.Tag("stable", v1)
	s.lib.Branch("stable", v2)
	s.app.Commit(map[string]string{".launchway/pipeline.yaml": testlib.AppPipeline("ci-templates", "stable")}, "use stable")
	s.changed()
	r, err = s.m.StartRun(ctx, "app", "main", "dana", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l := onlyLibrary(t, s.graph(t, r.ID)); l.Commit != v1 || l.RefKind != "tag" {
		t.Fatalf("stable: %+v", l)
	}

	// A version that does not exist, and a short commit id, which is not
	// accepted: only full ids are.
	for _, v := range []string{"v9", v1[:12]} {
		s.app.Commit(map[string]string{".launchway/pipeline.yaml": testlib.AppPipeline("ci-templates", v)}, "use "+v)
		s.changed()
		_, err = s.m.StartRun(ctx, "app", "main", "dana", nil)
		var ps pipeline.Problems
		if !errors.As(err, &ps) || len(ps) != 1 || ps[0].Line != 5 ||
			!strings.Contains(ps[0].Message, `the library platform (ci-templates) has no tag, branch or commit called "`+v+`", the version `+v) {
			t.Fatalf("%s: %v", v, err)
		}
	}

	// A library the server does not list.
	s.app.Commit(map[string]string{".launchway/pipeline.yaml": testlib.AppPipeline("elsewhere", "v1")}, "use another library")
	s.changed()
	_, err = s.m.StartRun(ctx, "app", "main", "dana", nil)
	var ps pipeline.Problems
	if !errors.As(err, &ps) || ps[0].Line != 5 || !strings.Contains(ps[0].Message, `"elsewhere" is not a library this server allows (ci-templates)`) {
		t.Fatalf("unknown library: %v", err)
	}
	if runs, _ := s.eng.ListRuns(engine.RunFilter{}); len(runs) != 5 {
		t.Fatalf("%d runs; refused starts must start nothing", len(runs))
	}
}

func TestLibraryOverridesOnManualAndDraftRuns(t *testing.T) {
	s := newLibSetup(t, testlib.AppPipeline("ci-templates", "v1"), nil)
	ctx := context.Background()
	tip := s.lib.Commit(map[string]string{"scripts/check.sh": testlib.CheckScript("next")}, "work in progress")

	r, err := s.m.StartRun(ctx, "app", "main", "dana", map[string]string{"platform": "main"})
	if err != nil {
		t.Fatal(err)
	}
	if l := onlyLibrary(t, s.graph(t, r.ID)); l.Version != "v1" || l.Override != "main" || l.Commit != tip || l.RefKind != "branch" {
		t.Fatalf("override: %+v", l)
	}

	text := testlib.AppPipeline("ci-templates", "v1")
	dr, err := s.m.StartDraft(ctx, DraftRequest{Project: "app", Ref: "main", Text: text, Actor: "dana", Libraries: map[string]string{"platform": tip}})
	if err != nil {
		t.Fatal(err)
	}
	if l := onlyLibrary(t, s.graph(t, dr.ID)); l.Override != tip || l.Commit != tip || l.RefKind != "commit" {
		t.Fatalf("draft override: %+v", l)
	}

	// Overrides are checked before anything is fetched.
	for _, bad := range []map[string]string{
		{"other": "main"},           // the pipeline has no such library
		{"platform": "-x"},          // not a version
		{"Platform": "main"},        // not a library name
		{"platform": "../../etc/x"}, // not a version
	} {
		_, err := s.m.StartRun(ctx, "app", "main", "dana", bad)
		if !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("%v: %v", bad, err)
		}
		_, err = s.m.StartDraft(ctx, DraftRequest{Project: "app", Ref: "main", Text: text, Actor: "dana", Libraries: bad})
		if !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("draft %v: %v", bad, err)
		}
	}
	// An override that does not exist is a problem, at the library's line.
	_, err = s.m.StartRun(ctx, "app", "main", "dana", map[string]string{"platform": "nope"})
	var ps pipeline.Problems
	if !errors.As(err, &ps) || !strings.Contains(ps[0].Message, `called "nope", the override nope`) {
		t.Fatalf("missing override: %v", err)
	}
}

func TestProjectPipelineFromALibrary(t *testing.T) {
	pp := &ProjectPipeline{Library: "ci-templates", Version: "v1", Template: "check", With: map[string]string{"greeting": "configured"},
		On: pipeline.Triggers{Push: &pipeline.PushTrigger{Branches: []string{"main"}}}}
	s := newLibSetup(t, "", pp)
	ctx := context.Background()
	p := s.m.c().projects["app"]
	if err := s.m.poll(ctx, p); err != nil { // first sight
		t.Fatal(err)
	}

	// No file in the repository: the configured pipeline runs.
	s.app.Commit(map[string]string{"code.txt": "1"}, "change code")
	s.changed()
	if err := s.m.poll(ctx, p); err != nil {
		t.Fatal(err)
	}
	st, _ := s.m.Get("app")
	if st.Pipeline == nil || st.Pipeline.Template != "check" {
		t.Fatalf("status %+v", st.Pipeline)
	}
	dec := st.Decisions[0]
	if dec.Outcome != "started" || !strings.Contains(dec.Detail, "the pipeline configured on the server ran: ci-templates@v1, template check") {
		t.Fatalf("decision %+v", dec)
	}
	d := s.graph(t, dec.RunID)
	if d.Graph.Name != "app" || d.Graph.Origin == nil || d.Graph.Origin.Kind != pipeline.OriginServer ||
		!strings.Contains(d.Graph.Origin.Text, "uses: ci-templates/check") || d.Graph.Group("check").Params["greeting"] != "configured" {
		t.Fatalf("graph %+v origin %+v", d.Graph, d.Graph.Origin)
	}
	onlyLibrary(t, d)

	// The editor starts from the configured pipeline.
	f, err := s.m.ReadPipelineFile(ctx, "app", "main")
	if err != nil {
		t.Fatal(err)
	}
	if f.Exists || !f.Configured || f.Text != d.Graph.Origin.Text {
		t.Fatalf("file %+v", f)
	}

	// A file in the repository takes its place.
	s.app.Commit(map[string]string{".launchway/pipeline.yaml": testlib.AppPipeline("ci-templates", "v1")}, "own pipeline")
	s.changed()
	if err := s.m.poll(ctx, p); err != nil {
		t.Fatal(err)
	}
	st, _ = s.m.Get("app")
	dec = st.Decisions[0]
	if dec.Outcome != "started" || !strings.Contains(dec.Detail, "the repository's own .launchway/pipeline.yaml ran, in place of the pipeline configured on the server") {
		t.Fatalf("decision %+v", dec)
	}
	if d := s.graph(t, dec.RunID); d.Graph.Origin != nil || d.Graph.Group("ci") == nil {
		t.Fatalf("graph %+v", d.Graph)
	}

	// The configuration is checked when the server starts.
	for _, bad := range []*ProjectPipeline{
		{Library: "nope", Version: "v1", Template: "check"},
		{Library: "ci-templates", Version: "-v1", Template: "check"},
		{Library: "ci-templates", Version: "v1", Template: "Check"},
	} {
		_, err := NewManagerWithLibraries(s.eng, t.TempDir(), []Project{{Name: "app", URL: s.app.Bare, Pipeline: bad}},
			[]Library{{Name: "ci-templates", URL: s.lib.Bare}}, nil)
		if err == nil {
			t.Fatalf("%+v was accepted", bad)
		}
	}
	if _, err := NewManagerWithLibraries(s.eng, t.TempDir(), nil, []Library{{Name: "Bad_Name", URL: s.lib.Bare}}, nil); err == nil {
		t.Fatal("a bad library name was accepted")
	}
}

// A library that needs credentials is read with the server's Git settings,
// as projects are, and the credentials never appear in what is shown.
func TestPrivateLibraryWithGitEnvCredentials(t *testing.T) {
	allowLoopbackHosts(t)
	dir := t.TempDir()
	lib := testlib.NewLibrary(t, filepath.Join(dir, "lib"))
	testlib.Git(t, lib.Bare, "update-server-info")
	files := http.FileServer(http.Dir(filepath.Dir(lib.Bare)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret-library-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		http.StripPrefix("/git/", files).ServeHTTP(w, r)
	}))
	defer srv.Close()
	url := srv.URL + "/git/origin.git"
	// Without these, Git asks for a password in a window on a desktop.
	t.Setenv("GIT_ASKPASS", "")
	t.Setenv("SSH_ASKPASS", "")
	app := testlib.New(t, filepath.Join(dir, "app"), map[string]string{".launchway/pipeline.yaml": testlib.AppPipeline("ci-templates", "v1")})
	eng := newTestEngine(t, testdb.New(t))
	m, err := NewManagerWithLibraries(eng, filepath.Join(dir, "data"), []Project{{Name: "app", URL: app.Bare}},
		[]Library{{Name: "ci-templates", URL: url}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = m.StartRun(ctx, "app", "main", "dana", nil)
	var ps pipeline.Problems
	if !errors.As(err, &ps) || !strings.Contains(ps[0].Message, "reading the library platform (ci-templates)") {
		t.Fatalf("without credentials: %v", err)
	}
	t.Logf("without credentials: %v", err)

	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http."+srv.URL+"/.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: Bearer s3cret-library-token")
	r, err := m.StartRun(ctx, "app", "main", "dana", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := eng.GetRun(r.ID)
	if d.Graph.Libraries[0].Commit != lib.Head() {
		t.Fatalf("libraries %+v", d.Graph.Libraries)
	}
	for _, st := range m.Libraries() {
		if strings.Contains(st.URL+st.Error, "s3cret") {
			t.Fatalf("credentials shown: %+v", st)
		}
	}
}

func TestCheckExpandsAgainstCurrentVersions(t *testing.T) {
	s := newLibSetup(t, "", nil)
	ctx := context.Background()
	p, err := s.m.Check(ctx, testlib.AppPipeline("ci-templates", "v1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Expanded() || p.Step("ci/run") == nil || p.Libraries[0].Commit != s.lib.Head() {
		t.Fatalf("%+v", p)
	}
	// A parameter mistake is reported at its line in the pipeline.
	_, err = s.m.Check(ctx, strings.Replace(testlib.AppPipeline("ci-templates", "v1"), "greeting: hi", "greting: hi", 1), nil)
	var ps pipeline.Problems
	if !errors.As(err, &ps) || ps[0].Line != 9 || ps[0].File != "" || !strings.Contains(ps[0].Message, `has no parameter "greting"`) {
		t.Fatalf("%v", err)
	}
	// A mistake in the template is reported in the template's file.
	s.lib.Commit(map[string]string{"templates/check.yaml": strings.Replace(testlib.CheckTemplate, "run:\n    image", "run:\n    imagge", 1)}, "break it")
	time.Sleep(checkFresh) // Check reuses a fetch this recent
	_, err = s.m.Check(ctx, testlib.AppPipeline("ci-templates", "main"), nil)
	if !errors.As(err, &ps) {
		t.Fatalf("%v", err)
	}
	found := false
	for _, p := range ps {
		if p.File == "templates/check.yaml" && p.Library == "platform" && p.Line == 8 && strings.Contains(p.Message, `unknown key "imagge"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("%v", ps)
	}
}
