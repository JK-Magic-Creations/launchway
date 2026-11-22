package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The build of schema 56 (the release published outside) is installed first, with a branch rule that asks for
// approvals, an SSH key, a user and a push mirror; the next build must be a pure upgrade from it: one start applies
// 60 (notifications for people) and 61 (verified commits and tags), leaving the numbers 57 to 59 to the branches that own them (this build
// holds empty stand-ins), keeps every row, and leaves the new tables empty. The build of schema 56 still starts against
// the upgraded database (a rolling update or a rollback) and reads its own data, and the merge rule's require_signed column,
// which has existed since schema 33, now carries a value. On SQLite and, with LAUNCHWAY_TEST_POSTGRES set, on PostgreSQL.
func TestUpgradeFromSchema56AddsNotificationsAndSigningKeepingEverything(t *testing.T) {
	ctx := context.Background()
	if schemaOutside != 56 || schemaNotices != 60 || schemaSigning != 61 || schemaLast != 61 {
		t.Fatalf("the schemas are outside %d, notices %d, signing %d, last %d; this test names 56, 60 and 61", schemaOutside, schemaNotices, schemaSigning, schemaLast)
	}
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaOutside, &syncBuffer{})
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO branch_rules(id, repo_id, pattern, push_role, created_at, created_by, required_approvals) VALUES('rul_1','rep1','main','admin',1,'ada',2)`)
	exec(`INSERT INTO ssh_keys(id, kind, owner, name, key_type, fingerprint, public, created_at) VALUES('key_1','user','ada','laptop','ssh-ed25519','SHA256:abc','ssh-ed25519 AAAA',1)`)
	exec(`INSERT INTO users(username, group_list, last_login) VALUES('ada','[]',1)`)
	exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('mir_1','rep1','push','https://git.example.com/acme/app.git')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, schemaSigning, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{57, 58, 59, 60, 61} {
		if !strings.Contains(log.String(), `applied a database migration" schema=`+string(rune('0'+v/10))+string(rune('0'+v%10))+" ") {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if n := strings.Count(log.String(), "applied a database migration"); n != 5 {
		t.Errorf("%d migrations applied, want the five from 57 to 61:\n%s", n, log.String())
	}
	checkMarkers(t, cur, schemaSigning)
	if info, err := cur.Schema(ctx); err != nil || info.Database != 61 || info.Code != 61 || !info.Compatible() || info.Minimum != 7 {
		t.Fatalf("%+v %v", info, err)
	}
	count := func(q string) (n int) {
		t.Helper()
		if err := cur.db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for _, c := range []struct {
		q    string
		want int
	}{
		{`SELECT count(*) FROM branch_rules WHERE id = 'rul_1' AND required_approvals = 2 AND require_signed = 0`, 1},
		{`SELECT count(*) FROM ssh_keys WHERE id = 'key_1' AND fingerprint = 'SHA256:abc'`, 1},
		{`SELECT count(*) FROM users WHERE username = 'ada'`, 1},
		{`SELECT count(*) FROM repo_mirrors WHERE id = 'mir_1'`, 1},
		{`SELECT count(*) FROM notices`, 0}, {`SELECT count(*) FROM notice_prefs`, 0}, {`SELECT count(*) FROM notice_watch`, 0},
		{`SELECT count(*) FROM notice_people`, 0}, {`SELECT count(*) FROM mail_queue`, 0},
		{`SELECT count(*) FROM signing_keys`, 0}, {`SELECT count(*) FROM signature_cache`, 0}, {`SELECT count(*) FROM signing_installation`, 0},
	} {
		if got := count(c.q); got != c.want {
			t.Errorf("%s: %d, want %d", c.q, got, c.want)
		}
	}
	// The new tables take rows; the signing key's fingerprint is unique across the installation.
	if _, err := cur.db.Exec(`INSERT INTO signing_keys(id, owner, name, fingerprint, public_text, created_at) VALUES('sgk_1','ada','laptop','SHA256:abc','ssh-ed25519 AAAA',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := cur.db.Exec(`INSERT INTO signing_keys(id, owner, name, fingerprint, public_text, created_at) VALUES('sgk_2','otto','laptop','SHA256:abc','ssh-ed25519 AAAA',1)`); err == nil {
		t.Fatal("one key was registered for two people")
	}
	if _, err := cur.db.Exec(`INSERT INTO notices(id, recipient, kind, title, created_at) VALUES('ntc_1','ada','review_requested','Review requested',1)`); err != nil {
		t.Fatal(err)
	}
	cur.Close()
	older, err := openAs(t, addr, clk, schemaOutside, &syncBuffer{})
	if err != nil {
		t.Fatalf("a build of schema 56 on a database of schema 61: %v", err)
	}
	defer older.Close()
	if n := 0; older.db.QueryRow(`SELECT count(*) FROM branch_rules WHERE id = 'rul_1'`).Scan(&n) != nil || n != 1 {
		t.Fatalf("the build of schema 56 does not read its own data: %d", n)
	}
}
