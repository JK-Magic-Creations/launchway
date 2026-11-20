package api

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// A change that is stored but not in effect on the server that took it is
// not answered 200: the caller is told it is stored, and the status shows
// each server's applied revision.
func TestStoredButNotAppliedIsNotAnswered200(t *testing.T) {
	e := setupAuth(t, envOpts{})
	broken := false
	if err := e.api.Settings.Load(func(*config.Config, *settings.Layout) error {
		if broken {
			return errors.New("the project manager is not ready")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ada := e.signIn(t, "ada")
	broken = true
	r := ada.do(http.MethodPut, e.srv.URL+"/api/v1/config/libraries/shared", `{"spec":{"repo":"https://git.example.com/shared.git"}}`)
	if r.status != http.StatusInternalServerError || !strings.Contains(string(r.body), `"stored":true`) || !strings.Contains(string(r.body), "not ready") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	o := ada.get(e.srv.URL + "/api/v1/config")
	if !strings.Contains(string(o.body), `"stored_revision":1`) || !strings.Contains(string(o.body), `"state":"failed"`) {
		t.Errorf("the status: %s", o.body)
	}
	broken = false
	if err := e.api.Settings.Refresh(); err != nil {
		t.Fatal(err)
	}
	o = ada.get(e.srv.URL + "/api/v1/config")
	if !strings.Contains(string(o.body), `"revision":1`) || !strings.Contains(string(o.body), `"state":"applied"`) {
		t.Errorf("after the retry: %s", o.body)
	}
}

// The overview is for everyone who holds a role, so it names only the
// projects the caller may see, and what is wrong with the configuration
// (which can name hosts and repositories) is for administrators.
func TestOverviewShowsOnlyWhatTheCallerMaySee(t *testing.T) {
	e := setupAuth(t, envOpts{})
	broken := false
	if err := e.api.Settings.Load(func(*config.Config, *settings.Layout) error {
		if broken {
			return errors.New("cannot reach git.corp.internal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	for _, r := range []response{
		ada.do(http.MethodPut, e.srv.URL+"/api/v1/config/units/alpha", `{"spec":{}}`),
		ada.do(http.MethodPut, e.srv.URL+"/api/v1/config/projects/pay-web", `{"unit":"alpha","spec":{"repo":"https://git.example.com/a.git"}}`),
		ada.do(http.MethodPut, e.srv.URL+"/api/v1/config/projects/layoffs-planner", `{"unit":"alpha","spec":{"repo":"https://git.example.com/b.git"}}`),
	} {
		if r.status != 200 && r.status != 201 {
			t.Fatalf("%d %s", r.status, r.body)
		}
	}
	broken = true
	ada.do(http.MethodPut, e.srv.URL+"/api/v1/config/projects/other", `{"unit":"alpha","spec":{"repo":"https://git.example.com/c.git"}}`)
	got := vic.get(e.srv.URL + "/api/v1/config")
	body := string(got.body)
	if got.status != 200 || !strings.Contains(body, "pay-web") {
		t.Fatalf("the viewer's overview: %d %s", got.status, body)
	}
	for _, leak := range []string{"layoffs-planner", "other", "corp.internal", "problem", "servers"} {
		if strings.Contains(body, leak) {
			t.Errorf("a viewer sees %q: %s", leak, body)
		}
	}
	if full := string(ada.get(e.srv.URL + "/api/v1/config").body); !strings.Contains(full, "layoffs-planner") || !strings.Contains(full, "corp.internal") {
		t.Errorf("the administrator's overview lacks them: %s", full)
	}
}

// A signed-in browser is the web UI: it cannot say it is Terraform, and a
// token can, but cannot change what Terraform owns without taking it.
func TestOnlyTokensActAsTerraformAndOwnershipIsKept(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	url := e.srv.URL + "/api/v1/config/libraries/shared"
	body := `{"spec":{"repo":"https://git.example.com/shared.git"}}`
	if r := ada.do(http.MethodPut, url, body, "X-Launchway-Manager", "terraform"); r.status != http.StatusForbidden {
		t.Fatalf("a browser claiming to be terraform: %d %s", r.status, r.body)
	}
	_, tok := ada.newToken(e.srv.URL, "terraform")
	tf := newBrowser(t)
	if r := tf.do(http.MethodPut, url, body, append(bearer(tok), "X-Launchway-Manager", "terraform")...); r.status != 201 {
		t.Fatalf("terraform with a token: %d %s", r.status, r.body)
	}
	r := ada.do(http.MethodPut, url, `{"spec":{"repo":"https://git.example.com/other.git"}}`)
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.body), "managed by terraform") {
		t.Fatalf("the UI changing terraform's object: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPut, url, `{"take_ownership":true,"spec":{"repo":"https://git.example.com/other.git"}}`); r.status != 200 {
		t.Fatalf("taking ownership: %d %s", r.status, r.body)
	}
	found := false
	for _, a := range e.auditEntries(t) {
		found = found || (a.Action == "config.put" && strings.Contains(a.Detail, "took ownership from terraform"))
	}
	if !found {
		t.Error("taking ownership is not in the audit trail")
	}
}
