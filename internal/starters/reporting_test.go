package starters_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test step of the Node and Python templates adds a reporter to whatever command runs the tests, so that the Tests
// and Coverage tabs fill without the repository listing one, and says in plain words when it cannot. The step runs
// here with stand-ins for npm, npx and pytest that write a report only when they were handed the reporter, which is
// what a real runner does.

func scriptPath(t *testing.T, name string) string {
	t.Helper()
	b, err := fs_readFile(name)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), filepath.Base(name))
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func fs_readFile(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("library", "scripts", name))
}

// stubs makes a directory of fake tools that log their arguments; those in reporting write junit.xml and a coverage file
// when their arguments carry the reporter.
func stubs(t *testing.T, dir string) (path, log string) {
	t.Helper()
	bin := filepath.Join(dir, "bin")
	log = filepath.Join(dir, "calls.log")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rep := `echo "$0 $*" >> "$LW_LOG"
case "$*" in *jest-junit*) mkdir -p "$JEST_JUNIT_OUTPUT_DIR" && echo '<testsuites/>' > "$JEST_JUNIT_OUTPUT_DIR/junit.xml";; esac
a="$*"; case "$a" in *--coverageDirectory=*) d=${a##*--coverageDirectory=}; d=${d%% *}; mkdir -p "$d" && echo '<coverage/>' > "$d/cobertura-coverage.xml";; esac
exit ${LW_EXIT:-0}
`
	write("npm", `echo "npm $*" >> "$LW_LOG"
case "$*" in *"install --prefix"*) p=$3; mkdir -p "$p/node_modules/.bin" "$p/node_modules/jest-junit";; esac
case "$*" in "test"*|"run "*) `+strings.TrimSpace(rep)+`;; esac
`)
	write("npx", rep)
	write("yarn", rep)
	write("jest", rep)
	write("mocha", `echo "$0 $*" >> "$LW_LOG"; case "$*" in *mocha-junit-reporter*) echo '<testsuites/>' > "$MOCHA_FILE";; esac`)
	write("pytest", `echo "$0 $*" >> "$LW_LOG"
case "$PYTEST_ADDOPTS" in *--junitxml=*) f=${PYTEST_ADDOPTS#*--junitxml=}; f=${f%% *}; mkdir -p "$(dirname "$f")"; echo '<testsuites/>' > "$f";; esac
case "$PYTEST_ADDOPTS" in *--cov-report=xml:*) f=${PYTEST_ADDOPTS#*--cov-report=xml:}; f=${f%% *}; echo '<coverage/>' > "$f";; esac
`)
	return bin, log
}

func runScript(t *testing.T, script, project string, env ...string) (out string, status int) {
	t.Helper()
	dir := t.TempDir()
	bin, log := stubs(t, dir)
	cmd := exec.Command("sh", script)
	cmd.Dir = project
	cmd.Env = append([]string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "LW_LOG=" + log}, env...)
	b, err := cmd.CombinedOutput()
	status = 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	return string(b) + "\n--- calls\n" + string(calls), status
}

func nodeProject(t *testing.T, testScript string) string {
	t.Helper()
	d := t.TempDir()
	pkg := `{"name":"x","scripts":{"test":"` + testScript + `"}}`
	if err := os.WriteFile(filepath.Join(d, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNodeTestsGetAReporterWhateverTheCommand(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is needed to read package.json")
	}
	script := scriptPath(t, "node/test.sh")
	common, _ := fs_readFile("common.sh")
	if err := os.WriteFile(filepath.Join(filepath.Dir(script), "..", "common.sh"), common, 0o644); err != nil {
		// The script sources ../common.sh next to its own directory.
		t.Skip("cannot place common.sh")
	}
	for _, c := range []struct {
		name, command, pkgScript, want string
		report                         bool
	}{
		{"npx jest as the owner wrote it", "npx jest", "jest", "npx jest --reporters=default --reporters=", true},
		{"a command with its own flags", "npx jest --runInBand --ci", "jest", "--runInBand --ci", true},
		{"the package's script, through npm test", "", "jest --ci", "npm test -- --reporters=", true},
		{"a script that runs jest by name", "npm run unit", "", "npm run unit -- --reporters=", true},
		{"a command that is not a runner it knows", "echo tests", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			proj := nodeProject(t, "jest")
			if c.pkgScript != "" {
				os.WriteFile(filepath.Join(proj, "package.json"), []byte(`{"name":"x","scripts":{"test":"`+c.pkgScript+`","unit":"jest -c x"}}`), 0o644)
			} else if c.command == "npm run unit" {
				os.WriteFile(filepath.Join(proj, "package.json"), []byte(`{"name":"x","scripts":{"unit":"jest -c x"}}`), 0o644)
			}
			env := []string{"PARAM_TEST_COMMAND=" + c.command}
			out, status := runScript(t, script, proj, env...)
			if status != 0 {
				t.Fatalf("exit %d\n%s", status, out)
			}
			if c.report {
				if !strings.Contains(out, c.want) {
					t.Errorf("the command was not given the reporter (%q):\n%s", c.want, out)
				}
				if _, err := os.Stat(filepath.Join(proj, "reports", "junit.xml")); err != nil {
					t.Errorf("no reports/junit.xml: %v\n%s", err, out)
				}
				if _, err := os.Stat(filepath.Join(proj, "reports", "coverage.xml")); err != nil {
					t.Errorf("no reports/coverage.xml: %v\n%s", err, out)
				}
				return
			}
			if !strings.Contains(out, "TESTS RAN, BUT NO TEST REPORT WAS PRODUCED") || !strings.Contains(out, "reports/junit.xml") {
				t.Errorf("a run with no report must say so, and say what to do:\n%s", out)
			}
		})
	}
}

func TestPythonTestsGetJUnitAndCoverageWhateverTheCommand(t *testing.T) {
	script := scriptPath(t, "python/test.sh")
	common, _ := fs_readFile("common.sh")
	if err := os.WriteFile(filepath.Join(filepath.Dir(script), "..", "common.sh"), common, 0o644); err != nil {
		t.Skip("cannot place common.sh")
	}
	for _, command := range []string{"", "pytest -q tests", "make test-fast"} {
		proj := t.TempDir()
		os.MkdirAll(filepath.Join(proj, ".venv", "bin"), 0o755)
		os.WriteFile(filepath.Join(proj, ".venv", "bin", "activate"), []byte(""), 0o644)
		os.WriteFile(filepath.Join(proj, ".venv", "bin", "python"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
		dir := t.TempDir()
		bin, log := stubs(t, dir)
		os.WriteFile(filepath.Join(bin, "make"), []byte("#!/bin/sh\npytest -q\n"), 0o755)
		os.WriteFile(filepath.Join(bin, "python"), []byte("#!/bin/sh\ncase \"$1 $2\" in \"-m pytest\") shift 2; exec pytest \"$@\";; esac\nexit 0\n"), 0o755)
		cmd := exec.Command("sh", script)
		cmd.Dir = proj
		cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "LW_LOG=" + log, "PARAM_TEST_COMMAND=" + command}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%q: %v\n%s", command, err, out)
		}
		for _, f := range []string{"junit.xml", "coverage.xml"} {
			if _, err := os.Stat(filepath.Join(proj, "reports", f)); err != nil {
				t.Errorf("%q: no reports/%s\n%s", command, f, out)
			}
		}
		if !strings.Contains(string(out), "PYTEST_ADDOPTS=--junitxml=reports/junit.xml --cov=. --cov-report=xml:reports/coverage.xml") {
			t.Errorf("%q: the log does not show what was added:\n%s", command, out)
		}
	}
	// A command that never runs pytest says so, loudly.
	proj := t.TempDir()
	os.MkdirAll(filepath.Join(proj, ".venv", "bin"), 0o755)
	os.WriteFile(filepath.Join(proj, ".venv", "bin", "activate"), []byte(""), 0o644)
	dir := t.TempDir()
	bin, log := stubs(t, dir)
	os.WriteFile(filepath.Join(bin, "python"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	cmd := exec.Command("sh", script)
	cmd.Dir = proj
	cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "LW_LOG=" + log, "PARAM_TEST_COMMAND=echo hello"}
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "TESTS RAN, BUT NO TEST REPORT WAS PRODUCED") {
		t.Errorf("no loud notice:\n%s", out)
	}
}
