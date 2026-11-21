package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func TestApplicationNameIsAddedOnlyWhenAbsent(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://u:p@h:5432/d?sslmode=disable":  "postgres://u:p@h:5432/d?application_name=launchway&sslmode=disable",
		"postgres://u:p@h/d?application_name=mine": "postgres://u:p@h/d?application_name=mine",
		"host=h user=u dbname=d":                   "host=h user=u dbname=d",
		"/var/lib/launchway/launchway.db":          "/var/lib/launchway/launchway.db",
	} {
		if got := withApplicationName(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// On PostgreSQL a connection that does not name itself (a server of an older
// release) is counted; this build's own connections are not; SQLite has none.
func TestOlderServersSeesUnnamedConnections(t *testing.T) {
	addr := testdb.New(t)
	e := mustOpenAs(t, addr, newClock(), 0, &syncBuffer{})
	ctx := context.Background()
	base, err := e.OlderServers(ctx) // the test helper's own connection, if it keeps one, is not this build's
	if err != nil {
		t.Fatal(err)
	}
	if !testdb.Postgres() {
		if base != 0 {
			t.Fatalf("SQLite: %d", base)
		}
		return
	}
	old, err := sql.Open("pgx", addr) // as the older release did: no application_name
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	conn, err := old.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.OlderServers(ctx); err != nil || n != base+1 {
		t.Fatalf("with an unnamed connection: %d %v", n, err)
	}
	conn.Close()
	old.Close()
	deadline := time.Now().Add(5 * time.Second)
	n := base + 1
	for n != base && time.Now().Before(deadline) {
		n, _ = e.OlderServers(ctx)
		time.Sleep(50 * time.Millisecond)
	}
	if n != base {
		t.Fatalf("after it closed: %d", n)
	}
}
