package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 56 (the outside-host release) is installed first and writes a repository, a release with a
// file and a mirror; the next build must be a pure upgrade from it: one start (stopped at schema 59, the first half of the 56 to 61 upgrade) applies exactly the empty 57 (reserved for the outside-hosts branch), 58
// and 59 (protected tags, download counts, Flight badges, old repository addresses, hosting settings), keeps every
// row and leaves the new tables empty. The build of schema 56 still starts against the upgraded database. On
// SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema56KeepsEverythingAndAddsTagsBadgesAndAliases(t *testing.T) {
	ctx := context.Background()
	if schemaOutside != 56 || schemaTagRules != 58 || schemaBadges != 59 || schemaLast < 59 {
		t.Fatalf("the schemas are outside %d, tags %d, badges %d, last %d; this test names 56, 58 and 59", schemaOutside, schemaTagRules, schemaBadges, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1','rep1','push','https://git.example.com/acme/app.git')`)
	exec(`INSERT INTO branch_rules(id, repo_id, pattern, push_role, created_at, created_by) VALUES('r1','rep1','main','admin',1,'ada')`)
	exec(`INSERT INTO releases(id, project, version, state, created_at) VALUES('rel_1','app','1.0.0','published',1)`)
	exec(`INSERT INTO release_assets(id, release_id, name, kind, added_at) VALUES('as_1','rel_1','app.tgz','file',1)`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaBadges, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for v := schemaStandIn57; v <= schemaBadges; v++ {
		if !strings.Contains(log.String(), fmt.Sprintf("applied a database migration\" schema=%d ", v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != 3 {
		t.Errorf("%d migrations applied, want 3:\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaBadges)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 59 || info.Code != 59 || !info.Compatible() {
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
		`SELECT count(*) FROM repo_mirrors WHERE id = 'mir_1'`:  1,
		`SELECT count(*) FROM branch_rules WHERE id = 'r1'`:     1,
		`SELECT count(*) FROM release_assets WHERE id = 'as_1'`: 1,
		`SELECT count(*) FROM tag_rules`:                        0,
		`SELECT count(*) FROM release_downloads`:                0,
		`SELECT count(*) FROM flight_badges`:                    0,
		`SELECT count(*) FROM repo_aliases`:                     0,
		`SELECT count(*) FROM hosting_settings`:                 0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	if _, err := cur.db.Exec(`INSERT INTO tag_rules(id, repo_id, pattern, created_at) VALUES('t1','rep1','v*',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := cur.db.Exec(`INSERT INTO tag_rules(id, repo_id, pattern, created_at) VALUES('t2','rep1','v*',1)`); err == nil {
		t.Error("a repository's pattern is unique")
	}
	cur.Close()

	older := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	defer older.Close()
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 59 || oi.Code != 56 || !oi.Compatible() {
		t.Fatalf("the build of schema 56 against schema 59: %+v %v", oi, err)
	}
	var url string
	if err := older.db.QueryRow(`SELECT url FROM repo_mirrors WHERE id = 'mir_1'`).Scan(&url); err != nil || url != "https://git.example.com/acme/app.git" {
		t.Fatalf("the build of schema 56 cannot read its mirror: %q %v", url, err)
	}
}
