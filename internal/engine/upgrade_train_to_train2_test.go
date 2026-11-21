package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The first combined build is installed first, at schema 39 (the issue archive); the second must be a pure
// upgrade from it: one start applies exactly 40 (the built-in registry) and 41 (feature toggles), keeps
// every row the first build wrote, and leaves the new tables empty. The build of schema 39 still starts
// against the upgraded database (a rolling update, or a rollback of the server) and reads its own data.
// On SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromTheFirstCombinedBuildKeepsEverything(t *testing.T) {
	ctx := context.Background()
	if schemaArchive != 39 || schemaRegistry != 40 || schemaToggles != 41 || len(migrations) != 41 {
		t.Fatalf("the schemas are archive %d, registry %d, toggles %d with %d migrations; this test names 39, 40 and 41", schemaArchive, schemaRegistry, schemaToggles, len(migrations))
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaArchive, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO imported_issues(repo_id, number, title, state, author, body) VALUES('rep1', 7, 'Prices round wrongly', 'open', 'ada', 'see the receipt')`)
	exec(`INSERT INTO imported_comments(repo_id, issue_number, position, author, body) VALUES('rep1', 7, 0, 'bob', 'same here')`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('pr_1','rep1',1,'Fix rounding','ada','fix','main')`)
	for i := 1; i <= 2; i++ {
		exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status, created_at, last_used_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			fmt.Sprintf("cch_%d", i), "shop", "branch:main", "gomod", fmt.Sprintf("k%d", i), "linux/amd64", fmt.Sprintf("caches/%d", i), "kept", 10, 20)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaRegistry, schemaToggles} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != 2 {
		t.Errorf("%d migrations applied, want 2:\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaToggles)
	if info, err := cur.Schema(ctx); err != nil || info.Database != schemaToggles || info.Code != schemaToggles || !info.Compatible() {
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
		`SELECT count(*) FROM imported_issues`: 1, `SELECT count(*) FROM imported_comments`: 1, `SELECT count(*) FROM pull_requests`: 1, `SELECT count(*) FROM caches WHERE status = 'kept'`: 2,
		`SELECT count(*) FROM registry_repositories`: 0, `SELECT count(*) FROM registry_blobs`: 0, `SELECT count(*) FROM toggles`: 0, `SELECT count(*) FROM toggle_envs`: 0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}

	older := mustOpenAs(t, addr, clk, schemaArchive, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != schemaToggles || oi.Code != schemaArchive || !oi.Compatible() {
		t.Fatalf("the build of schema 39 against the final schema: %+v %v", oi, err)
	}
	var title string
	if err := older.db.QueryRow(`SELECT title FROM imported_issues WHERE number = 7`).Scan(&title); err != nil || title != "Prices round wrongly" {
		t.Fatalf("the first build cannot read its issue: %q %v", title, err)
	}
}
