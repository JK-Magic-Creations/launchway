package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// A protected tag is protected the same way over HTTPS, over SSH and through the API: by default an administrator
// makes it and nobody moves or deletes it, an unprotected tag is anyone's, a rule can let operators make one and
// administrators move it, and every change of the rules is in the audit trail. The refusals say why.
func TestProtectedTagsAreEnforcedOnEveryDoor(t *testing.T) {
	h := sshEnv(t)
	hostedRepo(t, h.e, "acme/shop")
	ada, otto := h.e.signIn(t, "ada"), h.e.signIn(t, "otto")
	adaKey, adaPub := h.key(t, "ada")
	ottoKey, ottoPub := h.key(t, "otto")
	h.register(t, ada, "a", adaPub)
	h.register(t, otto, "o", ottoPub)
	ottoTok := h.e.tokenFor(t, "otto", "t")
	adaTok := h.e.tokenFor(t, "ada", "t")
	api := h.e.srv.URL + "/api/v1/repositories/acme/shop"

	src := workingCopy(t)
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "HEAD:refs/heads/main")
	mustGit(t, src, "tag", "v0.0.1")
	mustGit(t, src, "tag", "nightly")

	// An operator may not set rules; an administrator does, and it is audited.
	if r := otto.do("PUT", api+"/protected-tags", `{"rules":[{"pattern":"v*"}]}`); r.status != 403 {
		t.Fatalf("an operator set the rules: %d", r.status)
	}
	if r := ada.do("PUT", api+"/protected-tags", `{"rules":[{"pattern":"v[*"}]}`); r.status != 400 {
		t.Fatalf("a bad pattern: %d %s", r.status, r.body)
	}
	if r := ada.do("PUT", api+"/protected-tags", `{"rules":[{"pattern":"v*"},{"pattern":"rc-*","create_role":"operator","change_role":"admin"}]}`); r.status != 200 {
		t.Fatalf("set: %d %s", r.status, r.body)
	}
	var got struct {
		Rules []struct {
			Pattern    string
			CreateRole string `json:"create_role"`
			ChangeRole string `json:"change_role"`
		} `json:"rules"`
	}
	otto.get(api+"/protected-tags").json(t, &got) // any reader sees them
	if len(got.Rules) != 2 || got.Rules[0].CreateRole != "admin" || got.Rules[0].ChangeRole != "" || got.Rules[1].ChangeRole != "admin" {
		t.Fatalf("rules: %+v", got.Rules)
	}
	var audited bool
	for _, a := range h.e.auditEntries(t) {
		audited = audited || a.Action == "repository.protect_tags" && a.Outcome == "ok" && strings.Contains(a.Detail, "v*")
	}
	if !audited {
		t.Error("the change of rules is not in the audit trail")
	}

	refused := func(what string, out string, err error, want string) {
		t.Helper()
		if err == nil || !strings.Contains(out, want) {
			t.Errorf("%s: %v\n%s\nwant a refusal saying %q", what, err, out, want)
		}
	}
	httpsOtto := withToken(h.e.srv.URL, "/git/acme/shop.git", ottoTok)
	httpsAda := withToken(h.e.srv.URL, "/git/acme/shop.git", adaTok)

	// Making a protected tag: refused for an operator on both transports, with the same words; free if unprotected.
	sshOut, sshErr := h.git(t, ottoKey, src, "push", h.url("acme/shop"), "refs/tags/v0.0.1")
	httpsOut, httpsErr := git(t, src, "push", httpsOtto, "refs/tags/v0.0.1")
	refused("ssh create", sshOut, sshErr, "only an administrator of the repository may make a tag like it")
	refused("https create", httpsOut, httpsErr, "only an administrator of the repository may make a tag like it")
	h.mustGit(t, ottoKey, src, "push", "-q", h.url("acme/shop"), "refs/tags/nightly")
	// An administrator makes it; then nobody deletes or moves it, an administrator included.
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), "refs/tags/v0.0.1")
	out, err := h.git(t, adaKey, src, "push", h.url("acme/shop"), ":refs/tags/v0.0.1")
	refused("ssh delete", out, err, "is protected (pattern v*) and cannot be deleted")
	out, err = git(t, src, "push", httpsAda, ":refs/tags/v0.0.1")
	refused("https delete", out, err, "cannot be deleted")
	commit(t, src, "m.txt", "x\n", "more")
	out, err = git(t, src, "push", "--force", httpsAda, "HEAD:refs/tags/v0.0.1")
	refused("https move", out, err, "cannot be moved")

	// The API's tag routes meet the same rules.
	if r := otto.do("POST", api+"/tags", `{"name":"v9.9.9","from":"main"}`); r.status != 403 && r.status != 409 || !strings.Contains(string(r.body), "only an administrator of the repository may make a tag like it") {
		t.Errorf("an operator made a protected tag through the API: %d %s", r.status, r.body)
	}
	if r := ada.do("DELETE", api+"/tags?name=v0.0.1", ``); r.status == 200 || !strings.Contains(string(r.body), "cannot be deleted") {
		t.Errorf("the API deleted a protected tag: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", api+"/tags", `{"name":"nightly2","from":"main"}`); r.status != 201 && r.status != 200 {
		t.Errorf("an unprotected tag through the API: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", api+"/tags", `{"name":"v1.0.0","from":"main"}`); r.status != 201 && r.status != 200 {
		t.Errorf("an administrator's tag through the API: %d %s", r.status, r.body)
	}

	// A rule can let operators make one and administrators move or delete it.
	h.mustGit(t, ottoKey, src, "push", "-q", h.url("acme/shop"), "refs/tags/nightly:refs/tags/rc-1")
	mustGit(t, src, "tag", "rc-1")
	out, err = h.git(t, ottoKey, src, "push", "--force", h.url("acme/shop"), "HEAD:refs/tags/rc-1")
	refused("operator moves rc-1", out, err, "only an administrator of the repository may")
	h.mustGit(t, adaKey, src, "push", "-q", "--force", h.url("acme/shop"), "HEAD:refs/tags/rc-1")
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), ":refs/tags/rc-1")

	// Taking the protection away frees the tag again.
	if r := ada.do("PUT", api+"/protected-tags", `{"rules":[]}`); r.status != 200 {
		t.Fatalf("clear: %d %s", r.status, r.body)
	}
	h.mustGit(t, adaKey, src, "push", "-q", h.url("acme/shop"), ":refs/tags/v0.0.1")
}

// "Cut a release" on a hosted repository makes its tag through the same rules: an operator who may cut a release
// but not make a protected tag is told why, the draft stays a draft and no tag appears; once a rule lets
// operators make it, the same call publishes.
func TestCuttingAReleaseMeetsProtectedTags(t *testing.T) {
	pol, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{Group: "pay-ops", Role: auth.Operator, Projects: []string{"acme/*", "hub"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	e, _ := hostedEnvWith(t, envOpts{policy: pol, projectsAt: func(base string) []source.Project {
		return []source.Project{{Name: "hub", URL: base + "/git/acme/hub.git", Poll: time.Hour}}
	}}, hosting.Options{}, githttp.Options{})
	e.api.Projects.SetHostedLocal(e.api)
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	api := e.srv.URL + "/api/v1"
	if r := ada.do("POST", api+"/repositories", `{"owner":"acme","name":"hub","visibility":"private"}`); r.status != 201 {
		t.Fatalf("repository: %d %s", r.status, r.body)
	}
	src := workingCopy(t)
	mustGit(t, src, "branch", "-M", "main")
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/git/acme/hub.git", e.tokenFor(t, "ada", "git")), "HEAD:refs/heads/main")
	if r := ada.do("PUT", api+"/repositories/acme/hub/protected-tags", `{"rules":[{"pattern":"v*"}]}`); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	waitForMirror(t, "the commit to be readable", func() bool {
		r := otto.do("POST", api+"/projects/hub/releases", `{"version":"0.1.0"}`)
		return r.status == http.StatusCreated || r.status == http.StatusConflict
	})
	r := otto.do("POST", api+"/projects/hub/releases/0.1.0/publish", `{}`)
	if r.status == 200 || !strings.Contains(string(r.body), "only an administrator of the repository may make a tag like it") {
		t.Fatalf("an operator published past a protected tag: %d %s", r.status, r.body)
	}
	var tags struct{ Items []struct{ Name string } }
	ada.get(api+"/repositories/acme/hub/tags").json(t, &tags)
	if len(tags.Items) != 0 {
		t.Fatalf("a tag appeared: %+v", tags.Items)
	}
	if r := ada.do("PUT", api+"/repositories/acme/hub/protected-tags", `{"rules":[{"pattern":"v*","create_role":"operator"}]}`); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	if r := otto.do("POST", api+"/projects/hub/releases/0.1.0/publish", `{}`); r.status != 200 {
		t.Fatalf("the release was not published once operators may make the tag: %d %s", r.status, r.body)
	}
}
