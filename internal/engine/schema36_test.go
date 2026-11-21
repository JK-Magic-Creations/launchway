package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 33 (before the stand-ins for 34 and 35 and before
// built-in accounts) upgrades to 36 keeping everything, on SQLite and, with
// LAUNCHWAY_TEST_POSTGRES, PostgreSQL; an older build still reads it.
func TestUpgradeFromSchema33ToAccounts(t *testing.T) {
	clk := newClock()
	addr := testdb.New(t)
	var log0 syncBuffer
	old := mustOpenAs(t, addr, clk, schemaPulls, &log0)
	if hasTable(t, old, "local_accounts") {
		t.Fatal("a schema-33 database already has the accounts table")
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaStandIn34, schemaStandIn35, schemaAccounts} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, schemaAccounts)
	info, err := cur.Schema(context.Background())
	if err != nil || info.Database != schemaAccounts || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}

	older := mustOpenAs(t, addr, clk, schemaPulls, &log0)
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaAccounts || info.Code != schemaPulls || !info.Compatible() {
		t.Fatalf("the schema-33 build's view: %+v %v", info, err)
	}
}
