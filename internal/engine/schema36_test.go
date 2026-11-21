package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/sealing"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 33 (before encryption, the cache trust changes and
// built-in accounts) upgrades to 36 in one start, keeping everything, on
// SQLite and, with LAUNCHWAY_TEST_POSTGRES, PostgreSQL; an older build still
// reads it. This is the path of the installation deployed before 34.
func TestUpgradeFromSchema33ToAccounts(t *testing.T) {
	clk := newClock()
	addr := testdb.New(t)
	var log0 syncBuffer
	old := mustOpenAs(t, addr, clk, schemaPulls, &log0)
	if hasTable(t, old, "local_accounts") {
		t.Fatal("a schema-33 database already has the accounts table")
	}
	exec33 := func(q string) {
		t.Helper()
		if _, err := old.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec33(`INSERT INTO meta(key, value) VALUES('shared:runner_token', 'tok-before')`)
	exec33(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status) VALUES('cch_a', 'p', 'default', 'deps', 'k1', 'linux/amd64', 'caches/a', 'kept')`)
	exec33(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('p1','repo1',1,'t','ada','f','main')`)
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaEncryption, schemaCacheTrust, schemaAccounts} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, len(migrations))
	info, err := cur.Schema(context.Background())
	if err != nil || info.Database != len(migrations) || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}

	// What was stored survives, and the servers' sealing then seals the
	// runner token in place under a root key, as an upgraded installation does.
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM caches WHERE id = 'cch_a' AND generation = 1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the cache entry did not survive: %d %v", n, err)
	}
	if err := cur.db.QueryRow(`SELECT count(*) FROM pull_requests WHERE id = 'p1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the pull request did not survive: %d %v", n, err)
	}
	sealAndCheckRunnerToken(t, cur, "tok-before")

	older := mustOpenAs(t, addr, clk, schemaPulls, &log0)
	defer older.Close()
	if info, err := older.Schema(context.Background()); err != nil || info.Database != len(migrations) || info.Code != schemaPulls || !info.Compatible() {
		t.Fatalf("the schema-33 build's view: %+v %v", info, err)
	}
}

// sealAndCheckRunnerToken starts the sealing service with a fresh root key on
// the database, as the first upgraded server does: the plain runner token is
// sealed in place and reads back the same.
func sealAndCheckRunnerToken(t *testing.T, e *Engine, want string) {
	t.Helper()
	ctx := context.Background()
	k, err := keyring.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := keyring.FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	s, err := sealing.Start(ctx, sealing.Options{DB: e.Database(), Holder: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.SharedValue("runner_token", "unused"); err != nil || v != want {
		t.Fatalf("runner token under the key: %q %v", v, err)
	}
	var stored string
	if err := e.db.QueryRow(`SELECT value FROM meta WHERE key = 'shared:runner_token'`).Scan(&stored); err != nil || stored == want || strings.Contains(stored, want) {
		t.Fatalf("the runner token is not sealed in the database: %q %v", stored, err)
	}
}

// A database of schema 35 that already holds secrets sealed under a root key,
// cache entries and a pull request upgrades by migration 36 alone: the sealed
// values still open with the same key, every row stays, and a build of
// schema 35 still runs against the result.
func TestUpgradeFromSchema35ToAccountsWithSealedSecrets(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	ctx := context.Background()
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaCacheTrust, &syncBuffer{})
	exec := func(q string) {
		t.Helper()
		if _, err := old.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO meta(key, value) VALUES('shared:runner_token', 'tok-before')`)
	exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status) VALUES('cch_a', 'p', 'default', 'deps', 'k1', 'linux/amd64', 'caches/a', 'kept')`)
	exec(`INSERT INTO pull_requests(id, repo_id, number, title, author, source_branch, target_branch) VALUES('p1','repo1',1,'t','ada','f','main')`)
	k, err := keyring.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := keyring.FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := sealing.Start(ctx, sealing.Options{DB: old.Database(), Holder: h, Log: quiet, Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.SharedValue("runner_token", "unused"); err != nil || v != "tok-before" {
		t.Fatalf("runner token under the key: %q %v", v, err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	if strings.Count(log.String(), "applied a database migration") != len(migrations)-schemaCacheTrust || !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaAccounts)) {
		t.Fatalf("only migrations after 35 should apply:\n%s", log.String())
	}
	checkMarkers(t, cur, len(migrations))
	s2, err := sealing.Start(ctx, sealing.Options{DB: cur.Database(), Holder: h, Log: quiet, Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s2.SharedValue("runner_token", "unused"); err != nil || v != "tok-before" {
		t.Fatalf("runner token after the upgrade: %q %v", v, err)
	}
	if r, err := s2.Report(ctx); err != nil || r.Remaining != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM caches WHERE id = 'cch_a'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the cache entry did not survive: %d %v", n, err)
	}
	if err := cur.db.QueryRow(`SELECT count(*) FROM pull_requests WHERE id = 'p1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the pull request did not survive: %d %v", n, err)
	}
	cur.Close()
	older, err := openAs(t, addr, clk, schemaCacheTrust, &syncBuffer{})
	if err != nil {
		t.Fatalf("a build of schema 35 on a database of schema 36: %v", err)
	}
	older.Close()
}
