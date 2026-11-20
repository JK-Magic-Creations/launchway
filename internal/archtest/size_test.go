package archtest

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Limits of the size ratchet (engineering standard, "Small units").
const (
	maxFileLines = 1000
	maxFuncLines = 150
)

const sizeBaselinePath = "quality/size-baseline.json"

// sizeBaseline records the files and functions that were over the limit when
// the ratchet was introduced, with their size then. They may not grow; they
// may shrink (run `make ratchet-update`); nothing new may join them.
type sizeBaseline struct {
	Comment   string         `json:"comment"`
	MaxFile   int            `json:"max_file_lines"`
	MaxFunc   int            `json:"max_func_lines"`
	Files     map[string]int `json:"files"`
	Functions map[string]int `json:"functions"`
}

// measure returns the lines of every source file over the file limit (Go
// outside tests, and the web's .ts/.tsx) and of every Go function over the
// function limit. Functions are keyed by file, receiver and name so that
// edits elsewhere in the file do not move them.
func measure(t *testing.T) (files, funcs map[string]int) {
	t.Helper()
	root := repoRoot(t)
	files, funcs = map[string]int{}, map[string]int{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd", "scripts", "web/src"} {
		dir := filepath.Join(root, top)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			isGo := strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
			isWeb := strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx")
			if !isGo && !isWeb {
				return nil
			}
			if isWeb && (strings.Contains(rel, ".test.") || strings.Contains(rel, ".d.ts")) {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if n := strings.Count(string(src), "\n"); n > maxFileLines {
				files[rel] = n
			}
			if !isGo {
				return nil
			}
			f, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return err
			}
			seen := map[string]int{}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				lines := fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1
				if lines <= maxFuncLines {
					continue
				}
				key := rel + ":" + funcName(fn)
				if n := seen[key]; n > 0 {
					key = fmt.Sprintf("%s#%d", key, n+1)
				}
				seen[key]++
				funcs[key] = lines
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files, funcs
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	switch x := typ.(type) {
	case *ast.Ident:
		return x.Name + "." + fn.Name.Name
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name + "." + fn.Name.Name
		}
	}
	return fn.Name.Name
}

func readSizeBaseline(t *testing.T) (sizeBaseline, string) {
	t.Helper()
	path := filepath.Join(repoRoot(t), sizeBaselinePath)
	var b sizeBaseline
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (create it with `make ratchet-update`)", sizeBaselinePath, err)
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("%s: %v", sizeBaselinePath, err)
	}
	return b, path
}

// TestSizeRatchet fails when a file over 1,000 lines or a function over 150
// lines is new, or when one already in the baseline grew. When something
// shrank or was split, run `make ratchet-update` to lower the baseline.
func TestSizeRatchet(t *testing.T) {
	files, funcs := measure(t)
	base, path := readSizeBaseline(t)

	if os.Getenv("UPDATE_RATCHET") == "1" {
		updateSizeBaseline(t, path, base, files, funcs)
		return
	}

	check := func(kind string, current, recorded map[string]int, limit int) {
		var names []string
		for name := range current {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			was, listed := recorded[name]
			switch {
			case !listed:
				t.Errorf("%s %s is %d lines, over the limit of %d. Split it; do not add it to the baseline", kind, name, current[name], limit)
			case current[name] > was:
				t.Errorf("%s %s grew from %d to %d lines (limit %d, baseline allows %d). Move the new code to a new file or function", kind, name, was, current[name], limit, was)
			}
		}
		for name, was := range recorded {
			if cur, ok := current[name]; !ok {
				t.Logf("%s %s is now under the limit (was %d): run `make ratchet-update` to shrink the baseline", kind, name, was)
			} else if cur < was {
				t.Logf("%s %s shrank from %d to %d: run `make ratchet-update` to lower the baseline", kind, name, was, cur)
			}
		}
	}
	check("file", files, base.Files, maxFileLines)
	check("function", funcs, base.Functions, maxFuncLines)
}

// updateSizeBaseline rewrites the baseline. It refuses to record growth or a
// new entry, unless the baseline is empty (first creation).
func updateSizeBaseline(t *testing.T, path string, old sizeBaseline, files, funcs map[string]int) {
	t.Helper()
	first := len(old.Files) == 0 && len(old.Functions) == 0
	if !first {
		for n, v := range files {
			if was, ok := old.Files[n]; !ok || v > was {
				t.Fatalf("refusing to update: file %s is new or grew; the baseline may only shrink", n)
			}
		}
		for n, v := range funcs {
			if was, ok := old.Functions[n]; !ok || v > was {
				t.Fatalf("refusing to update: function %s is new or grew; the baseline may only shrink", n)
			}
		}
	}
	nb := sizeBaseline{
		Comment:   "Files over max_file_lines and functions over max_func_lines that predate the size ratchet. They may not grow and nothing may join them. Lower with `make ratchet-update`.",
		MaxFile:   maxFileLines,
		MaxFunc:   maxFuncLines,
		Files:     files,
		Functions: funcs,
	}
	out, err := json.MarshalIndent(nb, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("size baseline written: %d files, %d functions", len(files), len(funcs))
}
