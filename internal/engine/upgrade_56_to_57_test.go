package engine

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 56 is installed first and holds a plain connection and a push mirror; the next build must be
// a pure upgrade from it: one start applies exactly 57 (the connection to an outside Git host), keeps every row,
// gives the old connection the token credential kind and an empty allow-list, and leaves the new tables empty.
// The build of schema 56 still starts against the upgraded database (a rolling update, or a rollback) and reads
// its own data. On SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema56KeepsEverythingAndAddsTheOutsideConnection(t *testing.T) {
	ctx := t.Context()
	if schemaOutside != 56 || schemaHosts != 57 || schemaLast < 57 {
		t.Fatalf("the schemas are outside %d, hosts %d, last %d; this test names 56 and 57", schemaOutside, schemaHosts, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO forge_connections(id, name, kind, base_url, cred_env, cred_file, events, enabled, created_at, updated_at, created_by)
		VALUES('con_1','upstream','plain','https://git.example.com','GIT_TOKEN','','polling',1,1,1,'alice')`)
	exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1','rep1','push','https://git.example.com/acme/app.git')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaHosts, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), `applied a database migration" schema=57 `) || strings.Count(log.String(), "applied a database migration") != 1 {
		t.Errorf("exactly migration 57 should have been applied:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaHosts)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 57 || info.Code != 57 || !info.Compatible() {
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
		`SELECT count(*) FROM forge_connections WHERE id = 'con_1' AND cred_env = 'GIT_TOKEN' AND auth_kind = 'token' AND owners = '' AND webhook_env = '' AND upload_url = ''`: 1,
		`SELECT count(*) FROM repo_mirrors WHERE id = 'mir_1'`: 1,
		`SELECT count(*) FROM outside_flights`:                0,
		`SELECT count(*) FROM outside_comments`:               0,
	} {
		if got := count(q); got != want {
			t.Errorf("%s: %d, want %d", q, got, want)
		}
	}
	if _, err := cur.db.Exec(`INSERT INTO outside_flights(project, connection_id, repo_path) VALUES('app','con_1','acme/app')`); err != nil {
		t.Fatal(err)
	}
	if got := count(`SELECT count(*) FROM outside_flights WHERE report_statuses = 1 AND report_comment = 1`); got != 1 {
		t.Errorf("the report switches do not default to on: %d", got)
	}

	older := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	if oi, err := older.Schema(ctx); err != nil || oi.Database != 57 || oi.Code != 56 || !oi.Compatible() {
		t.Fatalf("the build of schema 56 against schema 57: %+v %v", oi, err)
	}
	var name string
	if err := older.db.QueryRow(`SELECT name FROM forge_connections WHERE id = 'con_1'`).Scan(&name); err != nil || name != "upstream" {
		t.Fatalf("the build of schema 56 cannot read its connection: %q %v", name, err)
	}
}
