package archtest

// The wall between Bases, the source half (internal/store, docs/development/the-wall.md).
//
//   - SQL is written in internal/store. Outside it, every file that holds SQL
//     text, or imports database/sql, is on a legacy baseline
//     (quality/wall-baseline.json) with its count; the count may only fall,
//     and no file may join. Moving a table to the store removes lines.
//   - The doors that issue a store.Scope are called only from the files that
//     are listed here, so a new caller is a decision someone makes in review.

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const wallBaselinePath = "quality/wall-baseline.json"

type wallBaseline struct {
	Comment string `json:"comment"`
	// SQL is the number of SQL statements (string literals that begin with
	// SELECT, INSERT INTO, UPDATE, DELETE FROM, WITH x AS, REPLACE INTO or PRAGMA) in each file
	// outside internal/store that predates the wall.
	SQL map[string]int `json:"sql"`
	// Imports are the files outside internal/store that import database/sql.
	Imports []string `json:"database_sql_imports"`
}

var sqlStart = regexp.MustCompile(`^(SELECT\s|INSERT\s+INTO\b|UPDATE\s|DELETE\s+FROM\b|WITH\s+\w+\s+AS\b|REPLACE\s+INTO\b|PRAGMA\s)`)

// wallExempt are files whose SQL is the schema itself (migrations): they run
// once, as the owner, and are checked by the schema tests.
func wallExempt(rel string) bool {
	return strings.HasPrefix(rel, "internal/store/") ||
		(strings.HasPrefix(rel, "internal/engine/schema") && !strings.HasPrefix(rel, "internal/engine/schema_"))
}

// scanWall counts SQL statements and database/sql imports per non-test Go
// file outside the store.
func scanWall(t *testing.T) (sqlCount map[string]int, imports []string) {
	t.Helper()
	root := repoRoot(t)
	sqlCount = map[string]int{}
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if wallExempt(rel) {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, im := range f.Imports {
				if p, _ := strconv.Unquote(im.Path.Value); p == "database/sql" {
					imports = append(imports, rel)
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err == nil && sqlStart.MatchString(strings.TrimLeft(s, " \t\r\n")) {
					sqlCount[rel]++
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(imports)
	return sqlCount, imports
}

func readWallBaseline(t *testing.T) (wallBaseline, string) {
	t.Helper()
	path := filepath.Join(repoRoot(t), wallBaselinePath)
	var b wallBaseline
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (create it with `make wall-update`)", wallBaselinePath, err)
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatalf("%s: %v", wallBaselinePath, err)
	}
	return b, path
}

// TestNoNewSQLOutsideTheStore fails when a file outside internal/store gains
// SQL it did not have, or a new file holds some, or imports database/sql. A
// file whose count fell may have the baseline lowered (`make wall-update`).
func TestNoNewSQLOutsideTheStore(t *testing.T) {
	sqlNow, importsNow := scanWall(t)
	base, path := readWallBaseline(t)

	if os.Getenv("UPDATE_RATCHET") == "1" {
		next := wallBaseline{Comment: base.Comment, SQL: map[string]int{}}
		if next.Comment == "" {
			next.Comment = "SQL outside internal/store that predates the wall between Bases. Counts may only fall and nothing may join (docs/development/the-wall.md). Lower with `make wall-update`."
		}
		for f, n := range sqlNow {
			was, listed := base.SQL[f]
			if len(base.SQL) > 0 && (!listed || n > was) {
				t.Fatalf("refusing to update: %s is new or grew (%d, was %d); the baseline may only shrink", f, n, was)
			}
			next.SQL[f] = n
		}
		known := map[string]bool{}
		for _, f := range base.Imports {
			known[f] = true
		}
		for _, f := range importsNow {
			if len(base.Imports) > 0 && !known[f] {
				t.Fatalf("refusing to update: %s newly imports database/sql", f)
			}
		}
		next.Imports = importsNow
		data, _ := json.MarshalIndent(next, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	var names []string
	for f := range sqlNow {
		names = append(names, f)
	}
	sort.Strings(names)
	for _, f := range names {
		was, listed := base.SQL[f]
		switch {
		case !listed:
			t.Errorf("%s holds %d SQL statements but is not in the baseline: SQL belongs in internal/store behind a store.Scope (docs/development/the-wall.md); do not add the file to %s", f, sqlNow[f], wallBaselinePath)
		case sqlNow[f] > was:
			t.Errorf("%s grew from %d to %d SQL statements: new queries go in internal/store, taking a store.Scope", f, was, sqlNow[f])
		case sqlNow[f] < was:
			t.Logf("%s shrank from %d to %d: run `make wall-update` to lower the baseline", f, was, sqlNow[f])
		}
	}
	for f, was := range base.SQL {
		if _, ok := sqlNow[f]; !ok {
			t.Logf("%s no longer holds SQL (was %d): run `make wall-update`", f, was)
		}
	}
	known := map[string]bool{}
	for _, f := range base.Imports {
		known[f] = true
	}
	for _, f := range importsNow {
		if !known[f] {
			t.Errorf("%s imports database/sql: only internal/store may, apart from the baseline in %s", f, wallBaselinePath)
		}
	}

	perPkg := map[string]int{}
	total := 0
	for f, n := range sqlNow {
		perPkg[filepath.ToSlash(filepath.Dir(f))] += n
		total += n
	}
	var pk []string
	for p := range perPkg {
		pk = append(pk, p)
	}
	sort.Strings(pk)
	for _, p := range pk {
		t.Logf("legacy SQL statements outside the store: %-28s %4d", p, perPkg[p])
	}
	t.Logf("legacy SQL statements outside the store: %d in %d files; %d files import database/sql", total, len(sqlNow), len(importsNow))
}

// doors are the store.Registry methods that issue a Scope.
var doors = map[string][]string{
	// The default Base: how the code that predates Bases acts. Only the
	// engine's one adapter calls it.
	"DefaultScope": {"internal/engine/wall.go"},
	// The router's doors (host name, path): no caller until Stage 8.
	"ScopeForHost":   {},
	"ScopeForHandle": {},
	// A background job acting in a named Base, and the loop over all of them.
	"ServiceScope": {},
	"ForEachBase":  {},
}

// TestScopeDoorsAreCalledOnlyFromTheirFiles lists who may obtain a Scope.
func TestScopeDoorsAreCalledOnlyFromTheirFiles(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/store/") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				allowed, isDoor := doors[sel.Sel.Name]
				if !isDoor {
					return true
				}
				for _, a := range allowed {
					if a == rel {
						return true
					}
				}
				t.Errorf("%s calls %s, which issues a store.Scope; the files allowed to are %v. Add the file to doors in internal/archtest/wall_test.go only with a reason in the review", rel, sel.Sel.Name, allowed)
				return true
			})
			return nil
		})
	}
}
