package sealing

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// secretLooking matches column names that may hold a secret. A column that
// matches must be in Registry (sealed) or in ExplainedColumns (with the reason), so
// that new code that adds a secret-looking column cannot slip past: it fails
// here until someone decides.
var secretLooking = regexp.MustCompile(`(?i)(secret|token|passw|credential|cred_|private|sealed|seal|api_?key|signing|hmac|hash|cert|(^|_)key$)`)

// schemaColumns lists table.column for every column of the test database.
func schemaColumns(t *testing.T, e *engine.Engine) []string {
	t.Helper()
	db := e.Database()
	q := `SELECT m.name, p.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`
	if db.Postgres() {
		q = `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = current_schema()`
	}
	var out []string
	if err := eachRow(ctx, db, q, nil, func(rs *sql.Rows) error {
		var tb, c string
		if err := rs.Scan(&tb, &c); err != nil {
			return err
		}
		out = append(out, tb+"."+c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestSchemaSecretColumnsAreRegistered(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	registered := map[string]bool{}
	for _, c := range Registry {
		registered[c.Table+"."+c.Column] = true
	}
	all := map[string]bool{}
	for _, col := range schemaColumns(t, e) {
		all[col] = true
		if !secretLooking.MatchString(col[strings.Index(col, ".")+1:]) {
			continue
		}
		if registered[col] {
			continue
		}
		if _, ok := ExplainedColumns[col]; !ok {
			t.Errorf("the column %s looks like it may hold a secret. If it does, seal it through internal/sealing and add it to Registry (internal/sealing/columns.go); if it does not, add it to ExplainedColumns there with the reason", col)
		}
	}
	for col := range registered {
		if !all[col] {
			t.Errorf("Registry names %s, which is not a column of the schema", col)
		}
		if why, dup := ExplainedColumns[col]; dup {
			t.Errorf("%s is both registered and listed as not secret (%s)", col, why)
		}
	}
	for col, why := range ExplainedColumns {
		if !all[col] {
			t.Errorf("ExplainedColumns names %s (%s), which is not a column of the schema: delete the entry", col, why)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("ExplainedColumns %s has no reason", col)
		}
	}
}

// repoRoot finds the directory with go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			t.Fatal("go.mod not found")
		}
	}
}

// Code that writes a registered column is on the column's list of writers,
// and each writer seals what it writes: no other file may write it, and a
// writer that does not call Seal fails. A new INSERT or UPDATE of a secret
// column in some other file fails here.
func TestRegisteredColumnsAreOnlyWrittenBySealingCode(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, top := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
				files = append(files, path)
			}
			return nil
		})
	}
	for _, c := range Registry {
		insert := regexp.MustCompile(`(?is)INSERT\s+INTO\s+` + c.Table + `\s*\([^)]*\b` + c.Column + `\b`)
		update := regexp.MustCompile(`(?is)UPDATE\s+` + c.Table + `\s+SET\s[^;"` + "`" + `]*\b` + c.Column + `\s*=`)
		allowed := map[string]bool{}
		for _, w := range c.Writers {
			allowed[w] = true
		}
		found := map[string]bool{}
		for _, f := range files {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			src := string(b)
			if c.Table == "meta" {
				// meta holds many things; only writes of shared: rows are the secrets.
				if !strings.Contains(src, "shared:") && !strings.Contains(src, "metaRowPrefix") {
					continue
				}
			}
			if !insert.MatchString(src) && !update.MatchString(src) {
				continue
			}
			found[rel] = true
			if !allowed[rel] {
				t.Errorf("%s writes %s.%s, a column that holds secrets (%s), and is not one of its writers: seal the value with internal/sealing and list the file in Registry, or use an existing writer", rel, c.Table, c.Column, c.Why)
				continue
			}
			if !strings.Contains(src, ".Seal(") && !strings.Contains(src, ".SealRow(") {
				t.Errorf("%s writes %s.%s but never calls Seal", rel, c.Table, c.Column)
			}
		}
		for w := range allowed {
			if !found[w] {
				t.Errorf("Registry lists %s as a writer of %s.%s, but it does not write it (stale)", w, c.Table, c.Column)
			}
		}
	}
}
