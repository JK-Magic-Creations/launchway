package starters_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/starters"
)

// expandStarter expands a pipeline file that uses the built-in library, as a
// server does when it creates a run, with no server.
func expandStarter(t testing.TB, text string) (*pipeline.Pipeline, error) {
	t.Helper()
	p, err := pipeline.Parse([]byte(text))
	if err != nil {
		return nil, err
	}
	p, err = pipeline.Bind(p, pipeline.PreviewValues(p), pipeline.BindOptions{Preview: true})
	if err != nil {
		return nil, err
	}
	for _, l := range p.Libraries {
		l.Commit = starters.Commit()
	}
	out, _, err := pipeline.ExpandWith(p, func(_ *pipeline.Library, name string) ([]byte, error) {
		if b, ok := starters.Template(name); ok {
			return b, nil
		}
		return nil, pipeline.ErrNoTemplate
	}, nil)
	return out, err
}

func use(name, with string) string {
	return "pipeline: demo\nsteps:\n  ci:\n    uses: launchway/" + name + "\n" + with
}

// Every template reads cleanly, and every flight plan (and every step
// template) expands with nothing but its defaults, which is how most
// repositories use them.
func TestEveryTemplateExpandsWithDefaults(t *testing.T) {
	for _, name := range starters.Templates() {
		t.Run(name, func(t *testing.T) {
			data, _ := starters.Template(name)
			info := pipeline.DescribeTemplate(data, name)
			if len(info.Problems) > 0 {
				t.Fatalf("problems in the template: %v", info.Problems)
			}
			if info.Description == "" {
				t.Error("a template needs a description")
			}
			for _, p := range info.Params {
				if p.Description == "" {
					t.Errorf("parameter %s has no description", p.Name)
				}
				// The deliver maneuver is used by the flight plans, which give it
				// the image they built; it has no meaning without one.
				if p.Default == nil && p.DefaultFrom == nil && name != "deliver" {
					t.Errorf("parameter %s has no default: a starter must work with nothing passed in", p.Name)
				}
			}
			if name == "deliver" {
				return
			}
			p, err := expandStarter(t, use(name, ""))
			if err != nil {
				t.Fatalf("expanding: %v", err)
			}
			if len(p.Steps) == 0 {
				t.Fatal("no steps")
			}
		})
	}
}

// Every script a template runs is in the library, and every script parses.
func TestScriptsExistAndParse(t *testing.T) {
	re := regexp.MustCompile(`\$LAUNCHWAY_LIB/(scripts/[A-Za-z0-9_./-]+\.sh)`)
	for _, name := range starters.Templates() {
		data, _ := starters.Template(name)
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			if _, err := starters.File(m[1]); err != nil {
				t.Errorf("template %s runs %s, which the library does not have", name, m[1])
			}
		}
	}
	dir := t.TempDir()
	for _, f := range starters.List() {
		if !strings.HasSuffix(f, ".sh") {
			continue
		}
		data, _ := starters.File(f)
		path := filepath.Join(dir, strings.ReplaceAll(f, "/", "_"))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %s", f, out)
		}
	}
}

func TestCommitIsStableAndLooksLikeACommit(t *testing.T) {
	if c := starters.Commit(); !pipeline.IsCommitID(c) || c != starters.Commit() {
		t.Fatalf("commit %q", c)
	}
	var n int
	_ = fs.WalkDir(starters.FS(), ".", func(string, fs.DirEntry, error) error { n++; return nil })
	if n < 10 {
		t.Fatal("the library is empty")
	}
}

func TestUnknownStarterNamesTheOnesThatExist(t *testing.T) {
	_, err := expandStarter(t, use("go-servce", ""))
	var ps pipeline.Problems
	if !errors.As(err, &ps) || !strings.Contains(ps.Error(), "no template go-servce") {
		t.Fatalf("%v", err)
	}
}

// The library's own unit tests of its scripts (tests/run.sh), run from a copy
// of the files exactly as a runner would write them.
func TestScriptsPassTheirOwnTests(t *testing.T) {
	dir := t.TempDir()
	for _, f := range starters.List() {
		data, _ := starters.File(f)
		path := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if starters.IsExecutable(f) {
			mode = 0o755
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", filepath.Join(dir, "tests", "run.sh"))
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "skip") {
			t.Log(l)
		}
	}
}
