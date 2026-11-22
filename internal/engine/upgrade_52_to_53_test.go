package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The published build is installed first, at schema 52 (the wall between Bases); the next build must be a pure
// upgrade from it: one start applies exactly 53 (the rollout page's inspect requests), keeps every row the
// published build wrote and leaves the new tables empty. The build of schema 52 still starts against the
// upgraded database (a rolling update, or a rollback of the server) and reads its own data.
// On SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromThePublishedBuildAtSchema52KeepsEverything(t *testing.T) {
	ctx := context.Background()
	if schemaWall != 52 || schemaInspect != 53 || schemaLast != 53 || len(migrations) != 53 {
		t.Fatalf("the schemas are wall %d, inspect %d, last %d with %d migrations; this test names 52 and 53", schemaWall, schemaInspect, schemaLast, len(migrations))
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaWall, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO imported_issues(repo_id, number, title, state, author, body) VALUES('rep1', 7, 'Prices round wrongly', 'open', 'ada', 'see the receipt')`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('pr_1','rep1',1,'Fix rounding','ada','fix','main')`)
	exec(`INSERT INTO work_scopes(scope_kind, scope_id, prefix) VALUES('project','shop','SHOP')`)
	exec(`INSERT INTO findings(run_id, step_id, attempt, project, run_number, fingerprint, tool, rule_id, severity, message) VALUES('run_1','scan',1,'shop',3,'fp1','gosec','G101','high','hard-coded credential')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), `applied a database migration" schema=53 `) {
		t.Errorf("migration 53 was not applied:\n%s", log.String())
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != 1 {
		t.Errorf("%d migrations applied, want exactly 1:\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaLast)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 53 || info.Code != 53 || !info.Compatible() {
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
		`SELECT count(*) FROM imported_issues`: 1, `SELECT count(*) FROM pull_requests`: 1, `SELECT count(*) FROM work_scopes`: 1,
		`SELECT count(*) FROM findings WHERE message = 'hard-coded credential'`: 1,
		`SELECT count(*) FROM bases WHERE id = 'bs_default'`:                    1,
		`SELECT count(*) FROM inspect_requests`:                                 0, `SELECT count(*) FROM inspect_chunks`: 0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}

	older := mustOpenAs(t, addr, clk, schemaWall, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 53 || oi.Code != 52 || !oi.Compatible() {
		t.Fatalf("the build of schema 52 against schema 53: %+v %v", oi, err)
	}
	var title string
	if err := older.db.QueryRow(`SELECT title FROM imported_issues WHERE number = 7`).Scan(&title); err != nil || title != "Prices round wrongly" {
		t.Fatalf("the published build cannot read its issue: %q %v", title, err)
	}
}
