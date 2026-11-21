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

// A database of schema 32 is migrated to 35 with the startup guard on: the
// cache table carries the new columns, entries written before stay (and are
// never read by a run), and a build of schema 32 starts against the newer
// database and carries on.
func TestUpgradeFromSchema32ToCacheTrustAndBack(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	old, err := openAs(t, addr, clk, schemaRollback, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status) VALUES('cch_old', 'p', 'default', 'deps', 'k', 'linux/amd64', 'caches/old', 'kept')`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaPulls, schemaEncryption, schemaCacheTrust} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	checkMarkers(t, cur, schemaCacheTrust)
	var pipeline string
	var gen, sens int
	if err := cur.db.QueryRow(`SELECT pipeline, generation, sensitive FROM caches WHERE id = 'cch_old'`).Scan(&pipeline, &gen, &sens); err != nil || pipeline != "" || gen != 1 || sens != 0 {
		t.Fatalf("the old entry: %q %d %d %v", pipeline, gen, sens, err)
	}
	// Two generations of one key are allowed now; the same generation twice is not.
	ins := func(id string, g int) error {
		_, err := cur.db.Exec(`INSERT INTO caches(id, project, scope, pipeline, name, key, platform, blob_key, status, generation) VALUES(?, 'p', 'default', 'ci', 'deps', 'k', 'linux/amd64', ?, 'kept', ?)`, id, "caches/"+id, g)
		return err
	}
	if err := ins("cch_g1", 1); err != nil {
		t.Fatal(err)
	}
	if err := ins("cch_g2", 2); err != nil {
		t.Fatalf("a second generation of a key: %v", err)
	}
	if err := ins("cch_g2b", 2); err == nil {
		t.Fatal("the same generation of a key was stored twice")
	}
	cur.Close()
	older, err := openAs(t, addr, clk, schemaRollback, &syncBuffer{})
	if err != nil {
		t.Fatalf("a build of schema 32 on a database of schema 35: %v", err)
	}
	older.Close()
}

// A database at schema 34, with secrets sealed under a root key and cache
// entries written by the release before, upgrades by migration 35 alone: the
// sealed values still open, every entry stays (and, written before the key
// held the image digest and the scope, none is ever restored), and a build of
// schema 34 still runs against the result.
func TestSchema35UpgradesFrom34WithSealedSecretsAndCacheEntries(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	ctx := context.Background()
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaEncryption, &syncBuffer{})
	if hasColumn(t, old, "caches", "generation") {
		t.Fatal("schema 34 already has cache generations")
	}
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := old.db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO meta(key, value) VALUES('shared:runner_token', 'tok-before')`)
	exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status) VALUES('cch_a', 'p', 'default', 'deps', 'k1', 'linux/amd64', 'caches/a', 'kept')`)
	exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status) VALUES('cch_b', 'p', 'default', 'build', 'k2', 'linux/arm64', 'caches/b', 'kept')`)
	k, err := keyring.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	h, err := keyring.FromKey("key_file", "test", k)
	if err != nil {
		t.Fatal(err)
	}
	s, err := sealing.Start(ctx, sealing.Options{DB: old.Database(), Holder: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Instance: "t"})
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
	if strings.Count(log.String(), "applied a database migration") != 1 || !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaCacheTrust)) {
		t.Fatalf("only migration 35 should apply:\n%s", log.String())
	}
	checkMarkers(t, cur, schemaCacheTrust)
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM caches WHERE pipeline = '' AND generation = 1 AND sensitive = 0`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("the old entries: %d %v", n, err)
	}
	// The sealed values open as before, with the same key.
	s2, err := sealing.Start(ctx, sealing.Options{DB: cur.Database(), Holder: h, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Instance: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s2.SharedValue("runner_token", "unused"); err != nil || v != "tok-before" {
		t.Fatalf("runner token after the upgrade: %q %v", v, err)
	}
	if r, err := s2.Report(ctx); err != nil || r.Remaining != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	// Entries of no pipeline are never restored: a lookup in a scope finds none.
	if err := cur.db.QueryRow(`SELECT count(*) FROM caches WHERE pipeline = 'ci' AND name = 'deps'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("an entry would be restored by a pipeline: %d %v", n, err)
	}
	cur.Close()
	older, err := openAs(t, addr, clk, schemaEncryption, &syncBuffer{})
	if err != nil {
		t.Fatalf("a build of schema 34 on a database of schema 35: %v", err)
	}
	older.Close()
}

// 31 to 35 in one start: every migration in between applies once, in order,
// and what was stored stays.
func TestSchema35UpgradesFrom31(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaSSH, &syncBuffer{})
	if _, err := old.db.Exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, status) VALUES('cch_old', 'p', 'default', 'deps', 'k', 'linux/amd64', 'caches/old', 'kept')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	var log syncBuffer
	cur, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer cur.Close()
	for _, v := range []int{schemaRollback, schemaPulls, schemaEncryption, schemaCacheTrust} {
		if !strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, log.String())
		}
	}
	if strings.Contains(log.String(), fmt.Sprintf(`applied a database migration" schema=%d `, schemaSSH)) {
		t.Error("migration 31, already applied, was applied again")
	}
	checkMarkers(t, cur, schemaCacheTrust)
	var n int
	if err := cur.db.QueryRow(`SELECT count(*) FROM caches WHERE id = 'cch_old'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the entry did not survive: %d %v", n, err)
	}
}
