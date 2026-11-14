// Package testdb gives each test a database of its own: a new SQLite file,
// or, when LAUNCHWAY_TEST_POSTGRES is set to a PostgreSQL URL, a new schema
// in that database that is dropped when the test ends.
package testdb

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// EnvVar names the variable holding the PostgreSQL base URL.
const EnvVar = "LAUNCHWAY_TEST_POSTGRES"

// Postgres reports whether tests run against PostgreSQL.
func Postgres() bool { return os.Getenv(EnvVar) != "" }

// New returns the address of an empty database for one test. Every call
// returns a different database; opening the same address twice shares it.
func New(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvVar)
	if base == "" {
		return filepath.Join(t.TempDir(), "launchway.db")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("%s: %v", EnvVar, err)
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	rand.Read(b)
	schema := "test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("creating a schema for the test: %v", err)
	}
	// Registered first, so it runs after the test's own clean-ups, which
	// close the engines using the schema.
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Errorf("dropping the test schema %s: %v", schema, err)
		}
		admin.Close()
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
