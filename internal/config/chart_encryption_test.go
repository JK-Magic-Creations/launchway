package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// helmOut renders the server chart with values and returns the manifest text,
// or the error text when it refuses.
func helmOut(t *testing.T, values string) (out string, failed bool) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	vf := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(vf, []byte("postgresql: { enabled: true }\n"+values), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", vf).CombinedOutput()
	return string(b), err != nil
}

// By default the chart makes a root key Secret that survives helm uninstall,
// mounts it on the servers with a mode they can read as a non-root user, and
// makes the servers refuse to start without it. The key's value is never in
// the ConfigMap, and the configuration file gains no key (the release before
// can still read it).
func TestChartEncryptsAtRestByDefault(t *testing.T) {
	out, failed := helmOut(t, "")
	if failed {
		t.Fatal(out)
	}
	for _, want := range []string{"name: t-launchway-root-key", "helm.sh/resource-policy: keep", "root.key:",
		"name: LAUNCHWAY_ENCRYPTION_REQUIRE", `value: "true"`, "name: LAUNCHWAY_ROOT_KEY_FILE", "value: /etc/launchway-keys/root.key",
		"mountPath: /etc/launchway-keys", "defaultMode: 0440"} {
		if !strings.Contains(out, want) {
			t.Errorf("the default render lacks %q", want)
		}
	}
	f := renderChart(t, "postgresql: { enabled: true }\n")["server"]
	if strings.Contains(f, "encryption") {
		t.Errorf("the configuration file mentions encryption, which a server of the release before refuses:\n%s", f)
	}
}

func TestChartHoldersAndRefusals(t *testing.T) {
	cases := []struct {
		name, values string
		want         []string
		fail         string
	}{
		{"an existing key Secret", "encryption: { keyFile: { existingSecret: my-key, existingSecretKey: k } }", []string{"name: my-key", "key: k"}, ""},
		{"no generation and no Secret", "encryption: { keyFile: { generate: false } }", nil, "encryption.keyFile.existingSecret"},
		{"env", "encryption: { holder: env, env: { existingSecret: ek } }", []string{"name: LAUNCHWAY_ROOT_KEY\n", "name: ek"}, ""},
		{"env without a Secret", "encryption: { holder: env }", nil, "encryption.env.existingSecret"},
		{"passphrase", "encryption: { holder: passphrase, passphrase: { keystoreSecret: ks, passphraseSecret: pp } }", []string{"LAUNCHWAY_KEYSTORE_FILE", "LAUNCHWAY_KEYSTORE_PASSPHRASE_FILE", "path: root.keystore", "path: passphrase"}, ""},
		{"transit with a token", "encryption: { holder: transit, transit: { address: 'https://bao.example:8200', key: lw, tokenSecret: bao-token, caSecret: bao-ca } }",
			[]string{"LAUNCHWAY_ENCRYPTION_HOLDER", "value: transit", "LAUNCHWAY_TRANSIT_ADDRESS", "LAUNCHWAY_TRANSIT_KEY", "LAUNCHWAY_TRANSIT_TOKEN_ENV", "name: bao-token", "path: ca.crt"}, ""},
		{"transit with Kubernetes auth", "encryption: { holder: transit, transit: { address: 'https://bao.example:8200', key: lw, role: launchway } }", []string{"LAUNCHWAY_TRANSIT_ROLE", "LAUNCHWAY_TRANSIT_AUTH_PATH"}, ""},
		{"transit without a login", "encryption: { holder: transit, transit: { address: 'https://bao.example:8200', key: lw } }", nil, "tokenSecret"},
		{"a root key replacement", "encryption: { previousKey: { existingSecret: old-key } }", []string{"LAUNCHWAY_ROOT_KEY_PREVIOUS_FILE", "path: previous.key", "name: old-key"}, ""},
		{"an unknown holder", "encryption: { holder: tpm }", nil, "must be one of"},
		{"none", "encryption: { holder: none }", []string{"name: LAUNCHWAY_ENCRYPTION_HOLDER"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, failed := helmOut(t, c.values)
			if c.fail != "" {
				if !failed || !strings.Contains(out, c.fail) {
					t.Fatalf("want a refusal mentioning %q, got failed=%v:\n%.600s", c.fail, failed, out)
				}
				return
			}
			if failed {
				t.Fatal(out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("lacks %q", w)
				}
			}
			if c.name == "none" && (strings.Contains(out, "LAUNCHWAY_ENCRYPTION_REQUIRE") || strings.Contains(out, "root-key")) {
				t.Error("holder none still requires a key or makes one")
			}
		})
	}
}
