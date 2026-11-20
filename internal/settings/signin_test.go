package settings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckSignIn(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + srv.URL + `","authorization_endpoint":"` + srv.URL + `/auth","token_endpoint":"` + srv.URL + `/token","jwks_uri":"` + srv.URL + `/keys","claims_supported":["sub","groups"]}`))
	}))
	defer srv.Close()

	ok := CheckSignIn(context.Background(), SignInRequest{Issuer: srv.URL, ClientID: "launchway", RedirectURL: "https://ci.example.test/auth/callback",
		ClientSecretEnv: "LAUNCHWAY_OIDC_SECRET", AdminGroups: []string{"platform"}}, nil)
	if !ok.OK || ok.Endpoints.Token == "" || ok.Endpoints.GroupsHint != "groups" {
		t.Fatalf("good answers: %+v", ok)
	}
	for _, want := range []string{"mode: oidc", "issuer: " + srv.URL, "client_secret_env: LAUNCHWAY_OIDC_SECRET", "groups:", "- platform"} {
		if !strings.Contains(ok.Snippet, want) {
			t.Errorf("the snippet lacks %q:\n%s", want, ok.Snippet)
		}
	}

	bad := CheckSignIn(context.Background(), SignInRequest{Issuer: srv.URL + "/wrong", ClientID: "x", RedirectURL: "https://ci.example.test/login"}, nil)
	if bad.OK || len(bad.Problems) < 3 {
		t.Errorf("wrong answers were accepted: %+v", bad)
	}
	down := CheckSignIn(context.Background(), SignInRequest{Issuer: "http://127.0.0.1:1", ClientID: "x", RedirectURL: "https://ci.example.test/auth/callback", ClientSecretEnv: "S", AdminUsers: []string{"ada"}}, nil)
	if down.OK || !strings.Contains(strings.Join(down.Problems, " "), "could not reach") {
		t.Errorf("an unreachable provider: %+v", down)
	}
}
