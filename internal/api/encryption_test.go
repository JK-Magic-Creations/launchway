package api

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/sealing"
)

// Only an administrator reads the encryption status, and what it says holds
// no key material.
func TestEncryptionStatusIsForAdministratorsAndHoldsNoKey(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	if r := vic.do("GET", e.srv.URL+"/api/v1/encryption", ""); r.status != 403 && r.status != 404 {
		t.Fatalf("a viewer: %d %s", r.status, r.body)
	}
	r := ada.do("GET", e.srv.URL+"/api/v1/encryption", "")
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var rep sealing.Report
	r.json(t, &rep)
	if !rep.Encrypted || !rep.DumpAloneIsUseless || rep.Holder == nil || rep.Holder.Kind != "key_file" || len(rep.NotYetCovered) == 0 {
		t.Fatalf("%+v", rep)
	}
	for _, leak := range []string{"wrapped", "lwe1."} {
		if strings.Contains(string(r.body), leak) {
			t.Errorf("the status holds %q", leak)
		}
	}
}

// With no root key the status says so plainly, and a webhook (which has a
// secret) cannot be made: 409, a code, and exactly how to create a key.
func TestNoRootKeyStatusAndWebhooksRefuse(t *testing.T) {
	e := setupAuth(t, envOpts{policy: hostedPolicy(t)})
	noKey, err := sealing.Start(t.Context(), sealing.Options{DB: e.eng.Database()})
	if err != nil {
		t.Fatal(err)
	}
	e.api.Secrets = noKey
	blobs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := hosting.New(t.Context(), hosting.Options{Engine: e.eng, Blobs: blobs, Data: t.TempDir(), Server: "t", BaseURL: e.srv.URL, Log: quiet(), Secrets: noKey})
	if err != nil {
		t.Fatal(err)
	}
	e.api.Forge = &ForgeHosting{Provider: h.Provider, Git: githttp.New(githttp.Options{Backend: h.Store, Log: quiet()}), RunTokens: h.RunTokens,
		Mirrors: h.Mirrors, Keys: h.Keys, Rules: h.Rules, Hooks: h.Hooks}
	ada := e.signIn(t, "ada")
	var rep sealing.Report
	r := ada.do("GET", e.srv.URL+"/api/v1/encryption", "")
	r.json(t, &rep)
	if r.status != 200 || rep.Encrypted || rep.DumpAloneIsUseless || !strings.HasPrefix(rep.Summary, "NOT ENCRYPTED AT REST") {
		t.Fatalf("%d %+v", r.status, rep)
	}
	if r := ada.do("POST", e.srv.URL+"/api/v1/repositories", `{"owner":"acme","name":"shop","visibility":"private"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = ada.do("POST", e.srv.URL+"/api/v1/repositories/acme/shop/hooks", `{"url":"https://example.test/h","events":["push"]}`)
	if r.status != 409 || !strings.Contains(string(r.body), `"code":"no_root_key"`) || !strings.Contains(string(r.body), "launchway encryption init") {
		t.Fatalf("a webhook with no root key: %d %s", r.status, r.body)
	}
}

// Rotating keys is online: while it runs (a new key-encryption key, new data
// keys, every stored value re-sealed), /readyz keeps answering ready and the
// shared values keep opening.
func TestReadyzIsNotBlockedByRotation(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	if _, err := e.api.sharedSecret(t.Context(), "login_state_key"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 5; i++ {
			if _, err := e.api.Secrets.Rotate(t.Context(), i%2 == 1); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for rotating := true; rotating; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			rotating = false
		default:
		}
		if r := newBrowser(t).do("GET", e.srv.URL+"/readyz", ""); r.status != 200 {
			t.Fatalf("/readyz during rotation: %d %s", r.status, r.body)
		}
		if _, err := e.api.sharedSecret(t.Context(), "login_state_key"); err != nil {
			t.Fatalf("a shared value during rotation: %v", err)
		}
	}
}
