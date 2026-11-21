package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 27 (before variables at levels; 28 is held for another
// branch) is migrated to 30 (29, variables, then 30, mirrors) with the startup guard on: its runs carry on, the
// three tables are there and empty, and what is written to them stays. Going
// back, a build of schema 27 starts against the database of 29 (the migration
// is additive: new tables only), says so once, and leaves the tables alone.
func TestUpgradeFromSchema27ToVariablesAndBack(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	old, err := openAs(t, addr, clk, schemaParity2, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	if hasTable(t, old, "variables") {
		t.Fatal("schema 27 already has the variables table")
	}
	half := create(t, old, linear, Trigger{Event: "manual"})
	ok(t, old, take(t, old), nil)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	for _, v := range []int{schemaStandIn28, schemaVariables, schemaMirror, schemaSSH, schemaStandIn32, schemaPulls} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, schemaPulls)
	if j := take(t, cur); j.RunID != half {
		t.Fatalf("the half-done run did not carry on: %+v", j)
	}
	if _, err := cur.db.Exec(`INSERT INTO variables(level, scope, name, type, value) VALUES('project', 'cart', 'replicas', 'number', '3')`); err != nil {
		t.Fatal(err)
	}
	cur.Close()

	// Going back: the older build runs on the newer database and leaves it be.
	var olderLog syncBuffer
	older, err := openAs(t, addr, clk, schemaParity2, &olderLog)
	if err != nil {
		t.Fatalf("a build of schema 27 on a database of 29: %v\n%s", err, olderLog.String())
	}
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaPulls || info.Code != schemaParity2 || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	var n int
	if err := older.db.QueryRow(`SELECT count(*) FROM variables`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the variable did not survive the older build: %d %v", n, err)
	}
}
