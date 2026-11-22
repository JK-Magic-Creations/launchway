package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A development build of the sizing work kept where a step's numbers came from, and whether it was killed for
// memory, in a prefix of step_usage.note. Schema 62 gives them columns and moves the prefixed notes into them,
// cutting the prefix off; plain notes are left as they are; a build of schema 61 still starts against the result.
// On SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema61MovesSizingNotesIntoColumns(t *testing.T) {
	ctx := context.Background()
	if schemaSigning != 61 || schemaSizing != 62 || schemaLast < 62 {
		t.Fatalf("the schemas are signing %d, sizing %d, last %d; this test names 61 and 62", schemaSigning, schemaSizing, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaSigning, &syncBuffer{})
	const us = "\x1f"
	for i, row := range []struct{ step, note string }{
		{"a", us + "sizing=learned;runs=7;killed=1" + us + "killed for memory"},
		{"b", us + "sizing=declared" + us},
		{"c", us + "sizing=raised;runs=12" + us + "doubled"},
		{"d", "no cgroup to read"},
		{"e", ""},
	} {
		if _, err := old.db.Exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, note, created_at) VALUES('run1',?,?,0,?,1)`, row.step, i+1, row.note); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaSizing, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if n := strings.Count(log.String(), "applied a database migration"); n != 1 {
		t.Errorf("%d migrations applied, want only 62:\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaSizing)
	for step, want := range map[string]struct {
		sizing string
		runs   int
		killed int
		note   string
	}{
		"a": {"learned", 7, 1, "killed for memory"},
		"b": {"declared", 0, 0, ""},
		"c": {"raised", 12, 0, "doubled"},
		"d": {"", 0, 0, "no cgroup to read"},
		"e": {"", 0, 0, ""},
	} {
		var got struct {
			sizing string
			runs   int
			killed int
			note   string
		}
		if err := cur.db.QueryRow(`SELECT sizing, sizing_runs, killed, note FROM step_usage WHERE run_id = 'run1' AND step_id = ?`, step).Scan(&got.sizing, &got.runs, &got.killed, &got.note); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("step %s: %+v, want %+v", step, got, want)
		}
	}
	cur.Close()
	older := mustOpenAs(t, addr, clk, schemaSigning, &syncBuffer{})
	defer older.Close()
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 62 || oi.Code != 61 || !oi.Compatible() {
		t.Fatalf("the build of schema 61 against schema 62: %+v %v", oi, err)
	}
}
