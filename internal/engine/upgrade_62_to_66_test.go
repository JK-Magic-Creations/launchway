package engine

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 65 is installed first and holds a hosted repository in the trash, a pull request with a
// comment, an old address and a setting; the next build must be a pure upgrade from it: one start applies exactly
// 66 (the trash ledger), keeps every row and leaves repo_trash empty.
// The build of schema 65 still starts against the upgraded database. On SQLite and, with LAUNCHWAY_TEST_POSTGRES
// set, on PostgreSQL.
func TestUpgradeFromSchema65KeepsEverythingAndAddsTheTrashLedger(t *testing.T) {
	ctx := context.Background()
	if schemaForkPulls != 65 || schemaTrash != 66 || schemaLast < 66 {
		t.Fatalf("the schemas are fork pulls %d, trash %d, last %d; this test names 65 and 66", schemaForkPulls, schemaTrash, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaForkPulls, &syncBuffer{})
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
	if n := strings.Count(log.String(), "applied a database migration"); n != 1 || !strings.Contains(log.String(), "applied a database migration\" schema=66 ") {
		t.Errorf("want exactly migration 66 applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaTrash)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 66 || info.Code != 66 || !info.Compatible() {
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
	// The removal of a later build also clears the tables of schema 67; this build of schema 66 has none, so they are made.
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

	older := mustOpenAs(t, addr, clk, schemaForkPulls, &syncBuffer{})
	defer older.Close()
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 66 || oi.Code != 65 || !oi.Compatible() {
		t.Fatalf("the build of schema 65 against schema 66: %+v %v", oi, err)
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

// The fork columns that name another repository are accounted for too. A fork's row names its parent and the root of
// its network; a pull request from a fork names the fork it comes from. None is removed with the repository they
// name (a repository's own removal takes its own rows, never another repository's), and the removal of a parent
// never deletes a pack a fork's manifest names: it goes through store.BlobInUse (storage.Store.Purge, the one removal
// path). A migration that adds another column naming a repository fails here until someone decides which it is.
func TestEveryColumnThatNamesAnotherRepositoryIsAccountedFor(t *testing.T) {
	accounted := map[string]string{
		"forge_repositories.parent_id":         "the fork's relationship; kept when the parent is removed (the fork says its parent is gone)",
		"forge_repositories.root_id":           "the fork's relationship; kept when the root is removed",
		"pull_requests.source_repo_id":         "the fork a pull request comes from; the request stays when the fork is removed",
		"forge_repositories.fork_point":        "a commit, not a repository",
		"owner_fork_settings.owner":            "an owner, not a repository",
		"repo_aliases.repo_id":                 "removed with the repository (store.RepoOwnedTables)",
		"repo_trash.repo_id":                   "the trash ledger of the repository itself",
		"repo_languages.repo_id":               "removed with the repository (store.RepoOwnedTables)",
		"repo_contributors.repo_id":            "removed with the repository (store.RepoOwnedTables)",
		"work_time_entries.item_id":            "a work item, not a repository",
		"pull_requests.allow_maintainer_edits": "a switch",
	}
	add := regexp.MustCompile(`(?i)ALTER TABLE (\w+) ADD COLUMN (\w*(?:repo_id|parent_id|root_id|repository_id)\w*)\s`)
	for i, m := range migrations {
		for _, g := range add.FindAllStringSubmatch(m.sql, -1) {
			key := g[1] + "." + g[2]
			if _, ok := accounted[key]; !ok {
				t.Errorf("migration %d: the column %s names a repository, and this test does not say what the removal does about it", i+1, key)
			}
		}
	}
}
