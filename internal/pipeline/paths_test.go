package pipeline

import (
	"strings"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		pat, file string
		want      bool
	}{
		{"src/**", "src/a/b.go", true},
		{"src/**", "src", true}, // ** matches no directories too
		{"src/**", "srcx/a.go", false},
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/a/b.md", true},
		{"*.md", "docs/a.md", false}, // * stops at /
		{"docs/", "docs/a/b.md", true},
		{"/go.mod", "go.mod", true},
		{"go.*", "go.sum", true},
		{"README.md", "readme.md", false}, // case matters
		{"cmd/*/main.go", "cmd/x/main.go", true},
		{"cmd/*/main.go", "cmd/x/y/main.go", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
	} {
		if got := GlobMatch(tc.pat, tc.file); got != tc.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", tc.pat, tc.file, got, tc.want)
		}
	}
}

func TestSelectsWithNegation(t *testing.T) {
	list := []string{"docs/**", "!docs/api/**", "docs/api/keep.md"}
	for file, want := range map[string]bool{"docs/a.md": true, "docs/api/x.md": false, "docs/api/keep.md": true, "src/a.go": false} {
		if got := Selects(list, file); got != want {
			t.Errorf("%s: %v, want %v", file, got, want)
		}
	}
	// A list that starts with a negation starts with everything selected.
	if !Selects([]string{"!vendor/**"}, "src/a.go") || Selects([]string{"!vendor/**"}, "vendor/x.go") {
		t.Error("a list of only negations selects everything but those")
	}
}

func TestPathFilterDecide(t *testing.T) {
	known := func(files ...string) *ChangeSet { return &ChangeSet{Known: true, Files: files} }
	paths := PathFilter{Paths: []string{"src/**", "go.mod"}}
	ignore := PathFilter{Ignore: []string{"docs/**", "*.md"}}
	for _, tc := range []struct {
		name string
		f    PathFilter
		cs   *ChangeSet
		run  bool
		why  string
	}{
		{"a source file", paths, known("README.md", "src/a.go"), true, ""},
		{"docs only", paths, known("README.md", "docs/a.md"), false, "no changed file matches paths (src/**, go.mod); changed: README.md, docs/a.md"},
		{"ignore: something else changed", ignore, known("docs/a.md", "src/a.go"), true, ""},
		{"ignore: only docs", ignore, known("docs/a.md", "README.md"), false, "every changed file matches paths_ignore"},
		{"unknown change always runs", paths, &ChangeSet{Why: "first push"}, true, ""},
		{"no change set always runs", paths, nil, true, ""},
		{"an empty commit runs", paths, known(), true, ""},
		{"no filter", PathFilter{}, known("x"), true, ""},
	} {
		run, why := tc.f.Decide(tc.cs)
		if run != tc.run || !strings.Contains(why, tc.why) {
			t.Errorf("%s: run %v why %q", tc.name, run, why)
		}
	}
}

const filtered = `
pipeline: app
on:
  push: { branches: [main], paths: [src/**, go.mod] }
  pull_request: { paths_ignore: ["docs/**"] }
steps:
  build: {image: x, run: y}
  image: {image: x, run: y, needs: build, when: { changed: [src/**, Dockerfile] }}
`

func TestPathKeysParse(t *testing.T) {
	p, err := Parse([]byte(filtered))
	if err != nil {
		t.Fatal(err)
	}
	if f := p.On.PushPaths(); len(f.Paths) != 2 || len(f.Ignore) != 0 {
		t.Fatalf("%+v", f)
	}
	if f := p.On.PullRequestPaths(); len(f.Ignore) != 1 {
		t.Fatalf("%+v", f)
	}
	if got := p.Step("image").When.Changed; len(got) != 2 {
		t.Fatalf("%v", got)
	}
	notes := strings.Join(p.PathNotes(), "\n")
	for _, w := range []string{"on.push: runs only when a changed file matches src/**, go.mod", "on.pull_request: runs unless every changed file matches docs/**", "step image: runs only when a changed file matches src/**, Dockerfile"} {
		if !strings.Contains(notes, w) {
			t.Errorf("notes lack %q:\n%s", w, notes)
		}
	}
}

func TestPathKeysAreChecked(t *testing.T) {
	for name, src := range map[string]string{
		"both":  "pipeline: a\non: { push: { branches: [main], paths: [a], paths_ignore: [b] } }\nsteps:\n  s: {image: x, run: y}\n",
		"empty": "pipeline: a\non: { push: { branches: [main], paths: [] } }\nsteps:\n  s: {image: x, run: y}\n",
		"up":    "pipeline: a\non: { pull_request: { paths: ['../x'] } }\nsteps:\n  s: {image: x, run: y}\n",
		"bad":   "pipeline: a\nsteps:\n  s: {image: x, run: y, when: { changed: ['[a'] }}\n",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: no problem reported", name)
		}
	}
}

func TestStepChangedCondition(t *testing.T) {
	c := &Condition{Changed: []string{"src/**"}}
	if !c.Match(Context{Event: EventPush, Changed: &ChangeSet{Known: true, Files: []string{"src/a"}}}) {
		t.Error("a matching file keeps the step")
	}
	ctx := Context{Event: EventPush, Changed: &ChangeSet{Known: true, Files: []string{"docs/a"}}}
	if c.Match(ctx) || !strings.HasPrefix(c.ChangedWhy(ctx), PathSkipPrefix+"no file under src/ changed") {
		t.Errorf("a docs-only change skips it: %q", c.ChangedWhy(ctx))
	}
	for _, unknown := range []*ChangeSet{nil, {Why: "force push"}} {
		if !c.Match(Context{Event: EventPush, Changed: unknown}) {
			t.Error("an unknown change runs every step")
		}
	}
	if !c.Match(Context{Event: EventDraft}) {
		t.Error("a draft run, whose change is not known, runs the step")
	}
}
