package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
)

func aliasPolicy(t *testing.T) *auth.Policy {
	t.Helper()
	p, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{User: "dana", Role: auth.Admin, Projects: []string{"acme/*"}},
		{Group: "pay-view", Role: auth.Viewer, Projects: []string{"acme/*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The address a repository had keeps working after a rename: Git over HTTPS clones and pushes through it and says
// where the repository is now, the API and the web pages redirect (with the rest of the address and the query kept),
// someone who may not read the repository where it is now learns nothing, and the address stops answering the day a
// new repository takes it.
func TestOldAddressKeepsWorkingUntilAnotherRepositoryTakesIt(t *testing.T) {
	e, _ := hostedEnvWith(t, envOpts{policy: aliasPolicy(t)}, hosting.Options{}, githttp.Options{})
	shop := hostedRepo(t, e, "acme/shop")
	ada, dana, vic := e.signIn(t, "ada"), e.signIn(t, "dana"), e.signIn(t, "vic")
	tok := e.tokenFor(t, "ada", "git")
	src := workingCopy(t)
	mustGit(t, src, "branch", "-M", "main")
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/main")

	if r := dana.do("PATCH", e.srv.URL+"/api/v1/repositories/acme/shop", `{"name":"store"}`); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	// Git, both address forms, through the old name; a push is told where the repository is.
	for _, form := range []string{"/git/acme/shop.git", "/acme/shop.git"} {
		dst := t.TempDir()
		if out, err := git(t, dst, "clone", "-q", withToken(e.srv.URL, form, tok), "."); err != nil {
			t.Fatalf("clone through the old address %s: %v\n%s", form, err, out)
		}
	}
	commit(t, src, "b.txt", "two\n", "second")
	out, err := git(t, src, "push", withToken(e.srv.URL, "/git/acme/shop.git", tok), "HEAD:refs/heads/main")
	if err != nil || !strings.Contains(out, "moved from acme/shop to acme/store") {
		t.Fatalf("a push through the old address: %v\n%s", err, out)
	}
	if refs, err := e.api.Forge.Provider.Refs(t.Context(), shop); err != nil || len(refs) == 0 {
		t.Fatalf("the push did not reach the repository: %v %v", refs, err)
	}
	// The API and the web redirect, keeping the rest of the address and the query.
	r := ada.get(e.srv.URL + "/api/v1/repositories/acme/shop/branches?limit=5")
	if r.status != 308 || r.location() != "/api/v1/repositories/acme/store/branches?limit=5" {
		t.Fatalf("the API at the old address: %d %q", r.status, r.location())
	}
	if r := ada.do("PATCH", e.srv.URL+"/api/v1/repositories/acme/shop", `{"archived":false}`); r.status != 308 {
		t.Errorf("a write at the old address: %d (the method is kept by a 308)", r.status)
	}
	if got := vic.get(e.srv.URL + "/api/v1/repositories/acme/shop"); got.status != 308 {
		t.Errorf("a viewer of the owner: %d", got.status)
	}
	if got := newBrowser(t).get(e.srv.URL + "/api/v1/repositories/acme/shop"); got.status != 401 {
		t.Errorf("signed out: %d", got.status)
	}
	// Moved to an owner the viewer may not read: the old address says nothing.
	if r := ada.do("PATCH", e.srv.URL+"/api/v1/repositories/acme/store", `{"owner":"other"}`); r.status != 200 {
		t.Fatalf("transfer: %d %s", r.status, r.body)
	}
	if got := vic.get(e.srv.URL + "/api/v1/repositories/acme/shop"); got.status != 404 {
		t.Errorf("a viewer learned where it went: %d", got.status)
	}
	if got := vic.get(e.srv.URL + "/api/v1/repositories/acme/store"); got.status != 404 {
		t.Errorf("the intermediate address: %d", got.status)
	}
	// Both old addresses still lead to it for someone who may read it there.
	if got := ada.get(e.srv.URL + "/api/v1/repositories/acme/shop"); got.status != 308 || got.location() != "/api/v1/repositories/other/store" {
		t.Errorf("the first old address after a second move: %d %q", got.status, got.location())
	}
	dst := t.TempDir()
	if out, err := git(t, dst, "clone", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "."); err != nil {
		t.Fatalf("clone through the first address after the transfer: %v\n%s", err, out)
	}
	// A new repository takes the address: it answers, and the old one no longer does.
	fresh := hostedRepo(t, e, "acme/shop")
	var got forge.Repository
	ada.get(e.srv.URL+"/api/v1/repositories/acme/shop").json(t, &got)
	if got.ID != fresh.ID || got.ID == shop.ID {
		t.Fatalf("the address after a new repository took it: %+v", got)
	}
	if out, err := git(t, t.TempDir(), "clone", "-q", withToken(e.srv.URL, "/git/acme/shop.git", tok), "x"); err != nil || strings.Contains(out, "moved from") {
		t.Errorf("cloning the new repository: %v\n%s", err, out)
	}
	if got := ada.get(e.srv.URL + "/api/v1/repositories/acme/store"); got.status != 308 || got.location() != "/api/v1/repositories/other/store" {
		t.Errorf("the second old address: %d %q", got.status, got.location())
	}
}

// Everything that named a repository follows it when it is transferred: webhooks, protected branches, a token
// limited to it, the repository's own pull request counter, and a Flight whose address is the old clone address.
// Moving it somewhere one does not administer is refused.
func TestTransferKeepsWhatReferencedTheRepository(t *testing.T) {
	e, _ := hostedEnvWith(t, envOpts{policy: aliasPolicy(t)}, hosting.Options{}, githttp.Options{})
	shop := hostedRepo(t, e, "acme/shop")
	ada, dana := e.signIn(t, "ada"), e.signIn(t, "dana")
	tok := e.tokenFor(t, "ada", "git")
	_, limited := repoToken(t, e, `{"name":"shop","repositories":["acme/shop"],"repo_access":"read"}`)
	hook, _ := whAdd(t, ada, e.srv.URL+"/api/v1/repositories/acme/shop/hooks", `{"name":"ci","url":"http://127.0.0.1:9/h","events":["push"]}`)
	if r := ada.do("PUT", e.srv.URL+"/api/v1/repositories/acme/shop/protected-branches", `{"patterns":["main"]}`); r.status != 200 {
		t.Fatalf("protect: %d %s", r.status, r.body)
	}
	oldAddress := shop.Clone.HTTPS
	for _, q := range []string{
		`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1', ?, 'push', 'https://git.example.com/acme/shop.git')`,
		`INSERT INTO ssh_keys(id, kind, repo_id, name, key_type, public, fingerprint) VALUES('k1', 'deploy', ?, 'ci', 'ssh-ed25519', 'AAAA', 'SHA256:x')`,
	} {
		if _, err := e.eng.Database().Exec(t.Context(), q, shop.ID); err != nil {
			t.Fatal(err)
		}
	}
	if r := dana.do("PATCH", e.srv.URL+"/api/v1/repositories/acme/shop", `{"owner":"other"}`); r.status != 403 {
		t.Fatalf("an administrator of acme moved a repository to an owner they do not administer: %d %s", r.status, r.body)
	}
	if r := ada.do("PATCH", e.srv.URL+"/api/v1/repositories/acme/shop", `{"owner":"other","name":"shop2"}`); r.status != 200 {
		t.Fatalf("transfer: %d %s", r.status, r.body)
	}
	base := e.srv.URL + "/api/v1/repositories/other/shop2"
	var hooks struct {
		Items []map[string]any `json:"items"`
	}
	ada.get(base+"/hooks").json(t, &hooks)
	if len(hooks.Items) != 1 || hooks.Items[0]["id"] != hook["id"] || hooks.Items[0]["repository"] != "other/shop2" {
		t.Errorf("the webhook after the transfer: %+v", hooks.Items)
	}
	var pb struct {
		Patterns []string `json:"patterns"`
	}
	ada.get(base+"/protected-branches").json(t, &pb)
	if len(pb.Patterns) != 1 || pb.Patterns[0] != "main" {
		t.Errorf("the protected branches after the transfer: %+v", pb)
	}
	for _, form := range []string{"/other/shop2", "/acme/shop"} {
		if got := gitStatus(e, limited, form, "git-upload-pack"); got != 200 {
			t.Errorf("the limited token at %s after the transfer: %d", form, got)
		}
	}
	if got := gitStatus(e, tok, "/acme/shop", "git-receive-pack"); got != 200 {
		t.Errorf("a push credential through the old address: %d", got)
	}
	// Mirrors and deploy keys are the repository's by identity: still there, still its own.
	for _, table := range []string{"repo_mirrors", "ssh_keys"} {
		if n := ledgerRows(t, e, table, shop.ID); n != 1 {
			t.Errorf("%s of the repository after the transfer: %d", table, n)
		}
	}
	// The Flight whose address is the old clone address still reads this repository.
	got, ok := e.api.hostedRepoOf(t.Context(), oldAddress)
	if !ok || got.ID != shop.ID || got.Path != "other/shop2" {
		t.Errorf("a Flight at the old clone address: %+v %v", got, ok)
	}
	if _, ok := e.api.hostedRepoOf(t.Context(), "http://elsewhere.example/git/acme/shop.git"); ok {
		t.Error("an address on another host was taken for this server's repository")
	}
	// Once another repository takes the old address, the Flight does not follow the old repository any more.
	hostedRepo(t, e, "acme/shop")
	if got, ok := e.api.hostedRepoOf(t.Context(), oldAddress); !ok || got.ID == shop.ID {
		t.Errorf("a Flight at an address another repository took: %+v %v", got, ok)
	}
}

// Git over SSH is mapped the same way: the old address clones and says where the repository is.
func TestOldAddressWorksOverSSHAndSaysSo(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada := h.e.signIn(t, "ada")
	key, pub := h.key(t, "ada")
	h.register(t, ada, "laptop", pub)
	src := workingCopy(t)
	h.mustGit(t, key, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	if r := ada.do("PATCH", h.e.srv.URL+"/api/v1/repositories/acme/shop", `{"name":"store"}`); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	out := h.mustGit(t, key, t.TempDir(), "clone", h.url("acme/shop"), "c")
	if !strings.Contains(out, "moved from acme/shop to acme/store") {
		t.Errorf("the clone through the old address was not told where the repository is:\n%s", out)
	}
}
