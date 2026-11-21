package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

// The reference documentation of the HTTP API and of roles must match the
// route table. docs/reference/http-api.md lists every route as a table row
// of exactly four cells:
//
//	| `METHOD /path` | `access-class` | `audit.action` or — | what it does |
//
// and docs/reference/roles.md names every access class.

const (
	httpAPIDoc = "../../docs/reference/http-api.md"
	rolesDoc   = "../../docs/reference/roles.md"
)

// docAccessNames are the names the documentation gives the access classes.
// A new access class needs a name here and a row in both pages.
var docAccessNames = map[access]string{
	public:         "public",
	runnerOnly:     "runner",
	signedIn:       "signed-in",
	readAny:        "any-viewer",
	projectRead:    "project-viewer",
	projectOperate: "project-operator",
	projectDraft:   "project-draft",
	runRead:        "run-viewer",
	runOperate:     "run-operator",
	administer:     "admin",
	repoRead:       "repo-viewer",
	repoWrite:      "repo-operator",
	repoAdmin:      "repo-admin",
}

type docRoute struct {
	access, audit, text string
	line                int
}

var routeRowRe = regexp.MustCompile("^`((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|LOCK|UNLOCK) /[^`]*)`$")

// docRoutes reads the route rows of http-api.md.
func docRoutes(t *testing.T) map[string]docRoute {
	t.Helper()
	data, err := os.ReadFile(httpAPIDoc)
	if err != nil {
		t.Fatalf("reading the HTTP API reference: %v", err)
	}
	out := map[string]docRoute{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 4 {
			continue
		}
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		m := routeRowRe.FindStringSubmatch(cells[0])
		if m == nil {
			continue
		}
		if prev, dup := out[m[1]]; dup {
			t.Errorf("http-api.md:%d lists %q again (first at line %d)", i+1, m[1], prev.line)
		}
		audit := strings.Trim(cells[2], "`")
		if audit == "—" {
			audit = ""
		}
		out[m[1]] = docRoute{access: strings.Trim(cells[1], "`"), audit: audit, text: cells[3], line: i + 1}
	}
	return out
}

// TestDocsReferenceRoutes fails when a route is missing from http-api.md,
// when the page lists a route that does not exist, or when its access class
// or audit action differs from the route table.
func TestDocsReferenceRoutes(t *testing.T) {
	doc := docRoutes(t)
	seen := map[string]bool{}
	for _, rt := range (&Server{}).routes() {
		seen[rt.pattern] = true
		name, ok := docAccessNames[rt.access]
		if !ok {
			t.Errorf("route %q has an access class with no name in docAccessNames (docs_reference_test.go); name it and document it in http-api.md and roles.md", rt.pattern)
			continue
		}
		d, ok := doc[rt.pattern]
		if !ok {
			t.Errorf("route %q is not in docs/reference/http-api.md; add a row: | `%s` | `%s` | %s | ... |", rt.pattern, rt.pattern, name, auditCell(rt.audit))
			continue
		}
		if d.access != name {
			t.Errorf("http-api.md:%d gives %q the access class %q; the route table says %q", d.line, rt.pattern, d.access, name)
		}
		if d.audit != rt.audit {
			t.Errorf("http-api.md:%d gives %q the audit action %q; the route table says %q", d.line, rt.pattern, d.audit, rt.audit)
		}
	}
	for p, d := range doc {
		if !seen[p] {
			t.Errorf("http-api.md:%d lists %q, which is not a route", d.line, p)
		}
	}
	if len(doc) == 0 {
		t.Error("http-api.md has no route rows; the convention is | `METHOD /path` | `class` | `action` or — | text |")
	}
}

func auditCell(a string) string {
	if a == "" {
		return "—"
	}
	return "`" + a + "`"
}

// TestDocsReferenceRoles fails when roles.md or http-api.md does not name an
// access class, or roles.md does not name a role.
func TestDocsReferenceRoles(t *testing.T) {
	for _, path := range []string{rolesDoc, httpAPIDoc} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		text := string(data)
		for _, name := range docAccessNames {
			if !strings.Contains(text, "| `"+name+"` |") {
				t.Errorf("%s has no table row for the access class `%s`", path, name)
			}
		}
	}
	data, err := os.ReadFile(rolesDoc)
	if err != nil {
		t.Fatal(err)
	}
	for r := auth.Viewer; r <= auth.Admin; r++ {
		if role := r.String(); !strings.Contains(string(data), "`"+role+"`") {
			t.Errorf("roles.md does not name the role `%s`", role)
		}
	}
}
