package engine

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

// eventsDoc is the reference page for run events and the live stream.
const eventsDoc = "../../docs/reference/events.md"

// docRowNames returns the names in the first column of table rows written
// as "| `name` |" in the section under heading (a "## " heading), up to the
// next "## " heading.
func docRowNames(t *testing.T, path, heading string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	row := regexp.MustCompile("^\\| `([^`]+)` \\|")
	names := map[string]bool{}
	in, found := false, false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == heading
			found = found || in
			continue
		}
		if m := row.FindStringSubmatch(line); in && m != nil {
			names[m[1]] = true
		}
	}
	if !found {
		t.Fatalf("%s has no section %q", path, "## "+heading)
	}
	return names
}

// goFiles parses the package's non-test Go files.
func goFiles(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", p, err)
		}
		files = append(files, f)
	}
	return fset, files
}

// codeEventTypes returns every constant of type EventType declared in the
// package, and every type eventSince knows.
func codeEventTypes(t *testing.T) map[string]bool {
	t.Helper()
	_, files := goFiles(t)
	out := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "EventType" {
					continue
				}
				for _, v := range vs.Values {
					if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, _ := strconv.Unquote(lit.Value)
						out[s] = true
					}
				}
			}
		}
	}
	for et := range eventSince {
		out[string(et)] = true
	}
	return out
}

// codeStreamTypes returns the Type of every hub.Message the package
// publishes.
func codeStreamTypes(t *testing.T) map[string]bool {
	t.Helper()
	_, files := goFiles(t)
	out := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := cl.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Message" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "hub" {
				return true
			}
			for _, el := range cl.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Type" {
					continue
				}
				lit, ok := kv.Value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("a hub.Message's Type is not a string literal; extend TestDocsReferenceStreamTypes to find it")
					continue
				}
				s, _ := strconv.Unquote(lit.Value)
				out[s] = true
			}
			return true
		})
	}
	return out
}

// Every run event type is in docs/reference/events.md, and the page names
// no event type that does not exist.
func TestDocsReferenceEventTypes(t *testing.T) {
	code := codeEventTypes(t)
	if len(code) < 20 {
		t.Fatalf("found only %d event types in the code; the test no longer finds them", len(code))
	}
	doc := docRowNames(t, eventsDoc, "Run events")
	for name := range code {
		if !doc[name] {
			t.Errorf("event type %q is not in %s: add a row \"| `%s` | ...\" under ## Run events", name, eventsDoc, name)
		}
	}
	for name := range doc {
		if strings.Contains(name, ".") && !code[name] {
			t.Errorf("%s lists the event type %q, which the code does not have", eventsDoc, name)
		}
	}
}

// Every live stream message type is in docs/reference/events.md, and the
// page names no type that does not exist.
func TestDocsReferenceStreamTypes(t *testing.T) {
	code := codeStreamTypes(t)
	if len(code) == 0 {
		t.Fatal("found no hub.Message in the code; the test no longer finds them")
	}
	doc := docRowNames(t, eventsDoc, "Live stream")
	for name := range code {
		if !doc[name] {
			t.Errorf("live stream message type %q is not in %s: add a row \"| `%s` | ...\" under ## Live stream", name, eventsDoc, name)
		}
	}
	for name := range doc {
		if !code[name] {
			t.Errorf("%s lists the live stream message type %q, which the code does not publish", eventsDoc, name)
		}
	}
}
