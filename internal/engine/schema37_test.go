package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 33 (pull requests) upgrades to 37 in one start: only
// the queues migration is applied (34 to 37 are empty stand-ins held for other
// branches), a pull request keeps its row, the queue tables are there and
// empty, and an older build then runs against the newer database.
func TestSchema37UpgradesFrom33KeepingPullRequests(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaPulls, &syncBuffer{})
	if hasTable(t, old, "run_queue") || hasTable(t, old, "step_waits") {
		t.Fatal("schema 33 already has queues")
	}
	if _, err := old.db.Exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('p1','repo1',1,'t','ada','f','main')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaQueues)) {
		t.Errorf("migration 37 was not applied:\n%s", log.String())
	}
	if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaPulls)) {
		t.Errorf("migration 33, already applied, was applied again")
	}
	checkMarkers(t, cur, schemaQueues)
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM run_queue`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("run_queue: %d %v", n, err)
	}
	if _, err := cur.db.Exec(`INSERT INTO step_waits(run_id, step_id, code, since, first_since) VALUES('r','a','quota_full',1,1)`); err != nil {
		t.Fatal(err)
	}
	cur.Close()

	older, err := openAs(t, addr, clk, schemaPulls, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaQueues || info.Code != schemaPulls || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	if err := older.db.QueryRow(`SELECT count(*) FROM pull_requests`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the pull request did not survive the older build: %d %v", n, err)
	}
}
