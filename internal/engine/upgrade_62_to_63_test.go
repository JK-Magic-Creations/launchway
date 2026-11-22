package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 62 with data in it is opened by this build, which applies exactly migration 63,
// custom steps' table. Every row survives, the table exists and works, and a build of schema 62 still
// starts against the result (the migration is additive).
func TestUpgradeFrom62ToCustomSteps(t *testing.T) {
	if schemaSizing != 62 || schemaCustomSteps != 63 {
		t.Fatal("this test names schemas 62 and 63")
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaSizing, &syncBuffer{})
	if _, err := old.db.Exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1','rep1','push','https://git.example.com/acme/app.git')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaCustomSteps, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != 1 {
		t.Errorf("%d migrations applied, want only 63:\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaCustomSteps)
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM repo_mirrors WHERE id = 'mir_1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the data did not survive: %d %v", n, err)
	}
	if _, err := cur.db.Exec(`INSERT INTO custom_step_uses(run_id, step_id, name, library, commit_sha, digest) VALUES('r','s','notes','platform','abc','d')`); err != nil {
		t.Fatalf("the table does not work: %v", err)
	}
	cur.Close()
	again, err := openAs(t, addr, clk, schemaSizing, &syncBuffer{})
	if err != nil {
		t.Fatalf("a build of schema 62 does not start against the result: %v", err)
	}
	again.Close()
}
