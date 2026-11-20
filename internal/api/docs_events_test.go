package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// auditEventsDoc is the reference page that lists the audit trail's actions.
const auditEventsDoc = "../../docs/reference/events.md"

// auditActionsInCode returns every action the package writes to the audit
// trail: the audit field of each route, string literals given as the
// Action of an engine.AuditEntry, and string literals passed as the action
// argument of hookDenied.
func auditActionsInCode(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, rt := range (&Server{}).routes() {
		if rt.audit != "" {
			out[rt.audit] = true
		}
	}
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	str := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", p, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				sel, ok := n.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "AuditEntry" {
					return true
				}
				for _, el := range n.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Action" {
						continue
					}
					if s, ok := str(kv.Value); ok {
						out[s] = true
					}
				}
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "hookDenied" && len(n.Args) >= 2 {
					if s, ok := str(n.Args[1]); ok {
						out[s] = true
					} else {
						t.Errorf("%s: hookDenied is given an action that is not a string literal; extend this test to find it",
							fset.Position(n.Pos()))
					}
				}
			}
			return true
		})
	}
	return out
}

// auditActionsInDoc returns the names in rows "| `name` |" under the
// "## Audit trail" heading of the page.
func auditActionsInDoc(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(auditEventsDoc)
	if err != nil {
		t.Fatalf("reading %s: %v", auditEventsDoc, err)
	}
	row := regexp.MustCompile("^\\| `([^`]+)` \\|")
	names := map[string]bool{}
	in, found := false, false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == "Audit trail"
			found = found || in
			continue
		}
		if m := row.FindStringSubmatch(line); in && m != nil {
			names[m[1]] = true
		}
	}
	if !found {
		t.Fatalf("%s has no section \"## Audit trail\"", auditEventsDoc)
	}
	return names
}

// Every audit action the API records is in docs/reference/events.md, and
// the page names no action that the code does not record.
func TestDocsEventsAuditActions(t *testing.T) {
	code := auditActionsInCode(t)
	for _, want := range []string{"run.start", "token.use", "webhook.git", "webhook.run", "auth.sign_in", "auth.sign_out"} {
		if !code[want] {
			t.Fatalf("the test no longer finds the audit action %q in the code; fix how it looks for them", want)
		}
	}
	doc := auditActionsInDoc(t)
	for name := range code {
		if !doc[name] {
			t.Errorf("audit action %q is not in %s: add a row \"| `%s` | ...\" under ## Audit trail", name, auditEventsDoc, name)
		}
	}
	for name := range doc {
		if strings.Contains(name, ".") && !code[name] {
			t.Errorf("%s lists the audit action %q, which the code does not record", auditEventsDoc, name)
		}
	}
}
