package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A database of schema 22, as the release before configuration in the
// database, flow control and build caches left it, is migrated to the newest
// schema with the startup schema guard on, and its runs carry on. Going back,
// a build of schema 22 starts against the database of the newest schema (every
// migration since is additive), says so once, passes the guard, and takes and
// finishes a plain run; a run of a pipeline that needs flow control or caches
// is left alone by it until a newer build runs again.
func TestUpgradeFromSchema22AndBack(t *testing.T) {
	t.Setenv(schemaCheckSkipEnv, "") // the guard is on, as in a release
	if schemaResources != 22 || len(migrations) < schemaCache {
		t.Fatalf("schemas: resources %d, migrations %d", schemaResources, len(migrations))
	}
	const flow = "pipeline: p\nsteps:\n  a: {image: x, run: a, on_failure: continue}\n"
	clk := newClock()
	addr := testdb.New(t)

	// What the release before left: a run half done.
	old, err := openAs(t, addr, clk, schemaResources, &syncBuffer{})
	if err != nil {
		t.Fatal(err)
	}
	checkMarkers(t, old, schemaResources)
	if info, err := old.Schema(context.Background()); err != nil || info.Database != schemaResources || info.Code != schemaResources {
		t.Fatalf("the schema-22 database: %+v %v", info, err)
	}
	half := create(t, old, linear, Trigger{Event: "manual"})
	ok(t, old, take(t, old), nil)
	old.Close()

	// Upgrade: every migration from 23 to the newest is applied once, the guard
	// passes, and the half-done run carries on.
	var logCur syncBuffer
	cur, err := openAs(t, addr, clk, 0, &logCur)
	if err != nil {
		t.Fatalf("the newest build on a schema-22 database: %v\n%s", err, logCur.String())
	}
	info, err := cur.Schema(context.Background())
	if err != nil || info.Database != schemaCache || info.Code != schemaCache || !info.Compatible() || info.Minimum != 7 {
		t.Fatalf("after the upgrade: %+v %v", info, err)
	}
	for v := schemaResources + 1; v <= schemaCache; v++ {
		if !strings.Contains(logCur.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
			t.Errorf("migration %d was not applied:\n%s", v, logCur.String())
		}
	}
	if strings.Contains(logCur.String(), "schema check failed") {
		t.Fatalf("the startup guard spoke:\n%s", logCur.String())
	}
	checkMarkers(t, cur, schemaCache)
	if j := take(t, cur); j.RunID != half {
		t.Fatalf("the half-done run did not carry on: %+v", j)
	}
	cur.Close()

	// Rollback: the schema-22 build starts on the newest database, with the guard.
	newest := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	plain := createIn(t, newest, "shop", oneStep, Trigger{Event: "push"})
	flowRun := createIn(t, newest, "shop", flow, Trigger{Event: "push"})
	newest.Close()

	var log syncBuffer
	older, err := openAs(t, addr, clk, schemaResources, &log)
	if err != nil {
		t.Fatalf("a build of schema 22 on a database of schema %d: %v\n%s", schemaCache, err, log.String())
	}
	t.Cleanup(func() { older.Close() })
	got, err := older.Schema(context.Background())
	if err != nil || got.Database != schemaCache || got.Code != schemaResources || !got.Compatible() {
		t.Fatalf("the schema-22 build's view: %+v %v", got, err)
	}
	if c := strings.Count(log.String(), "running against a database written by a newer version"); c != 1 {
		t.Fatalf("it said %d times that the database is newer:\n%s", c, log.String())
	}
	if strings.Contains(log.String(), "schema check failed") {
		t.Fatalf("the guard spoke against a rollback:\n%s", log.String())
	}
	if d, err := older.GetRun(flowRun); err != nil || !d.NeedsNewerServer {
		t.Fatalf("the schema-22 build's view of a flow control run: %+v %v", d, err)
	}
	arm := RunnerInfo{ID: "r22", Name: "r22", Features: FeatureArtifacts}
	for {
		j, err := older.Lease(context.Background(), arm, 0)
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		if j.RunID == flowRun {
			t.Fatalf("the schema-22 build leased the flow control run")
		}
		if _, err := older.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		ok(t, older, j, nil)
		if j.RunID == plain {
			break
		}
	}
	if d, err := older.GetRun(plain); err != nil || d.Status != RunSucceeded {
		t.Fatalf("the plain run on the schema-22 build: %+v %v", d, err)
	}
}
