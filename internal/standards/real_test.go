package standards

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/findings"
	"github.com/jk-magic-creations/launchway/internal/standards/standardstest"
)

// TestRealTools runs every check's script in its pinned image, against the
// planted fixture repository, and reads what the tools wrote through the
// same path the server uses. It needs a container runtime, the network (the
// tools fetch advisories and modules) and several minutes, so it runs only
// when LAUNCHWAY_STANDARDS_REAL=1; without it the same normalisation is
// tested against the recorded outputs of the same tools (testdata/recorded).
// The images are pulled once and stay in the runtime's store.
func TestRealTools(t *testing.T) {
	if os.Getenv("LAUNCHWAY_STANDARDS_REAL") != "1" {
		t.Skip("set LAUNCHWAY_STANDARDS_REAL=1 to run the real tools from their pinned images (needs podman or docker and the network)")
	}
	bin := os.Getenv("CONTAINER")
	if bin == "" {
		bin = "podman"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("no %s on this machine: the real tools were not run", bin)
	}
	scratch := os.Getenv("LAUNCHWAY_STANDARDS_SCRATCH")
	if scratch == "" {
		home, _ := os.UserHomeDir()
		scratch = filepath.Join(home, "scratch")
	}
	_ = os.MkdirAll(scratch, 0o755)
	dir, err := os.MkdirTemp(scratch, "standards-real-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := standardstest.Write(dir); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	// The secret is removed in a later commit: it must still be found, in the history.
	if err := os.Remove(filepath.Join(dir, "config", "settings.env")); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "remove the settings file")

	run := func(c Check, env map[string]string) []Finding {
		t.Helper()
		tool, _ := ToolNamed(c.Tool)
		ctx, cancel := context.WithTimeout(context.Background(), c.Budget)
		defer cancel()
		args := []string{"run", "--rm", "--security-opt", "label=disable", "-v", dir + ":/workspace", "-w", "/workspace", "--entrypoint", "/bin/sh"}
		for k, v := range env {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, tool.Image, "-c", c.Script)
		cmd := exec.CommandContext(ctx, bin, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s did not run: %v\n%s", c.ID, err, tail(string(out)))
		}
		var all []Finding
		for _, g := range c.Outputs {
			matches, _ := filepath.Glob(filepath.Join(dir, filepath.FromSlash(g)))
			if len(matches) == 0 {
				t.Fatalf("%s wrote no file matching %s\n%s", c.ID, g, tail(string(out)))
			}
			for _, m := range matches {
				data, _ := os.ReadFile(m)
				if c.Report == ReportSBOM {
					if !strings.Contains(string(data), `"components"`) {
						t.Errorf("%s: no components in the bill of materials", c.ID)
					}
					continue
				}
				data, _, err := Adapt(data)
				if err != nil {
					t.Fatalf("%s: %v", c.ID, err)
				}
				rep, err := findings.Parse(ctx, data, findings.Limits{MaxHelpBytes: 16 << 10})
				if err != nil {
					t.Fatalf("%s: %s: %v", c.ID, m, err)
				}
				all = append(all, Normalize(c.ID, rep)...)
			}
		}
		return all
	}
	want := func(t *testing.T, fs []Finding, rule string) *Finding {
		t.Helper()
		f := find(fs, rule)
		if f == nil {
			t.Fatalf("no %s among %v", rule, rules(fs))
		}
		return f
	}
	for _, c := range Checks() {
		if !c.Available() {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			env := map[string]string{}
			if c.ID == "license" {
				env["STANDING_LICENSE_DENY"] = "BSD-3-Clause"
			}
			fs := run(c, env)
			switch c.ID {
			case "secrets":
				f := want(t, fs, "github-pat")
				if f.File != "config/settings.env" || f.Kind != KindSecret || strings.Contains(f.Title, "aB3d") {
					t.Errorf("%+v", f)
				}
			case "vuln-deps":
				f := want(t, fs, "CVE-2023-45288")
				if f.Package != "golang.org/x/net" || f.Version != "0.17.0" || f.FixedIn == "" || f.File != "go/go.mod" {
					t.Errorf("%+v", f)
				}
				var js, py bool
				for _, f := range fs {
					js = js || f.Package == "lodash"
					py = py || f.Package == "flask" || f.Package == "pyyaml"
				}
				if !js || !py {
					t.Errorf("lodash %v, python packages %v", js, py)
				}
			case "vuln-reach-go":
				reached := false
				for _, f := range fs {
					reached = reached || f.Reachable == "yes"
				}
				if !reached || find(fs, "GO-2024-3333") == nil {
					t.Errorf("no reachable finding among %v", rules(fs))
				}
				if f := find(fs, "GO-2024-3333"); f.File != "go/go.mod" && !strings.HasPrefix(f.File, "go/") {
					t.Errorf("a module's paths were not given from the repository root: %s", f.File)
				}
			case "sast-go":
				want(t, fs, "G401")
				if f := want(t, fs, "G204"); !strings.HasPrefix(f.File, "go/") {
					t.Errorf("path %s", f.File)
				}
			case "sast-python":
				want(t, fs, "S602")
			case "lint-python":
				if len(fs) == 0 {
					t.Error("no lint findings")
				}
			case "license":
				f := want(t, fs, "license-denied")
				if f.Package == "" {
					t.Errorf("%+v", f)
				}
			case "dockerfile":
				want(t, fs, "DL3007")
			case "misconfig":
				if len(fs) < 2 {
					t.Errorf("only %d misconfigurations: %v", len(fs), rules(fs))
				}
			case "k8s-practice":
				want(t, fs, "latest-tag")
				want(t, fs, "privileged-container")
			case "shell":
				want(t, fs, "SC2045")
			case "pipeline-lint":
				for _, r := range []string{"unsafe-pipe-to-shell", "unpinned-image", "secret-in-pipeline"} {
					f := want(t, fs, r)
					if f.File != ".launchway/pipeline.yaml" || f.Line == 0 {
						t.Errorf("%+v", f)
					}
				}
			}
		})
	}
	_ = time.Now
}

func tail(s string) string {
	if len(s) > 1500 {
		return "..." + s[len(s)-1500:]
	}
	return s
}
