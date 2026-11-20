package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A run records the newest schema any of its features needs: a policy
// governing it 16, artifacts kept in the database store 17, test
// attachments, coverage or HTML reports 18, a runner: choice 21; a run
// using several gets the highest, so a server of any schema between leaves
// it alone.
func TestRunMinSchemaAcrossFeatures(t *testing.T) {
	if schemaPolicy != 16 || schemaBlobs != 17 || schemaReports != 18 || schemaVerify != 19 || schemaIdentity != 20 || schemaPools != 21 || schemaResources != 22 || schemaCache != 25 || len(migrations) != 25 {
		t.Fatalf("schemas: policy %d, blobs %d, reports %d, verify %d, identity %d, pools %d, resources %d, cache %d, migrations %d",
			schemaPolicy, schemaBlobs, schemaReports, schemaVerify, schemaIdentity, schemaPools, schemaResources, schemaCache, len(migrations))
	}
	report := PolicyOptions{Inline: mustPolicies(t, strings.Replace(shapePolicy, "enforce", "report", 1))}
	open := func(t *testing.T, addr, store string, po PolicyOptions) *Engine {
		t.Helper()
		o := ArtifactOptions{Choice: store}
		if store == StoreGiven {
			o.Store = fsStore(t, t.TempDir())
		}
		e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: newClock().Now, Artifacts: o, Policy: po})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { e.Close() })
		return e
	}
	for _, tc := range []struct {
		name     string
		store    string
		policy   PolicyOptions
		pipeline string
		want     int
	}{
		{"plain", StoreDatabase, PolicyOptions{}, linear, 0}, // 0: below 16
		{"artifacts on the filesystem", StoreGiven, PolicyOptions{}, artifactsPipeline, schemaArtifacts},
		{"policy", StoreDatabase, report, linear, schemaPolicy},
		{"policy, artifacts on the filesystem", StoreGiven, report, artifactsPipeline, schemaPolicy},
		{"artifacts in the database", StoreDatabase, PolicyOptions{}, artifactsPipeline, schemaBlobs},
		{"policy and artifacts in the database", StoreDatabase, report, artifactsPipeline, schemaBlobs},
		{"reports", StoreGiven, PolicyOptions{}, coveragePipeline, schemaReports},
		{"reports with attachments in the database", StoreDatabase, PolicyOptions{}, attachmentsPipeline, schemaReports},
		{"policy, artifacts in the database and reports", StoreDatabase, report, attachmentsPipeline + `
  ship:
    image: x
    run: ship
    needs: e2e
    artifacts: [ {name: bin, paths: [bin/app]} ]
`, schemaReports},
		{"runner labels, policy, artifacts in the database and reports", StoreDatabase, report, "runner: { labels: [arm64] }\n" + attachmentsPipeline + `
  ship:
    image: x
    run: ship
    needs: e2e
    artifacts: [ {name: bin, paths: [bin/app]} ]
`, schemaPools},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := testdb.New(t)
			e := open(t, addr, tc.store, tc.policy)
			id := create(t, e, tc.pipeline, Trigger{Event: "manual"})
			sum, err := e.Summary(id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == 0 {
				if sum.MinSchema >= schemaPolicy {
					t.Fatalf("min_schema %d for a run using none of 16 to 18", sum.MinSchema)
				}
				return
			}
			if sum.MinSchema != tc.want {
				t.Fatalf("min_schema %d, want %d", sum.MinSchema, tc.want)
			}
			// A server of the schema below leaves it alone.
			old, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: newClock().Now, schema: tc.want - 1})
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
				t.Fatalf("schema %d's view: %v %v", tc.want-1, d.NotUnderstood, err)
			}
		})
	}
}

// A database a release of schema 16 (policy gates, as main was before the
// blob store and reports were merged) migrated and used is migrated to 18
// by this build, and its runs carry on. Going back, the build of 16 starts
// against the database of 18 (every migration since is additive), says so,
// and refuses what it does not know: it leaves runs that need 17 or 18
// alone, will not cancel them, and will not create runs that use reports.
func TestUpgradeFromSchema16AndBack(t *testing.T) {
	for n := schemaPolicy + 1; n <= len(migrations); n++ {
		if migrations[n-1].compat != additive {
			t.Fatalf("migration %d must be additive", n)
		}
	}
	clk := newClock()
	addr := testdb.New(t)
	var log16 syncBuffer
	e16, err := openAs(t, addr, clk, schemaPolicy, &log16)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := e16.Schema(context.Background()); info.Database != schemaPolicy {
		t.Fatalf("schema-16 build migrated to %+v", info)
	}
	half := create(t, e16, linear, Trigger{Event: "manual"})
	ok(t, e16, take(t, e16), nil)
	e16.Close()

	// Upgrade: this build migrates 17 and 18 and carries the run on.
	var logCur syncBuffer
	cur, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Logger: slogTo(&logCur),
		Artifacts: ArtifactOptions{Choice: StoreAuto, Store: fsStore(t, t.TempDir())}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cur.Close() })
	if info, err := cur.Schema(context.Background()); err != nil || info.Database != len(migrations) || info.Code != len(migrations) || !info.Compatible() {
		t.Fatalf("after the upgrade: %+v %v", info, err)
	}
	for !strings.Contains(statuses(t, cur, half)["run"], "succeeded") {
		ok(t, cur, take(t, cur), nil)
	}
	rich := create(t, cur, coveragePipeline, Trigger{Event: "manual"})

	// Back to 16: it starts, says once that the database is newer, and
	// refuses the run of 18.
	var back syncBuffer
	old := mustOpenAs(t, addr, clk, schemaPolicy, &back)
	if info, err := old.Schema(context.Background()); err != nil || info.Code != schemaPolicy || info.Database != len(migrations) || !info.Compatible() {
		t.Fatalf("the schema-16 build's view: %+v %v", info, err)
	}
	if c := strings.Count(back.String(), "running against a database written by a newer version"); c != 1 {
		t.Fatalf("said %d times that the database is newer:\n%s", c, back.String())
	}
	if d, err := old.GetRun(rich); err != nil || !d.NeedsNewerServer {
		t.Fatalf("the schema-16 build's view of a reports run: %+v %v", d, err)
	}
	if err := old.Cancel(rich, "ann"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("cancelled a run it does not understand: %v", err)
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, coveragePipeline), Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/tmp"}}); err == nil {
		t.Fatal("the schema-16 build created a run that uses reports")
	}
	if j, _ := old.Lease(context.Background(), runner, 0); j != nil {
		t.Fatalf("the schema-16 build leased %s/%s", j.RunID, j.StepID)
	}
	if d, err := old.GetRun(half); err != nil || d.NeedsNewerServer || d.Status != RunSucceeded {
		t.Fatalf("its own run: %+v %v", d, err)
	}
}

// A run with a verify step (19) that also keeps artifacts in the database
// store (17), declares coverage (18) and is governed by a policy (16)
// records 19, and a build of 18 leaves it alone.
func TestVerifyRunTakesTheHighestSchema(t *testing.T) {
	clk := newClock()
	addr := testdb.New(t)
	ps := mustPolicies(t, "policies:\n  - name: shape\n    mode: report\n    scope: { projects: [shop] }\n    rules:\n      step_images: { digest: true }\n")
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Environments: prodEnv, HealthSources: prom,
		Artifacts: ArtifactOptions{Choice: StoreDatabase}, Policy: PolicyOptions{Inline: ps}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	src := envVerify + `  build:
    image: x
    run: make
    artifacts: [ {name: bin, paths: [bin/app]} ]
    reports: { coverage: cover.out }
`
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, src), Trigger: Trigger{Event: "manual"}, Source: gitSource})
	if err != nil {
		t.Fatal(err)
	}
	if sum, err := e.Summary(r.ID); err != nil || sum.MinSchema != schemaVerify {
		t.Fatalf("min_schema %d %v", sum.MinSchema, err)
	}
	old := mustOpenAs(t, addr, clk, schemaVerify-1, &syncBuffer{})
	if d, err := old.GetRun(r.ID); err != nil || !d.NeedsNewerServer {
		t.Fatalf("the schema-18 build's view: %v %v", d.NotUnderstood, err)
	}
}
