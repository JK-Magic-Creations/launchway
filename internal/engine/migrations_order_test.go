package engine

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// hasColumn reports whether the database has the table's column.
func hasColumn(t *testing.T, e *Engine, table, column string) bool {
	t.Helper()
	ok, err := schemaColumnExists(e.db, table, column)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// hasTable reports whether the database has the table.
func hasTable(t *testing.T, e *Engine, table string) bool {
	t.Helper()
	ok, err := schemaTableExists(e.db, table)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// checkMarkers fails unless exactly what schemas up to n brought is there.
func checkMarkers(t *testing.T, e *Engine, n int) {
	t.Helper()
	for _, m := range schemaMarkers {
		should := m.dueAt(n)
		for _, tb := range m.tables {
			if got := hasTable(t, e, tb); got != should {
				t.Errorf("at schema %d table %s (%s, schema %d): present %v, want %v", n, tb, m.feature, m.schema, got, should)
			}
		}
		for _, c := range m.columns {
			tb, col, _ := strings.Cut(c, ".")
			if got := hasColumn(t, e, tb, col); got != should {
				t.Errorf("at schema %d column %s (%s, schema %d): present %v, want %v", n, c, m.feature, m.schema, got, should)
			}
		}
	}
}

// Every number from 17 to 32 (28 is held for an unmerged branch) holds its real migration, once: none is empty,
// each is additive, and what each brings is written by that migration and
// no other (a placeholder where a real migration belongs, a real migration
// repeated, or two numbers swapped, fails here). Applied one at a time to a
// database of the schema before, each brings exactly its own tables and
// columns, on SQLite and (with LAUNCHWAY_TEST_POSTGRES) PostgreSQL.
func TestEachSchemaFrom17HoldsItsRealMigration(t *testing.T) {
	// The schemas from 16 to the last are numbered in a row (one line each is a merge conflict, so this is a list):
	// a new migration adds its constant at the end.
	order := []int{schemaPolicy, schemaBlobs, schemaReports, schemaVerify, schemaIdentity, schemaPools, schemaResources, schemaConfig, schemaFlow, schemaCache, schemaForge, schemaParity2, schemaStandIn28, schemaVariables, schemaMirror, schemaSSH, schemaRollback, schemaPulls, schemaEncryption, schemaCacheTrust, schemaAccounts, schemaQueues, schemaDelivery, schemaArchive, schemaRegistry, schemaToggles,
		schemaEvents,
		schemaReview,
		schemaDebrief,
		schemaStandings,
		schemaReleases,
		schemaGraph,
		schemaConfigAsCode}
	for i := 1; i < len(order); i++ {
		if order[i] != order[i-1]+1 {
			t.Fatalf("schemas are not in a row: the %dth is %d after %d", i+1, order[i], order[i-1])
		}
	}
	if len(migrations) != order[len(order)-1] {
		t.Fatalf("%d migrations, but the last schema is %d", len(migrations), order[len(order)-1])
	}
	create := regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?(\w+)`)
	alter := regexp.MustCompile(`(?i)ALTER TABLE (\w+) ADD COLUMN (\w+)`)
	for _, m := range schemaMarkers {
		mig := migrations[m.schema-1]
		if strings.TrimSpace(mig.sql) == "" {
			t.Errorf("migration %d (%s) is an empty placeholder", m.schema, m.feature)
		}
		if mig.compat != additive {
			t.Errorf("migration %d (%s) is not additive", m.schema, m.feature)
		}
		// Which migrations write each table and column: exactly this one.
		wrote := map[string][]int{}
		for i, other := range migrations {
			for _, g := range create.FindAllStringSubmatch(other.sql, -1) {
				wrote[g[1]] = append(wrote[g[1]], i+1)
			}
			for _, g := range alter.FindAllStringSubmatch(other.sql, -1) {
				wrote[g[1]+"."+g[2]] = append(wrote[g[1]+"."+g[2]], i+1)
			}
		}
		for _, name := range append(append([]string{}, m.tables...), m.columns...) {
			if got := wrote[name]; len(got) != 1 || got[0] != m.schema {
				t.Errorf("%s (%s) is written by migrations %v, want only %d", name, m.feature, got, m.schema)
			}
		}
	}
	// No number after 16 is a placeholder, whatever it brings.
	for i := schemaPolicy; i < len(migrations); i++ {
		if strings.TrimSpace(migrations[i].sql) == "" && i+1 != schemaStandIn28 { // 28 is held for an unmerged branch (schema28.go)
			t.Errorf("migration %d is empty", i+1)
		}
	}

	// Applied one at a time, each brings its own and nothing of the next.
	clk := newClock()
	addr := testdb.New(t)
	for n := schemaPolicy; n <= len(migrations); n++ {
		var buf syncBuffer
		e := mustOpenAs(t, addr, clk, n, &buf)
		info, err := e.Schema(context.Background())
		if err != nil || info.Database != n || info.Code != n || !info.Compatible() {
			t.Fatalf("a build of schema %d: %+v %v", n, info, err)
		}
		checkMarkers(t, e, n)
		e.Close()
	}
}

// A new database reaches the newest schema, applying every migration once,
// in order, and has everything the five features brought.
func TestFreshDatabaseReachesTheNewestSchema(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	var buf syncBuffer
	e := mustOpenAs(t, addr, clk, 0, &buf)
	info, err := e.Schema(context.Background())
	if err != nil || info.Database != len(migrations) || info.Code != len(migrations) || !info.Compatible() {
		t.Fatalf("%+v %v", info, err)
	}
	if info.Minimum != 7 {
		t.Fatalf("minimum code schema %d: every migration since 7 is additive, so it must stay 7", info.Minimum)
	}
	checkMarkers(t, e, len(migrations))
	var applied []string
	for _, m := range regexp.MustCompile(`applied a database migration" schema=(\d+)`).FindAllStringSubmatch(buf.String(), -1) {
		applied = append(applied, m[1])
	}
	var want []string
	for i := 1; i <= len(migrations); i++ {
		want = append(want, fmt.Sprint(i))
	}
	if strings.Join(applied, ",") != strings.Join(want, ",") {
		t.Fatalf("migrations applied: %v, want %v", applied, want)
	}
	// Starting again applies nothing.
	var again syncBuffer
	e2, err := openAs(t, addr, clk, 0, &again)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	if strings.Contains(again.String(), "applied a database migration") {
		t.Fatalf("a second start migrated again:\n%s", again.String())
	}
}

// A database the integration branch's code left, at schema 19 (storage,
// reports and verification merged, runner identities not yet) or 20, is
// migrated to 21 by this build; its runs and runners carry on. Going back,
// builds of 19 and 20 start against the database of 21 (every migration
// since is additive), say so once, finish their own runs, and leave alone a
// run that chooses its runners (which needs 21) until a build of 21 runs it.
func TestUpgradeFromTheIntegrationSchemaAndBack(t *testing.T) {
	const picky = "runner: { labels: [arm64] }\n" + oneStep
	arm := RunnerInfo{ID: "arm", Name: "arm", Features: FeatureArtifacts, Labels: []string{"arm64"}}
	for _, from := range []int{schemaVerify, schemaIdentity} {
		t.Run(fmt.Sprintf("from schema %d", from), func(t *testing.T) {
			clk := newClock()
			addr := testdb.New(t)
			old, err := openAs(t, addr, clk, from, &syncBuffer{})
			if err != nil {
				t.Fatal(err)
			}
			checkMarkers(t, old, from)
			half := create(t, old, linear, Trigger{Event: "manual"})
			ok(t, old, take(t, old), nil) // a runs, b is queued; the runner is on record
			var events int
			if err := old.db.QueryRow(`SELECT count(*) FROM events`).Scan(&events); err != nil || events == 0 {
				t.Fatalf("events %d %v", events, err)
			}
			old.Close()

			// Upgrade.
			var logCur syncBuffer
			cur, err := openAs(t, addr, clk, 0, &logCur)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cur.Close() })
			info, err := cur.Schema(context.Background())
			if err != nil || info.Database != len(migrations) || info.Code != len(migrations) || !info.Compatible() || info.Minimum != 7 {
				t.Fatalf("after the upgrade: %+v %v", info, err)
			}
			for v := from + 1; v <= len(migrations); v++ {
				if !strings.Contains(logCur.String(), fmt.Sprintf(`applied a database migration" schema=%d `, v)) {
					t.Errorf("migration %d was not applied:\n%s", v, logCur.String())
				}
			}
			if strings.Contains(logCur.String(), fmt.Sprintf(`applied a database migration" schema=%d `, from)) {
				t.Errorf("migration %d, already applied, was applied again", from)
			}
			checkMarkers(t, cur, len(migrations))
			var after int
			if err := cur.db.QueryRow(`SELECT count(*) FROM events`).Scan(&after); err != nil || after < events {
				t.Fatalf("events after the upgrade: %d, before %d: %v", after, events, err)
			}
			for !strings.Contains(statuses(t, cur, half)["run"], "succeeded") {
				ok(t, cur, take(t, cur), nil)
			}
			// The runner the older build recorded is listed, as one on a
			// shared token (its new columns have their defaults).
			rs, err := cur.Runners()
			if err != nil {
				t.Fatal(err)
			}
			seen := false
			for _, r := range rs {
				seen = seen || r.ID == runner.ID
			}
			if !seen {
				t.Fatalf("runner %s not listed after the upgrade: %+v", runner.ID, rs)
			}
			chosen := createIn(t, cur, "shop", picky, Trigger{Event: "push"})
			if sum, err := cur.Summary(chosen); err != nil || sum.MinSchema != schemaPools {
				t.Fatalf("a run that chooses runners: min_schema %d %v", sum.MinSchema, err)
			}
			cur.Close()

			// Rollback, to the schema the database came from and then to
			// 19: each older build is given a plain run to take, made by a
			// build of 21 just before.
			for back := from; back >= schemaVerify; back-- {
				newest := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
				plain := createIn(t, newest, "shop", oneStep, Trigger{Event: "push"})
				newest.Close()

				var log syncBuffer
				older := mustOpenAs(t, addr, clk, back, &log)
				if info, err := older.Schema(context.Background()); err != nil || info.Code != back || !info.Compatible() {
					t.Fatalf("the schema-%d build's view: %+v %v", back, info, err)
				}
				if c := strings.Count(log.String(), "running against a database written by a newer version"); c != 1 {
					t.Fatalf("the schema-%d build said %d times that the database is newer:\n%s", back, c, log.String())
				}
				if d, err := older.GetRun(chosen); err != nil || !d.NeedsNewerServer {
					t.Fatalf("the schema-%d build's view of a run that chooses runners: %+v %v", back, d, err)
				}
				if err := older.Cancel(chosen, "ann"); err == nil {
					t.Fatalf("the schema-%d build cancelled a run it does not understand", back)
				}
				if _, err := older.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, picky), Trigger: Trigger{Event: "push"},
					Source: Source{Kind: "dir", Path: "/tmp"}}); err == nil {
					t.Fatalf("the schema-%d build created a run that chooses runners", back)
				}
				j, err := older.Lease(context.Background(), arm, 0)
				if err != nil || j == nil || j.RunID != plain {
					t.Fatalf("the schema-%d build leased %+v %v: it should take the plain run %s", back, j, err, plain)
				}
				if _, err := older.Start(j.LeaseID); err != nil {
					t.Fatal(err)
				}
				if _, err := older.Start(j.LeaseID); err != nil {
					t.Fatal(err)
				}
				ok(t, older, j, nil)
				if j, err := older.Lease(context.Background(), arm, 0); err != nil || j != nil {
					t.Fatalf("the schema-%d build leased the run that chooses runners: %+v %v", back, j, err)
				}
				older.Close()
			}

			// Roll forward: the build of 21 finishes what the older ones left.
			fwd := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
			j := leaseAs(t, fwd, arm)
			for j != nil && j.RunID != chosen {
				ok(t, fwd, j, nil)
				j = leaseAs(t, fwd, arm)
			}
			if j == nil {
				t.Fatal("the build of 21 did not take the run that chooses runners")
			}
			ok(t, fwd, j, nil)
			want(t, statuses(t, fwd, chosen), "run", "succeeded")
		})
	}
}
