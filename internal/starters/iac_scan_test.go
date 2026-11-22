package starters_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The infrastructure scan uses the checks built into the scanner, so that a pinned scanner is not given checks written
// for a newer one, and it fails when the scanner reports an error of its own, even when the scanner exits 0.
func TestIaCScanUsesBuiltInChecksAndFailsOnScannerErrors(t *testing.T) {
	script := scriptPath(t, "iac-scan.sh")
	run := func(trivy string, env ...string) (string, int) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		os.MkdirAll(bin, 0o755)
		os.WriteFile(filepath.Join(bin, "trivy"), []byte("#!/bin/sh\necho \"trivy $*\" >> \"$LW_LOG\"\n"+trivy), 0o755)
		proj := filepath.Join(dir, "proj")
		os.MkdirAll(proj, 0o755)
		cmd := exec.Command("sh", script)
		cmd.Dir = proj
		cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "LW_LOG=" + filepath.Join(dir, "calls.log")}, env...)
		out, err := cmd.CombinedOutput()
		status := 0
		if ee, ok := err.(*exec.ExitError); ok {
			status = ee.ExitCode()
		}
		calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
		return string(out) + "\n--- calls\n" + string(calls), status
	}
	out, status := run("exit 0")
	if status != 0 || !strings.Contains(out, "--skip-check-update") || !strings.Contains(out, "--exit-code 0") {
		t.Errorf("a clean scan passes with the built-in checks (%d):\n%s", status, out)
	}
	if out, status := run("exit 0", "PARAM_MODE=enforce"); status != 0 || !strings.Contains(out, "--exit-code 1") {
		t.Errorf("enforce asks for exit code 1 on findings (%d):\n%s", status, out)
	}
	if out, _ := run("exit 0", "PARAM_CHECK_UPDATES=true"); strings.Contains(out, "--skip-check-update") {
		t.Errorf("check_updates: true downloads the newest checks:\n%s", out)
	}
	// The scanner prints a policy error and exits 0: that is a failure, in either mode.
	bad := "printf '2026-11-22T18:16:47Z\\tERROR\\t[rego] Error occurred while parsing\\tfile_path=\"x.rego\"\\n' >&2\nexit 0"
	for _, mode := range []string{"report", "enforce"} {
		out, status := run(bad, "PARAM_MODE="+mode)
		if status == 0 || !strings.Contains(out, "the scanner reported errors of its own") || !strings.Contains(out, "[rego] Error occurred") {
			t.Errorf("%s: a scanner that errors must fail the step, and its errors be in the log (%d):\n%s", mode, status, out)
		}
	}
	// A scanner that fails outright fails the step with its own status.
	if _, status := run("echo boom >&2; exit 3"); status != 3 {
		t.Errorf("status %d", status)
	}
}
