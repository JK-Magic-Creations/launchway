package config

import (
	"crypto/sha256"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The simple install (design ruling 27): the chart starts a secure, working installation from the
// address alone, and what it renders holds no secret material and is the same every time.

func helmDefault(t *testing.T, extra ...string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	args := append([]string{"template", "t", chartDir, "--set", "externalURL=https://launchway.example.com"}, extra...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestChartDefaultRenderIsDeterministicAndHoldsNoSecrets(t *testing.T) {
	a, b := helmDefault(t), helmDefault(t)
	if sha256.Sum256([]byte(a)) != sha256.Sum256([]byte(b)) {
		t.Fatal("two renders of the default chart differ")
	}
	dec := yaml.NewDecoder(strings.NewReader(a))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d["kind"] == "Secret" {
			t.Errorf("the default render has a Secret (%v): secrets are made by the hook Job", dig(d, "metadata", "name"))
		}
	}
	for _, bad := range []string{"randAlphaNum", "lookup"} {
		if out, _ := exec.Command("grep", "-rl", bad, "../../deploy/helm/launchway/templates").Output(); len(out) > 0 {
			t.Errorf("%s is used in %s", bad, out)
		}
	}
}

func TestChartDefaultsAreSecure(t *testing.T) {
	out := helmDefault(t)
	for _, want := range []string{
		"mode: local",                             // sign-in required, built-in accounts
		"kind: NetworkPolicy",                     // default deny
		"LAUNCHWAY_REQUIRE",                       // encryption on (see below)
		"LAUNCHWAY_ROOT_KEY_FILE",                 // the root key is a mounted file
		"LAUNCHWAY_SETUP_CODE_FILE",               // the first administrator's one-time code
		`"helm.sh/hook": pre-install,pre-upgrade`, // the secrets Job
		"helm.sh/hook-delete-policy\": before-hook-creation,hook-succeeded",
		"ephemeral-storage",
		"max_total_gib: 4", // artifact cap below the 8Gi volume
		"storage: 8Gi",
	} {
		if want == "LAUNCHWAY_REQUIRE" {
			want = "LAUNCHWAY_ENCRYPTION_REQUIRE"
		}
		if !strings.Contains(out, want) {
			t.Errorf("the default render lacks %q", want)
		}
	}
	if strings.Contains(out, "kind: Ingress") || strings.Contains(out, "HTTPRoute") {
		t.Error("the default chart publishes the address; it must not")
	}
}

// Step pods may reach neither the database, the Kubernetes API nor the cloud metadata service.
func TestChartStepPodPolicy(t *testing.T) {
	for _, d := range chartDocs(t, helmDefault(t)) {
		if d["kind"] != "NetworkPolicy" || !strings.HasSuffix(name(d), "-steps") {
			continue
		}
		spec := dig(d, "spec").(map[string]any)
		blocks := 0
		for _, rule := range spec["egress"].([]any) {
			for _, to := range dig(rule, "to").([]any) {
				ib, ok := dig(to, "ipBlock").(map[string]any)
				if !ok {
					continue
				}
				blocks++
				ex := strings.Join(strOf(ib["except"]), " ")
				for _, must := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"} {
					if !strings.Contains(ex, must) {
						t.Errorf("step pods may reach %s", must)
					}
				}
			}
		}
		if blocks == 0 {
			t.Error("no internet rule found")
		}
		return
	}
	t.Fatal("no step pod policy")
}

func TestChartRefusesWhatCannotWork(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	for _, c := range []struct{ set, say string }{
		{"externalURL=https://x.example.com/path", "externalURL"},
		{"postgresql.enabled=false", "needs a PostgreSQL database"},
		{"postgresql.persistence.size=1Gi", "too small"},
		{"config.artifacts.database.maxTotalGiB=50", "cap must stay under"},
	} {
		out, err := exec.Command("helm", "template", "t", chartDir, "--set", "externalURL=https://launchway.example.com", "--set", c.set).CombinedOutput()
		if c.set[:11] == "externalURL" {
			out, err = exec.Command("helm", "template", "t", chartDir, "--set", c.set).CombinedOutput()
		}
		if err == nil || !strings.Contains(string(out), c.say) {
			t.Errorf("--set %s: want an error saying %q, got %v\n%s", c.set, c.say, err, out)
		}
	}
	if out, err := exec.Command("helm", "template", "t", chartDir).CombinedOutput(); err == nil || !strings.Contains(string(out), "externalURL") {
		t.Errorf("no address: %v\n%s", err, out)
	}
}

func chartDocs(t *testing.T, rendered string) []map[string]any {
	t.Helper()
	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(rendered))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			return docs
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
}

func name(d map[string]any) string { n, _ := dig(d, "metadata", "name").(string); return n }

func strOf(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, x.(string))
	}
	return out
}
