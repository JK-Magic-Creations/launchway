package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/egress"
	"github.com/jk-magic-creations/launchway/internal/forge/files"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// A Flight that reads a repository this server hosts is cut and published end to end: the annotated tag is made
// in the hosted repository, the notes say what the merged pull requests did (their commits not twice), the file
// is attached, a push mirror carries the tag out, and the Logbook has each step.
func TestCutAReleaseOnAHostedRepository(t *testing.T) {
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e, _ := hostedEnvWith(t, envOpts{artifacts: &engine.ArtifactOptions{Store: store}, projectsAt: func(base string) []source.Project {
		return []source.Project{{Name: "hub", URL: base + "/git/acme/hub.git", Poll: time.Hour}}
	}}, hosting.Options{Guard: &egress.Guard{AllowFiles: true}}, githttp.Options{})
	e.api.Projects.SetHostedLocal(e.api) // as main.go does: the server reads a hosted repository from its own storage
	e.api.Forge.Mirrors.Start(t.Context())
	e.hostRunner(t, "station-1", nil) // a real Station, handed a credential for this run only: nothing is configured for it
	ada := e.signIn(t, "ada")
	api := e.srv.URL + "/api/v1"
	if r := ada.do("POST", api+"/repositories", `{"owner":"acme","name":"hub","visibility":"private"}`); r.status != 201 {
		t.Fatalf("repository: %d %s", r.status, r.body)
	}
	off := filepath.Join(t.TempDir(), "off.git")
	mustGit(t, filepath.Dir(off), "init", "-q", "--bare", off)
	if r := ada.do("POST", api+"/repositories/acme/hub/mirrors", `{"direction":"push","url":"file://`+off+`"}`); r.status != 201 {
		t.Fatalf("mirror: %d %s", r.status, r.body)
	}

	tok := e.tokenFor(t, "ada", "git")
	push := withToken(e.srv.URL, "/git/acme/hub.git", tok)
	src := workingCopy(t)
	mustGit(t, src, "branch", "-M", "main")
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/main")
	for _, n := range []string{"1", "2"} {
		mustGit(t, src, "checkout", "-q", "-b", "feat"+n, "main")
		commit(t, src, "f"+n+".txt", n+"\n", "feat: add feature "+n)
		mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/feat"+n)
		mustGit(t, src, "checkout", "-q", "main")
		if r := ada.do("POST", api+"/repositories/acme/hub/pulls", `{"title":"Add feature `+n+`","source_branch":"feat`+n+`","target_branch":"main"}`); r.status != 201 {
			t.Fatalf("pull %s: %d %s", n, r.status, r.body)
		}
		var merged bool
		for i := 0; i < 50 && !merged; i++ { // the server works out whether it merges in the background
			r := ada.do("POST", api+"/repositories/acme/hub/pulls/"+n+"/merge", `{}`)
			merged = r.status == 200
			if !merged {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if !merged {
			t.Fatalf("pull %s did not merge", n)
		}
	}

	// The proposal reads the commits of the merged pull requests, so two features are a minor version.
	var next struct {
		Proposal struct{ Next, Bump string }
		Hosted   bool `json:"hosted"`
		CanTag   bool `json:"can_tag"`
	}
	ada.get(api+"/projects/hub/releases/next").json(t, &next)
	if next.Proposal.Next != "0.1.0" || !next.Hosted || !next.CanTag {
		t.Fatalf("next: %+v", next)
	}

	// The repository is private and sign-in is on, and nothing is configured anywhere: a pipeline on its main branch
	// runs on the Station and keeps a file, which the release then carries (attach_cargo).
	mustGit(t, src, "checkout", "-q", "main")
	mustGit(t, src, "pull", "-q", push, "main")
	commit(t, src, ".launchway/pipeline.yaml", "pipeline: hub\nsteps:\n  build:\n    image: none\n    run: \"mkdir -p out && echo built > out/hub-cli\"\n    artifacts: [ { name: cli, paths: [out/hub-cli] } ]\n", "ci: build the command line tool")
	mustGit(t, src, "push", "-q", push, "HEAD:refs/heads/main")
	var run *engine.RunSummary
	waitForMirror(t, "the pipeline commit to be readable", func() bool { // the server's copy follows the push
		var err error
		run, err = e.api.Projects.StartRun(t.Context(), "hub", "main", "ada", nil)
		return err == nil
	})
	if run == nil {
		t.Fatal("no run was started")
	}
	if err != nil {
		t.Fatal(err)
	}
	if d := waitRun(t, e.eng, run.ID); d.Status != engine.RunSucceeded {
		t.Fatalf("the pipeline on the private hosted repository: %s %+v", d.Status, d.StepList)
	}

	// Cut: a draft with notes, a file, then publish: the tag is made by Launchway in the hosted repository.
	r := ada.do("POST", api+"/projects/hub/releases", `{"version":"0.1.0","generate_notes":true,"attach_cargo":true}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	notes := viewRelease(t, r).Notes
	for _, want := range []string{"Add feature 1 (#1)", "Add feature 2 (#2)"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "feat: add feature") {
		t.Fatalf("a pull request's commits are listed beside it:\n%s", notes)
	}
	if r := ada.do("PUT", api+"/projects/hub/releases/0.1.0/assets/hub-app.apk", "package"); r.status != 201 {
		t.Fatalf("asset: %d %s", r.status, r.body)
	}
	r = ada.do("POST", api+"/projects/hub/releases/0.1.0/publish", `{}`)
	rel := viewRelease(t, r)
	if r.status != 200 || rel.State != "published" || rel.Tag != "v0.1.0" || !rel.TagCreated || len(rel.Assets) != 2 || !hasAsset(rel.Assets, "hub-app.apk") || !hasAsset(rel.Assets, "hub-cli") {
		t.Fatalf("publish: %d %s", r.status, r.body)
	}

	// The tag is annotated, made by the person who cut it, at the release's commit, and says what the release is.
	var tags struct {
		Items []files.RefInfo `json:"items"`
	}
	ada.get(api+"/repositories/acme/hub/tags").json(t, &tags)
	if len(tags.Items) != 1 || !tags.Items[0].Annotated || tags.Items[0].Name != "v0.1.0" || tags.Items[0].Commit != rel.SHA ||
		tags.Items[0].Tagger != "Ada Example" || !strings.HasPrefix(tags.Items[0].Message, "Release 0.1.0") || strings.Contains(tags.Items[0].Message, "##") {
		t.Fatalf("tags: %+v", tags.Items)
	}
	// The push mirror was told, so the tag reaches the outside copy as the same annotated object.
	waitForMirror(t, "the tag to reach the outside copy", func() bool {
		out, err := git(t, off, "cat-file", "-t", "refs/tags/v0.1.0")
		return err == nil && strings.TrimSpace(out) == "tag"
	})
	if out := strings.TrimSpace(mustGit(t, off, "rev-parse", "refs/tags/v0.1.0^{commit}")); out != rel.SHA {
		t.Fatalf("the outside tag points at %s, want %s", out, rel.SHA)
	}

	// The Logbook has the release and the tag.
	log, err := e.eng.AuditLog(engine.AuditFilter{Limit: 300})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range log {
		seen[a.Action] = true
	}
	for _, want := range []string{"release.create", "release.asset.add", "release.publish"} {
		if !seen[want] {
			t.Errorf("no %s in the Logbook: %v", want, seen)
		}
	}
	// A second publish of the same version is refused, not repeated.
	if r := ada.do("POST", api+"/projects/hub/releases/0.1.0/publish", `{}`); r.status != 409 {
		t.Fatalf("publish again: %d", r.status)
	}
}

func hasAsset(list []engine.ReleaseAsset, name string) bool {
	for _, a := range list {
		if a.Name == name {
			return true
		}
	}
	return false
}
