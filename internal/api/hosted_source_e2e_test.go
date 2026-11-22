package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

const helloPipeline = `
pipeline: hello
steps:
  say: {image: none, run: "echo hello from a hosted repository"}
`

// A repository hosted by this installation, added as a Flight, runs a pipeline
// with no credential configured anywhere: the server reads it from its own
// storage and the Station is handed a credential for that run and repository.
func TestPipelineOnAHostedRepositoryNeedsNoCredentials(t *testing.T) {
	for _, k := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_ASKPASS"} {
		t.Setenv(k, "")
	}
	os.Unsetenv("GIT_CONFIG_COUNT")
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")

	// Push a pipeline to it as a person (the only credential in this test).
	src := t.TempDir()
	gitRepo(t, src, helloPipeline)
	tok := e.tokenFor(t, "ada", "pusher")
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/shop", tok), "HEAD:refs/heads/main")

	// "Add a Flight" for it: the repository's own address, nothing else.
	addr := e.srv.URL + "/git/acme/shop.git"
	if err := e.api.Projects.Reconfigure([]source.Project{{Name: "shop", URL: addr, Poll: time.Hour}}, nil); err != nil {
		t.Fatal(err)
	}
	e.api.Projects.SetHostedLocal(e.api)
	e.hostRunner(t, "station-1", nil)

	// The rule: only the installation's own addresses count.
	for addr, want := range map[string]bool{
		e.srv.URL + "/git/acme/shop.git": true, e.srv.URL + "/git/acme/shop": true, e.srv.URL + "/acme/shop.git": true,
		strings.ToUpper(e.srv.URL[:4]) + e.srv.URL[4:] + "/git/acme/shop.git/": true,
		"http://elsewhere.example.com/git/acme/shop.git":                       false, e.srv.URL + "/git/acme/other.git": false,
		e.srv.URL + "/git/acme/shop.git/../other": false, "": false, "git@evil:acme/shop.git": false,
	} {
		if _, _, ok := e.api.Resolve(context.Background(), addr); ok != want {
			t.Errorf("Resolve(%q) = %v, want %v", addr, ok, want)
		}
	}

	r, err := e.api.Projects.StartRun(context.Background(), "shop", "main", "someone-else", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := waitRun(t, e.eng, r.ID)
	if d.Status != engine.RunSucceeded {
		t.Fatalf("the run: %s %+v", d.Status, d.StepList)
	}
	// The server read the repository from its storage: its mirror's origin is the local copy, not the address.
	mirrorCfg, _ := os.ReadFile(filepath.Join(e.dir, "data", "repos", "shop.git", "config"))
	if strings.Contains(string(mirrorCfg), addr) {
		t.Errorf("the server's mirror points at the network address:\n%s", mirrorCfg)
	}
	// The credential given to the Station is for this run and this repository only, and is not stored with the run.
	job := &engine.Job{RunID: r.ID, Source: engine.Source{Kind: "git", URL: addr}}
	e.api.sourceJobFor(context.Background(), job)
	if job.Source.Hosted != "acme/shop" || !strings.HasPrefix(job.Source.Credential, "lwr_") {
		t.Fatalf("the job's source: %+v", job.Source)
	}
	if code := gitStatusBearer(e, job.Source.Credential, "/git/acme/shop.git", "git-upload-pack"); code != 200 {
		t.Errorf("the run credential reading its repository: %d", code)
	}
	hostedRepo(t, e, "acme/other")
	if code := gitStatusBearer(e, job.Source.Credential, "/git/acme/other.git", "git-upload-pack"); code != 404 {
		t.Errorf("the run credential reading another repository: %d", code)
	}
	if code := gitStatusBearer(e, job.Source.Credential, "/git/acme/shop.git", "git-receive-pack"); code != 403 {
		t.Errorf("the run credential pushing: %d", code)
	}
	// A repository elsewhere gets no credential.
	other := &engine.Job{RunID: r.ID, Source: engine.Source{Kind: "git", URL: "https://example.com/acme/shop.git"}}
	e.api.sourceJobFor(context.Background(), other)
	if other.Source.Credential != "" {
		t.Errorf("a credential for an address that is not this installation's: %+v", other.Source)
	}
}

// A pipeline that uses a shared library hosted by this same installation runs with nothing configured: the
// server reads the library from its storage and the Station is handed a credential for it.
func TestPipelineUsingAHostedLibraryNeedsNoCredentials(t *testing.T) {
	os.Unsetenv("GIT_CONFIG_COUNT")
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/ci-templates")
	tok := e.tokenFor(t, "ada", "pusher")
	lib := testlib.NewLibrary(t, filepath.Join(e.dir, "lib"))
	mustGit(t, lib.Bare, "push", "-q", withToken(e.srv.URL, "/acme/ci-templates", tok), "--all")
	mustGit(t, lib.Bare, "push", "-q", withToken(e.srv.URL, "/acme/ci-templates", tok), "--tags")
	app := testlib.New(t, filepath.Join(e.dir, "app"), map[string]string{".launchway/pipeline.yaml": libApp("v1", "none")})

	libAddr := e.srv.URL + "/git/acme/ci-templates.git"
	if err := e.api.Projects.Reconfigure([]source.Project{{Name: "app", URL: app.Bare, Poll: time.Hour}},
		[]source.Library{{Name: "ci-templates", URL: libAddr}}); err != nil {
		t.Fatal(err)
	}
	e.api.Projects.SetHostedLocal(e.api)
	e.hostRunner(t, "station-1", nil)
	r, err := e.api.Projects.StartRun(context.Background(), "app", "main", "someone-else", nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := waitRun(t, e.eng, r.ID); d.Status != engine.RunSucceeded {
		t.Fatalf("the run: %s %+v", d.Status, d.StepList)
	}
	job := &engine.Job{RunID: r.ID, Source: engine.Source{Kind: "git", URL: app.Bare, Libraries: []engine.LibrarySource{{Name: "p", URL: libAddr}}}}
	e.api.sourceJobFor(context.Background(), job)
	if l := job.Source.Libraries[0]; l.Hosted != "acme/ci-templates" || !strings.HasPrefix(l.Credential, "lwr_") {
		t.Fatalf("the library's source: %+v", l)
	}
}
