package gitexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exempt may keep a plain call: test support code, which builds repositories
// for tests and is never part of the product.
var exempt = map[string]bool{"internal/testlib": true}

// TestNoOtherPackageRunsGit fails when production code outside this package
// starts git itself (exec.Command or exec.CommandContext with "git") or
// imports a Go Git library.
func TestNoOtherPackageRunsGit(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var found []string
	for _, top := range []string{"cmd", "internal", "scripts"} {
		filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			dir := filepath.ToSlash(filepath.Dir(rel))
			if dir == "internal/gitexec" || exempt[dir] {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Errorf("%s: %v", rel, err)
				return nil
			}
			for _, imp := range f.Imports {
				if strings.Contains(imp.Path.Value, "go-git") {
					found = append(found, rel+": imports "+imp.Path.Value+" (go-git is banned)")
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "exec" {
					return true
				}
				for _, a := range call.Args {
					if lit, ok := a.(*ast.BasicLit); ok && lit.Value == `"git"` {
						found = append(found, rel+": "+fset.Position(call.Pos()).String()+" starts git; use internal/gitexec")
					}
				}
				return true
			})
			return nil
		})
	}
	for _, f := range found {
		t.Error(f)
	}
}
