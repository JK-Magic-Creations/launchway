package secrets

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeVault answers the few Vault API calls the store makes, and checks
// that they are made correctly.
type fakeVault struct {
	t         *testing.T
	mount     string
	authPath  string
	namespace string // required on every request when set
	role, jwt string
	lease     int // seconds, for logins

	mu      sync.Mutex
	valid   map[string]bool // tokens that may read
	logins  int
	reads   int
	sealed  bool   // answer 503 to everything
	denyAll bool   // answer 403 to every read
	loginAs string // status for logins, e.g. "400" or "500"; empty: succeed
	data    map[string]map[string]any
}

func newFakeVault(t *testing.T) *fakeVault {
	return &fakeVault{t: t, mount: "secret", authPath: "kubernetes", role: "ci", jwt: "service-account-jwt", lease: 3600,
		valid: map[string]bool{},
		data: map[string]map[string]any{
			"apps/harbor/robot": {"password": "robot-pass-1", "port": 5000},
			"a b/c":             {"v": "spaced"},
		}}
}

func (f *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fail := func(code int, msg string) {
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"errors":[%q]}`, msg)
	}
	if got := r.Header.Get("X-Vault-Namespace"); got != f.namespace {
		f.t.Errorf("%s %s: namespace header %q, want %q", r.Method, r.URL.Path, got, f.namespace)
	}
	if f.sealed {
		fail(503, "Vault is sealed")
		return
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/auth/"+f.authPath+"/login":
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["role"] != f.role || body["jwt"] != f.jwt {
			f.t.Errorf("login body %v, %v", body, err)
		}
		if r.Header.Get("X-Vault-Token") != "" {
			f.t.Error("login sent a token")
		}
		switch f.loginAs {
		case "400":
			fail(400, "invalid role name \"ci\"")
			return
		case "500":
			fail(500, "internal error")
			return
		}
		f.logins++
		tok := fmt.Sprintf("login-%d", f.logins)
		f.valid[tok] = true
		fmt.Fprintf(w, `{"auth":{"client_token":%q,"lease_duration":%d}}`, tok, f.lease)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/"+f.mount+"/data/"):
		f.reads++
		if !f.valid[r.Header.Get("X-Vault-Token")] || f.denyAll {
			fail(403, "permission denied")
			return
		}
		d, ok := f.data[strings.TrimPrefix(r.URL.Path, "/v1/"+f.mount+"/data/")]
		if !ok {
			w.WriteHeader(404)
			fmt.Fprint(w, `{"errors":[]}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": d, "metadata": map[string]any{"version": 1}}})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		fail(400, "unsupported path")
	}
}

func (f *fakeVault) counts() (logins, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logins, f.reads
}

func (f *fakeVault) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func openVault(t *testing.T, c StoreConfig) *Vault {
	t.Helper()
	c.Type = "vault"
	st, err := newStore("vault", c)
	if err != nil {
		t.Fatal(err)
	}
	return st.(*Vault)
}

func TestVaultTokenFromEnv(t *testing.T) {
	f := newFakeVault(t)
	f.namespace = "team-a"
	f.valid["root-token"] = true
	srv := httptest.NewServer(f)
	defer srv.Close()
	t.Setenv("LW_TEST_VAULT_TOKEN", "root-token")
	v := openVault(t, StoreConfig{Address: srv.URL + "/", TokenEnv: "LW_TEST_VAULT_TOKEN", Namespace: "team-a"})

	if got, err := v.Get(ctx, "apps/harbor/robot", "password"); err != nil || got != "robot-pass-1" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := v.Get(ctx, "apps/harbor/robot", "port"); err != nil || got != "5000" {
		t.Fatalf("a number field: got %q, %v", got, err)
	}
	if got, err := v.Get(ctx, "a b/c", "v"); err != nil || got != "spaced" {
		t.Fatalf("a key with a space: got %q, %v", got, err)
	}
	_, err := v.Get(ctx, "apps/missing", "password")
	wantNotFound(t, err, "there is no secret at secret/apps/missing")
	_, err = v.Get(ctx, "apps/harbor/robot", "username")
	wantNotFound(t, err, `has no field "username"`)
	_, err = v.Get(ctx, "apps/harbor/robot", "")
	wantNotFound(t, err, "need a field")
	_, err = v.Get(ctx, "apps/../sys/x", "f")
	wantNotFound(t, err, "without empty, . or .. parts")
	if logins, _ := f.counts(); logins != 0 {
		t.Fatalf("token auth logged in %d times", logins)
	}

	// Revoked: one more try with the token read afresh, then a refusal.
	f.set(func() { delete(f.valid, "root-token") })
	_, before := f.counts()
	_, err = v.Get(ctx, "apps/harbor/robot", "password")
	wantNotFound(t, err, "permission denied reading secret/apps/harbor/robot")
	if _, after := f.counts(); after-before != 2 {
		t.Fatalf("%d reads after a 403, want 2", after-before)
	}
}

func TestVaultTokenFileIsReadAgainAfter403(t *testing.T) {
	f := newFakeVault(t)
	f.mount = "kv/ci"
	f.data = map[string]map[string]any{"db": {"password": "pw-1234"}}
	f.valid["old"] = true
	srv := httptest.NewServer(f)
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "token")
	must(t, os.WriteFile(file, []byte("old\n"), 0o600))
	v := openVault(t, StoreConfig{Address: srv.URL, TokenFile: file, Mount: "kv/ci"})

	if got, err := v.Get(ctx, "db", "password"); err != nil || got != "pw-1234" {
		t.Fatalf("got %q, %v", got, err)
	}
	// The token is rotated: the old one stops working, the file has the new one.
	f.set(func() { delete(f.valid, "old"); f.valid["new"] = true })
	must(t, os.WriteFile(file, []byte("new\n"), 0o600))
	if got, err := v.Get(ctx, "db", "password"); err != nil || got != "pw-1234" {
		t.Fatalf("after rotation: got %q, %v", got, err)
	}
}

func TestVaultKubernetesAuth(t *testing.T) {
	f := newFakeVault(t)
	f.authPath = "k8s/prod"
	f.lease = 100
	srv := httptest.NewServer(f)
	defer srv.Close()
	jwt := filepath.Join(t.TempDir(), "jwt")
	must(t, os.WriteFile(jwt, []byte("service-account-jwt\n"), 0o600))
	v := openVault(t, StoreConfig{Address: srv.URL, Role: "ci", AuthPath: "k8s/prod", JWTFile: jwt})
	now := time.Unix(1_800_000_000, 0)
	v.now = func() time.Time { return now }

	get := func() {
		t.Helper()
		if got, err := v.Get(ctx, "apps/harbor/robot", "password"); err != nil || got != "robot-pass-1" {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	get()
	get()
	if logins, reads := f.counts(); logins != 1 || reads != 2 {
		t.Fatalf("logins %d, reads %d: the login token was not reused", logins, reads)
	}
	// Shortly before the lease ends, it logs in again.
	now = now.Add(89 * time.Second)
	get()
	if logins, _ := f.counts(); logins != 1 {
		t.Fatalf("logged in again too early (%d)", logins)
	}
	now = now.Add(2 * time.Second)
	get()
	if logins, _ := f.counts(); logins != 2 {
		t.Fatalf("did not log in again before the token expired (%d)", logins)
	}
	// A token revoked early: a 403, a new login, and the read succeeds.
	f.set(func() { f.valid = map[string]bool{} })
	get()
	if logins, _ := f.counts(); logins != 3 {
		t.Fatalf("no new login after a 403 (%d)", logins)
	}
	// A policy that forbids the read: one new login, then a refusal.
	f.set(func() { f.denyAll = true })
	_, err := v.Get(ctx, "apps/harbor/robot", "password")
	wantNotFound(t, err, "permission denied")
	if logins, _ := f.counts(); logins != 4 {
		t.Fatalf("logins %d after a forbidden read, want exactly one more", logins)
	}
	// A login vault refuses fails the step; a failing vault does not.
	f.set(func() { f.denyAll = false; f.valid = map[string]bool{}; f.loginAs = "400" })
	_, err = v.Get(ctx, "apps/harbor/robot", "password")
	wantNotFound(t, err, `invalid role name`)
	f.set(func() { f.loginAs = "500" })
	_, err = v.Get(ctx, "apps/harbor/robot", "password")
	if !IsUnavailable(err) {
		t.Fatalf("a 500 from login: got %T %v, want unavailable", err, err)
	}
}

func TestVaultUnavailable(t *testing.T) {
	f := newFakeVault(t)
	f.valid["root-token"] = true
	srv := httptest.NewServer(f)
	t.Setenv("LW_TEST_VAULT_TOKEN", "root-token")
	v := openVault(t, StoreConfig{Address: srv.URL, TokenEnv: "LW_TEST_VAULT_TOKEN"})

	f.set(func() { f.sealed = true })
	_, err := v.Get(ctx, "apps/harbor/robot", "password")
	if !IsUnavailable(err) || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("sealed: got %T %v", err, err)
	}
	f.set(func() { f.sealed = false })
	if _, err := v.Get(ctx, "apps/harbor/robot", "password"); err != nil {
		t.Fatalf("unsealed again: %v", err)
	}
	srv.Close()
	_, err = v.Get(ctx, "apps/harbor/robot", "password")
	if !IsUnavailable(err) || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("no server: got %T %v", err, err)
	}
}

func TestVaultCAFile(t *testing.T) {
	f := newFakeVault(t)
	f.valid["root-token"] = true
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	t.Setenv("LW_TEST_VAULT_TOKEN", "root-token")
	ca := filepath.Join(t.TempDir(), "ca.pem")
	must(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600))

	// Without the CA the certificate is not trusted, which is a store problem.
	untrusted := openVault(t, StoreConfig{Address: srv.URL, TokenEnv: "LW_TEST_VAULT_TOKEN"})
	if _, err := untrusted.Get(ctx, "apps/harbor/robot", "password"); !IsUnavailable(err) {
		t.Fatalf("untrusted certificate: got %v", err)
	}
	v := openVault(t, StoreConfig{Address: srv.URL, TokenEnv: "LW_TEST_VAULT_TOKEN", CAFile: ca})
	if got, err := v.Get(ctx, "apps/harbor/robot", "password"); err != nil || got != "robot-pass-1" {
		t.Fatalf("got %q, %v", got, err)
	}
}
