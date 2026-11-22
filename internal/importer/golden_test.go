package importer

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// loadCase reads the files of a fixture directory: every file below in/.
func loadCase(t testing.TB, dir string) []File {
	t.Helper()
	var files []File
	root := filepath.Join(dir, "in")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		files = append(files, File{Path: filepath.ToSlash(rel), Content: b})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// validates runs the product's own validator over the text.
func validates(text string) error {
	_, err := source.ExpandOffline([]byte(text), nil, pipeline.BindOptions{}, pipeline.WithProjectFacts(nil, pipeline.ProjectFacts{Project: "shop"}))
	return err
}

func render(res *Result) string {
	var b strings.Builder
	for _, p := range res.Plans {
		fmt.Fprintf(&b, "=== %s (%s)\n%s\n--- findings\n", strings.Join(p.Files, ","), p.Format, p.Pipeline)
		for _, f := range p.Findings {
			fmt.Fprintf(&b, "%-9s %-28s %s:%d %s", f.Class, f.Code, f.File, f.Line, f.Message)
			if f.Next != "" {
				fmt.Fprintf(&b, " -> %s", f.Next)
			}
			b.WriteString("\n")
		}
		if len(p.Secrets) > 0 {
			fmt.Fprintf(&b, "secrets: %s\n", strings.Join(p.Secrets, ", "))
		}
	}
	return b.String()
}

func TestGolden(t *testing.T) {
	cases, _ := filepath.Glob("testdata/*/*/in")
	if len(cases) == 0 {
		t.Fatal("no fixtures")
	}
	sort.Strings(cases)
	for _, c := range cases {
		dir := filepath.Dir(c)
		name := filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			files := loadCase(t, dir)
			opts := Options{}
			if _, err := os.Stat(filepath.Join(dir, "dry")); err == nil {
				opts.Dry = true
			}
			res, err := Import(Request{Files: files, Options: opts})
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range res.Plans {
				if p.Pipeline == "" {
					continue
				}
				if err := validates(p.Pipeline); err != nil {
					t.Errorf("the proposed pipeline for %v does not validate: %v\n%s", p.Files, err, numbered(p.Pipeline))
				}
			}
			got := render(res)
			gold := filepath.Join(dir, "golden.txt")
			if *update {
				if err := os.WriteFile(gold, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(gold)
			if err != nil {
				t.Fatalf("no golden file: run go test -update (%v)", err)
			}
			if string(want) != got {
				t.Errorf("output differs from %s (run go test -update to accept):\n%s", gold, got)
			}
		})
	}
}

func numbered(s string) string {
	var b strings.Builder
	for i, l := range strings.Split(s, "\n") {
		fmt.Fprintf(&b, "%3d %s\n", i+1, l)
	}
	return b.String()
}

// Every construct of every reader's table has a fixture that exercises it, so the
// guide's table is never ahead of what is tested.
func TestEveryConstructHasAFixture(t *testing.T) {
	for _, f := range formats {
		used := map[string]bool{}
		files, _ := filepath.Glob("testdata/" + f.ID + "/*/golden.txt")
		for _, g := range files {
			b, _ := os.ReadFile(g)
			for _, c := range f.Table {
				if strings.Contains(string(b), " "+c.Code+" ") {
					used[c.Code] = true
				}
			}
		}
		for _, c := range f.Table {
			if !used[c.Code] {
				t.Errorf("%s: no fixture exercises %s (%s)", f.ID, c.Code, c.Name)
			}
		}
		if bad := CheckTable(f.Table); len(bad) > 0 {
			t.Errorf("%s: %v", f.ID, bad)
		}
	}
	if bad := CheckTable(writerTable); len(bad) > 0 {
		t.Errorf("writer: %v", bad)
	}
}
