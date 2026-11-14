package secrets

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenBao runs the vault store against a real server in dev mode. See
// the test-vault target in the Makefile.
func TestOpenBao(t *testing.T) {
	addr, root := os.Getenv("LAUNCHWAY_TEST_VAULT_ADDR"), os.Getenv("LAUNCHWAY_TEST_VAULT_TOKEN")
	if addr == "" || root == "" {
		t.Skip("set LAUNCHWAY_TEST_VAULT_ADDR and LAUNCHWAY_TEST_VAULT_TOKEN to run against Vault or OpenBao")
	}
	call := func(method, path string, body any) map[string]any {
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
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %s %s", method, path, resp.Status, out)
		}
		var m map[string]any
		json.Unmarshal(out, &m)
		return m
	}
	call("POST", "secret/data/launchway-test/robot", map[string]any{"data": map[string]any{"password": "bao-pass-42", "user": "robot"}})
	call("POST", "secret/data/launchway-test-other/db", map[string]any{"data": map[string]any{"password": "other"}})
	call("PUT", "sys/policies/acl/launchway-test", map[string]any{
		"policy": `path "secret/data/launchway-test/*" { capabilities = ["read"] }`,
	})
	limited := call("POST", "auth/token/create", map[string]any{"policies": []string{"launchway-test"}, "ttl": "10m"})
	limitedToken := limited["auth"].(map[string]any)["client_token"].(string)

	t.Setenv("LW_TEST_BAO_ROOT", root)
	v := openVault(t, StoreConfig{Address: addr, TokenEnv: "LW_TEST_BAO_ROOT"})
	if got, err := v.Get(ctx, "launchway-test/robot", "password"); err != nil || got != "bao-pass-42" {
		t.Fatalf("got %q, %v", got, err)
	}
	_, err := v.Get(ctx, "launchway-test/missing", "password")
	wantNotFound(t, err, "there is no secret")
	_, err = v.Get(ctx, "launchway-test/robot", "token")
	wantNotFound(t, err, `no field "token"`)

	file := filepath.Join(t.TempDir(), "token")
	must(t, os.WriteFile(file, []byte(limitedToken+"\n"), 0o600))
	lv := openVault(t, StoreConfig{Address: addr, TokenFile: file})
	if got, err := lv.Get(ctx, "launchway-test/robot", "user"); err != nil || got != "robot" {
		t.Fatalf("limited token: got %q, %v", got, err)
	}
	_, err = lv.Get(ctx, "launchway-test-other/db", "password")
	wantNotFound(t, err, "permission denied")

	// Nothing answers on this port: the store is unavailable, not missing.
	gone := openVault(t, StoreConfig{Address: "http://127.0.0.1:1", TokenEnv: "LW_TEST_BAO_ROOT"})
	if _, err := gone.Get(ctx, "launchway-test/robot", "password"); !IsUnavailable(err) {
		t.Fatalf("got %v, want unavailable", err)
	}
}
