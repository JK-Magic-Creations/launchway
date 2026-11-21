package sealing

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A server that finds secrets in the clear while an older server runs does
// not seal them (the older server would read a sealed value as the value),
// reports that it waits, and seals once no older server has been seen for the
// quiet time; a fresh installation is not delayed at all.
func TestSealingOfExistingSecretsWaitsForOlderServers(t *testing.T) {
	addr := testdb.New(t)
	e := openEngine(t, addr)
	if _, err := e.Database().Exec(ctx, `INSERT INTO meta(key, value) VALUES('shared:runner_token', 'tok-before')`); err != nil {
		t.Fatal(err)
	}
	var older atomic.Int64
	older.Store(2)
	s, err := Start(ctx, Options{DB: e.Database(), Holder: newHolder(t), Log: quiet, Instance: "new-1",
		OlderServers: func(context.Context) (int, error) { return int(older.Load()), nil }, QuietFor: 150 * time.Millisecond, Poll: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// Served at once, the old value still readable, nothing sealed while an older server is seen.
	if v, err := s.SharedValue("runner_token", "unused"); err != nil || v != "tok-before" {
		t.Fatalf("the runner token before sealing: %q %v", v, err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := metaValue(t, e, "runner_token"); got != "tok-before" {
		t.Fatalf("sealed while an older server was still seen: %q", got)
	}
	r, err := s.Report(ctx)
	if err != nil || !r.Migration.Waiting || r.Migration.OlderServers != 2 || r.Migration.State != "waiting for older servers" || r.DumpAloneIsUseless {
		t.Fatalf("%+v %v", r.Migration, err)
	}
	if !strings.Contains(r.Summary, "previous release") {
		t.Fatalf("the report does not say why: %s", r.Summary)
	}
	// A new secret is sealed from the first moment, whatever waits.
	if v, err := s.SharedValue("brand_new_key", "fresh-value"); err != nil || v != "fresh-value" {
		t.Fatal(err)
	}
	if got := metaValue(t, e, "brand_new_key"); got == "fresh-value" {
		t.Fatal("a new secret was stored in the clear while sealing waits")
	}
	// The older servers stop; after the quiet time it seals, by itself.
	older.Store(0)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := metaValue(t, e, "runner_token"); got != "tok-before" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if got := metaValue(t, e, "runner_token"); got == "tok-before" {
		t.Fatal("never sealed after the older servers stopped")
	}
	if v, err := s.SharedValue("runner_token", "unused"); err != nil || v != "tok-before" {
		t.Fatalf("the runner token after sealing: %q %v", v, err)
	}
	time.Sleep(100 * time.Millisecond)
	if r, _ := s.Report(ctx); !r.DumpAloneIsUseless || r.Migration.Waiting {
		t.Fatalf("%+v", r.Migration)
	}

	// A fresh database has nothing to wait for.
	e2 := openEngine(t, testdb.New(t))
	s2, err := Start(ctx, Options{DB: e2.Database(), Holder: newHolder(t), Log: quiet, Instance: "new-1",
		OlderServers: func(context.Context) (int, error) { return 5, nil }, QuietFor: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := s2.Report(ctx); r.Migration.Waiting || r.Remaining != 0 {
		t.Fatalf("%+v", r)
	}
}
