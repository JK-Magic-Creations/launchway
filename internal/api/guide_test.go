package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/egress"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

func TestRootKeyCopyWrapsAndUnwraps(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	file, err := WrapRootKey(key, "purple walrus drinks coffee", time.Unix(1_700_000_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "AAECAwQFBgcICQ") { // the key's base64 must not appear
		t.Fatal("the key is in the copy in the clear")
	}
	got, err := UnwrapRootKey(file, "purple walrus drinks coffee")
	if err != nil || string(got) != string(key) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := UnwrapRootKey(file, "a different passphrase!!"); err == nil {
		t.Fatal("a wrong passphrase must not open the copy")
	}
}

func TestGuideIsForAdministratorsAndSaysWhatCannotWork(t *testing.T) {
	e := setupAuth(t, envOpts{})
	if st := newBrowser(t).get(e.srv.URL + "/api/v1/setup-guide").status; st != 401 {
		t.Fatalf("anonymous: %d", st)
	}
	b := e.signIn(t, "ada")
	var g SetupGuide
	r := b.get(e.srv.URL + "/api/v1/setup-guide")
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r.json(t, &g)
	byID := map[string]GuideStep{}
	for _, s := range g.Steps {
		byID[s.ID] = s
	}
	for _, id := range []string{"sign-in", "encryption", "runner", "git-hosting", "import", "pipeline"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("no step %s", id)
		}
	}
	// Hosting is off here: importing is unavailable, and every unavailable action says why.
	if byID["import"].State != "unavailable" {
		t.Fatalf("import: %+v", byID["import"])
	}
	for _, s := range g.Steps {
		for _, a := range s.Actions {
			if !a.Available && a.Reason == "" {
				t.Errorf("%s/%s is unavailable with no reason", s.ID, a.Name)
			}
		}
	}
	// Switching a provider on cannot be done from here, and that is said, not hidden behind a button.
	for _, a := range byID["sign-in"].Actions {
		if a.Name == "switch_provider_on" && (a.Available || a.Reason == "") {
			t.Fatalf("switch_provider_on must be unavailable with a reason: %+v", a)
		}
	}
	if len(g.Checklist) == 0 {
		t.Fatal("no checklist")
	}
}

func TestRootKeyCopyIsGuarded(t *testing.T) {
	e := setupAuth(t, envOpts{})
	b := e.signIn(t, "ada")
	// No root key configured: the answer is a plain refusal with a way forward.
	r := b.do("POST", e.srv.URL+"/api/v1/encryption/root-key-copy", `{"current_password":"x","code":"1","passphrase":"purple walrus drinks coffee","passphrase_again":"purple walrus drinks coffee"}`)
	if r.status != 409 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var out map[string]string
	_ = json.Unmarshal(r.body, &out)
	if out["error"] == "" || out["code"] == "" || out["next"] == "" {
		t.Fatalf("an error needs error, code and next: %v", out)
	}
	if strings.Contains(string(r.body), "key\":") {
		t.Fatal("never a key in an answer")
	}
}

// Without a root key a pasted token is refused with the way forward; nothing is kept in the clear.
func TestPasteTokenIsRefusedWithoutARootKey(t *testing.T) {
	e := setupAuth(t, envOpts{policy: hostedPolicy(t)})
	noKey, err := sealing.Start(t.Context(), sealing.Options{DB: e.eng.Database()})
	if err != nil {
		t.Fatal(err)
	}
	e.api.Secrets = noKey
	if why := e.api.pasteUnavailable(); !strings.Contains(why, "root key") {
		t.Fatalf("with no root key: %q", why)
	}
}

// A pasted token is kept sealed: the connection refers to it by name, a full dump of the database
// holds no trace of it, the connection can read it, and removing the connection removes it.
func TestPastedTokenIsKeptSealedAndRemovedWithItsConnection(t *testing.T) {
	const marker = "PASTED-TOKEN-MARKER-5d1e7a93c0b84f26"
	e, _ := hostedEnv(t, envOpts{})
	forge.SetSealedReader(func(name string) (string, error) {
		return e.api.Secrets.ConnectionToken(t.Context(), name)
	})
	t.Cleanup(func() { forge.SetSealedReader(nil) })
	ada := e.signIn(t, "ada")
	if why := e.api.pasteUnavailable(); why != "" {
		t.Fatalf("with a root key a paste can be kept: %q", why)
	}
	r := ada.do("POST", e.srv.URL+"/api/v1/connections/token", `{"name":"oldgit","kind":"plain","base_url":"","token":"`+marker+`"}`)
	if r.status != 201 || strings.Contains(string(r.body), marker) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if !strings.Contains(string(r.body), `"sealed:oldgit"`) {
		t.Fatalf("the connection should refer to the sealed token: %s", r.body)
	}
	got, err := forge.Sealed("oldgit").Resolve()
	if err != nil || got != marker {
		t.Fatalf("resolve: %v", err)
	}
	var raw string
	if err := e.eng.Database().QueryRow(t.Context(), `SELECT value FROM meta WHERE key = ?`, "shared:conn-token/oldgit").Scan(&raw); err != nil || strings.Contains(raw, marker) {
		t.Fatalf("the stored value must be sealed: %v", err)
	}
	if again := ada.do("POST", e.srv.URL+"/api/v1/connections/token", `{"name":"oldgit","kind":"plain","token":"x"}`); again.status != 409 {
		t.Fatalf("a second paste under the name: %d %s", again.status, again.body)
	}
	if del := ada.do("DELETE", e.srv.URL+"/api/v1/connections/oldgit", ""); del.status != 204 {
		t.Fatalf("remove: %d %s", del.status, del.body)
	}
	if _, err := forge.Sealed("oldgit").Resolve(); err == nil {
		t.Fatal("the token must go with its connection")
	}
}

// The provider test, end to end with the test provider: the administrator begins, a second window signs
// in at the provider, and the result is in the audit trail; nothing about sign-in changes.
func TestProviderTestBeforeSwitch(t *testing.T) {
	e := setupAuth(t, envOpts{})
	allowLocalProvider(t)
	t.Setenv("TEST_IDP_SECRET", e.idp.ClientSecret)
	b := e.signIn(t, "ada")
	body := `{"issuer":"` + e.idp.URL + `","client_id":"` + e.idp.ClientID + `","client_secret_env":"TEST_IDP_SECRET"}`
	r := b.do("POST", e.srv.URL+"/api/v1/signin/test", body)
	if r.status != 200 {
		t.Fatalf("begin: %d %s", r.status, r.body)
	}
	var begun struct {
		URL         string `json:"url"`
		RedirectURL string `json:"redirect_url"`
	}
	r.json(t, &begun)
	if !strings.HasSuffix(begun.RedirectURL, "/auth/signin-test") {
		t.Fatalf("redirect %q", begun.RedirectURL)
	}
	var st signinTestResult
	b.get(e.srv.URL+"/api/v1/signin/test").json(t, &st)
	if st.Tested {
		t.Fatal("nothing was tested yet")
	}
	// The second window: the provider's page, then back to the test's own address.
	e.idp.SignInAs("someone-at-the-provider", "staff")
	page := b.get(b.authorize(begun.URL))
	if page.status != 200 || !strings.Contains(string(page.body), "The test sign-in worked") {
		t.Fatalf("callback: %d %s", page.status, page.body)
	}
	b.get(e.srv.URL+"/api/v1/signin/test").json(t, &st)
	if !st.Tested || !st.OK || !strings.Contains(st.Detail, "someone-at-the-provider") {
		t.Fatalf("result: %+v", st)
	}
	// A callback with no test in progress is refused with a page, not a session.
	if p := newBrowser(t).get(e.srv.URL + "/auth/signin-test?code=x&state=y"); p.status != 200 || !strings.Contains(string(p.body), "expired") {
		t.Fatalf("stray callback: %d %s", p.status, p.body)
	}
}

func TestProviderTestRefusesAMissingSecretVariable(t *testing.T) {
	e := setupAuth(t, envOpts{})
	allowLocalProvider(t)
	b := e.signIn(t, "ada")
	r := b.do("POST", e.srv.URL+"/api/v1/signin/test", `{"issuer":"`+e.idp.URL+`","client_id":"`+e.idp.ClientID+`","client_secret_env":"NOT_SET_ANYWHERE_X"}`)
	if r.status != 409 || !strings.Contains(string(r.body), "NOT_SET_ANYWHERE_X") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

// allowLocalProvider lets the guarded client reach the test provider on this machine.
func allowLocalProvider(t *testing.T) {
	t.Helper()
	old := egress.Default.Policy()
	_ = egress.Default.Set(egress.Policy{AllowLocal: true, Allow: []string{"127.0.0.1"}})
	t.Cleanup(func() { _ = egress.Default.Set(old) })
}
