package transit

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// fakeTransit is a stand-in for the transit engine's encrypt and decrypt
// endpoints (tokens, base64 plaintext, "vault:v1:" ciphertext). The same
// tests run against a real OpenBao when LAUNCHWAY_TEST_VAULT_ADDR is set
// (see TestOpenBaoTransit and the test-vault target of the Makefile).
func fakeTransit(t *testing.T, token string) *httptest.Server {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	blk, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(blk)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var in map[string]string
		_ = json.Unmarshal(b, &in)
		switch r.URL.Path {
		case "/v1/transit/encrypt/launchway":
			pt, _ := base64.StdEncoding.DecodeString(in["plaintext"])
			n := make([]byte, 12)
			_, _ = rand.Read(n)
			ct := "vault:v1:" + base64.StdEncoding.EncodeToString(g.Seal(n, n, pt, nil))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"ciphertext": ct}})
		case "/v1/transit/decrypt/launchway":
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(in["ciphertext"], "vault:v1:"))
			if err != nil || len(raw) < 12 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":["invalid ciphertext"]}`))
				return
			}
			pt, err := g.Open(nil, raw[:12], raw[12:], nil)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":["cipher: message authentication failed"]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"plaintext": base64.StdEncoding.EncodeToString(pt)}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func holderFor(t *testing.T, addr, tokenEnv, key string) *Holder {
	t.Helper()
	h, err := Open(Config{StoreConfig: secrets.StoreConfig{Address: addr, TokenEnv: tokenEnv}, TransitKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTransitHolderWrapsAndBindsTheAuthenticatedData(t *testing.T) {
	t.Setenv("LW_TEST_TRANSIT_TOKEN", "tok")
	srv := fakeTransit(t, "tok")
	h := holderFor(t, srv.URL, "LW_TEST_TRANSIT_TOKEN", "launchway")
	exerciseHolder(t, h)
	if st := h.Status(context.Background()); !st.Reachable || st.Kind != "transit" {
		t.Fatalf("%+v", st)
	}
	// The wrong token is refused with a plain message.
	t.Setenv("LW_TEST_TRANSIT_BAD", "nope")
	bad := holderFor(t, srv.URL, "LW_TEST_TRANSIT_BAD", "launchway")
	if _, err := bad.Wrap(context.Background(), make([]byte, 32), []byte("a")); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("wrong token: %v", err)
	}
	if st := bad.Status(context.Background()); st.Reachable || st.Problem == "" {
		t.Fatalf("a refused token reads as reachable: %+v", st)
	}
	// An unknown transit key.
	nokey := holderFor(t, srv.URL, "LW_TEST_TRANSIT_TOKEN", "missing")
	if _, err := nokey.Wrap(context.Background(), make([]byte, 32), []byte("a")); err == nil || !strings.Contains(err.Error(), "no transit key") {
		t.Fatalf("missing key: %v", err)
	}
}

func exerciseHolder(t *testing.T, h *Holder) {
	t.Helper()
	ctx := context.Background()
	key := bytes.Repeat([]byte{7}, 32)
	w, err := h.Wrap(ctx, key, []byte("kek 1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(w, key) {
		t.Fatal("the wrapped key holds the key")
	}
	got, err := h.Unwrap(ctx, w, []byte("kek 1"))
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("%v", err)
	}
	if _, err := h.Unwrap(ctx, w, []byte("kek 2")); err == nil {
		t.Fatal("a key wrapped for one place unwrapped for another")
	}
}

func TestOpenValidatesSettings(t *testing.T) {
	if _, err := Open(Config{StoreConfig: secrets.StoreConfig{Address: "http://x", TokenEnv: "NOPE_UNSET"}, TransitKey: "k"}); err == nil {
		t.Error("an unset token variable was accepted")
	}
	t.Setenv("LW_TEST_TRANSIT_TOKEN", "tok")
	if _, err := Open(Config{StoreConfig: secrets.StoreConfig{Address: "http://x", TokenEnv: "LW_TEST_TRANSIT_TOKEN"}}); err == nil {
		t.Error("a missing key name was accepted")
	}
	if _, err := Open(Config{StoreConfig: secrets.StoreConfig{Address: "ftp://x", TokenEnv: "LW_TEST_TRANSIT_TOKEN"}, TransitKey: "k"}); err == nil {
		t.Error("a bad address was accepted")
	}
}

// TestOpenBaoTransit runs the holder against a real OpenBao or Vault in dev
// mode: it enables the transit engine and makes a key (see the test-vault
// target in the Makefile). Skipped unless LAUNCHWAY_TEST_VAULT_ADDR and
// LAUNCHWAY_TEST_VAULT_TOKEN are set.
func TestOpenBaoTransit(t *testing.T) {
	addr, root := os.Getenv("LAUNCHWAY_TEST_VAULT_ADDR"), os.Getenv("LAUNCHWAY_TEST_VAULT_TOKEN")
	if addr == "" || root == "" {
		t.Skip("set LAUNCHWAY_TEST_VAULT_ADDR and LAUNCHWAY_TEST_VAULT_TOKEN to run against Vault or OpenBao")
	}
	call := func(method, path string, body any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, addr+"/v1/"+path, bytes.NewReader(b))
		req.Header.Set("X-Vault-Token", root)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		// 400 "path is already in use" is fine when the test is run twice.
		if resp.StatusCode >= 300 && !strings.Contains(string(out), "already in use") {
			t.Fatalf("%s %s: %s %s", method, path, resp.Status, out)
		}
	}
	call("POST", "sys/mounts/transit-lw", map[string]string{"type": "transit"})
	call("POST", "transit-lw/keys/launchway-root", map[string]any{"type": "aes256-gcm96"})
	t.Setenv("LW_TEST_BAO_ROOT", root)
	h, err := Open(Config{StoreConfig: secrets.StoreConfig{Address: addr, TokenEnv: "LW_TEST_BAO_ROOT"}, TransitMount: "transit-lw", TransitKey: "launchway-root"})
	if err != nil {
		t.Fatal(err)
	}
	exerciseHolder(t, h)
	if st := h.Status(context.Background()); !st.Reachable {
		t.Fatalf("%+v", st)
	}
}
