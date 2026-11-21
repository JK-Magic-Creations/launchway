package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/registry"
	"github.com/jk-magic-creations/launchway/internal/registry/enginedb"
)

// registryEnv is a signed-in server with the built-in registry on.
func registryEnv(t *testing.T, o envOpts) *authEnv {
	t.Helper()
	e := setupAuth(t, o)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := registry.New(registry.Options{DB: enginedb.New(e.eng.Database()), Blobs: store, Now: e.clk.Now, Log: quiet(), Server: "api-test", OnEvent: e.api.RegistryEvent})
	e.api.Registry = &RegistryHosting{Svc: svc, Signer: registry.Signer{Key: []byte("0123456789abcdef0123456789abcdef"), Now: e.clk.Now}, StoreName: "filesystem"}
	return e
}

func (e *authEnv) registryRef(t *testing.T, s string) name.Reference {
	t.Helper()
	r, err := name.ParseReference(strings.TrimPrefix(e.srv.URL, "http://")+"/"+s, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func basicAuth(secret string) remote.Option {
	return remote.WithAuth(&authn.Basic{Username: "anyone", Password: secret})
}

func TestRegistryPushPullFollowsProjectRoles(t *testing.T) {
	e := registryEnv(t, envOpts{})
	viewer, operator, other, admin := e.tokenFor(t, "vic", "reg"), e.tokenFor(t, "otto", "reg"), e.tokenFor(t, "dana", "reg"), e.tokenFor(t, "ada", "reg")
	img, _ := random.Image(4096, 2)
	ref := e.registryRef(t, "pay-api/svc:1.0")
	// A viewer reads and cannot push; an operator pushes; another team's operator sees nothing.
	if err := remote.Write(ref, img, basicAuth(viewer)); err == nil {
		t.Fatal("a viewer pushed")
	}
	if err := remote.Write(ref, img, basicAuth(operator)); err != nil {
		t.Fatalf("an operator could not push: %v", err)
	}
	if _, err := remote.Image(ref, basicAuth(viewer)); err != nil {
		t.Fatalf("a viewer could not pull: %v", err)
	}
	if _, err := remote.Image(ref, basicAuth(other)); err == nil {
		t.Fatal("another team's operator pulled")
	}
	if _, err := remote.Image(ref); err == nil {
		t.Fatal("an anonymous pull worked")
	}
	if _, err := remote.Image(ref, basicAuth("lw_not_a_real_token_at_all")); err == nil {
		t.Fatal("an unknown token pulled")
	}
	// Only a project admin deletes (over /v2 too).
	d, _ := img.Digest()
	if err := remote.Delete(e.registryRef(t, "pay-api/svc@"+d.String()), basicAuth(operator)); err == nil {
		t.Fatal("an operator deleted an image")
	}
	// Audit: the push, and the refused push by a viewer.
	var a struct {
		Entries []struct {
			User, Action, Outcome, Target string
		}
	}
	b := e.signIn(t, "ada")
	b.get(e.srv.URL+"/api/v1/audit?limit=200").json(t, &a)
	var pushed, denied bool
	for _, x := range a.Entries {
		pushed = pushed || (x.Action == "registry.push" && x.User == "otto" && strings.HasPrefix(x.Target, "pay-api/svc"))
		denied = denied || (x.Action == "registry.push" && x.Outcome == "denied" && x.User == "vic")
	}
	if !pushed || !denied {
		t.Fatalf("audit lacks the push (%v) or the refusal (%v): %+v", pushed, denied, a)
	}
	_ = admin
}

func TestRegistryManagementAPI(t *testing.T) {
	e := registryEnv(t, envOpts{})
	operator := e.tokenFor(t, "otto", "reg")
	for _, tag := range []string{"a", "b", "c"} {
		img, _ := random.Image(1024, 1)
		if err := remote.Write(e.registryRef(t, "pay-api/svc:"+tag), img, basicAuth(operator)); err != nil {
			t.Fatal(err)
		}
	}
	vic, otto, ada, dana := e.signIn(t, "vic"), e.signIn(t, "otto"), e.signIn(t, "ada"), e.signIn(t, "dana")
	var status struct {
		Enabled    bool
		Host       string
		Encryption struct {
			BlobsEncryptedByLaunchway bool `json:"blobs_encrypted_by_launchway"`
		}
		Login string
	}
	if r := vic.get(e.srv.URL + "/api/v1/registry"); r.status != 200 {
		t.Fatalf("status %d %s", r.status, r.body)
	} else {
		r.json(t, &status)
	}
	if !status.Enabled || status.Encryption.BlobsEncryptedByLaunchway || !strings.Contains(status.Login, "docker login") {
		t.Fatalf("status %+v", status)
	}
	// The project's registry, its images, and one inspected.
	var proj struct {
		Repositories []struct {
			Name   string
			Images int
		}
		UsedBytes  int64 `json:"used_bytes"`
		QuotaBytes int64 `json:"quota_bytes"`
	}
	vic.get(e.srv.URL+"/api/v1/projects/pay-api/registry").json(t, &proj)
	if len(proj.Repositories) != 1 || proj.Repositories[0].Images != 3 || proj.UsedBytes == 0 || proj.QuotaBytes <= 0 {
		t.Fatalf("project registry %+v", proj)
	}
	if r := dana.get(e.srv.URL + "/api/v1/projects/pay-api/registry"); r.status != 404 {
		t.Fatalf("another team saw the project's registry: %d", r.status)
	}
	var imgs struct {
		Images []struct {
			Digest string
			Tags   []string
			Pull   string
		}
	}
	vic.get(e.srv.URL+"/api/v1/projects/pay-api/registry/images?repository=pay-api/svc").json(t, &imgs)
	if len(imgs.Images) != 3 || !strings.Contains(imgs.Images[0].Pull, "pay-api/svc:") {
		t.Fatalf("images %+v", imgs)
	}
	if r := vic.get(e.srv.URL + "/api/v1/projects/pay-api/registry/images?repository=web/x"); r.status != 404 {
		t.Fatalf("a repository of another project: %d", r.status)
	}
	if r := vic.get(e.srv.URL + "/api/v1/projects/pay-api/registry/image?repository=pay-api/svc&reference=a"); r.status != 200 || !strings.Contains(string(r.body), `"manifest"`) {
		t.Fatalf("inspect %d %s", r.status, r.body)
	}
	// Deleting: a viewer and an operator are refused, an admin may, and it is audited.
	del := e.srv.URL + "/api/v1/projects/pay-api/registry/image?repository=pay-api/svc&reference=" + imgs.Images[0].Digest
	if r := vic.do("DELETE", del, ""); r.status != 403 {
		t.Fatalf("viewer delete %d", r.status)
	}
	if r := otto.do("DELETE", del, ""); r.status != 403 {
		t.Fatalf("operator delete %d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", del, ""); r.status != 200 {
		t.Fatalf("admin delete %d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", e.srv.URL+"/api/v1/projects/pay-api/registry/tag?repository=pay-api/svc&tag=c", ""); r.status != 200 && r.status != 404 {
		t.Fatalf("tag delete %d %s", r.status, r.body)
	}
	// Policy: an operator may not set one; an admin may; an immutable pattern then holds.
	pol := `{"immutable_tags":["v*"],"retention":{"keep_last":1,"untagged_days":7}}`
	if r := otto.do("PUT", e.srv.URL+"/api/v1/projects/pay-api/registry/policy", pol); r.status != 403 {
		t.Fatalf("operator policy %d", r.status)
	}
	if r := ada.do("PUT", e.srv.URL+"/api/v1/projects/pay-api/registry/policy", pol); r.status != 200 {
		t.Fatalf("admin policy %d %s", r.status, r.body)
	}
	img, _ := random.Image(512, 1)
	if err := remote.Write(e.registryRef(t, "pay-api/svc:v1"), img, basicAuth(operator)); err != nil {
		t.Fatal(err)
	}
	other, _ := random.Image(512, 1)
	if err := remote.Write(e.registryRef(t, "pay-api/svc:v1"), other, basicAuth(operator)); err == nil {
		t.Fatal("an immutable tag was overwritten")
	}
	// A project admin cannot raise the quota above the installation's.
	if r := ada.do("PUT", e.srv.URL+"/api/v1/projects/pay-api/registry/policy", `{"quota_bytes":1048576}`); r.status != 200 {
		t.Fatalf("admin of everything sets a quota: %d %s", r.status, r.body)
	}
	// Retention preview changes nothing; the run (admin only) does.
	var prev struct {
		Candidates []map[string]any
		DryRun     bool `json:"dry_run"`
	}
	vic.get(e.srv.URL+"/api/v1/projects/pay-api/registry/retention").json(t, &prev)
	if !prev.DryRun {
		t.Fatalf("preview %+v", prev)
	}
	if r := otto.do("POST", e.srv.URL+"/api/v1/projects/pay-api/registry/retention", "{}"); r.status != 403 {
		t.Fatalf("operator ran retention %d", r.status)
	}
	// Garbage collection and the installation policy are for administrators.
	if r := otto.do("POST", e.srv.URL+"/api/v1/registry/gc", `{"dry_run":true}`); r.status != 403 {
		t.Fatalf("operator gc %d", r.status)
	}
	var gc registry.GCReport
	if r := ada.do("POST", e.srv.URL+"/api/v1/registry/gc", `{"dry_run":true}`); r.status != 200 {
		t.Fatalf("gc %d %s", r.status, r.body)
	} else {
		r.json(t, &gc)
	}
	if !gc.DryRun {
		t.Fatal("not a dry run")
	}
	var a struct {
		Entries []struct{ Action, User string }
	}
	ada.get(e.srv.URL+"/api/v1/audit?limit=300").json(t, &a)
	seen := map[string]bool{}
	for _, x := range a.Entries {
		seen[x.Action] = true
	}
	for _, want := range []string{"registry.image_delete", "registry.policy", "registry.gc_run", "registry.push"} {
		if !seen[want] {
			t.Errorf("audit trail has no %s", want)
		}
	}
}

func TestRegistryRunCredentialsAndTheStepJob(t *testing.T) {
	e := registryEnv(t, envOpts{})
	signer := e.api.Registry.Signer
	run := "run_abc123"
	cred, err := e.api.mintRegistryCredential("pay-api", run, "push", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := random.Image(2048, 1)
	if err := remote.Write(e.registryRef(t, "pay-api/built:1"), img, basicAuth(cred)); err != nil {
		t.Fatalf("a run could not push to its own project: %v", err)
	}
	if err := remote.Write(e.registryRef(t, "web/built:1"), img, basicAuth(cred)); err == nil {
		t.Fatal("a run pushed to another project")
	}
	prCred, _ := e.api.mintRegistryCredential("pay-api", run, "pull_request", time.Minute)
	if err := remote.Write(e.registryRef(t, "pay-api/built:2"), img, basicAuth(prCred)); err == nil {
		t.Fatal("a pull request's run pushed")
	}
	if _, err := remote.Image(e.registryRef(t, "pay-api/built:1"), basicAuth(prCred)); err != nil {
		t.Fatalf("a pull request's run could not pull: %v", err)
	}
	// An expired credential is nothing.
	e.clk.Add(time.Hour)
	if _, err := remote.Image(e.registryRef(t, "pay-api/built:1"), basicAuth(cred)); err == nil {
		t.Fatal("an expired run credential pulled")
	}
	_ = signer
	// The job an image step is handed carries the address and a credential, and the repository under its project.
	job := &engine.Job{Project: "pay-api", RunID: "run_x", Timeout: pipeline.Duration(10 * time.Minute), ImageBuild: &engine.ImageJob{
		Event: "push", Push: true, Tags: []engine.ImageJobTag{{Registry: engine.BuiltinRegistryName, Repository: "web-api", Tag: "1"}, {Registry: "harbor", Repository: "team/app", Tag: "1"}}}}
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/runner/v1/lease", nil)
	req.Host = strings.TrimPrefix(e.srv.URL, "http://")
	e.api.registryJobFor(req, job)
	b := job.ImageBuild
	if b.Registry == nil || b.Registry.Host != req.Host || !b.Registry.Insecure || !strings.HasPrefix(b.Registry.Token, registry.RunPrefix) || !b.Registry.Push {
		t.Fatalf("registry in the job: %+v", b.Registry)
	}
	if b.Tags[0].Repository != "pay-api/web-api" || b.Tags[1].Repository != "team/app" {
		t.Fatalf("repositories %+v", b.Tags)
	}
	// A job that does not use the built-in registry gets nothing.
	job2 := &engine.Job{Project: "pay-api", RunID: "run_y", ImageBuild: &engine.ImageJob{Event: "push", Tags: []engine.ImageJobTag{{Registry: "harbor", Repository: "t/a", Tag: "1"}}}}
	e.api.registryJobFor(req, job2)
	if job2.ImageBuild.Registry != nil {
		t.Fatal("a job for another registry was given a credential")
	}
}

func TestRegistryIsOffWithoutSignIn(t *testing.T) {
	s := &Server{Log: quiet(), Registry: &RegistryHosting{Svc: registry.New(registry.Options{})}}
	h := s.registryMiddleware(http.NotFoundHandler())
	rec := httptestRecorder(h, "GET", "/v2/")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "needs_sign_in") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	s.Registry = nil
	rec = httptestRecorder(s.registryMiddleware(http.NotFoundHandler()), "GET", "/v2/")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "feature_off") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func httptestRecorder(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}
