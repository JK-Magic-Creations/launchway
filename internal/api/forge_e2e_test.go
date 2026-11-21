package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
)

// hostedPolicy: ada administers everything, vic reads and otto pushes to
// repositories of the owner acme, dana is a viewer of web only.
func hostedPolicy(t *testing.T) *auth.Policy {
	t.Helper()
	p, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{Group: "pay-view", Role: auth.Viewer, Projects: []string{"acme/*"}},
		{Group: "pay-ops", Role: auth.Operator, Projects: []string{"acme/*"}},
		{User: "dana", Role: auth.Operator, Projects: []string{"web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// hostedEnv is a signed-in server with Git hosting on.
func hostedEnv(t *testing.T, o envOpts) (*authEnv, *hosting.Hosting) {
	t.Helper()
	return hostedEnvWith(t, o, hosting.Options{}, githttp.Options{})
}

// hostedEnvWith is hostedEnv with limits.
func hostedEnvWith(t *testing.T, o envOpts, ho hosting.Options, go_ githttp.Options) (*authEnv, *hosting.Hosting) {
	t.Helper()
	if o.policy == nil {
		o.policy = hostedPolicy(t)
	}
	e := setupAuth(t, o)
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ho.Engine, ho.Blobs, ho.Data, ho.Server, ho.BaseURL, ho.Log = e.eng, blobs, t.TempDir(), "api-test", e.srv.URL, quiet()
	h, err := hosting.New(t.Context(), ho)
	if err != nil {
		t.Fatal(err)
	}
	go_.Backend, go_.Log = h.Store, quiet()
	e.api.Forge = &ForgeHosting{Provider: h.Provider, Git: githttp.New(go_), RunTokens: h.RunTokens, Keys: h.Keys, Rules: h.Rules, Hooks: h.Hooks}
	return e, h
}

func TestRepositoriesAPI(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic, otto := e.signIn(t, "ada"), e.signIn(t, "vic"), e.signIn(t, "otto")
	base := e.srv.URL + "/api/v1/repositories"

	r := ada.do("POST", base, `{"owner":"acme","name":"shop","visibility":"private"}`)
	var made forge.Repository
	r.json(t, &made)
	if r.status != 201 || made.Path != "acme/shop" || made.DefaultBranch != "main" || !strings.HasSuffix(made.Clone.HTTPS, "/git/acme/shop.git") {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", base, `{"owner":"acme","name":"shop"}`); r.status != 409 {
		t.Errorf("duplicate: %d %s", r.status, r.body)
	}
	for _, bad := range []string{`{"owner":"acme","name":".."}`, `{"owner":"acme","name":"a/b"}`, `{"owner":"..","name":"x"}`, `{"owner":"acme","name":"x.git"}`,
		`{"owner":"acme","name":"` + strings.Repeat("a", 5000) + `"}`, `{"owner":"api","name":"x"}`} {
		if r := ada.do("POST", base, bad); r.status != 400 {
			t.Errorf("%.60s: %d %s", bad, r.status, r.body)
		}
	}
	if r := vic.do("POST", base, `{"owner":"acme","name":"mine"}`); r.status != 403 {
		t.Errorf("a viewer created a repository: %d", r.status)
	}
	ada.do("POST", base, `{"owner":"other","name":"secret"}`)

	// Lists show only what the caller may read; an unreadable repository is one that does not exist.
	var page forge.Page[forge.Repository]
	vic.get(base).json(t, &page)
	if len(page.Items) != 1 || page.Items[0].Path != "acme/shop" {
		t.Errorf("vic sees %+v", page.Items)
	}
	hidden, missing := vic.get(base+"/other/secret"), vic.get(base+"/other/nothing")
	if hidden.status != 404 || missing.status != 404 || string(hidden.body) != string(missing.body) {
		t.Errorf("hidden %d %s, missing %d %s", hidden.status, hidden.body, missing.status, missing.body)
	}
	if r := vic.get(base + "/acme/shop"); r.status != 200 {
		t.Errorf("vic reads: %d", r.status)
	}
	if r := otto.do("PATCH", base+"/acme/shop", `{"visibility":"public"}`); r.status != 403 {
		t.Errorf("an operator changed visibility: %d", r.status)
	}
	if r := ada.do("PATCH", base+"/acme/shop", `{"visibility":"internal","default_branch":"trunk","name":"store"}`); r.status != 200 {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	var got forge.Repository
	ada.get(base+"/acme/store").json(t, &got)
	if got.ID != made.ID || got.Visibility != forge.Internal || got.DefaultBranch != "trunk" {
		t.Errorf("%+v", got)
	}
	if r := ada.get(base + "/acme/shop"); r.status != 404 {
		t.Errorf("old name: %d", r.status)
	}
	if r := ada.do("DELETE", base+"/acme/store", ""); r.status != 204 {
		t.Errorf("delete: %d", r.status)
	}
	if r := ada.get(base + "/acme/store"); r.status != 404 {
		t.Errorf("a deleted repository answers: %d", r.status)
	}

	// The audit trail has create, update and delete.
	log, err := e.eng.AuditLog(engine.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, a := range log {
		if strings.HasPrefix(a.Action, "repository.") && a.Outcome == "ok" {
			seen[a.Action] = a.Detail
		}
	}
	if seen["repository.create"] == "" || !strings.Contains(seen["repository.update"], "visibility internal") || seen["repository.delete"] == "" {
		t.Errorf("audit: %v", seen)
	}
}

func TestHostingOffAnswersFeatureOffAndKeepsTheData(t *testing.T) {
	e, h := hostedEnv(t, envOpts{})
	ada := e.signIn(t, "ada")
	base := e.srv.URL + "/api/v1/repositories"
	if r := ada.do("POST", base, `{"owner":"acme","name":"kept"}`); r.status != 201 {
		t.Fatal(r.status, string(r.body))
	}
	on := e.api.Forge
	e.api.Forge = nil // forge.hosting turned off
	for _, c := range [][2]string{{"GET", base}, {"POST", base}, {"GET", base + "/acme/kept"}, {"PATCH", base + "/acme/kept"}, {"DELETE", base + "/acme/kept"}} {
		r := ada.do(c[0], c[1], `{"owner":"acme","name":"x"}`)
		var body map[string]string
		_ = json.Unmarshal(r.body, &body)
		if r.status != http.StatusNotFound || !strings.HasPrefix(body["error"], "feature_off") {
			t.Errorf("%s %s: %d %s", c[0], c[1], r.status, r.body)
		}
	}
	if _, err := h.Resolve(t.Context(), "acme/kept"); err != nil {
		t.Errorf("turning hosting off lost the data: %v", err)
	}
	e.api.Forge = on
	if r := ada.get(base + "/acme/kept"); r.status != 200 {
		t.Errorf("turned on again: %d", r.status)
	}
}

func TestGiteaStyleRepositoryCalls(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada := e.signIn(t, "ada")
	var me struct{ Login string }
	ada.get(e.srv.URL+"/api/v1/user").json(t, &me)
	if me.Login != "ada" {
		t.Fatalf("login %q", me.Login)
	}
	api := e.srv.URL + "/api/v1"
	var made compatRepo
	r := ada.do("POST", api+"/user/repos", `{"name":"lw-x","private":true,"default_branch":"main"}`)
	r.json(t, &made)
	if r.status != 201 || made.FullName != "ada/lw-x" || !made.Private || !made.Empty || made.Owner.Login != "ada" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do("PATCH", api+"/repos/ada/lw-x", `{"has_actions":false}`); r.status != 200 {
		t.Errorf("an unknown field: %d %s", r.status, r.body)
	}
	if r := ada.get(api + "/repos/ada/lw-x/branches/main"); r.status != 404 {
		t.Errorf("a branch of an empty repository: %d", r.status)
	}
	tok := e.tokenFor(t, "ada", "git")
	mustGit(t, work(t), "version")
	src := work(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/ada/lw-x.git", tok), "HEAD:refs/heads/main")
	if r := ada.get(api + "/repos/ada/lw-x/branches/main"); r.status != 200 {
		t.Errorf("the pushed branch: %d %s", r.status, r.body)
	}
	if r := ada.do("PATCH", api+"/repos/ada/lw-x", `{"private":false,"archived":true}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if out, err := git(t, src, "push", withToken(e.srv.URL, "/ada/lw-x.git", tok), "HEAD:refs/heads/more"); err == nil || !strings.Contains(out, "archived") {
		t.Errorf("a push to an archived repository: %v\n%s", err, out)
	}
	if r := ada.do("DELETE", api+"/repos/ada/lw-x", ""); r.status != 204 {
		t.Errorf("delete: %d", r.status)
	}
}
