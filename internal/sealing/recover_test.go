package sealing

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func kekRows(t *testing.T, db DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The state an image upgraded under an older chart left: a key-encryption key
// wrapped by a throwaway root key, nothing sealed under it.
func TestRecoverDiscardsAnEmptyOrphanKey(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	throwaway, real := newHolder(t), newHolder(t)
	start(t, e, throwaway)

	_, err := Start(ctx, Options{DB: e.Database(), Holder: real, Log: quiet})
	if err == nil || !strings.Contains(err.Error(), "does not open this database's encryption keys") || !strings.Contains(err.Error(), "launchway encryption recover") {
		t.Fatalf("the refusal must name the recover command: %v", err)
	}
	var events []keyring.Event
	o := Options{DB: e.Database(), Holder: real, Log: quiet, Instance: "cli", Audit: func(_ context.Context, ev keyring.Event) { events = append(events, ev) }}
	rep, err := Recover(ctx, o, false)
	if err != nil || !rep.Discardable || rep.Discarded || len(rep.Orphans) != 1 || kekRows(t, e.Database()) != 1 {
		t.Fatalf("show only: %+v %v", rep, err)
	}
	rep, err = Recover(ctx, o, true)
	if err != nil || !rep.Discarded || kekRows(t, e.Database()) != 0 {
		t.Fatalf("discard: %+v %v", rep, err)
	}
	if len(events) != 1 || events[0].Action != "encryption.recovered" {
		t.Fatalf("audit: %+v", events)
	}
	s := start(t, e, real)
	if !s.Encrypted() || kekRows(t, e.Database()) != 1 {
		t.Fatal("the next start did not make a proper key")
	}
	if _, err := s.SharedValue("k", "v"); err != nil {
		t.Fatal(err)
	}
}

// Anything sealed: refuse, and explain the only ways out.
func TestRecoverRefusesWhenSomethingIsSealed(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	old, other := newHolder(t), newHolder(t)
	s := start(t, e, old)
	if _, err := s.SharedValue("k", "v"); err != nil {
		t.Fatal(err)
	}
	_, err := Start(ctx, Options{DB: e.Database(), Holder: other, Log: quiet})
	if err == nil || strings.Contains(err.Error(), "encryption recover") {
		t.Fatalf("the recover command must not be offered when secrets are sealed: %v", err)
	}
	rep, err := Recover(ctx, Options{DB: e.Database(), Holder: other, Log: quiet, Instance: "cli"}, true)
	if err != nil || rep.Discardable || rep.Discarded || kekRows(t, e.Database()) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if !strings.Contains(rep.Message, "supply the root key") || !strings.Contains(rep.Message, "restore the database") {
		t.Fatal(rep.Message)
	}
	// The right key still opens everything.
	if v, err := start(t, e, old).SharedValue("k", "x"); err != nil || v != "v" {
		t.Fatal(v, err)
	}
}

// With the lease held by another server or command, recover waits its turn;
// two recoveries at once discard once.
func TestRecoverTakesTheLease(t *testing.T) {
	e := openEngine(t, testdb.New(t))
	start(t, e, newHolder(t))
	real := newHolder(t)
	if _, err := e.Database().Exec(ctx, `INSERT INTO encryption_state(key, value) VALUES('migration_lease', ?)`, "other|"+millis(timeNowPlus(5))); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(ctx, Options{DB: e.Database(), Holder: real, Log: quiet}, true); err == nil || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("%v", err)
	}
	if _, err := e.Database().Exec(ctx, `DELETE FROM encryption_state WHERE key = 'migration_lease'`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Recover(ctx, Options{DB: e.Database(), Holder: real, Log: quiet, Instance: "cli"}, true)
		}()
	}
	wg.Wait()
	if kekRows(t, e.Database()) != 0 {
		t.Fatal("the orphan is still there")
	}
}
