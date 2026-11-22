package starters_test

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Python flight plans install the project with the manager the repository's files name (uv, Poetry, PDM or pip),
// then put the tools of their steps (pytest, pytest-cov, ruff) into that same environment, and audit the dependencies
// the manager installed, not the pip the image seeded and not the tools. These tests run the real scripts, with stand-ins
// for the managers (which install two fake packages, as a lock file would), no network (a directory of fake wheels is
// the only index) and the real venv, ensurepip and pip of this machine's python3, so that "uv leaves the environment
// without pip" is the situation tested, not a description of it.

func writeWheel(t *testing.T, dir, name, version, module, body, script string) {
	t.Helper()
	file := filepath.Join(dir, fmt.Sprintf("%s-%s-py3-none-any.whl", name, version))
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	di := fmt.Sprintf("%s-%s.dist-info/", name, version)
	files := map[string]string{
		module + ".py":  body,
		di + "METADATA": fmt.Sprintf("Metadata-Version: 2.1\nName: %s\nVersion: %s\n", name, version),
		di + "WHEEL":    "Wheel-Version: 1.0\nGenerator: test\nRoot-Is-Purelib: true\nTag: py3-none-any\n",
		di + "RECORD":   di + "RECORD,,\n",
	}
	if script != "" {
		files[di+"entry_points.txt"] = "[console_scripts]\n" + script + "\n"
	}
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(c))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

type pyFixture struct {
	root, scripts, wheels, bin, proj, log string
}

func newPyFixture(t *testing.T, manager string) *pyFixture {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed")
	}
	if out, err := exec.Command("python3", "-c", "import venv, ensurepip").CombinedOutput(); err != nil {
		t.Skipf("python3 has no venv or ensurepip: %s", out)
	}
	x := &pyFixture{root: t.TempDir()}
	x.scripts, x.wheels, x.bin, x.proj = filepath.Join(x.root, "scripts"), filepath.Join(x.root, "wheels"), filepath.Join(x.root, "bin"), filepath.Join(x.root, "project")
	x.log = filepath.Join(x.root, "calls.log")
	for _, d := range []string{x.wheels, x.bin, x.proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The library's scripts, as they are shipped.
	src := filepath.Join("library", "scripts")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(x.scripts, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	// The only index there is: the project's two packages, and the tools.
	writeWheel(t, x.wheels, "hello", "1.0", "hello", "", "")
	writeWheel(t, x.wheels, "leftpad", "2.0", "leftpad", "", "")
	writeWheel(t, x.wheels, "pytest", "9.2.0", "pytest", "", "")
	writeWheel(t, x.wheels, "pytest_cov", "7.1.0", "pytest_cov", "", "")
	writeWheel(t, x.wheels, "ruff", "0.99.0", "ruff", "def main():\n    pass\n", "ruff = ruff:main")
	writeWheel(t, x.wheels, "pip_audit", "2.0.0", "pip_audit", `import os, sys
def main():
    log = open(os.environ["LW_LOG"], "a")
    log.write("pip-audit " + " ".join(sys.argv[1:]) + "\n")
    path = sys.argv[sys.argv.index("-r") + 1]
    log.write("audited: " + open(path).read().replace("\n", " ") + "\n")
    sys.exit(int(os.environ.get("LW_AUDIT_EXIT", "0")))
`, "pip-audit = pip_audit:main")
	// A manager stand-in installs the two packages of the project into the environment it uses, as a lock file would.
	install := `PY="$1"
"$PY" - "$LW_WHEELS" <<'PYEND'
import glob, sys, sysconfig, zipfile
for w in ("hello-1.0", "leftpad-2.0"):
    zipfile.ZipFile(glob.glob(sys.argv[1] + "/" + w + "-*.whl")[0]).extractall(sysconfig.get_paths()["purelib"])
PYEND
`
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(x.bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("uv", `echo "uv $*" >> "$LW_LOG"
[ "$1 $2" = "sync --frozen" ] || exit 0
env=${UV_PROJECT_ENVIRONMENT:-.venv}
python3 -m venv --without-pip "$env"
`+strings.Replace(install, `PY="$1"`, `PY="$env/bin/python"`, 1))
	write("poetry", `echo "poetry $*" >> "$LW_LOG"
[ "$1" = install ] || exit 0
`+strings.Replace(install, `PY="$1"`, `PY="$VIRTUAL_ENV/bin/python"`, 1))
	write("pdm", `echo "pdm $*" >> "$LW_LOG"
[ "$1" = install ] || exit 0
`+strings.Replace(install, `PY="$1"`, `PY="$VIRTUAL_ENV/bin/python"`, 1))
	switch manager {
	case "uv":
		x.file(t, "uv.lock", "version = 1\n\n[[package]]\nname = \"hello\"\nversion = \"1.0\"\n\n[[package]]\nname = \"leftpad\"\nversion = \"2.0\"\n")
		x.file(t, "pyproject.toml", "[project]\nname = \"app\"\nversion = \"0.1\"\ndependencies = [\"hello\", \"leftpad\"]\n")
	case "poetry":
		x.file(t, "poetry.lock", "[[package]]\nname = \"hello\"\nversion = \"1.0\"\n\n[[package]]\nname = \"leftpad\"\nversion = \"2.0\"\n")
		x.file(t, "pyproject.toml", "[tool.poetry]\nname = \"app\"\nversion = \"0.1\"\n")
	case "pdm":
		x.file(t, "pdm.lock", "[[package]]\nname = \"hello\"\nversion = \"1.0\"\n\n[[package]]\nname = \"leftpad\"\nversion = \"2.0\"\n")
		x.file(t, "pyproject.toml", "[project]\nname = \"app\"\nversion = \"0.1\"\n[tool.pdm]\n")
	case "pip":
		x.file(t, "requirements.txt", "hello==1.0\nleftpad==2.0\n")
	}
	return x
}

func (x *pyFixture) file(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(x.proj, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (x *pyFixture) run(t *testing.T, script string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(x.scripts, "python", script))
	cmd.Dir = x.proj
	cmd.Env = append([]string{"PATH=" + x.bin + ":" + os.Getenv("PATH"), "HOME=" + x.root, "LW_LOG=" + x.log, "LW_WHEELS=" + x.wheels,
		"PIP_NO_INDEX=1", "PIP_FIND_LINKS=" + x.wheels, "PIP_CONFIG_FILE=/dev/null", "PIP_DISABLE_PIP_VERSION_CHECK=1", "LANG=C.UTF-8"}, env...)
	b, err := cmd.CombinedOutput()
	status := 0
	if ee, ok := err.(*exec.ExitError); ok {
		status = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(b), status
}

func (x *pyFixture) calls() string {
	b, _ := os.ReadFile(x.log)
	return string(b)
}

func TestPythonSetupAndAuditForEachPackageManager(t *testing.T) {
	cases := []struct{ manager, installs string }{
		{"uv", "uv sync --frozen"},
		{"poetry", "poetry install --no-interaction"},
		{"pdm", "pdm install --frozen-lockfile"},
		{"pip", ""},
	}
	for _, c := range cases {
		t.Run(c.manager, func(t *testing.T) {
			x := newPyFixture(t, c.manager)
			out, status := x.run(t, "setup.sh")
			if status != 0 {
				t.Fatalf("setup failed (%d):\n%s", status, out)
			}
			if !strings.Contains(out, "package manager: "+c.manager) {
				t.Errorf("the log does not name the manager:\n%s", out)
			}
			if c.installs != "" && !strings.Contains(x.calls(), c.installs) {
				t.Errorf("%s was not run as %q:\n%s", c.manager, c.installs, x.calls())
			}
			py := filepath.Join(x.proj, ".venv", "bin", "python")
			// The tools are in the project's own environment, and pip is there to have put them in.
			for _, check := range []string{"import pytest, pytest_cov, hello, leftpad", "import pip"} {
				if b, err := exec.Command(py, "-c", check).CombinedOutput(); err != nil {
					t.Errorf("%q fails in the project's environment: %s\n%s", check, b, out)
				}
			}
			if _, err := os.Stat(filepath.Join(x.proj, ".venv", "bin", "ruff")); err != nil {
				t.Errorf("ruff is not in the environment: %v", err)
			}
			// What the manager installed, and nothing else, is what the audit reads.
			deps, err := os.ReadFile(filepath.Join(x.proj, ".cache", "lw-tools", "project-deps.txt"))
			if err != nil {
				t.Fatalf("no record of the project's dependencies: %v\n%s", err, out)
			}
			if string(deps) != "hello==1.0\nleftpad==2.0\n" {
				t.Errorf("the project's dependencies were recorded as %q (tools or seeded pip leaked in)", deps)
			}

			out, status = x.run(t, "audit.sh")
			if status != 0 {
				t.Fatalf("audit failed (%d):\n%s", status, out)
			}
			calls := x.calls()
			if !strings.Contains(calls, "--no-deps --disable-pip") || !strings.Contains(calls, "audited: hello==1.0 leftpad==2.0 ") {
				t.Errorf("pip-audit was not given the project's dependencies alone:\n%s", calls)
			}
			if !strings.Contains(out, "auditing the 2 dependencies the project installed") {
				t.Errorf("the log does not say what was audited:\n%s", out)
			}
			// A finding fails the step, unless the mode is report.
			if _, status := x.run(t, "audit.sh", "LW_AUDIT_EXIT=1"); status == 0 {
				t.Error("a vulnerable dependency did not fail the audit")
			}
			if out, status := x.run(t, "audit.sh", "LW_AUDIT_EXIT=1", "PARAM_AUDIT_MODE=report"); status != 0 || !strings.Contains(out, "audit_mode: report") {
				t.Errorf("report mode failed (%d):\n%s", status, out)
			}
		})
	}
}

// A project that really depends on setuptools (it is in its lock) has it audited; one that does not has the image's
// copy left out and the log says which.
func TestPythonAuditKeepsADeclaredSetuptoolsAndLeavesOutASeededOne(t *testing.T) {
	x := newPyFixture(t, "pip")
	writeWheel(t, x.wheels, "setuptools", "70.0.0", "setuptools", "", "")
	x.file(t, "requirements.txt", "hello==1.0\nsetuptools==70.0.0\n")
	out, status := x.run(t, "setup.sh")
	if status != 0 {
		t.Fatalf("%d\n%s", status, out)
	}
	deps, _ := os.ReadFile(filepath.Join(x.proj, ".cache", "lw-tools", "project-deps.txt"))
	if string(deps) != "hello==1.0\nsetuptools==70.0.0\n" {
		t.Errorf("a dependency the project names was left out: %q", deps)
	}
	if !strings.Contains(out, "left out as tooling of the image or environment") || !strings.Contains(out, "pip ") {
		t.Errorf("the log does not say that the image's pip was left out:\n%s", out)
	}
}

// placePythonLib puts the files the Python scripts read beside a script that a test copied alone.
func placePythonLib(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"python/lib.sh", "python/project_deps.py"} {
		b, err := fs_readFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
