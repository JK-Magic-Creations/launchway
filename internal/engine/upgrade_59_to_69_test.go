package engine

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 59 is installed first and holds a hosted repository in the trash, a pull request with a
// comment, an old address and a setting; the next build must be a pure upgrade from it: one start applies exactly
// 60 to 69 (nine stand-ins for other branches and the trash ledger), keeps every row and leaves repo_trash empty.
// The build of schema 59 still starts against the upgraded database. On SQLite and, with LAUNCHWAY_TEST_POSTGRES
// set, on PostgreSQL.
func TestUpgradeFromSchema59KeepsEverythingAndAddsTheTrashLedger(t *testing.T) {
	ctx := context.Background()
	if schemaBadges != 59 || schemaTrash != 69 || schemaLast < 69 {
		t.Fatalf("the schemas are badges %d, trash %d, last %d; this test names 59 and 69", schemaBadges, schemaTrash, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaBadges, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO forge_connections(id, name, kind) VALUES('c1','launchway','launchway')`)
	exec(`INSERT INTO forge_repositories(id, connection_id, home, name, path, default_branch, visibility, clone_https, clone_ssh, created_at, updated_at)
		VALUES('rep1','c1','acme','shop','.trash/rep1','main','private','','',1,2)`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('pr_1','rep1',1,'Fix','ada','fix','main')`)
	exec(`INSERT INTO pull_comments(id, pull_id, author, body) VALUES('cm_1','pr_1','bob','ok')`)
	exec(`INSERT INTO repo_aliases(path, repo_id, created_at, created_by) VALUES('acme/old-shop','rep1',1,'ada')`)
	exec(`INSERT INTO hosting_settings(name, value, updated_at, updated_by) VALUES('trash_retention_days','45',1,'ada')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaTrash, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for v := schemaStandInFirst; v <= schemaTrash; v++ {
		if !strings.Contains(log.String(), fmt.Sprintf("applied a database migration\" schema=%d ", v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != 10 {
		t.Errorf("%d migrations applied, want 10:\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaTrash)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 69 || info.Code != 69 || !info.Compatible() {
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
		`SELECT count(*) FROM forge_repositories WHERE path = '.trash/rep1'`: 1,
		`SELECT count(*) FROM pull_comments`:                                 1,
		`SELECT count(*) FROM repo_aliases`:                                  1,
		`SELECT count(*) FROM hosting_settings`:                              1,
		`SELECT count(*) FROM repo_trash`:                                    0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	// A repository that was in the trash before has no note: the trash shows it, and removing it for good works.
	if err := store.MarkPurgeStarted(ctx, cur.Database(), "rep1", 5); err != nil {
		t.Fatal(err)
	}
	if left, err := store.PurgingRepositories(ctx, cur.Database()); err != nil || !slices.Equal(left, []string{"rep1"}) {
		t.Fatalf("unfinished removals: %v %v", left, err)
	}
	// The removal of a later build also clears the tables of schema 70; this build of schema 69 has none, so they are made.
	if _, err := cur.db.Exec(strings.ReplaceAll(overviewMigration.sql, "{{BIG}}", "BIGINT")); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.DeleteRepoRows(ctx, cur.Database(), "rep1", ".trash/"); err != nil || !removed {
		t.Fatalf("removing the rows: %v %v", removed, err)
	}
	for _, q := range []string{`SELECT count(*) FROM forge_repositories`, `SELECT count(*) FROM pull_requests`, `SELECT count(*) FROM pull_comments`, `SELECT count(*) FROM repo_aliases`, `SELECT count(*) FROM repo_trash`} {
		if got := count(q); got != 0 {
			t.Errorf("%s: %d after the removal", q, got)
		}
	}
	if got := count(`SELECT count(*) FROM hosting_settings`); got != 1 {
		t.Errorf("a repository's removal took a setting with it: %d", got)
	}
	cur.Close()

	older := mustOpenAs(t, addr, clk, schemaBadges, &syncBuffer{})
	defer older.Close()
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 69 || oi.Code != 59 || !oi.Compatible() {
		t.Fatalf("the build of schema 59 against schema 69: %+v %v", oi, err)
	}
}

// Every table that holds a repository's ID is either removed with the repository (store.RepoOwnedTables), part of
// the Git storage removed by the storage package, or on this list with the reason it stays. A table a later
// migration adds with a repo_id column fails here until someone decides which.
func TestEveryTableThatNamesARepositoryIsAccountedForInTheTrashRemoval(t *testing.T) {
	storage := []string{"repo_generation", "repo_refs", "repo_packs", "repo_maintenance", "repo_trash"}
	kept := map[string]string{
		"forge_events": "history of what happened; names the repository by repository_id and is not repository data",
	}
	create := regexp.MustCompile(`(?is)CREATE TABLE (?:IF NOT EXISTS )?(\w+)\s*\((.*?)\n\);`)
	col := regexp.MustCompile(`(?m)^\s*(repo_id|repository_id)\s`)
	owned := store.RepoOwnedTables()
	for i, m := range migrations {
		for _, g := range create.FindAllStringSubmatch(m.sql, -1) {
			table := g[1]
			if !col.MatchString(g[2]) {
				continue
			}
			if slices.Contains(owned, table) || slices.Contains(storage, table) {
				continue
			}
			if _, ok := kept[table]; ok {
				continue
			}
			t.Errorf("migration %d: table %s names a repository, and neither store.RepoOwnedTables nor the storage removal nor the kept list in this test accounts for it", i+1, table)
		}
	}
}
