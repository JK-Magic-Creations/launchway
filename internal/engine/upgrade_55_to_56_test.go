package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 55 (narrow credentials) is installed first and writes a push mirror, a repository-limited
// token, a service account and a measured step; the next build must be a pure upgrade from it: one start applies
// exactly 56 (the release published to an outside Git host),
// keeps every row, leaves release_outside empty and gives the mirror publish_releases off. The build of schema 55
// still starts against the upgraded database (a rolling update, or a rollback) and reads its own data. On SQLite and,
// with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema55KeepsEverythingAndAddsTheOutsideRelease(t *testing.T) {
	ctx := context.Background()
	if schemaNarrow != 55 || schemaOutside != 56 || schemaLast < 56 {
		t.Fatalf("the schemas are narrow %d, outside %d, last %d; this test names 55 and 56", schemaNarrow, schemaOutside, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaNarrow, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1','rep1','push','https://git.example.com/acme/app.git')`)
	exec(`INSERT INTO step_usage(run_id, step_id, attempt, measured, memory_peak) VALUES('run_1','build',1,1,123456)`)
	if err := old.CreateLimitedToken(APIToken{ID: "tok_ltd", Name: "mirror", User: "service:gitops", CreatedAt: 1, ExpiresAt: 9999999999999,
		Repositories: []TokenRepo{{Pattern: "acme/app", ID: "rep1"}}, RepoAccess: "read"}, "h-ltd"); err != nil {
		t.Fatal(err)
	}
	if err := old.CreateServiceAccount(ServiceAccount{Name: "gitops", Role: "viewer", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaOutside, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), `applied a database migration" schema=56 `) || strings.Count(log.String(), "applied a database migration") != 1 {
		t.Errorf("exactly migration 56 should have been applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaOutside)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 56 || info.Code != 56 || !info.Compatible() {
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
		`SELECT count(*) FROM step_usage`:                                               1,
		`SELECT count(*) FROM service_accounts WHERE name = 'gitops'`:                   1,
		`SELECT count(*) FROM api_tokens WHERE id = 'tok_ltd' AND repo_access = 'read'`: 1,
		`SELECT count(*) FROM release_outside`:                                          0,
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

	older := mustOpenAs(t, addr, clk, schemaNarrow, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 56 || oi.Code != 55 || !oi.Compatible() {
		t.Fatalf("the build of schema 55 against schema 56: %+v %v", oi, err)
	}
	var url string
	if err := older.db.QueryRow(`SELECT url FROM repo_mirrors WHERE id = 'mir_1'`).Scan(&url); err != nil || url != "https://git.example.com/acme/app.git" {
		t.Fatalf("the build of schema 55 cannot read its mirror: %q %v", url, err)
	}
}
