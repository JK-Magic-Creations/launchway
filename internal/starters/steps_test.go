package starters_test

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/starters"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
	"github.com/jk-magic-creations/launchway/internal/steptest"
)

// Three of the starters' scripts are also written as custom steps, against the step contract, in
// shell, Go and Python. Each does what its script does: the tests run both on the same
// workspace and compare what they ran and whether they passed.

func extract(t *testing.T, dest string) {
	t.Helper()
	err := fs.WalkDir(starters.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(starters.FS(), p)
		if err != nil {
			return err
		}
		out := filepath.Join(dest, p)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// layout makes a workspace that holds the starters' files as a library at .launchway/lib/launchway (for the scripts)
// and its steps at steps/ (for the steps), and a tiny repository.
func layout(t *testing.T, files map[string]string) (ws, lib string) {
	t.Helper()
	ws = t.TempDir()
	lib = filepath.Join(ws, ".launchway", "lib", "launchway")
	extract(t, lib)
	for _, name := range starters.Steps() {
		if err := os.CopyFS(filepath.Join(ws, "steps", name), os.DirFS(filepath.Join(lib, "steps", name))); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range files {
		p := filepath.Join(ws, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return ws, lib
}

func script(t *testing.T, ws, lib, rel string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(lib, rel))
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func runStep(t *testing.T, ws, name string, with map[string]string) *steptest.Report {
	t.Helper()
	rep, err := steptest.Run(context.Background(), steptest.Options{Dir: filepath.Join(ws, "steps", name), Workspace: ws, With: with, Executor: &executor.Host{AllowUnsandboxed: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Conforms() {
		t.Fatalf("the step breaks the contract: %v\n%s", rep.Problems, rep.Stdout)
	}
	return rep
}

func lines(s, prefix string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, prefix) || strings.Contains(l, "BEGIN PRIVATE KEY") {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func TestStartersStepsAreManifestsThatParse(t *testing.T) {
	if len(starters.Steps()) < 3 {
		t.Fatalf("steps: %v", starters.Steps())
	}
	for _, n := range starters.Steps() {
		data, ok := starters.Step(n)
		if !ok {
			t.Fatal(n)
		}
		m, probs := stepkit.ParseManifest(data)
		if len(probs) > 0 || m.Name != n {
			t.Errorf("%s: %v", n, probs)
		}
	}
}

func TestScanSecretsStepDoesWhatTheScriptDoes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git")
	}
	for name, files := range map[string]map[string]string{
		"clean":   {"a.txt": "hello\n", "main.go": "package main\n"},
		"a key":   {"a.txt": "hello\n", "secrets/key.pem": "-----BEGIN PRIVATE KEY-----\nabc\n"},
		"a token": {"x/conf": "token = ghp_abcdefghijklmnopqrstuvwxyz0123456789\n"},
	} {
		t.Run(name, func(t *testing.T) {
			ws, lib := layout(t, files)
			out, code := script(t, ws, lib, "scripts/secrets.sh", "LAUNCHWAY_LIB="+lib)
			rep := runStep(t, ws, "scan-secrets", nil)
			if (code == 0) != (rep.Result.Code == 0) {
				t.Fatalf("the script exits %d, the step %d (%s)", code, rep.Result.Code, rep.Result.Why)
			}
			if got, want := lines(rep.Stdout, "launchway:"), lines(out, "launchway:"); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("what they print differs:\nstep:   %q\nscript: %q", got, want)
			}
			if code != 0 && rep.Result.Findings == 0 {
				t.Error("the step found secrets but reported no finding")
			}
			if code != 0 && rep.Outcome != stepkit.OutcomeFailed {
				t.Errorf("outcome %v", rep.Outcome)
			}
		})
	}
}

// fakeTool puts a program on PATH that records its arguments and, if fail, fails. The host executor
// passes a step only PATH, so the log's place is written into the program.
func fakeTool(t *testing.T, name string, fail bool) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(t.TempDir(), "calls")
	exit := "0"
	if fail && name != "" {
		exit = "1"
	}
	body := "#!/bin/sh\necho \"" + name + " $*\" >> " + log + "\n[ \"$1\" = \"--fail-on=GPL-3.0\" -o \"$1\" = \"check\" ] && exit " + exit + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func calls(t *testing.T, log string) string {
	b, _ := os.ReadFile(log)
	_ = os.Remove(log)
	return string(b)
}

func TestGoLicensesStepDoesWhatTheScriptDoes(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go")
	}
	for _, c := range []struct {
		name string
		deny []string
		fail string
	}{{"lists only", nil, ""}, {"denies", []string{"forbidden", "restricted"}, ""}, {"denied license found", []string{"forbidden"}, "go-licenses"}} {
		t.Run(c.name, func(t *testing.T) {
			ws, lib := layout(t, map[string]string{"go.mod": "module x\n\ngo 1.25\n"})
			log := fakeTool(t, "go-licenses", c.fail != "")
			env := []string{"LAUNCHWAY_LIB=" + lib, "PARAM_LICENSE_DENY=" + strings.Join(c.deny, ",")}
			_, code := script(t, ws, lib, "scripts/go/licenses.sh", env...)
			want := calls(t, log)
			with := map[string]string{}
			if len(c.deny) > 0 {
				with["license_deny"] = stepkit.JSONList(c.deny)
			}
			rep := runStep(t, ws, "go-licenses", with)
			got := calls(t, log)
			if got != want {
				t.Errorf("what they ran differs:\nstep:\n%s\nscript:\n%s", got, want)
			}
			if (code == 0) != (rep.Result.Code == 0) {
				t.Errorf("the script exits %d, the step %d (%s)", code, rep.Result.Code, rep.Result.Why)
			}
		})
	}
}

func TestPythonLicensesStepDoesWhatTheScriptDoes(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3")
	}
	for _, c := range []struct {
		name string
		deny []string
		fail string
	}{{"lists only", nil, ""}, {"denies", []string{"GPL-3.0"}, ""}, {"denied license found", []string{"GPL-3.0"}, "pip-licenses"}} {
		t.Run(c.name, func(t *testing.T) {
			ws, lib := layout(t, map[string]string{"requirements.txt": "x\n"})
			// The script activates the project's environment; give it an empty one.
			os.MkdirAll(filepath.Join(ws, ".venv", "bin"), 0o755)
			os.WriteFile(filepath.Join(ws, ".venv", "bin", "activate"), []byte(":\n"), 0o644)
			log := fakeTool(t, "pip-licenses", c.fail != "")
			env := []string{"LAUNCHWAY_LIB=" + lib, "PARAM_LICENSE_DENY=" + strings.Join(c.deny, ",")}
			_, code := script(t, ws, lib, "scripts/python/licenses.sh", env...)
			want := calls(t, log)
			with := map[string]string{}
			if len(c.deny) > 0 {
				with["license_deny"] = stepkit.JSONList(c.deny)
			}
			rep := runStep(t, ws, "python-licenses", with)
			got := calls(t, log)
			if got != want {
				t.Errorf("what they ran differs:\nstep:\n%s\nscript:\n%s", got, want)
			}
			if (code == 0) != (rep.Result.Code == 0) {
				t.Errorf("the script exits %d, the step %d (%s)", code, rep.Result.Code, rep.Result.Why)
			}
		})
	}
}

// The script passes a list of licenses to pip-licenses through eval, where the semicolons that
// pip-licenses wants between names end the command (the script exits 127 for two names); the step
// passes them as one argument, as the script means to.
func TestPythonLicensesStepPassesSeveralNamesAsOneArgument(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3")
	}
	ws, _ := layout(t, map[string]string{"requirements.txt": "x\n"})
	log := fakeTool(t, "pip-licenses", false)
	runStep(t, ws, "python-licenses", map[string]string{"license_deny": `["GPL-3.0","AGPL-3.0"]`})
	if got := calls(t, log); !strings.Contains(got, "pip-licenses --fail-on=GPL-3.0;AGPL-3.0\n") {
		t.Errorf("%q", got)
	}
}
