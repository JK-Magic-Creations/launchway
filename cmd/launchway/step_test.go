package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func capture(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

const stepV1 = "step: notes\ncontract: 1\nimage: i\ncommand: c\ninputs:\n  title: { type: string, default: x }\npermissions:\n  network: [registry.example.org]\n"

// launchway validate says what a library's step may do, and, for a library that is a Git checkout, which
// newer version asks for more.
func TestValidateCallsOutAWiderStepInANewerVersion(t *testing.T) {
	lib := t.TempDir()
	git(t, lib, "init", "-q", "-b", "main")
	write := func(text string) {
		os.MkdirAll(filepath.Join(lib, "steps", "notes"), 0o755)
		os.WriteFile(filepath.Join(lib, "steps", "notes", "step.yaml"), []byte(text), 0o644)
		git(t, lib, "add", "-A")
		git(t, lib, "commit", "-qm", "x")
	}
	write(stepV1)
	git(t, lib, "tag", "v1")
	write(strings.Replace(stepV1, "[registry.example.org]", "[registry.example.org, evil.example.net]", 1) + "  write: [dist]\n")
	git(t, lib, "tag", "v2")
	// The library as a person has it checked out: at the version the pipeline uses.
	git(t, lib, "checkout", "-q", "v1")
	text := "pipeline: p\nlibraries:\n  platform: { repo: tools, version: v1 }\nsteps:\n  n: { uses: platform/notes }\n"
	p, err := source.ExpandOffline([]byte(text), map[string]string{"platform": lib}, pipeline.BindOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() { printStepNotices(p, map[string]string{"platform": lib}) })
	for _, want := range []string{"library platform has v2 (you use v1)", "ASKS FOR MORE: permission: the step may now reach evil.example.net", "may now write dist"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing from:\n%s", want, out)
		}
	}
	if s := p.Steps[0]; s.Custom == nil || !strings.Contains(s.Custom.Permissions(), "reach registry.example.org") {
		t.Errorf("%+v", s.Custom)
	}
}

func TestStepNewThenValidateAgainstItsOwnHistory(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	if _, err := stepScaffoldInto(repo); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "first")
	git(t, repo, "tag", "v1")
	old, _ := os.Getwd()
	defer os.Chdir(old)
	os.Chdir(repo)
	out := capture(t, func() {
		if err := stepValidate([]string{"--against", "v1"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "nothing changed since v1") || !strings.Contains(out, "This step can reach no network") {
		t.Errorf("%s", out)
	}
	// Widening the permissions is called out.
	m := filepath.Join(repo, "steps", "hello", "step.yaml")
	b, _ := os.ReadFile(m)
	os.WriteFile(m, append(b, []byte("permissions:\n  network: [example.org]\n")...), 0o644)
	out = capture(t, func() { _ = stepValidate([]string{"--against", "v1"}) })
	if !strings.Contains(out, "WIDENS: permission: the step may now reach example.org") {
		t.Errorf("%s", out)
	}
}

func stepScaffoldInto(repo string) (string, error) {
	return scaffoldFor(repo, "hello", "sh")
}
