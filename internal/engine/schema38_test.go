package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 33 (pull requests, a protected branch recorded)
// upgrades to 38 in one start: the real migrations 34 to 37 and the delivery migration are
// applied, the branch rule keeps its row, branch_writers is there and empty,
// and the build of schema 33 then runs against the newer database and
// leaves what is in it alone.
func TestSchema38UpgradesFrom33KeepingBranchRules(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaPulls, &syncBuffer{})
	if hasTable(t, old, "branch_writers") {
		t.Fatal("schema 33 already has branch_writers")
	}
	if _, err := old.db.Exec(`INSERT INTO branch_rules(id, repo_id, pattern, push_role, created_at, created_by) VALUES('r1','repo1','main','admin',1,'ada')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	for v := schemaEncryption; v <= schemaDelivery; v++ {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaPulls)) {
		t.Errorf("migration 33, already applied, was applied again")
	}
	checkMarkers(t, cur, schemaDelivery)
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM branch_writers`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("branch_writers: %d %v", n, err)
	}
	if _, err := cur.db.Exec(`INSERT INTO branch_writers(id, repo_id, branch, projects, files, created_at) VALUES('w1','repo1','main','["mcp-*"]','["apps/x.yaml"]',1)`); err != nil {
		t.Fatal(err)
	}
	cur.Close()

	older, err := openAs(t, addr, clk, schemaPulls, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaDelivery || info.Code != schemaPulls || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	var rules, writers int
	if err := older.db.QueryRow(`SELECT (SELECT count(*) FROM branch_rules), (SELECT count(*) FROM branch_writers)`).Scan(&rules, &writers); err != nil || rules != 1 || writers != 1 {
		t.Fatalf("the rows did not survive the older build: %d %d %v", rules, writers, err)
	}
}
