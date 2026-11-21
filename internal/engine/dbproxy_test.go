package engine

import (
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Against a real PostgreSQL behind a proxy that drops every connection: a
// command issued during the outage waits, and succeeds once, when the
// database is back; and a slow network only slows it. This checks the error
// classes against the errors the real driver produces.
func TestStoreRidesThroughRealConnectionLoss(t *testing.T) {
	addr, proxy := testdb.ThroughProxy(t, testdb.New(t))
	clk := &clock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}
	e := open(t, addr, clk)
	t.Cleanup(func() { e.Close() })

	proxy.SetDelay(15 * time.Millisecond)
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, fanIn), Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
		t.Fatalf("a slow network must only slow it: %v", err)
	}
	proxy.SetDelay(0)

	proxy.SetDown(true)
	time.AfterFunc(4*time.Second, func() { proxy.SetDown(false) })
	start := time.Now()
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, fanIn), Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatalf("the command should have waited for the database: %v", err)
	}
	if time.Since(start) < 3*time.Second {
		t.Fatalf("it returned after %s, before the database was back", time.Since(start))
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE id = ?`, r.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("runs %d, err %v", n, err)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d runs in all, want 2 (nothing created twice)", n)
	}
}
