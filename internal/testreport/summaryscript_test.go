package testreport

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The pipeline's test step prints scripts/ci/go-test-summary.sh's output into the
// step log: one line per package, and for each failure its name and the last
// lines of its output, so the reason is in the log as well as in the Tests tab.
func runSummary(t *testing.T, sample string, env ...string) string {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "ci", "go-test-summary.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk on this machine")
	}
	cmd := exec.Command("sh", script, filepath.Join("testdata", sample))
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the script failed (it must never fail): %v\n%s", err, out)
	}
	return string(out)
}

func TestSummaryScriptNamesEachFailureAndItsOutput(t *testing.T) {
	out := runSummary(t, "go-test.json")
	for _, want := range []string{
		"fail  example.com/app/calc  0.012s",
		"pass  example.com/app/ok  0.041s",
		"FAIL example.com/app/calc TestDiv\n          calc_test.go:22: Div(1, 0) = 0, want an error",
		"FAIL example.com/app/calc TestTable/three",
		"FAIL example.com/app/crash TestWorkerStop (did not finish: the test binary stopped)",
		"panic: close of closed channel",
		"FAIL example.com/app/broken (the package failed, no test failed)",
		"broken/parse.go:9:6: undefined: strings.Cutt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "=== RUN") || strings.Contains(out, "--- FAIL") {
		t.Errorf("framework lines are in the summary:\n%s", out)
	}
}

func TestSummaryScriptKeepsOnlyTheTailOfLongOutput(t *testing.T) {
	out := runSummary(t, "go-test.json", "TAIL_LINES=2")
	if !strings.Contains(out, "earlier lines left out") {
		t.Errorf("a cut output does not say so:\n%s", out)
	}
	if strings.Contains(out, "crash_test.go:30") {
		t.Errorf("the head of a long output is still there:\n%s", out)
	}
}

func TestSummaryScriptSaysNothingMoreWhenAllPass(t *testing.T) {
	out := runSummary(t, "go-test-pass.json")
	if strings.Contains(out, "Failures") || !strings.Contains(out, "pass  example.com/app/ok  0.041s") {
		t.Errorf("summary of a passing run:\n%s", out)
	}
}

// Go 1.27 adds an OutputType field after Output in each line; the real streams are read as well.
func TestSummaryScriptReadsRealGo127Output(t *testing.T) {
	out := runSummary(t, "go-test-real-1.27-failures.json")
	for _, want := range []string{
		"FAIL example.com/demo/subproc TestChildSaysWhy\n      child: fatal: the reason is here",
		"FAIL example.com/demo/subproc TestParent/three",
		"panic: assignment to entry in nil map",
		"FAIL example.com/demo/mainfail (the package failed, no test failed)\n      setup: cannot reach the database",
		"broken/b.go:3:23: undefined: undefinedName",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary does not contain %q:\n%s", want, out)
		}
	}
	out = runSummary(t, "go-test-real-failing-test.json")
	if !strings.Contains(out, "FAIL github.com/jk-magic-creations/launchway/internal/forge/storage TestQuotas") ||
		!strings.Contains(out, "multiple updates for 'refs/heads/main'") {
		t.Errorf("the reason of the failure that started this is not in the summary:\n%s", out)
	}
}
