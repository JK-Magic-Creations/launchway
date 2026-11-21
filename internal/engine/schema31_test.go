package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 29 (before the mirrors and this migration)
// upgrades to 31, brings the SSH and webhook tables empty, and an older build
// then runs against the newer database and leaves the new rows alone.
func TestSchema31UpgradesFrom29AndOlderBuildsLeaveItBe(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaVariables, &syncBuffer{})
	if hasTable(t, old, "ssh_keys") || hasTable(t, old, "repo_webhooks") {
		t.Fatal("schema 29 already has the SSH tables")
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	for _, v := range []int{schemaMirror, schemaSSH} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, schemaDelivery)
	if _, err := cur.db.Exec(`INSERT INTO ssh_keys(id, kind, owner, name, key_type, public, fingerprint) VALUES('k1','user','ada','laptop','ssh-ed25519','AAAA','SHA256:x')`); err != nil {
		t.Fatal(err)
	}
	// The fingerprint is unique over every kind of key: a key has one owner.
	if _, err := cur.db.Exec(`INSERT INTO ssh_keys(id, kind, repo_id, name, key_type, public, fingerprint) VALUES('k2','deploy','r1','ci','ssh-ed25519','AAAA','SHA256:x')`); err == nil {
		t.Fatal("the same key was accepted twice")
	}
	cur.Close()

	older, err := openAs(t, addr, clk, schemaVariables, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaDelivery || info.Code != schemaVariables || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	var n int
	if err := older.db.QueryRow(`SELECT count(*) FROM ssh_keys`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the key did not survive the older build: %d %v", n, err)
	}
}

// A database of schema 30 (mirrors present, with a row) upgrades to 31 and
// keeps its mirror rows; only the SSH and webhook tables are new.
func TestSchema31UpgradesFrom30KeepingMirrors(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaMirror, &syncBuffer{})
	if hasTable(t, old, "ssh_keys") || hasTable(t, old, "repo_webhooks") {
		t.Fatal("schema 30 already has the SSH tables")
	}
	if _, err := old.db.Exec(`INSERT INTO repo_mirrors(id, repo_id, direction, url) VALUES('m1','r1','pull','https://example.com/x.git')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaSSH)) || strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaMirror)) {
		t.Errorf("only migration %d should apply:\n%s", schemaSSH, log.String())
	}
	checkMarkers(t, cur, schemaDelivery)
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM repo_mirrors`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the mirror did not survive: %d %v", n, err)
	}
	if !hasTable(t, cur, "ssh_keys") || !hasTable(t, cur, "repo_webhooks") {
		t.Fatal("the SSH tables are missing")
	}
}
