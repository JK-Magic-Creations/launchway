package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 56 with data in it is opened by this build, which
// applies exactly migrations 57 to 68: eleven empty stand-ins held for other
// branches, then custom steps' table (68). Every row survives, the table
// exists and works, and a build of schema 56 still starts against the result
// (the migration is additive).
func TestUpgradeFrom56ToCustomSteps(t *testing.T) {
	if schemaOutside != 56 || schemaCustomSteps != 68 || customStandInFirst != 57 || customStandInLast != 67 {
		t.Fatal("this test names schemas 56, 57 to 67 (stand-ins) and 68")
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	if _, err := old.db.Exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1','rep1','push','https://git.example.com/acme/app.git')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaCustomSteps, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != 12 {
		t.Errorf("%d migrations applied, want 12 (57 to 68):\n%s", n, log.String())
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
	again, err := openAs(t, addr, clk, schemaOutside, &syncBuffer{})
	if err != nil {
		t.Fatalf("a build of schema 56 does not start against the result: %v", err)
	}
	again.Close()
}
