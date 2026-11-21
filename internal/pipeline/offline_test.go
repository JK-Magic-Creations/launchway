package pipeline

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Offline expansion gives what a server's expansion gives, from the same
// library directory, for every example pipeline of the example library.
func TestOfflineExpansionMatchesTheServersOnTheExampleLibrary(t *testing.T) {
	lib := filepath.Join("..", "..", "examples", "library")
	files, _ := filepath.Glob(filepath.Join(lib, "pipelines", "*.yaml"))
	if len(files) == 0 {
		t.Fatal("no example pipelines")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		dirs := map[string]string{}
		for _, l := range p.Libraries {
			dirs[l.Name] = lib
		}
		got, err := ExpandOffline(data, dirs, BindOptions{})
		if err != nil {
			t.Fatalf("%s offline: %v", f, err)
		}
		// What a server does with the same text once it has resolved the
		// libraries: Preview, set the commits, ExpandWith.
		want, err := Preview(p, BindOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range want.Libraries {
			l.Commit, l.RefKind = OfflineCommit, "directory"
		}
		want, _, err = ExpandWith(want, func(l *Library, name string) ([]byte, error) {
			return os.ReadFile(filepath.Join(lib, TemplatePath(name)))
		}, nil)
		if err != nil {
			t.Fatalf("%s server: %v", f, err)
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(want)
		if string(a) != string(b) || len(got.Steps) < len(p.Steps) {
			t.Errorf("%s: offline and server expansions differ", f)
		}
	}
}

func TestOfflineExpansionSaysWhatIsWrong(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl := "template: broken\nparams:\n  name: { type: string }\nsteps:\n  a: { image: alpine, run: echo, nope: 1 }\n"
	if err := os.WriteFile(filepath.Join(dir, "templates", "broken.yaml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	pl := "pipeline: p\nlibraries:\n  platform: { repo: lib, version: v1 }\nsteps:\n  s: { uses: platform/%s, with: { name: x } }\n"
	cases := []struct {
		name, uses string
		dirs       map[string]string
		want       string
	}{
		{"no directory", "broken", nil, "give its directory with --library platform=DIRECTORY"},
		{"not a library", "broken", map[string]string{"platform": t.TempDir()}, "takes the root of a library"},
		{"no template", "missing", map[string]string{"platform": dir}, "missing"},
		{"a mistake in the template", "broken", map[string]string{"platform": dir}, `unknown key "nope"`},
		{"a library the pipeline lacks", "broken", map[string]string{"platform": dir, "other": dir}, "the pipeline has no library called other"},
	}
	for _, c := range cases {
		_, err := ExpandOffline([]byte(strings.Replace(pl, "%s", c.uses, 1)), c.dirs, BindOptions{})
		var ps Problems
		if !errors.As(err, &ps) || !strings.Contains(ps.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
	if _, err := ParseLibraryFlags([]string{"platform"}); err == nil {
		t.Error("a flag without a directory was accepted")
	}
	if got, err := ParseLibraryFlags([]string{"platform=/x", "ci=/y"}); err != nil || got["ci"] != "/y" {
		t.Errorf("%v %v", got, err)
	}
}
