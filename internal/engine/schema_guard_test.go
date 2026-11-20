package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A feature-branch build could record schema 21 while leaving placeholders
// for migrations owned by another branch. Opening the merged build must say
// exactly which objects those placeholders omitted, before serving any work.
func TestSchemaGuardRefusesIncompleteDatabase(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	if _, err := e.db.Exec(`DROP TABLE coverage_files`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`ALTER TABLE steps DROP COLUMN kind`); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := openAs(t, addr, clk, 0, &syncBuffer{})
	want := "database schema check failed: recorded schema 21 does not match its tables and columns: " +
		"schema 18 (richer test reports and coverage): missing table coverage_files; " +
		"schema 19 (verify and ai steps): missing column steps.kind. " +
		"Restore the database from a known-good backup, or recreate only the listed objects with the corresponding migration SQL in internal/engine/db.go; " +
		"do not change meta.schema_version to hide the mismatch. To start anyway, set LAUNCHWAY_SKIP_SCHEMA_CHECK=1 (unsafe: runs may fail later)"
	if err == nil || err.Error() != want {
		t.Fatalf("opening an incomplete database:\n got: %v\nwant: %s", err, want)
	}
}

// Every schema an older supported build can record passes the guard. In
// particular, a build below 17 is not required to have later markers.
func TestSchemaGuardAcceptsSupportedSchemas(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	for n := 7; n <= len(migrations); n++ {
		t.Run(fmt.Sprintf("schema %d", n), func(t *testing.T) {
			clk, addr := newClock(), testdb.New(t)
			e, err := openAs(t, addr, clk, n, &syncBuffer{})
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			info, err := e.Schema(context.Background())
			if err != nil || info.Database != n || info.Code != n || !info.Compatible() {
				t.Fatalf("schema %d: %+v, %v", n, info, err)
			}
		})
	}
}

// The escape hatch is deliberate and visible in the log. It lets an operator
// repair a database while running a build that would otherwise refuse it.
func TestSchemaGuardEscapeHatch(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	if _, err := e.db.Exec(`DROP TABLE coverage_files`); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	t.Setenv(schemaCheckSkipEnv, "1")
	var log syncBuffer
	e, err := openAs(t, addr, clk, 0, &log)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if !strings.Contains(log.String(), "starting despite an incomplete database schema") ||
		!strings.Contains(log.String(), "coverage_files") || !strings.Contains(log.String(), schemaCheckSkipEnv+"=1") {
		t.Fatalf("escape hatch log does not name the mismatch and reason:\n%s", log.String())
	}
}

// A migration that removes a marked table sets retired on its marker: from
// that schema on the guard no longer asks for the table, while a database
// recorded before still must have it.
func TestSchemaGuardSkipsRetiredMarkers(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	e := mustOpenAs(t, testdb.New(t), newClock(), 0, &syncBuffer{})
	old := schemaMarkers
	t.Cleanup(func() { schemaMarkers = old })
	log := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	const future = 40
	schemaMarkers = append(append([]schemaMarker{}, old...),
		schemaMarker{schema: future, feature: "a future table", tables: []string{"no_such_table"}, retired: future + 2})

	if err := checkSchemaMarkers(e.db, future-1, log); err != nil {
		t.Errorf("before the marker's schema: %v", err)
	}
	if err := checkSchemaMarkers(e.db, future, log); err == nil || !strings.Contains(err.Error(), "no_such_table") {
		t.Errorf("at the marker's schema the missing table is not refused: %v", err)
	}
	if err := checkSchemaMarkers(e.db, future+1, log); err == nil {
		t.Errorf("before the marker is retired the missing table is not refused")
	}
	for _, recorded := range []int{future + 2, future + 5} {
		if err := checkSchemaMarkers(e.db, recorded, log); err != nil {
			t.Errorf("recorded %d, at or past the retirement: %v", recorded, err)
		}
	}
	// A retired marker does not excuse the others.
	if _, err := e.db.Exec(`DROP TABLE coverage_files`); err != nil {
		t.Fatal(err)
	}
	if err := checkSchemaMarkers(e.db, future+5, log); err == nil || !strings.Contains(err.Error(), "coverage_files") {
		t.Errorf("a retired marker hid another marker's missing table: %v", err)
	}
}

// Rolling back means rolling back the image, never the database: an older
// build opens a database recorded at a newer schema, within schema_min, and
// the guard, which asks for what the recorded schema brought, finds it all
// and does not refuse.
func TestSchemaGuardLetsAnOlderBuildStartOnANewerDatabase(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "")
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	newest := len(migrations)
	info, err := e.Schema(context.Background())
	if err != nil || info.Database != newest {
		t.Fatalf("the database: %+v %v", info, err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	for _, older := range []int{newest - 1, schemaVerify, schemaBlobs} {
		if older < info.Minimum {
			t.Fatalf("schema %d is below the database's minimum %d: the test would not show a supported rollback", older, info.Minimum)
		}
		var log syncBuffer
		old, err := openAs(t, addr, clk, older, &log)
		if err != nil {
			t.Fatalf("a build of schema %d on a database of schema %d: %v", older, newest, err)
		}
		got, err := old.Schema(context.Background())
		if err != nil || got.Database != newest || got.Code != older || !got.Compatible() {
			t.Errorf("a build of schema %d: %+v %v", older, got, err)
		}
		if strings.Contains(log.String(), "schema check failed") {
			t.Errorf("the guard spoke against a rollback:\n%s", log.String())
		}
		old.Close()
	}
}
