package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 29 (before rollback runs; 30 and 31 are real) is
// migrated to 32 with the startup guard on: its runs carry
// on and the two tables are there, empty. Going back, a build of schema 29
// starts against the database of 32 (the migration is additive), says so
// once, and leaves a rollback run, which it cannot read, alone: it lists it,
// refuses to change it, and carries on with other runs.
func TestUpgradeFromSchema29ToRollbackAndBack(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	old, err := openAs(t, addr, clk, schemaVariables, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	if hasTable(t, old, "rollbacks") {
		t.Fatal("schema 29 already has the rollbacks table")
	}
	half := create(t, old, linear, Trigger{Event: "manual"})
	ok(t, old, take(t, old), nil)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	for _, v := range []int{schemaMirror, schemaSSH, schemaRollback} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, schemaCacheTrust)
	for _, tb := range []string{"rollbacks", "rollback_reverts"} {
		var n int
		if err := cur.db.QueryRow(`SELECT count(*) FROM ` + tb).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d %v", tb, n, err)
		}
	}
	if j := take(t, cur); j.RunID != half {
		t.Fatalf("the half-done run did not carry on: %+v", j)
	}
	cur.Close()

	// A rollback run, made by the newest build.
	newest := openDeploy(t, addr, clk, prodEnv)
	shopDone(t, newest, 1, "1.0.0", "c1")
	shopDone(t, newest, 2, "2.0.0", "c2")
	sum, err := newest.RequestRollback(RollbackRequest{Project: "shop", Environment: "prod", Actor: "ann", Reason: "r"})
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := newest.db.QueryRow(`SELECT count(*) FROM rollbacks WHERE run_id = ?`, sum.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("the rollbacks row: %d %v", rows, err)
	}
	if sum.MinSchema != schemaRollback {
		t.Fatalf("a rollback run needs schema %d, not %d", schemaRollback, sum.MinSchema)
	}
	newest.Close()

	var olderLog syncBuffer
	older, err := openAs(t, addr, clk, schemaVariables, &olderLog)
	if err != nil {
		t.Fatalf("a build of schema 29 on a database of 32: %v\n%s", err, olderLog.String())
	}
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != schemaCacheTrust || info.Code != schemaVariables || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	if d, err := older.GetRun(sum.ID); err != nil || !d.NeedsNewerServer {
		t.Fatalf("the older build's view of a rollback run: %+v %v", d, err)
	}
	if err := older.Cancel(sum.ID, "ann"); err == nil {
		t.Fatal("the older build changed a rollback run it cannot read")
	}
}

// A database of schema 31 (mirrors, SSH keys and hooks present, with rows)
// upgrades to 32 and applies only the rollback migration: what it holds
// stays, and the two tables are new and empty.
func TestSchema32UpgradesFrom31ApplyingOnlyItself(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaSSH, &syncBuffer{})
	if hasTable(t, old, "rollbacks") {
		t.Fatal("schema 31 already has the rollbacks table")
	}
	if _, err := old.db.Exec(`INSERT INTO ssh_keys(id, kind, owner, name, key_type, public, fingerprint) VALUES('k1','user','ada','laptop','ssh-ed25519','AAAA','SHA256:x')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaRollback)) {
		t.Errorf("migration %d was not applied:\n%s", schemaRollback, log.String())
	}
	for _, v := range []int{schemaMirror, schemaSSH} {
		if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d, already applied, was applied again", v)
		}
	}
	checkMarkers(t, cur, schemaCacheTrust)
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM ssh_keys`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the key did not survive: %d %v", n, err)
	}
	if err := cur.db.QueryRow(`SELECT count(*) FROM rollbacks`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollbacks: %d %v", n, err)
	}
}
