package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/cac/tfstate"
)

const stateCanary = "CANARY-tfstate-9d41c2e07b5a"

func stateDoc(serial int, lineage, secret string) string {
	return fmt.Sprintf(`{"version":4,"terraform_version":"1.10.10","serial":%d,"lineage":%q,"outputs":{"pw":{"value":%q,"type":"string","sensitive":true},"name":{"value":"web","type":"string"}},
	"resources":[{"mode":"managed","type":"random_password","name":"db","provider":"provider[\"registry.opentofu.org/hashicorp/random\"]","instances":[{"schema_version":3,"attributes":{"result":%q}}]}]}`, serial, lineage, secret, secret)
}

func basic(pass string) []string {
	return []string{"Authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte("launchway:"+pass))}
}

// The built-in state backend end to end: the protocol, who may use it, the
// versions and rollback, the lock, and the canary that state is sealed at rest.
func TestStateBackendProtocolVersionsAndSealing(t *testing.T) {
	e := setupAuth(t, envOpts{})
	e.api.Secrets = testSecrets(t, e.eng)
	ada, vic := e.signIn(t, "ada"), e.signIn(t, "vic")
	_, adaTok := ada.newToken(e.srv.URL, "tofu")
	_, vicTok := vic.newToken(e.srv.URL, "viewer")
	anon := newBrowser(t)
	addr := e.srv.URL + "/api/state/v1/pay-api/prod"
	lock := addr + "/lock"
	call := func(method, url, body, tok string) response {
		t.Helper()
		var h []string
		if tok != "" {
			h = basic(tok)
		}
		return anon.do(method, url, body, h...)
	}

	if r := call("GET", addr, "", ""); r.status != 401 {
		t.Fatalf("no credentials: %d", r.status)
	}
	if r := call("GET", addr, "", vicTok); r.status != 404 {
		t.Fatalf("a viewer may read; there is no state yet: %d %s", r.status, r.body)
	}
	if r := call("POST", addr, stateDoc(1, "lin-1", stateCanary), vicTok); r.status != 403 {
		t.Fatalf("a viewer may not write: %d", r.status)
	}
	if r := call("GET", e.srv.URL+"/api/state/v1/web/prod", "", vicTok); r.status != 404 && r.status != 403 {
		t.Fatalf("a project the viewer cannot see: %d", r.status)
	}

	// Lock, write under the lock, unlock.
	info := `{"ID":"lock-a","Operation":"OperationTypeApply","Who":"ada@laptop","Version":"1.10.10","Created":"2026-11-21T00:00:00Z","Path":""}`
	if r := call("LOCK", lock, info, adaTok); r.status != 200 {
		t.Fatalf("lock: %d %s", r.status, r.body)
	}
	// A second client is refused with the holder's document: lock contention.
	r := call("LOCK", lock, `{"ID":"lock-b","Operation":"OperationTypePlan","Who":"someone-else"}`, adaTok)
	if r.status != 423 || !strings.Contains(string(r.body), "lock-a") {
		t.Fatalf("contention: %d %s", r.status, r.body)
	}
	if r := call("POST", addr+"?ID=lock-b", stateDoc(1, "lin-1", stateCanary), adaTok); r.status != 423 {
		t.Fatalf("a write under another's lock: %d", r.status)
	}
	if r := call("POST", addr+"?ID=lock-a", stateDoc(1, "lin-1", stateCanary), adaTok); r.status != 200 {
		t.Fatalf("write: %d %s", r.status, r.body)
	}
	if r := call("UNLOCK", lock, `{"ID":"lock-b"}`, adaTok); r.status != 423 {
		t.Fatalf("unlock with the wrong id: %d", r.status)
	}
	if r := call("UNLOCK", lock, info, adaTok); r.status != 200 {
		t.Fatalf("unlock: %d", r.status)
	}
	got := call("GET", addr, "", vicTok)
	if got.status != 200 || !strings.Contains(string(got.body), stateCanary) {
		t.Fatalf("read back: %d", got.status)
	}

	// Serial rules and lineage.
	if r := call("POST", addr, stateDoc(0, "lin-1", "x"), adaTok); r.status != 409 {
		t.Fatalf("an older serial: %d", r.status)
	}
	if r := call("POST", addr, stateDoc(2, "other", "x"), adaTok); r.status != 409 {
		t.Fatalf("another lineage: %d", r.status)
	}
	if r := call("POST", addr, stateDoc(1, "lin-1", "different"), adaTok); r.status != 409 {
		t.Fatalf("same serial, other content: %d", r.status)
	}
	if r := call("POST", addr, `not json`, adaTok); r.status != 400 {
		t.Fatalf("garbage: %d", r.status)
	}
	if r := call("POST", addr, stateDoc(2, "lin-1", "second-secret-value"), adaTok); r.status != 200 {
		t.Fatalf("second write: %d", r.status)
	}

	// Versions: who, when, serial; the interface never shows a value.
	api := e.srv.URL + "/api/v1/projects/pay-api/state/prod"
	var vs struct {
		Versions []tfstate.Version `json:"versions"`
	}
	vr := vic.do("GET", api+"/versions", "")
	vr.json(t, &vs)
	if vr.status != 200 || len(vs.Versions) != 2 || vs.Versions[0].Version != 2 || vs.Versions[0].Actor != "ada" {
		t.Fatalf("versions: %d %s", vr.status, vr.body)
	}
	show := vic.do("GET", api, "")
	if show.status != 200 || strings.Contains(string(show.body), stateCanary) || strings.Contains(string(show.body), "second-secret-value") ||
		!strings.Contains(string(show.body), "random_password.db") || !strings.Contains(string(show.body), `"sensitive":true`) {
		t.Fatalf("show: %d %s", show.status, show.body)
	}
	if r := vic.do("GET", api+"/pull?reason=x", ""); r.status != 403 {
		t.Fatalf("a viewer pulling raw state: %d", r.status)
	}
	if r := ada.do("GET", api+"/pull", ""); r.status != 400 {
		t.Fatalf("pull without a reason: %d", r.status)
	}
	pull := ada.do("GET", api+"/pull?reason=debugging+the+import&version=1", "")
	if pull.status != 200 || !strings.Contains(string(pull.body), stateCanary) {
		t.Fatalf("pull: %d", pull.status)
	}

	// Rollback: a new version with the old content and a higher serial.
	if r := vic.do("POST", api+"/rollback", `{"version":1,"reason":"x"}`); r.status != 403 {
		t.Fatalf("a viewer rolling back: %d", r.status)
	}
	if r := ada.do("POST", api+"/rollback", `{"version":1}`); r.status != 400 {
		t.Fatalf("rollback without a reason: %d", r.status)
	}
	rb := ada.do("POST", api+"/rollback", `{"version":1,"reason":"the second apply was wrong"}`)
	if rb.status != 200 {
		t.Fatalf("rollback: %d %s", rb.status, rb.body)
	}
	cur := call("GET", addr, "", adaTok)
	if strings.Contains(string(cur.body), "second-secret-value") || !strings.Contains(string(cur.body), stateCanary) || !strings.Contains(string(cur.body), `"serial":3`) {
		t.Fatalf("current after rollback: %s", cur.body)
	}
	vr = ada.do("GET", api+"/versions", "")
	vr.json(t, &vs)
	if len(vs.Versions) != 3 || vs.Versions[0].Source != "rollback" || vs.Versions[0].RestoredFrom != 1 {
		t.Fatalf("versions after rollback: %s", vr.body)
	}

	// Manual lock, then force unlock only with a reason.
	if r := ada.do("POST", api+"/lock", `{"reason":"maintenance"}`); r.status != 201 {
		t.Fatalf("manual lock: %d %s", r.status, r.body)
	}
	if r := call("POST", addr, stateDoc(4, "lin-1", "x"), adaTok); r.status != 423 {
		t.Fatalf("write while locked by hand: %d", r.status)
	}
	if r := ada.do("DELETE", api+"/lock", ""); r.status != 400 {
		t.Fatalf("force unlock without a reason: %d", r.status)
	}
	if r := ada.do("DELETE", api+"/lock?reason=done", ""); r.status != 200 {
		t.Fatalf("force unlock: %d %s", r.status, r.body)
	}

	// Audit: pull, rollback, lock and unlock are in the trail with who and why.
	trail := ""
	for _, a := range e.auditEntries(t) {
		trail += a.Action + " " + a.User + " " + a.Detail + "\n"
	}
	for _, want := range []string{"state.pull ada", "debugging the import", "state.rollback ada", "the second apply was wrong", "state.lock ada", "state.unlock ada", "state.denied"} {
		if !strings.Contains(trail, want) {
			t.Errorf("the audit trail lacks %q:\n%s", want, trail)
		}
	}
	if strings.Contains(trail, stateCanary) || strings.Contains(trail, adaTok) {
		t.Error("the audit trail holds a secret")
	}

	// The canary: the state's secret is nowhere in the database, in any encoding.
	dump := []byte(dumpDatabase(t, e.db))
	if len(dump) < 1000 {
		t.Fatalf("the dump is %d bytes; it was not read", len(dump))
	}
	for _, secret := range []string{stateCanary, "second-secret-value"} {
		for name, enc := range map[string][]byte{"raw": []byte(secret), "base64": []byte(base64.StdEncoding.EncodeToString([]byte(secret))), "base64url": []byte(base64.RawURLEncoding.EncodeToString([]byte(secret)))} {
			if bytes.Contains(dump, enc) {
				t.Errorf("%q appears in the database dump (%s)", secret, name)
			}
		}
	}
}

// With no root key, state is refused, not stored in the clear.
func TestStateBackendRefusesWithoutARootKey(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	_, tok := ada.newToken(e.srv.URL, "tofu")
	r := newBrowser(t).do("POST", e.srv.URL+"/api/state/v1/pay-api/prod", stateDoc(1, "l", stateCanary), basic(tok)...)
	if r.status != 503 || !strings.Contains(string(r.body), "root key") {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if strings.Contains(dumpDatabase(t, e.db), stateCanary) {
		t.Fatal("state was stored in the clear")
	}
}

// A run's credential works for its own project and workspace only, is
// read-only for pull request runs, and dies with the run.
func TestStateCredentialIsRunScoped(t *testing.T) {
	e := setupAuth(t, envOpts{})
	e.api.Secrets = testSecrets(t, e.eng)
	ada := e.signIn(t, "ada")
	r := ada.do("POST", e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`)
	if r.status != 201 {
		t.Fatalf("starting a run: %d %s", r.status, r.body)
	}
	var run struct {
		ID string `json:"id"`
	}
	r.json(t, &run)
	tk, err := e.api.stateTokens(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tok := tk.Mint(tfstate.Claims{Project: "pay-api", Workspace: "prod", RunID: run.ID, Step: "apply"}, time.Hour)
	anon := newBrowser(t)
	addr := e.srv.URL + "/api/state/v1/pay-api/"
	if r := anon.do("POST", addr+"prod", stateDoc(1, "l", "x"), basic(tok)...); r.status != 200 {
		t.Fatalf("the run's own workspace: %d %s", r.status, r.body)
	}
	if r := anon.do("POST", addr+"staging", stateDoc(1, "l", "x"), basic(tok)...); r.status != 403 {
		t.Fatalf("another workspace: %d", r.status)
	}
	if r := anon.do("GET", e.srv.URL+"/api/state/v1/web/prod", "", basic(tok)...); r.status != 403 {
		t.Fatalf("another project: %d", r.status)
	}
	ro := tk.Mint(tfstate.Claims{Project: "pay-api", Workspace: "prod", RunID: run.ID, Step: "plan", ReadOnly: true}, time.Hour)
	if r := anon.do("GET", addr+"prod", "", basic(ro)...); r.status != 200 {
		t.Fatalf("read-only read: %d", r.status)
	}
	if r := anon.do("POST", addr+"prod", stateDoc(2, "l", "x"), basic(ro)...); r.status != 403 {
		t.Fatalf("read-only write: %d", r.status)
	}
	expired := tfstate.Tokens{Key: tk.Key, Now: func() time.Time { return tk.Now().Add(-5 * time.Hour) }}.Mint(tfstate.Claims{Project: "pay-api", Workspace: "prod", RunID: run.ID}, time.Hour)
	if r := anon.do("GET", addr+"prod", "", basic(expired)...); r.status != 401 {
		t.Fatalf("expired: %d", r.status)
	}
	if r := anon.do("GET", addr+"prod", "", basic(tok[:len(tok)-3]+"abc")...); r.status != 401 {
		t.Fatalf("forged: %d", r.status)
	}
	// The run ends: the credential is good for nothing.
	if r := ada.do("POST", e.srv.URL+"/api/v1/runs/"+run.ID+"/cancel", ""); r.status != 200 && r.status != 202 {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	if r := anon.do("GET", addr+"prod", "", basic(tok)...); r.status != 403 {
		t.Fatalf("after the run ended: %d", r.status)
	}
}

// Two clients racing for one lock: exactly one wins.
func TestStateLockContention(t *testing.T) {
	e := setupAuth(t, envOpts{})
	e.api.Secrets = testSecrets(t, e.eng)
	ada := e.signIn(t, "ada")
	_, tok := ada.newToken(e.srv.URL, "tofu")
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := newBrowser(t).do("LOCK", e.srv.URL+"/api/state/v1/pay-api/race/lock", fmt.Sprintf(`{"ID":"l%d"}`, i), basic(tok)...)
			codes <- r.status
		}()
	}
	wg.Wait()
	close(codes)
	won := 0
	for c := range codes {
		switch c {
		case 200:
			won++
		case 423:
		default:
			t.Errorf("status %d", c)
		}
	}
	if won != 1 {
		t.Fatalf("%d clients got the lock", won)
	}
	_ = json.Valid
}
