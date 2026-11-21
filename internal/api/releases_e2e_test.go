package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

const shipPipeline = `pipeline: pay-ship
inputs:
  airfield: { type: string, default: dev }
  release: { type: string, default: none }
  digest: { type: string, default: none }
steps:
  hello:
    image: alpine
    run: echo hi
`

const imageOnly = `pipeline: pay-ship
steps:
  image:
    kind: image
    tags:
      - { registry: harbor, repository: team/ship, tag: latest }
`

const shipDigest = "sha256:abababababababababababababababababababababababababababababababab"

type shipEnv struct {
	*authEnv
	repo                string
	ada, otto, vic, mel *browser
}

func relGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitIn(t, dir, append([]string{"-c", "user.name=Dana", "-c", "user.email=d@example.com"}, args...)...)
}

func relCommit(t *testing.T, dir, file, msg string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(msg), 0o644)
	relGit(t, dir, "add", "-A")
	relGit(t, dir, "commit", "-q", "-m", msg)
}

func setupShip(t *testing.T, more ...string) *shipEnv {
	t.Helper()
	root := t.TempDir()
	repo := gitRepo(t, filepath.Join(root, "ship"), shipPipeline)
	relGit(t, repo, "tag", "-a", "v1.0.0", "-m", "one")
	relCommit(t, repo, "a.txt", "feat(ui): the matrix (#1)")
	relCommit(t, repo, "b.txt", "fix: a crash on start (#2)")
	store, err := blobstore.NewFilesystem(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	testPeople["mel"] = []string{"pay-ops"}
	e := &shipEnv{repo: repo}
	projects := []source.Project{{Name: "pay-ship", URL: repo, Poll: time.Hour, Push: &source.PushSettings{}}}
	names := []string{"pay-ship"}
	for _, m := range more {
		projects = append(projects, source.Project{Name: m, URL: gitRepo(t, filepath.Join(root, m), strings.Replace(shipPipeline, "pay-ship", m, 1)), Poll: time.Hour, Push: &source.PushSettings{}})
		names = append(names, m)
	}
	e.authEnv = setupAuth(t, envOpts{
		projects:     projects,
		environments: []engine.Environment{{Name: "dev", Target: "homelab", Projects: names}, {Name: "prod", Target: "homelab", Projects: names, Protected: true}},
		artifacts:    &engine.ArtifactOptions{Store: store},
	})
	e.ada, e.otto, e.vic, e.mel = e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "vic"), e.signIn(t, "mel")
	return e
}

func (e *shipEnv) url(path string) string { return e.srv.URL + "/api/v1/projects/pay-ship" + path }

func (e *shipEnv) post(t *testing.T, b *browser, path string, body any, want int, out any) response {
	t.Helper()
	data, _ := json.Marshal(body)
	r := b.do("POST", e.url(path), string(data))
	if r.status != want {
		t.Fatalf("POST %s: %d %s (want %d)", path, r.status, r.body, want)
	}
	if out != nil {
		r.json(t, out)
	}
	return r
}

func viewRelease(t *testing.T, r response) engine.Release {
	t.Helper()
	var v struct{ Release engine.Release }
	r.json(t, &v)
	return v.Release
}

// The whole of a release over HTTP with a real repository: the version proposed from the changes,
// notes written from the pull requests, a file attached, publishing that makes an annotated tag,
// and everything about it frozen afterwards.
func TestReleaseLifecycleOverHTTP(t *testing.T) {
	e := setupShip(t)
	// The first release adopts the tag that exists.
	r := e.otto.do("POST", e.url("/releases"), `{"version":"1.0.0","adopt":"v1.0.0"}`)
	if r.status != http.StatusCreated {
		t.Fatalf("adopt: %d %s", r.status, r.body)
	}
	e.post(t, e.otto, "/releases/1.0.0/publish", map[string]any{}, 200, nil)
	// The next version is proposed from what changed, with the reasons; nothing is made by asking.
	var next struct {
		Proposal struct {
			Next    string
			Bump    string
			Reasons []struct{ Why string }
		}
	}
	r = e.otto.get(e.url("/releases/next"))
	r.json(t, &next)
	if next.Proposal.Next != "1.1.0" || next.Proposal.Bump != "minor" || len(next.Proposal.Reasons) != 2 {
		t.Fatalf("proposal: %+v", next.Proposal)
	}
	// A draft with generated notes.
	r = e.otto.do("POST", e.url("/releases"), `{"version":"1.1.0","generate_notes":true}`)
	if r.status != http.StatusCreated {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	rel := viewRelease(t, r)
	for _, want := range []string{"## 1.1.0", "### Features", "the matrix", "### Fixes", "a crash on start"} {
		if !strings.Contains(rel.Notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, rel.Notes)
		}
	}
	if !strings.HasPrefix(rel.NotesSource, "generated from") || rel.Previous != "1.0.0" {
		t.Fatalf("source %q previous %q", rel.NotesSource, rel.Previous)
	}
	// A viewer does not see the draft; nor can a viewer change anything.
	if got := e.vic.get(e.url("/releases/1.1.0")); got.status != 404 {
		t.Fatalf("a viewer sees a draft: %d", got.status)
	}
	if got := e.vic.do("PATCH", e.url("/releases/1.1.0"), `{"title":"x"}`); got.status != 403 {
		t.Fatalf("a viewer edits: %d", got.status)
	}
	// A wrong checksum is refused; a right one is kept.
	if got := e.otto.do("PUT", e.url("/releases/1.1.0/assets/pay-ship.tar.gz"), "the binary", "X-Launchway-SHA256", strings.Repeat("0", 64)); got.status != 400 {
		t.Fatalf("wrong checksum: %d %s", got.status, got.body)
	}
	if got := e.otto.do("PUT", e.url("/releases/1.1.0/assets/pay-ship.tar.gz"), "the binary"); got.status != 201 {
		t.Fatalf("upload: %d %s", got.status, got.body)
	}
	if got := e.otto.do("PUT", e.url("/releases/1.1.0/assets/pay-ship.tar.gz"), "another"); got.status != 409 {
		t.Fatalf("an asset is not replaced: %d", got.status)
	}
	// Publishing makes the annotated tag in the repository.
	r = e.post(t, e.otto, "/releases/1.1.0/publish", map[string]any{}, 200, nil)
	pub := viewRelease(t, r)
	if pub.State != "published" || pub.Tag != "v1.1.0" || !pub.TagCreated || pub.PublishedBy != "otto" || !pub.Immutable {
		t.Fatalf("published: %+v", pub)
	}
	if got := relGit(t, e.repo, "cat-file", "-t", "refs/tags/v1.1.0"); got != "tag" {
		t.Fatalf("the tag is a %s, not an annotated tag", got)
	}
	if got := relGit(t, e.repo, "rev-parse", "v1.1.0^{commit}"); got != pub.SHA {
		t.Fatalf("tag at %s, release at %s", got, pub.SHA)
	}
	// Frozen: notes, assets, deletion, publishing again.
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/releases/1.1.0", `{"notes":"new"}`}, {"PUT", "/releases/1.1.0/assets/late.bin", "x"}, {"DELETE", "/releases/1.1.0/assets/pay-ship.tar.gz", ""},
		{"DELETE", "/releases/1.1.0", ""}, {"POST", "/releases/1.1.0/publish", `{}`}, {"POST", "/releases/1.1.0/notes", `{}`},
	} {
		if got := e.ada.do(c.method, e.url(c.path), c.body); got.status != 409 {
			t.Errorf("%s %s on a published release: %d %s (an administrator may not change it either)", c.method, c.path, got.status, got.body)
		}
	}
	// A viewer downloads it signed in, with its checksum; nobody downloads it unsigned.
	got := e.vic.get(e.url("/releases/1.1.0/assets/pay-ship.tar.gz"))
	if got.status != 200 || string(got.body) != "the binary" || got.header.Get("X-Launchway-SHA256") == "" {
		t.Fatalf("download: %d %q", got.status, got.body)
	}
	if got := newBrowser(t).get(e.url("/releases/1.1.0/assets/pay-ship.tar.gz")); got.status != 401 {
		t.Fatalf("an unsigned download: %d", got.status)
	}
	if got := newBrowser(t).get(e.srv.URL + "/public/releases/pay-ship/1.1.0/pay-ship.tar.gz"); got.status != 404 {
		t.Fatalf("public downloads are off by default: %d", got.status)
	}
	// A version that is not higher is refused, and allowed when asked for from a maintenance branch.
	if got := e.otto.do("POST", e.url("/releases"), `{"version":"1.0.5"}`); got.status != 409 {
		t.Fatalf("lower version: %d %s", got.status, got.body)
	}
	if got := e.otto.do("POST", e.url("/releases"), `{"version":"1.0.5","allow_lower":true}`); got.status != 201 {
		t.Fatalf("maintenance release: %d %s", got.status, got.body)
	}
	// Withdrawing needs a reason and keeps the record.
	if got := e.otto.do("POST", e.url("/releases/1.1.0/yank"), `{}`); got.status != 400 {
		t.Fatalf("yank without a reason: %d", got.status)
	}
	e.post(t, e.otto, "/releases/1.1.0/yank", map[string]any{"reason": "crashes on start"}, 200, nil)
	var view struct{ Release engine.Release }
	e.vic.get(e.url("/releases/1.1.0")).json(t, &view)
	if view.Release.State != "yanked" || view.Release.YankReason != "crashes on start" || len(view.Release.Assets) != 1 {
		t.Fatalf("after yanking: %+v", view.Release)
	}
}

// Public downloads need three yeses: the installation, the project and the release; and only an administrator says them.
func TestPublicDownloadsNeedEveryLevelToAgree(t *testing.T) {
	e := setupShip(t)
	mk := func(version string) engine.Release {
		r := e.otto.do("POST", e.url("/releases"), `{"version":"`+version+`","from":"main"}`)
		if r.status != 201 {
			t.Fatalf("create: %d %s", r.status, r.body)
		}
		rel := viewRelease(t, r)
		if got := e.otto.do("PUT", e.url("/releases/"+version+"/assets/a.bin"), "public bytes"); got.status != 201 {
			t.Fatalf("upload: %d %s", got.status, got.body)
		}
		return rel
	}
	mk("2.0.0")
	pub := func() int {
		return newBrowser(t).get(e.srv.URL + "/public/releases/pay-ship/2.0.0/a.bin").status
	}
	// Nobody but an administrator of the project asks for it; and not before the installation allows.
	if got := e.otto.do("POST", e.url("/releases/2.0.0/publish"), `{"public":true}`); got.status != 403 {
		t.Fatalf("an operator makes a release public: %d", got.status)
	}
	if got := e.ada.do("PUT", e.url("/release-settings"), `{"scheme":"semver","tag_prefix":"v","changelog":"CHANGELOG.md","public":true}`); got.status != 409 {
		t.Fatalf("the project asks before the installation allows: %d %s", got.status, got.body)
	}
	if got := e.otto.do("PUT", e.srv.URL+"/api/v1/release-installation", `{"public_downloads":true}`); got.status != 403 {
		t.Fatalf("an operator allows public content: %d", got.status)
	}
	if got := e.ada.do("PUT", e.srv.URL+"/api/v1/release-installation", `{"public_downloads":true}`); got.status != 200 {
		t.Fatalf("the administrator allows: %d %s", got.status, got.body)
	}
	if got := e.ada.do("PUT", e.url("/release-settings"), `{"scheme":"semver","tag_prefix":"v","changelog":"CHANGELOG.md","public":true}`); got.status != 200 {
		t.Fatalf("the project asks: %d %s", got.status, got.body)
	}
	if got := e.ada.do("POST", e.url("/releases/2.0.0/publish"), `{"public":true}`); got.status != 200 {
		t.Fatalf("publish public: %d %s", got.status, got.body)
	}
	if pub() != 200 {
		t.Fatalf("a public release downloads without signing in: %d", pub())
	}
	// A release that did not ask is not public even then.
	mk("2.1.0")
	e.post(t, e.ada, "/releases/2.1.0/publish", map[string]any{}, 200, nil)
	if got := newBrowser(t).get(e.srv.URL + "/public/releases/pay-ship/2.1.0/a.bin"); got.status != 404 {
		t.Fatalf("a release that did not ask: %d", got.status)
	}
	// Withdrawing a release stops its public download at once.
	e.post(t, e.ada, "/releases/2.0.0/yank", map[string]any{"reason": "oops"}, 200, nil)
	if pub() != 404 {
		t.Fatalf("a withdrawn release is still public: %d", pub())
	}
}

// Promotion starts the project's pipeline at the release's tag with its version and its image digest, in the
// order of the checklist: a freeze or a missing clearance stops it, an administrator may override a freeze
// and an operator may not, and two people are needed for a clearance.
func TestPromotionUsesTheExactDigestAndTheChecklist(t *testing.T) {
	e := setupShip(t)
	// A Sortie of the release's commit that built an image.
	tip := relGit(t, e.repo, "rev-parse", "HEAD")
	run, err := e.eng.CreateRun(engine.NewRun{Project: "pay-ship", Pipeline: parseShip(t, imageOnly), Source: engine.Source{Kind: "git", URL: e.repo},
		Trigger: engine.Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: tip}})
	if err != nil {
		t.Fatal(err)
	}
	runner := engine.RunnerInfo{ID: "r1", Name: "builder", Features: engine.FeatureArtifacts + " " + engine.FeatureImages}
	job, err := e.eng.Lease(context.Background(), runner, 0)
	if err != nil || job == nil {
		t.Fatalf("lease: %v %v", job, err)
	}
	if _, err := e.eng.Start(job.LeaseID); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := e.eng.Finish(job.LeaseID, engine.Result{Outcome: "succeeded", ExitCode: &zero, Image: &engine.ImageRecord{Digest: shipDigest, Image: "harbor.example/team/ship@" + shipDigest,
		Pushed: true, Signed: true, Platforms: []engine.ImagePlatform{}, Tags: []string{}, Builders: []string{}}}); err != nil {
		t.Fatal(err)
	}
	// The release carries that digest, made from the Sortie.
	r := e.otto.do("POST", e.url("/releases"), `{"version":"2.0.0","from":"main","run_id":"`+run.ID+`","notes":"first"}`)
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	if rel := viewRelease(t, r); len(rel.Digests) != 1 || rel.Digests[0].Digest != shipDigest || rel.RunNumber != run.Number {
		t.Fatalf("digests: %+v", rel.Digests)
	}
	e.post(t, e.otto, "/releases/2.0.0/assets", map[string]any{"run_id": run.ID, "images": true}, 201, nil)
	// A draft is not promoted.
	if got := e.otto.do("POST", e.url("/releases/2.0.0/promote"), `{"airfield":"dev"}`); got.status != 409 {
		t.Fatalf("promote a draft: %d %s", got.status, got.body)
	}
	e.post(t, e.otto, "/releases/2.0.0/publish", map[string]any{}, 200, nil)
	// Prod asks for a clearance, a previous Airfield and no freeze.
	if got := e.otto.do("PUT", e.url("/release-gates/prod"), `{"clearances":1}`); got.status != 403 {
		t.Fatalf("an operator sets a checklist: %d", got.status)
	}
	if got := e.ada.do("PUT", e.url("/release-gates/prod"), `{"clearances":1,"previous_airfield":"dev","require_notes":true}`); got.status != 200 {
		t.Fatalf("set the checklist: %d %s", got.status, got.body)
	}
	if got := e.otto.do("POST", e.url("/releases/2.0.0/promote"), `{"airfield":"prod"}`); got.status != 409 || !strings.Contains(string(got.body), "has not reached dev") {
		t.Fatalf("prod before dev: %d %s", got.status, got.body)
	}
	// Promote to dev: the pipeline is started at the tag with the version, the Airfield and the digest.
	var pr struct {
		Promotion engine.ReleasePromotion
		Run       engine.RunSummary
	}
	e.post(t, e.otto, "/releases/2.0.0/promote", map[string]any{"airfield": "dev"}, 201, &pr)
	if pr.Run.SHA != tip || pr.Promotion.SHA != tip || len(pr.Promotion.Digests) != 1 {
		t.Fatalf("promotion: %+v %+v", pr.Promotion, pr.Run)
	}
	detail, err := e.eng.GetRun(pr.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Tag != "" && detail.Tag != "v2.0.0" {
		t.Fatalf("run tag %q", detail.Tag)
	}
	gotInputs := map[string]string{}
	if detail.TriggerDetail != nil {
		gotInputs = detail.TriggerDetail.Inputs
	}
	if gotInputs["digest"] != shipDigest || gotInputs["airfield"] != "dev" || gotInputs["release"] != "2.0.0" {
		t.Fatalf("the Sortie was given %v: build once, promote the same thing", gotInputs)
	}
	// Promoting again while it runs there is refused only when it is healthy; the checklist of prod now waits for a clearance.
	if got := e.otto.do("POST", e.url("/releases/2.0.0/clearances"), `{"airfield":"prod"}`); got.status != 403 {
		t.Fatalf("the publisher clears their own release: %d %s", got.status, got.body)
	}
	if got := e.mel.do("POST", e.url("/releases/2.0.0/clearances"), `{"airfield":"prod","comment":"read the notes"}`); got.status != 201 {
		t.Fatalf("another person's clearance: %d %s", got.status, got.body)
	}
	// A freeze stops the promotion; an operator cannot override it, an administrator can, with a reason.
	until := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	if got := e.otto.do("POST", e.srv.URL+"/api/v1/release-freezes", `{"project":"pay-ship","to":"`+until+`","reason":"audit week"}`); got.status != 403 {
		t.Fatalf("an operator freezes: %d", got.status)
	}
	if got := e.ada.do("POST", e.srv.URL+"/api/v1/release-freezes", `{"project":"pay-ship","airfield":"dev","to":"`+until+`","reason":"audit week"}`); got.status != 201 {
		t.Fatalf("the administrator freezes dev: %d %s", got.status, got.body)
	}
	rel2 := e.otto.do("POST", e.url("/releases"), `{"version":"2.0.1","from":"main","notes":"n"}`)
	if rel2.status != 201 {
		t.Fatalf("second release: %d %s", rel2.status, rel2.body)
	}
	e.post(t, e.otto, "/releases/2.0.1/publish", map[string]any{}, 200, nil)
	if got := e.otto.do("POST", e.url("/releases/2.0.1/promote"), `{"airfield":"dev"}`); got.status != 409 || !strings.Contains(string(got.body), "audit week") {
		t.Fatalf("the freeze: %d %s", got.status, got.body)
	}
	if got := e.otto.do("POST", e.url("/releases/2.0.1/promote"), `{"airfield":"dev","override":"hotfix"}`); got.status != 403 {
		t.Fatalf("an operator overrides a freeze: %d %s", got.status, got.body)
	}
	if got := e.ada.do("POST", e.url("/releases/2.0.1/promote"), `{"airfield":"dev","override":"customer outage"}`); got.status != 201 {
		t.Fatalf("an administrator overrides a freeze: %d %s", got.status, got.body)
	}
	var proms []engine.ReleasePromotion
	var view struct{ Promotions []engine.ReleasePromotion }
	e.ada.get(e.url("/releases/2.0.1")).json(t, &view)
	proms = view.Promotions
	if len(proms) != 1 || proms[0].Override != "customer outage" || !strings.Contains(proms[0].Overridden, "freeze") || proms[0].RequestedBy != "ada" {
		t.Fatalf("the override is recorded with its reason: %+v", proms)
	}
	// The audit trail has it.
	var audit struct{ Entries []engine.AuditEntry }
	e.ada.get(e.srv.URL+"/api/v1/audit?action=release.promote").json(t, &audit)
	overrides := 0
	for _, a := range audit.Entries {
		if a.Outcome == "ok" && strings.Contains(a.Detail, "override: customer outage") {
			overrides++
		}
	}
	if len(audit.Entries) < 4 || overrides != 1 {
		t.Fatalf("the audit trail has %d promotions, %d with the override reason", len(audit.Entries), overrides)
	}
}

func parseShip(t *testing.T, text string) *pipeline.Pipeline {
	t.Helper()
	p, err := pipeline.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *shipEnv) publishAs(t *testing.T, project, version string) {
	t.Helper()
	p := "/api/v1/projects/" + project
	if r := e.otto.do("POST", e.srv.URL+p+"/releases", `{"version":"`+version+`","from":"main","notes":"n"}`); r.status != 201 {
		t.Fatalf("create %s: %d %s", project, r.status, r.body)
	}
	if r := e.otto.do("POST", e.srv.URL+p+"/releases/"+version+"/publish", `{}`); r.status != 200 {
		t.Fatalf("publish %s: %d %s", project, r.status, r.body)
	}
}

// A formation promotes releases of several projects in the order given, all or nothing to begin with and
// reporting which launched, which failed and which were not started when one fails.
func TestFormationPromotesInOrderAndReportsAPartialFailure(t *testing.T) {
	e := setupShip(t, "pay-api2", "pay-web2")
	e.publishAs(t, "pay-ship", "5.0.0")
	e.publishAs(t, "pay-api2", "2.0.0")
	e.publishAs(t, "pay-web2", "3.0.0")
	form := func(name string) engine.Formation {
		var f engine.Formation
		r := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations", `{"name":"`+name+`","releases":["pay-api2@2.0.0","pay-ship@5.0.0","pay-web2@3.0.0"]}`)
		if r.status != 201 {
			t.Fatalf("create formation: %d %s", r.status, r.body)
		}
		r.json(t, &f)
		return f
	}
	f := form("autumn")
	if f.Members[0].Project != "pay-api2" || f.Members[2].Project != "pay-web2" || f.OrderSource != "explicit" {
		t.Fatalf("order: %+v", f.Members)
	}
	// All or nothing to begin with: a freeze on one member's project stops every member from starting.
	until := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	r := e.ada.do("POST", e.srv.URL+"/api/v1/release-freezes", `{"project":"pay-web2","to":"`+until+`","reason":"web freeze"}`)
	if r.status != 201 {
		t.Fatalf("freeze: %d %s", r.status, r.body)
	}
	var fz engine.Formation
	if got := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations/autumn/promote", `{"airfield":"dev"}`); got.status != 409 || !strings.Contains(string(got.body), "nothing was started") || !strings.Contains(string(got.body), "web freeze") {
		t.Fatalf("a blocked member: %d %s", got.status, got.body)
	}
	e.otto.get(e.srv.URL+"/api/v1/release-formations/autumn").json(t, &fz)
	if fz.State != engine.FormationPlanned {
		t.Fatalf("nothing started, so it stays planned: %s", fz.State)
	}
	var fr struct{ ID string }
	fl := e.ada.get(e.srv.URL + "/api/v1/release-freezes")
	var frs []struct{ ID string }
	fl.json(t, &frs)
	fr.ID = frs[0].ID
	if got := e.ada.do("POST", e.srv.URL+"/api/v1/release-freezes/"+fr.ID+"/cancel", `{}`); got.status != 200 {
		t.Fatalf("cancel freeze: %d", got.status)
	}
	// Promote: only the first member starts.
	if got := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations/autumn/promote", `{"airfield":"dev"}`); got.status != 200 {
		t.Fatalf("promote: %d %s", got.status, got.body)
	}
	e.otto.get(e.srv.URL+"/api/v1/release-formations/autumn").json(t, &fz)
	if fz.State != engine.FormationPromoting || fz.Members[0].State != engine.MemberStarted || fz.Members[1].State != engine.MemberWaiting || fz.Members[2].State != engine.MemberWaiting {
		t.Fatalf("one at a time, in order: %+v", fz.Members)
	}
	// The first launches; the second starts; the second fails; the third is not started.
	finishPromotion := func(project string, ok bool) {
		t.Helper()
		var list []engine.ReleasePromotion
		var v struct{ Promotions []engine.ReleasePromotion }
		e.otto.get(e.srv.URL+"/api/v1/projects/"+project+"/releases").json(t, &list)
		_ = list
		rel, err := e.eng.FindRelease(project, map[string]string{"pay-api2": "2.0.0", "pay-ship": "5.0.0", "pay-web2": "3.0.0"}[project])
		if err != nil {
			t.Fatal(err)
		}
		_ = v
		ps, err := e.eng.ReleasePromotions(rel.ID, "", 5)
		if err != nil || len(ps) == 0 {
			t.Fatalf("promotions of %s: %v %v", project, ps, err)
		}
		if ok {
			runLaunch(t, e, ps[0].RunID)
		} else if err := e.eng.Cancel(ps[0].RunID, "ada"); err != nil {
			t.Fatal(err)
		}
	}
	finishPromotion("pay-api2", true)
	e.otto.do("POST", e.srv.URL+"/api/v1/release-formations/autumn/advance", `{}`)
	e.otto.get(e.srv.URL+"/api/v1/release-formations/autumn").json(t, &fz)
	if fz.Members[0].State != engine.MemberLaunched || fz.Members[1].State != engine.MemberStarted {
		t.Fatalf("after the first launched: %+v", fz.Members)
	}
	finishPromotion("pay-ship", false)
	e.otto.do("POST", e.srv.URL+"/api/v1/release-formations/autumn/advance", `{}`)
	e.otto.get(e.srv.URL+"/api/v1/release-formations/autumn").json(t, &fz)
	if fz.State != engine.FormationPartial || fz.Members[0].State != engine.MemberLaunched || fz.Members[1].State != engine.MemberFailed || fz.Members[2].State != engine.MemberNotStarted {
		t.Fatalf("the report: %s %+v", fz.State, fz.Members)
	}
	// A formation that all launch is done.
	e.publishAs(t, "pay-ship", "5.0.1")
	e.publishAs(t, "pay-api2", "2.0.1")
	if r := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations", `{"name":"winter","releases":["pay-api2@2.0.1","pay-ship@5.0.1"]}`); r.status != 201 {
		t.Fatalf("second formation: %d %s", r.status, r.body)
	}
	if got := e.otto.do("POST", e.srv.URL+"/api/v1/release-formations/winter/promote", `{"airfield":"dev"}`); got.status != 200 {
		t.Fatalf("promote: %d %s", got.status, got.body)
	}
	for _, c := range []struct{ project, version string }{{"pay-api2", "2.0.1"}, {"pay-ship", "5.0.1"}} {
		rel, _ := e.eng.FindRelease(c.project, c.version)
		ps, _ := e.eng.ReleasePromotions(rel.ID, "", 5)
		runLaunch(t, e, ps[0].RunID)
		e.otto.do("POST", e.srv.URL+"/api/v1/release-formations/winter/advance", `{}`)
	}
	e.otto.get(e.srv.URL+"/api/v1/release-formations/winter").json(t, &fz)
	if fz.State != engine.FormationDone || fz.Members[0].State != engine.MemberLaunched || fz.Members[1].State != engine.MemberLaunched {
		t.Fatalf("all launched: %s %+v", fz.State, fz.Members)
	}
	// A person who may operate only some of the projects may not promote the whole formation.
	if got := e.vic.do("POST", e.srv.URL+"/api/v1/release-formations/winter/promote", `{"airfield":"dev"}`); got.status != 403 {
		t.Fatalf("a viewer promotes a formation: %d", got.status)
	}
}

// runLaunch runs a run to success with a runner.
func runLaunch(t *testing.T, e *shipEnv, runID string) {
	t.Helper()
	runner := engine.RunnerInfo{ID: "r-" + runID, Name: "r"}
	for range 10 {
		job, err := e.eng.Lease(context.Background(), runner, 0)
		if err != nil {
			t.Fatal(err)
		}
		if job == nil {
			break
		}
		if _, err := e.eng.Start(job.LeaseID); err != nil {
			t.Fatal(err)
		}
		zero := 0
		if err := e.eng.Finish(job.LeaseID, engine.Result{Outcome: "succeeded", ExitCode: &zero}); err != nil {
			t.Fatal(err)
		}
		if d, _ := e.eng.GetRun(runID); d != nil && d.Status.Terminal() {
			return
		}
	}
}

// Who may draft, publish, promote, override and freeze, for a project the people can read: a viewer reads and
// does nothing else, an operator works with releases but cannot administer, an administrator can, and a person
// with no right on the project is told it does not exist.
func TestReleasePermissionsMatrix(t *testing.T) {
	e := setupShip(t)
	dana := e.signIn(t, "dana") // an operator of web only: pay-ship is another team's project
	// A published release to act on, and a draft.
	e.publishAs(t, "pay-ship", "7.0.0")
	if r := e.otto.do("POST", e.url("/releases"), `{"version":"7.1.0","from":"main"}`); r.status != 201 {
		t.Fatalf("draft: %d %s", r.status, r.body)
	}
	until := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	type act struct{ name, method, path, body string }
	acts := []act{
		{"read a release", "GET", "/releases/7.0.0", ""},
		{"read the matrix settings", "GET", "/release-settings", ""},
		{"draft a release", "POST", "/releases", `{"version":"7.2.0","from":"main"}`},
		{"edit a draft", "PATCH", "/releases/7.1.0", `{"title":"t"}`},
		{"publish", "POST", "/releases/7.1.0/publish", `{}`},
		{"promote", "POST", "/releases/7.0.0/promote", `{"airfield":"dev"}`},
		{"withdraw", "POST", "/releases/7.0.0/yank", `{"reason":"r"}`},
		{"change release settings", "PUT", "/release-settings", `{"scheme":"semver","tag_prefix":"v","changelog":"CHANGELOG.md"}`},
		{"set a checklist", "PUT", "/release-gates/dev", `{}`},
		{"freeze the project", "POST", "ROOT/api/v1/release-freezes", `{"project":"pay-ship","to":"` + until + `","reason":"x"}`},
		{"freeze every project", "POST", "ROOT/api/v1/release-freezes", `{"to":"` + until + `","reason":"x"}`},
	}
	// Each person's expected answer: 0 means the permission check let it through (anything but 401, 403 and 404).
	want := map[string][]int{
		//            read  settings draft edit publish promote withdraw set   gate  freeze freeze-all
		"vic":  {200, 200, 403, 403, 403, 403, 403, 403, 403, 403, 403},
		"otto": {200, 200, 0, 0, 0, 0, 0, 403, 403, 403, 403},
		"dana": {404, 404, 404, 404, 404, 404, 404, 404, 404, 403, 403},
		"ada":  {200, 200, 0, 0, 0, 0, 0, 0, 0, 201, 201},
	}
	people := map[string]*browser{"vic": e.vic, "otto": e.otto, "dana": dana, "ada": e.ada}
	for _, who := range []string{"vic", "otto", "dana", "ada"} {
		for i, a := range acts {
			url := e.url(a.path)
			if strings.HasPrefix(a.path, "ROOT") {
				url = e.srv.URL + strings.TrimPrefix(a.path, "ROOT")
			}
			got := people[who].do(a.method, url, a.body).status
			w := want[who][i]
			ok := got == w
			if w == 0 {
				ok = got != 401 && got != 403 && got != 404 && got < 500
			}
			if !ok {
				t.Errorf("%s: %s gave %d, want %d (0: let through)", who, a.name, got, w)
			}
		}
	}
}
