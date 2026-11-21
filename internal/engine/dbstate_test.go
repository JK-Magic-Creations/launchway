package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func TestDatabaseStateReportsWritableNotOnlyReachable(t *testing.T) {
	e, _ := newEngine(t)
	ctx := context.Background()
	st, err := e.DatabaseState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Writable || st.Standby {
		t.Fatalf("a normal database is writable: %+v", st)
	}
	// Reachable, but taking no writes: a standby or a read-only database.
	e.db.SetMaxOpenConns(1)
	if e.db.pg {
		if _, err := e.db.Exec(`SET default_transaction_read_only = on`); err != nil {
			t.Fatal(err)
		}
	} else if _, err := e.db.Exec(`PRAGMA query_only = 1`); err != nil {
		t.Fatal(err)
	}
	if err := e.Ping(ctx); err != nil {
		t.Fatalf("a read-only database still answers: %v", err)
	}
	st, err = e.DatabaseState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Writable {
		t.Fatalf("a read-only database must not be reported writable: %+v", st)
	}
}

func TestPostgresStateSaysStandbyAndReplication(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	e, _ := newEngine(t)
	st, err := e.DatabaseState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != "postgresql" || st.Standby || !st.Writable || st.Version == "" {
		t.Fatalf("%+v", st)
	}
	if st.ReplicationVisible && (st.Replication == nil || st.Replication.Standbys != 0) {
		t.Fatalf("a lone primary has no standbys: %+v", st.Replication)
	}
	if st.Archiving == nil {
		t.Fatal("archiving state is visible to any user")
	}
}

func TestAssessmentSaysPlainlyWhatASingleDatabaseCosts(t *testing.T) {
	single := DatabaseState{Kind: "postgresql", Version: "17.2", Writable: true, ReplicationVisible: true, Replication: &ReplicationState{}}
	a := single.Assess()
	text := strings.Join(a.Lines, " ")
	if a.Level != "not highly available" || !strings.Contains(text, "a database failure stops Launchway until it is restored") {
		t.Fatalf("%s: %s", a.Level, text)
	}
	if !strings.Contains(text, "last backup") {
		t.Fatalf("it must say what is known about backups: %s", text)
	}
	cases := map[string]DatabaseState{
		"at risk":              {Kind: "postgresql", Version: "17", Writable: true, ReplicationVisible: true, Replication: &ReplicationState{Standbys: 1}},
		"highly available":     {Kind: "postgresql", Version: "17", Writable: true, ReplicationVisible: true, Replication: &ReplicationState{Standbys: 2, SyncStandbys: 1}},
		"unknown":              {Kind: "postgresql", Version: "17", Writable: true},
		"not highly available": {Kind: "sqlite", Writable: true},
	}
	for want, st := range cases {
		if got := st.Assess().Level; got != want {
			t.Errorf("want %q, got %q for %+v", want, got, st)
		}
	}
	standby := DatabaseState{Kind: "postgresql", Version: "17", Standby: true}
	if a := standby.Assess(); a.Level != "at risk" || !strings.Contains(a.Lines[0], "standby") {
		t.Fatalf("%+v", a)
	}
}
