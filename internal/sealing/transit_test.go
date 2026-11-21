package sealing

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/keyring/transit"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The whole path with the root key in OpenBao or Vault: a legacy database is
// sealed in place through the transit engine, a restart opens it, a database
// started with a different transit key refuses, and the service never has the
// root key (only wrapped keys are in the database). It runs against a real
// server when LAUNCHWAY_TEST_VAULT_ADDR and LAUNCHWAY_TEST_VAULT_TOKEN are set
// (make test-vault); otherwise it is skipped, and the holder is covered by the
// fake transit server of the transit package's tests.
func TestOpenBaoTransitHoldsTheRootKey(t *testing.T) {
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
		if resp.StatusCode >= 300 && !strings.Contains(string(out), "already in use") {
			t.Fatalf("%s %s: %s %s", method, path, resp.Status, out)
		}
	}
	call("POST", "sys/mounts/transit-lw", map[string]string{"type": "transit"})
	call("POST", "transit-lw/keys/launchway-a", map[string]any{"type": "aes256-gcm96"})
	call("POST", "transit-lw/keys/launchway-b", map[string]any{"type": "aes256-gcm96"})
	t.Setenv("LW_TEST_BAO_ROOT", root)
	holder := func(key string) keyring.Holder {
		h, err := transit.Open(transit.Config{StoreConfig: secrets.StoreConfig{Address: addr, TokenEnv: "LW_TEST_BAO_ROOT"}, TransitMount: "transit-lw", TransitKey: key})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	e := openEngine(t, testdb.New(t))
	legacySeed(t, e)
	s := start(t, e, holder("launchway-a"))
	r, err := s.Report(ctx)
	if err != nil || r.Remaining != 0 || !r.DumpAloneIsUseless || r.Holder.Kind != "transit" || !r.Holder.Reachable {
		t.Fatalf("%+v %v", r, err)
	}
	if v, err := start(t, e, holder("launchway-a")).SharedValue("runner_token", "x"); err != nil || v != "legacy-runner-token-canary" {
		t.Fatalf("after a restart: %q %v", v, err)
	}
	if _, err := Start(ctx, Options{DB: e.Database(), Holder: holder("launchway-b"), Log: quiet}); err == nil || !strings.Contains(err.Error(), "never re-keys") {
		t.Fatalf("a different transit key: %v", err)
	}
	// Moving to another key of the service: the old one beside the new, then rewrap.
	s2, err := Start(ctx, Options{DB: e.Database(), Holder: holder("launchway-b"), Previous: []keyring.Holder{holder("launchway-a")}, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s2.Rewrap(ctx); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if v, err := start(t, e, holder("launchway-b")).SharedValue("runner_token", "x"); err != nil || v != "legacy-runner-token-canary" {
		t.Fatalf("%q %v", v, err)
	}
}
