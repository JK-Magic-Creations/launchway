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
