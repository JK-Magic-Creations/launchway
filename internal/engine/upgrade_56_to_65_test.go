package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 56 writes repositories and pull requests; the next build
// is a pure upgrade from it: one start applies 57 to 65 (57 is main's reserved
// real migration of the outside-host connection, 58 to 62 are main's, 63 custom steps), keeps every row, makes every existing
// repository a repository that is no fork, follows the default for forks and is
// no template, and every pull request one that gives maintainers no edit
// rights. The build of schema 56 still starts against the upgraded database.
func TestUpgradeFromSchema56KeepsEverythingAndAddsForks(t *testing.T) {
	ctx := context.Background()
	if schemaOutside != 56 || schemaForks != 64 || schemaForkPulls != 65 || schemaLast < 65 {
		t.Fatalf("the schemas are outside %d, forks %d, fork pulls %d, last %d; this test names 56, 64 and 65", schemaOutside, schemaForks, schemaForkPulls, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO forge_repositories(id, connection_id, home, name, path, visibility) VALUES('rep_1','con_1','acme','app','acme/app','private')`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('pul_1','rep_1',1,'t','ada','topic','main')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaForkPulls, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if n := strings.Count(log.String(), "applied a database migration"); n != 9 {
		t.Errorf("%d migrations applied, want 9 (57 to 65):\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaForkPulls)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 65 || info.Code != 65 || !info.Compatible() {
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
		`SELECT count(*) FROM forge_repositories WHERE id = 'rep_1' AND parent_id = '' AND root_id = '' AND fork_point = '' AND forked_at = 0 AND allow_forks = -1 AND is_template = 0`: 1,
		`SELECT count(*) FROM pull_requests WHERE id = 'pul_1' AND allow_maintainer_edits = 0 AND run_approved_head = '' AND source_repo_id = ''`:                                       1,
		`SELECT count(*) FROM owner_fork_settings`: 0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	older := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 65 || oi.Code != 56 || !oi.Compatible() {
		t.Fatalf("the build of schema 56 against schema 65: %+v %v", oi, err)
	}
}
