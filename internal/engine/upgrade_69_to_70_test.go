package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/store"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 69 holds a trashed repository and a pull request; the next build applies exactly migration 70
// (the Overview's two tables), keeps every row, and the new statements work against the upgraded data. The build of
// schema 69 still starts against the upgraded database.
func TestUpgradeFromSchema69KeepsEverythingAndAddsTheOverviewTables(t *testing.T) {
	ctx := context.Background()
	if schemaTrash != 69 || schemaOverview != 70 || schemaLast < 70 {
		t.Fatalf("the schemas are trash %d, overview %d, last %d; this test names 69 and 70", schemaTrash, schemaOverview, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaTrash, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO forge_connections(id, name, kind) VALUES('c1','launchway','launchway')`)
	exec(`INSERT INTO forge_repositories(id, connection_id, home, name, path, default_branch, visibility, clone_https, clone_ssh, created_at, updated_at)
		VALUES('rep1','c1','acme','shop','acme/shop','main','private','','',1,2)`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch, created_at, merged_at, merged_by) VALUES('pr_1','rep1',1,'Fix','ada','fix','main',100,200,'bob')`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch, created_at, closed_at, closed_by) VALUES('pr_2','rep1',2,'Drop','ada','drop','main',150,250,'ada')`)
	exec(`INSERT INTO audit(at, username, source, action, project, target, outcome, detail) VALUES(300,'ada','x','git.push','','acme/shop','ok','refs/heads/main aaaa..bbbb')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaOverview, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if n := strings.Count(log.String(), "applied a database migration"); n != 1 || !strings.Contains(log.String(), "applied a database migration\" schema=70 ") {
		t.Errorf("want exactly migration 70 applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaOverview)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 70 || info.Code != 70 || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	// The new statements read the old rows.
	pulls, err := cur.PullActivity(ctx, []string{"rep1"}, "", 0, 10)
	if err != nil || len(pulls) != 4 || pulls[0].Kind != "pull_closed" || pulls[1].Kind != "pull_merged" || pulls[1].Who != "bob" || pulls[3].Kind != "pull_opened" {
		t.Fatalf("pull activity: %+v %v", pulls, err)
	}
	pushes, err := cur.AuditActivity(ctx, []string{"acme/shop"}, []string{"git.push"}, "", 0, 10)
	if err != nil || len(pushes) != 1 || pushes[0].Who != "ada" {
		t.Fatalf("audit activity: %+v %v", pushes, err)
	}
	if mine, err := cur.PullActivity(ctx, []string{"rep1"}, "bob", 0, 10); err != nil || len(mine) != 1 || mine[0].Kind != "pull_merged" {
		t.Fatalf("one person's pull activity: %+v %v", mine, err)
	}
	if none, err := cur.AuditActivity(ctx, []string{"acme/shop"}, []string{"git.push"}, "bob", 0, 10); err != nil || len(none) != 0 {
		t.Fatalf("one person's pushes: %+v %v", none, err)
	}
	// The job's rows: a request is new once, a new commit makes a new request, a finished job is kept.
	need, err := cur.RequestContributors(ctx, "rep1", "90d", "c1@d1")
	if err != nil || !need {
		t.Fatalf("first request: %v %v", need, err)
	}
	if need, _ = cur.RequestContributors(ctx, "rep1", "90d", "c1@d1"); need {
		t.Fatal("the same commit asked again made a second job")
	}
	j := ContributorsJob{RepoID: "rep1", Period: "90d", Commit: "c1@d1"}
	if ok, err := cur.StartContributors(ctx, j); err != nil || !ok {
		t.Fatalf("start: %v %v", ok, err)
	}
	if err := cur.FinishContributors(ctx, j, "done", `{"walked":3}`); err != nil {
		t.Fatal(err)
	}
	if row, ok, _ := cur.ContributorsRow(ctx, "rep1", "90d"); !ok || row.State != "done" || row.Data != `{"walked":3}` {
		t.Fatalf("row: %+v", row)
	}
	if need, _ = cur.RequestContributors(ctx, "rep1", "90d", "c1@d1"); need {
		t.Fatal("a finished job was asked for again")
	}
	if need, _ = cur.RequestContributors(ctx, "rep1", "90d", "c2@d1"); !need {
		t.Fatal("a new head commit did not make a new job")
	}
	// A job that finishes for a commit the row has moved past changes nothing.
	if err := cur.FinishContributors(ctx, j, "done", `{"walked":9}`); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := cur.ContributorsRow(ctx, "rep1", "90d"); row.State != "pending" || row.Commit != "c2@d1" {
		t.Fatalf("a late finish overwrote the newer request: %+v", row)
	}
	if err := cur.SaveLanguages(ctx, "rep1", "c1", `{"x":1}`); err != nil {
		t.Fatal(err)
	}
	if c, d, ok, _ := cur.LanguagesCached(ctx, "rep1"); !ok || c != "c1" || d != `{"x":1}` {
		t.Fatalf("languages: %v %v %v", c, d, ok)
	}
	// Removing a repository for good takes both tables' rows with it.
	if err := store.MarkPurgeStarted(ctx, cur.Database(), "rep1", 5); err != nil {
		t.Fatal(err)
	}
	cur.db.Exec(`UPDATE forge_repositories SET path = '.trash/rep1' WHERE id = 'rep1'`)
	if removed, err := store.DeleteRepoRows(ctx, cur.Database(), "rep1", ".trash/"); err != nil || !removed {
		t.Fatalf("%v %v", removed, err)
	}
	for _, q := range []string{`SELECT count(*) FROM repo_languages`, `SELECT count(*) FROM repo_contributors`} {
		var n int
		if err := cur.db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d %v", q, n, err)
		}
	}
	cur.Close()

	older := mustOpenAs(t, addr, clk, schemaTrash, &syncBuffer{})
	defer older.Close()
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 70 || oi.Code != 69 || !oi.Compatible() {
		t.Fatalf("the build of schema 69 against schema 70: %+v %v", oi, err)
	}
}
