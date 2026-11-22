package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 54 (measured step use) is installed first and writes a push mirror and a release; the next
// build must be a pure upgrade from it: one start applies exactly 55 (the release published to an outside Git host),
// keeps every row, leaves release_outside empty and gives the mirror publish_releases off. The build of schema 54
// still starts against the upgraded database (a rolling update, or a rollback) and reads its own data. On SQLite and,
// with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema54KeepsEverythingAndAddsTheOutsideRelease(t *testing.T) {
	ctx := context.Background()
	if schemaUsage != 54 || schemaOutside != 55 || schemaLast < 55 {
		t.Fatalf("the schemas are usage %d, outside %d, last %d; this test names 54 and 55", schemaUsage, schemaOutside, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaUsage, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1','rep1','push','https://git.example.com/acme/app.git')`)
	exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, memory_peak) VALUES('run_1','build',1,1,123456)`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaOutside, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), `applied a database migration" schema=55 `) || strings.Count(log.String(), "applied a database migration") != 1 {
		t.Errorf("exactly migration 55 should have been applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaOutside)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 55 || info.Code != 55 || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	count := func(q string) (n int) {
		t.Helper()
		if err := cur.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM repo_mirrors WHERE id = 'mir_1' AND publish_releases = 0 AND outside_api = ''`: 1,
		`SELECT count(*) FROM step_usage`:      1,
		`SELECT count(*) FROM release_outside`: 0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	if err := cur.QueueReleaseOutside("rel_1", "mir_1"); err != nil {
		t.Fatal(err)
	}
	if err := cur.QueueReleaseOutside("rel_1", "mir_1"); err != nil { // the pair is unique: the second does nothing
		t.Fatal(err)
	}
	if got := count(`SELECT count(*) FROM release_outside`); got != 1 {
		t.Errorf("the pair is queued %d times", got)
	}

	older := mustOpenAs(t, addr, clk, schemaUsage, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 55 || oi.Code != 54 || !oi.Compatible() {
		t.Fatalf("the build of schema 54 against schema 55: %+v %v", oi, err)
	}
	var url string
	if err := older.db.QueryRow(`SELECT url FROM repo_mirrors WHERE id = 'mir_1'`).Scan(&url); err != nil || url != "https://git.example.com/acme/app.git" {
		t.Fatalf("the build of schema 54 cannot read its mirror: %q %v", url, err)
	}
}
