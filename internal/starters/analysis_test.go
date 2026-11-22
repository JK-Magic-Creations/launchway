package starters_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Python analysis step reads the project's own source and nothing else, writes every finding of medium severity or
// worse as SARIF for the findings backlog, and fails on the severity and confidence it is given, unless it is told to
// only report. It runs here as a step runs it, with a stand-in for bandit that logs the files it is given and writes
// the report a real bandit would, so that what the step selects and decides is what is tested.

const banditSarif = `{"runs":[{"results":[
 {"ruleId":"B602","message":{"text":"subprocess with shell=True"},"properties":{"issue_severity":"MEDIUM","issue_confidence":"MEDIUM"},
  "locations":[{"physicalLocation":{"artifactLocation":{"uri":"src/app.py"},"region":{"startLine":7}}}]},
 {"ruleId":"B105","message":{"text":"hard-coded password"},"properties":{"issue_severity":"LOW","issue_confidence":"HIGH"},
  "locations":[{"physicalLocation":{"artifactLocation":{"uri":"pkg/util.py"},"region":{"startLine":2}}}]}]}]}`

// analysisProject makes a repository with the project's own files and everything a Python project carries that is not
// its own, and the environment the step activates; it returns the project directory and the directory of the stand-ins.
func analysisProject(t *testing.T) (proj, bin, log string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed to read the report")
	}
	root := t.TempDir()
	scripts := filepath.Join(root, "scripts")
	for src, dst := range map[string]string{"python/analysis.sh": "python/analysis.sh", "common.sh": "common.sh"} {
		b, err := fs_readFile(src)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(scripts, dst)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	proj = filepath.Join(root, "project")
	for _, f := range []string{"src/app.py", "pkg/lib/util.py", "tests/test_app.py", ".venv/lib/python3.12/site-packages/evil.py", ".venv/bin/activate",
		"node_modules/x/y.py", "vendor/v.py", "build/b.py", "third_party/t.py", ".cache/c.py", "reports/r.py", "src/__pycache__/p.py", "docs/notes.txt"} {
		p := filepath.Join(proj, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin = filepath.Join(root, "bin")
	log = filepath.Join(root, "calls.log")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// bandit logs its arguments and writes the SARIF named after -o, as bandit does, exiting 1 when it found something.
	write("bandit", `echo "bandit $*" >> "$LW_LOG"
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift;; esac; shift; done
cat > "$out" <<'JSON'
`+banditSarif+`
JSON
exit 1
`)
	write("pip", `echo "pip $*" >> "$LW_LOG"`)
	// python is python3, except that the check for bandit's SARIF support passes (the real package is not installed here).
	write("python", `case "$*" in *sarif_om*) exit 0;; esac; exec python3 "$@"`)
	return proj, bin, log
}

func runAnalysis(t *testing.T, proj, bin, log string, env ...string) (out string, status int) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(filepath.Dir(bin), "scripts", "python", "analysis.sh"))
	cmd.Dir = proj
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + filepath.Dir(bin), "LW_LOG=" + log}, env...)
	b, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	return string(b) + "\n--- calls\n" + string(calls), status
}

func TestAnalysisReadsOnlyTheProjectsOwnSource(t *testing.T) {
	proj, bin, log := analysisProject(t)
	out, _ := runAnalysis(t, proj, bin, log)
	calls := out[strings.Index(out, "--- calls"):]
	for _, own := range []string{"src/app.py", "pkg/lib/util.py"} {
		if !strings.Contains(calls, own) {
			t.Errorf("the project's own file %s was not analysed:\n%s", own, out)
		}
	}
	for _, foreign := range []string{"site-packages", "evil.py", "node_modules", "vendor/", "build/", "third_party", ".cache", "reports/r.py", "__pycache__", "tests/test_app.py", "notes.txt"} {
		if strings.Contains(calls, foreign) {
			t.Errorf("%s was analysed, and it is not the project's own source:\n%s", foreign, out)
		}
	}
	if !strings.Contains(out, "analysing 2 Python files") {
		t.Errorf("the step does not say what it analysed:\n%s", out)
	}
}

func TestAnalysisWritesTheFindingsAndFailsOnTheSeverityItWasGiven(t *testing.T) {
	proj, bin, log := analysisProject(t)
	// By default only high severity at high confidence fails: the two findings are recorded, and the step passes.
	out, status := runAnalysis(t, proj, bin, log)
	if status != 0 {
		t.Fatalf("exit %d at the default severity\n%s", status, out)
	}
	sarif, err := os.ReadFile(filepath.Join(proj, "reports", "bandit.sarif"))
	if err != nil || !strings.Contains(string(sarif), "B602") {
		t.Fatalf("the findings are not in reports/bandit.sarif: %v %s", err, sarif)
	}
	if !strings.Contains(out, "bandit recorded 2 findings") || !strings.Contains(out, "0 at or above high severity and high confidence") {
		t.Errorf("the step does not say what it recorded:\n%s", out)
	}
	// Medium and medium fails on the first finding and names it with its file and line.
	out, status = runAnalysis(t, proj, bin, log, "PARAM_ANALYSIS_SEVERITY=medium", "PARAM_ANALYSIS_CONFIDENCE=medium")
	if status == 0 || !strings.Contains(out, "src/app.py:7") || !strings.Contains(out, "B602") {
		t.Fatalf("a finding at the given severity did not fail the step with its place (exit %d):\n%s", status, out)
	}
	// In report mode it says so and passes.
	out, status = runAnalysis(t, proj, bin, log, "PARAM_ANALYSIS_SEVERITY=medium", "PARAM_ANALYSIS_CONFIDENCE=medium", "PARAM_ANALYSIS_MODE=report")
	if status != 0 || !strings.Contains(out, "does not fail the run") {
		t.Fatalf("report mode must pass and say why (exit %d):\n%s", status, out)
	}
}

func TestAnalysisPathsAndExclusionsNarrowWhatItReads(t *testing.T) {
	proj, bin, log := analysisProject(t)
	out, _ := runAnalysis(t, proj, bin, log, "PARAM_ANALYSIS_PATHS=pkg", "PARAM_ANALYSIS_EXCLUDE=lib")
	if !strings.Contains(out, "no Python source to analyse") {
		t.Errorf("an excluded directory, named at any depth, was still read:\n%s", out)
	}
	out, _ = runAnalysis(t, proj, bin, log, "PARAM_ANALYSIS_PATHS=pkg")
	if !strings.Contains(out, "pkg/lib/util.py") || strings.Contains(out, "src/app.py") {
		t.Errorf("PARAM_ANALYSIS_PATHS did not limit the files:\n%s", out)
	}
}
