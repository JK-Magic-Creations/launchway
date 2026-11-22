package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every SQL statement in this package that names a Base table says base_id.
// It is the cheap, static half of "no query path lacks scope": the type
// system makes a Scope necessary to call a method, this makes the method use
// it, and row-level security catches a mistake this cannot see.
func TestEveryStatementOnABaseTableNamesBaseID(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	table := regexp.MustCompile(`\b(?:FROM|JOIN|INTO|UPDATE)\s+([a-z_]+)`)
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		// The migration step and the policy generator are the schema itself.
		if strings.HasSuffix(name, "_test.go") || name == "contract.go" || name == "rls.go" {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		// A statement is a string literal or a + chain of them.
		seen := map[ast.Node]bool{}
		ast.Inspect(f, func(n ast.Node) bool {
			if seen[n] {
				return false
			}
			var text string
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if x.Op != token.ADD {
					return true
				}
				var leaves []string
				ast.Inspect(x, func(m ast.Node) bool {
					seen[m] = true
					if l, ok := m.(*ast.BasicLit); ok && l.Kind == token.STRING {
						s, _ := strconv.Unquote(l.Value)
						leaves = append(leaves, s)
					}
					return true
				})
				text = strings.Join(leaves, " ")
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					return true
				}
				text, _ = strconv.Unquote(x.Value)
			default:
				return true
			}
			if !regexp.MustCompile(`^\s*(SELECT|INSERT|UPDATE|DELETE|WITH)\b`).MatchString(text) {
				return true
			}
			for _, m := range table.FindAllStringSubmatch(text, -1) {
				tb, ok := Lookup(m[1])
				if !ok || tb.Class != Scoped {
					continue
				}
				checked++
				if !strings.Contains(text, "base_id") {
					t.Errorf("%s: a statement on %s does not say base_id:\n%s", fset.Position(n.Pos()), m[1], strings.TrimSpace(text))
				}
			}
			return true
		})
	}
	if checked < 40 {
		t.Errorf("only %d statements on Base tables were found; the check is not looking at the code", checked)
	}
}

// A Scope cannot be written outside this package: it has no exported field,
// so the zero Scope is the only one a caller can make, and it is not valid.
func TestTheZeroScopeIsNotValid(t *testing.T) {
	var s Scope
	if s.Valid() || s.ID() != "" {
		t.Fatal("the zero Scope names a Base")
	}
	env := &Env{}
	if err := env.Do(s, false, func(*Q) error { t.Fatal("ran with no Base"); return nil }); err == nil {
		t.Fatal("a zero Scope was accepted")
	}
}

func TestRegistryHasNoDuplicatesAndPendingNamesAStage(t *testing.T) {
	seen := map[string]bool{}
	for _, tb := range Tables {
		if seen[tb.Name] {
			t.Errorf("%s is listed twice", tb.Name)
		}
		seen[tb.Name] = true
		if tb.Class == Pending && !regexp.MustCompile(`^S[2-9]$`).MatchString(tb.Stage) {
			t.Errorf("pending table %s has stage %q", tb.Name, tb.Stage)
		}
	}
}
