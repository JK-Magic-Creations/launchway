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
