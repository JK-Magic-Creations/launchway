package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 53 (inspect requests) is installed first and writes a run's data; the next build must be a pure
// upgrade from it: one start applies exactly 54 (what each step attempt used), keeps every row and leaves step_usage
// empty. The build of schema 53 still starts against the upgraded database (a rolling update, or a rollback of the
// server) and reads its own data. On SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema53KeepsEverythingAndAddsStepUsage(t *testing.T) {
	ctx := context.Background()
	if schemaInspect != 53 || schemaUsage != 54 || schemaLast < 54 {
		t.Fatalf("the schemas are inspect %d, usage %d, last %d; this test names 53 and 54", schemaInspect, schemaUsage, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaInspect, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO imported_issues(repo_id, number, title, state, author, body) VALUES('rep1', 7, 'Prices round wrongly', 'open', 'ada', 'see the receipt')`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('pr_1','rep1',1,'Fix rounding','ada','fix','main')`)
	exec(`INSERT INTO findings(run_id, step_id, attempt, project, run_number, fingerprint, tool, rule_id, severity, message) VALUES('run_1','scan',1,'shop',3,'fp1','gosec','G101','high','hard-coded credential')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaUsage, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), `applied a database migration" schema=54 `) || strings.Count(log.String(), "applied a database migration") != 1 {
		t.Errorf("exactly migration 54 should have been applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaUsage)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 54 || info.Code != 54 || !info.Compatible() {
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
		`SELECT count(*) FROM imported_issues`: 1, `SELECT count(*) FROM pull_requests`: 1,
		`SELECT count(*) FROM findings WHERE message = 'hard-coded credential'`: 1,
		`SELECT count(*) FROM step_usage`:                                       0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	if _, err := cur.db.Exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, memory_peak) VALUES('run_1','build',1,1,123456)`); err != nil {
		t.Fatal(err)
	}

	older := mustOpenAs(t, addr, clk, schemaInspect, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 54 || oi.Code != 53 || !oi.Compatible() {
		t.Fatalf("the build of schema 53 against schema 54: %+v %v", oi, err)
	}
	var title string
	if err := older.db.QueryRow(`SELECT title FROM imported_issues WHERE number = 7`).Scan(&title); err != nil || title != "Prices round wrongly" {
		t.Fatalf("the build of schema 53 cannot read its issue: %q %v", title, err)
	}
}
