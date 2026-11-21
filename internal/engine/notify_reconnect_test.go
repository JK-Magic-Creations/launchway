package engine

import (
	"database/sql"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// After the listening connection is dropped and comes back, the
// configuration handlers run, not only the wake-up: a configuration change
// whose notification was lost is noticed at once.
func TestListenerReconnectRunsTheConfigHandlers(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	e, _ := newEngine(t)
	var calls atomic.Int32
	e.OnConfigChange(func() { calls.Add(1) })

	admin, err := sql.Open("pgx", os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// Drop the connection that listens for this engine's channel.
	var killed int
	if err := admin.QueryRow(`SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
		WHERE query LIKE 'LISTEN %' AND pid <> pg_backend_pid() AND query LIKE '%' || $1 || '%'`, e.channel).Scan(&killed); err != nil || killed == 0 {
		t.Fatalf("could not drop the listening connection: killed %d, err %v", killed, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("the configuration handler did not run after the listener reconnected")
	}
}

// reconnected does both: wake the dispatcher and run the config handlers.
func TestReconnectedRunsEveryHandler(t *testing.T) {
	e, _ := newEngine(t)
	var calls atomic.Int32
	e.OnConfigChange(func() { calls.Add(1) })
	e.reconnected()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("config handlers ran %d times", calls.Load())
	}
}
