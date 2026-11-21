package api

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// The hosting acceptance suite (test/acceptance/githost, see its README) run
// against this package's in-process server. Auth mode "none" cannot issue
// tokens, so the suite cannot be pointed at a plain `launchway serve`; this
// test starts the signed-in test server with Git hosting on, makes a token
// for an administrator, and runs the suite as a child `go test` with
// LW_ACCEPT_URL, LW_ACCEPT_TOKEN and LW_ACCEPT_FLAVOR=launchway.
//
//	LAUNCHWAY_ACCEPTANCE=1 LAUNCHWAY_ACCEPTANCE_REPORT=/some/dir \
//	  go test -count=1 -timeout 15m -run TestHostingAcceptanceSuite -v ./internal/api/
//
// It is off by default (the suite takes minutes and needs the network for
// its pull-mirror test); with the variable unset it skips and says why. The
// suite's own result, id by id, is in the report directory (report.md and
// report.json) and in the test's log. The test fails when the suite does, so
// a run is a real check; the ids that need the compatibility routes fail
// until those routes exist (the README lists them).
func TestHostingAcceptanceSuite(t *testing.T) {
	if os.Getenv("LAUNCHWAY_ACCEPTANCE") == "" {
		t.Skip("set LAUNCHWAY_ACCEPTANCE=1 to run the hosting acceptance suite against an in-process server (it takes minutes)")
	}
	e, _ := hostedEnv(t, envOpts{})
	tok := e.tokenFor(t, "ada", "acceptance")
	report := os.Getenv("LAUNCHWAY_ACCEPTANCE_REPORT")
	if report == "" {
		report = t.TempDir()
	}
	suite, err := filepath.Abs("../../test/acceptance/githost")
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"test", "-count=1", "-timeout", "12m", "-v"}
	if run := os.Getenv("LAUNCHWAY_ACCEPTANCE_RUN"); run != "" {
		args = append(args, "-run", run)
	}
	goenv := func(name string) string { // the child gets an isolated HOME, so the Go caches are named, not derived
		b, _ := exec.CommandContext(t.Context(), "go", "env", name).Output()
		return strings.TrimSpace(string(b))
	}
	cmd := exec.CommandContext(t.Context(), "go", append(args, suite)...)
	cmd.Dir = filepath.Dir(suite)
	cmd.Env = testlib.GitEnv(t, "LW_ACCEPT_URL="+e.srv.URL, "LW_ACCEPT_TOKEN="+tok, "LW_ACCEPT_FLAVOR=launchway", "LW_ACCEPT_REPORT="+report,
		"GOFLAGS="+os.Getenv("GOFLAGS"), "GOCACHE="+goenv("GOCACHE"), "GOMODCACHE="+goenv("GOMODCACHE"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err = cmd.Run()
	text := strings.ReplaceAll(out.String(), tok, "<token>")
	t.Logf("the suite ran for %s; report in %s\n%s", time.Since(start).Round(time.Second), report, text)
	if err != nil {
		t.Errorf("the acceptance suite failed: %v", err)
	}
}
