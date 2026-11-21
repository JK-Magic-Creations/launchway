package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 31 (SSH host keys and webhook secrets stored as the
// release before sealed them) upgrades to 34: the stand-ins 32 and 33 are
// empty, the three key tables are new and empty, and what was stored stays
// exactly as it was (sealing it is the server's job, with a root key, not the
// migration's). Older builds run on the newer database and leave the key
// tables alone.
func TestSchema34UpgradesFrom31KeepingWhatIsStored(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaSSH, &syncBuffer{})
	if hasTable(t, old, "encryption_keks") {
		t.Fatal("schema 31 already has the key tables")
	}
	if _, err := old.db.Exec(`INSERT INTO meta(key, value) VALUES('shared:runner_token', 'tok-before')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO ssh_host_keys(algo, public, sealed, fingerprint) VALUES('ed25519','pub','sealed-before','fp')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaStandIn32, schemaStandIn33, schemaEncryption} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaSSH)) {
		t.Error("migration 31, already applied, was applied again")
	}
	checkMarkers(t, cur, schemaEncryption)
	for _, tb := range []string{"encryption_state", "encryption_keks", "encryption_deks"} {
		var n int
		if err := cur.db.QueryRow(`SELECT count(*) FROM ` + tb).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows, %v (the migration must not create keys)", tb, n, err)
		}
	}
	var tok, sealed string
	if err := cur.db.QueryRow(`SELECT value FROM meta WHERE key = 'shared:runner_token'`).Scan(&tok); err != nil || tok != "tok-before" {
		t.Fatalf("the runner token changed: %q %v", tok, err)
	}
	if err := cur.db.QueryRow(`SELECT sealed FROM ssh_host_keys WHERE algo = 'ed25519'`).Scan(&sealed); err != nil || sealed != "sealed-before" {
		t.Fatalf("the host key changed: %q %v", sealed, err)
	}
	cur.Close()

	// A build of schema 31 runs on the database of 34 and leaves the new tables be.
	older, err := openAs(t, addr, clk, schemaSSH, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaEncryption || info.Code != schemaSSH || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	if _, err := older.db.Exec(`INSERT INTO encryption_state(key, value) VALUES('installation_id', 'x')`); err != nil {
		t.Fatalf("the tables are not there for a newer server: %v", err)
	}
}

// The three tables enforce what the keyring relies on: one row per key version
// and one per data key version, so that servers racing to create a key
// cannot both win.
func TestSchema34KeyTablesRefuseDuplicates(t *testing.T) {
	e := mustOpenAs(t, testdb.New(t), newClock(), 0, &syncBuffer{})
	defer e.Close()
	for _, q := range []string{
		`INSERT INTO encryption_keks(version, holder, wrapped) VALUES(1, 'key_file', 'w')`,
		`INSERT INTO encryption_deks(purpose, org, version, kek_version, wrapped) VALUES('secrets', '', 1, 1, 'w')`,
		`INSERT INTO encryption_state(key, value) VALUES('installation_id', 'a')`,
	} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.Exec(q); err == nil {
			t.Errorf("a second row was accepted: %s", q)
		}
	}
}
