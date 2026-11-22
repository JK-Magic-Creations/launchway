package runner

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The credential for a repository on another host goes only in the environment of the one git process, scoped to
// the host's origin; the address carries none.
func TestOutsideCloneKeepsTheCredentialInTheEnvironmentOfOneProcess(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	from, env := outsideClone(&engine.OutsideAuth{URL: "https://git.example.com/acme/app.git", Username: "x-access-token", Secret: "tok-secret-123"})
	if from != "https://git.example.com/acme/app.git" || strings.Contains(from, "tok-secret") {
		t.Fatalf("the address carries a credential: %q", from)
	}
	joined := strings.Join(env, "\n")
	want := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:tok-secret-123"))
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.https://git.example.com/.extraHeader") || !strings.Contains(joined, want) {
		t.Fatalf("env: %v", env)
	}
}

func TestOutsideCloneCarriesThePinnedAddresses(t *testing.T) {
	_, env := outsideClone(&engine.OutsideAuth{URL: "https://git.example.com/acme/app.git", Username: "u", Secret: "s", Resolve: []string{"git.example.com:443:93.184.216.34"}})
	j := strings.Join(env, "\n")
	if !strings.Contains(j, "http.curloptResolve") || !strings.Contains(j, "=git.example.com:443:93.184.216.34") || !strings.Contains(j, "GIT_CONFIG_COUNT=2") {
		t.Fatalf("env = %s", j)
	}
}
