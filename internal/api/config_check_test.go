package api

import (
	"net/http"
	"strings"
	"testing"
)

// The check routes make the server contact an address the caller gives: an
// internal one is refused with a short answer, each call is audited, and a
// caller who keeps asking is slowed down.
func TestCheckRoutesAreGuardedAuditedAndLimited(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	url := e.srv.URL + "/api/v1/config/check/repository"
	r := ada.do(http.MethodPost, url, `{"repo":"http://169.254.169.254/latest/meta-data/"}`)
	if r.status != 200 || !strings.Contains(string(r.body), `"blocked":true`) || strings.Contains(string(r.body), "fatal") {
		t.Fatalf("metadata address: %d %s", r.status, r.body)
	}
	r = ada.do(http.MethodPost, e.srv.URL+"/api/v1/config/check/signin", `{"issuer":"http://127.0.0.1:1"}`)
	if r.status != 200 || !strings.Contains(string(r.body), `"blocked":true`) {
		t.Fatalf("loopback issuer: %d %s", r.status, r.body)
	}
	var repo, signin int
	for _, a := range e.auditEntries(t) {
		switch a.Action {
		case "config.check_repository":
			repo++
			if a.Target != "http://169.254.169.254/latest/meta-data/" || a.User != "ada" {
				t.Errorf("audit entry: %+v", a)
			}
		case "config.check_signin":
			signin++
		}
	}
	if repo != 1 || signin != 1 {
		t.Errorf("audited %d repository and %d sign-in checks", repo, signin)
	}
	limited := false
	for i := 0; i < 30 && !limited; i++ {
		limited = ada.do(http.MethodPost, url, `{"repo":"http://10.0.0.1/x.git"}`).status == http.StatusTooManyRequests
	}
	if !limited {
		t.Error("thirty checks in a row were all answered")
	}
}
