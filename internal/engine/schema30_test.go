package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Mirrors are migration 30, after the empty stand-in 28 and the variables
// migration 29, so that a database that already recorded 29 gets the tables
// by an ordinary step forward and nothing recorded is ever rewritten.

// A database of schema 27 reaches 30 in one start: 28 (the stand-in), 29 and
// 30 are applied, its runs carry on and the mirrors table is there and empty.
func TestUpgradeFromSchema27ToMirrors(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	old, err := openAs(t, addr, clk, schemaParity2, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	if hasTable(t, old, "repo_mirrors") || hasTable(t, old, "variables") {
		t.Fatal("schema 27 already has later tables")
	}
	half := create(t, old, linear, Trigger{Event: "manual"})
	ok(t, old, take(t, old), nil)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaStandIn28, schemaVariables, schemaMirror, schemaSSH, schemaRollback, schemaPulls, schemaEncryption} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, schemaCacheTrust)
	if j := take(t, cur); j.RunID != half {
		t.Fatalf("the half-done run did not carry on: %+v", j)
	}
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM repo_mirrors`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("repo_mirrors: %d %v", n, err)
	}
}

// A database that already ran 28 (the stand-in) and 29, as a build of schema
// 29 leaves it, gets 30 and 31: nothing is applied twice, what was written to
// the variables table stays.
func TestUpgradeFromSchema29ToMirrorsKeepsWhatIsThere(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	prev, err := openAs(t, addr, clk, schemaVariables, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	if hasTable(t, prev, "repo_mirrors") {
		t.Fatal("schema 29 already has the mirrors table")
	}
	if _, err := prev.db.Exec(`INSERT INTO variables(level, scope, name, type, value) VALUES('project', 'cart', 'replicas', 'number', '3')`); err != nil {
		t.Fatal(err)
	}
	prev.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaMirror, schemaSSH, schemaRollback, schemaPulls, schemaEncryption} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	for _, v := range []int{schemaStandIn28, schemaVariables} {
		if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d, already applied, was applied again", v)
		}
	}
	checkMarkers(t, cur, schemaCacheTrust)
	var val string
	if err := cur.db.QueryRow(`SELECT value FROM variables WHERE name = 'replicas'`).Scan(&val); err != nil || val != "3" {
		t.Fatalf("variable after the upgrade: %q %v", val, err)
	}
}
