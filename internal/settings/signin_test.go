package settings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// A repository or model address that leads off the public internet or onto
// this server is refused when it is set, unless the server allows local paths
// or an administrator listed the host.
func TestChangesToInternalAddressesAreRefusedUnlessAllowed(t *testing.T) {
	objs := func(repo, endpoint string) []engine.ConfigObject {
		return []engine.ConfigObject{
			{Kind: "project", Name: "p", Spec: []byte(`{"repo":"` + repo + `"}`)},
			{Kind: "model", Name: "m", Spec: []byte(`{"provider":"openai","model":"q","endpoint":"` + endpoint + `"}`)},
		}
	}
	c := &config.Config{}
	for _, tc := range [][2]string{{"/srv/git/x", "https://api.example.com/v1"}, {"https://git.example.com/x", "http://localhost:11434/v1"},
		{"http://10.0.0.5/x.git", "https://api.example.com/v1"}, {"file:///srv/x", "https://api.example.com/v1"}} {
		if err := checkEgress(c, objs(tc[0], tc[1])); err == nil || !strings.Contains(err.Error(), "allowed hosts") {
			t.Errorf("%v: %v", tc, err)
		}
	}
	if err := checkEgress(c, objs("https://git.example.com/x", "https://api.example.com/v1")); err != nil {
		t.Errorf("public addresses: %v", err)
	}
	c.Egress = config.EgressConfig{AllowLocal: true, Allow: []string{"10.0.0.5", "localhost"}}
	if err := checkEgress(c, objs("/srv/git/x", "http://localhost:11434/v1")); err != nil {
		t.Errorf("allowed: %v", err)
	}
	if err := checkEgress(c, objs("http://10.0.0.5/x.git", "https://api.example.com/v1")); err != nil {
		t.Errorf("an allowed host: %v", err)
	}
	if err := Check(&config.Config{Egress: config.EgressConfig{Allow: []string{"https://x"}}}, nil); err == nil {
		t.Error("a bad allowed-hosts entry was accepted")
	}
}

// allowLoopback lets a test reach servers on this machine, as an
// administrator's allowed hosts would.
func allowLoopback(t *testing.T) {
	t.Helper()
	old := egress.Default.Policy()
	_ = egress.Default.Set(egress.Policy{Allow: []string{"127.0.0.0/8"}})
	t.Cleanup(func() { _ = egress.Default.Set(old) })
}

// The check contacts an address a caller gave: an internal one is refused
// unless allowed, with a short answer that holds no detail, and a redirect
// is reported, never followed.
func TestCheckSignInDoesNotReachInternalAddresses(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	req := SignInRequest{Issuer: srv.URL, ClientID: "x", RedirectURL: "https://ci.example.test/auth/callback", ClientSecretEnv: "S", AdminUsers: []string{"ada"}}
	got := CheckSignIn(context.Background(), req, nil)
	if !got.Blocked || got.BlockedHost != "127.0.0.1" || hits != 0 || got.OK {
		t.Fatalf("an internal issuer was contacted (%d hits): %+v", hits, got)
	}
	if text := strings.Join(got.Problems, " "); strings.Contains(text, "dial") || strings.Contains(text, srv.Listener.Addr().String()) {
		t.Errorf("the answer carries network detail: %s", text)
	}
	allowLoopback(t)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirect.Close()
	req.Issuer = redirect.URL
	got = CheckSignIn(context.Background(), req, nil)
	if got.Blocked || !strings.Contains(strings.Join(got.Problems, " "), "redirects") {
		t.Errorf("a redirect: %+v", got)
	}
}

func TestCheckSignIn(t *testing.T) {
	allowLoopback(t)
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
