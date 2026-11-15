package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Upgrades that can be rolled back. A database written by this build must
// still serve a build that knows one schema less, as long as every
// migration since is additive: that is what lets a canary, or a rolling
// upgrade, go back to the old version after the new one has migrated.
//
// The "older build" here is this code limited to the migrations and events
// of schema N-1 (Options.schema), as a release built with schemaLimit is.
// When a new migration is added, these tests then check the previous
// release's behaviour against it.

// openAs opens an engine that knows only schemas 1..schema (0: all), with
// its log in buf.
func openAs(t *testing.T, addr string, clk *clock, schema int, buf *syncBuffer) (*Engine, error) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(buf, nil))
	return Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Logger: log, schema: schema})
}

func mustOpenAs(t *testing.T, addr string, clk *clock, schema int, buf *syncBuffer) *Engine {
	t.Helper()
	e, err := openAs(t, addr, clk, schema, buf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestMigrationsDeclareTheirCompatibility(t *testing.T) {
	for i, m := range migrations {
		if m.compat != additive && m.compat != breaking {
			t.Errorf("migration %d has no compatibility class", i+1)
		}
	}
	if migrations[0].compat != breaking {
		t.Error("migration 1 is breaking: there is no older schema")
	}
	// Every event type belongs to a schema this build knows.
	for typ, since := range eventSince {
		if since < 1 || since > len(migrations) {
			t.Errorf("event %s is from schema %d, which is not a migration", typ, since)
		}
	}
	if got := minimumFor(len(migrations)); got != 7 {
		t.Errorf("minimum for the current schema is %d, want 7 (the newest breaking migration)", got)
	}
}

// The heart of it: a database at schema N, written by this build, is
// opened by a build that knows N-1. It starts, says once that the database
// is newer, runs a run end to end, and leaves alone a re-run, whose events
// it does not know, while a server of schema N finishes it.
func TestOlderBuildRunsAgainstNewerAdditiveSchema(t *testing.T) {
	n := len(migrations)
	if migrations[n-1].compat != additive {
		t.Skipf("migration %d is breaking: no older build may run against it", n)
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)

	// This build creates the database and a finished run, and starts a
	// re-run of it from its second step.
	var newLog syncBuffer
	cur := mustOpenAs(t, addr, clk, 0, &newLog)
	orig := create(t, cur, linear, Trigger{Event: "manual"})
	for range 3 {
		ok(t, cur, take(t, cur), map[string]string{"v": "1"})
	}
	re, err := cur.Rerun(RerunRequest{Run: orig, From: "b", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := cur.Schema(context.Background()); info.Database != n || info.Minimum > n-1 {
		t.Fatalf("schema after migrating: %+v", info)
	}

	// The older build starts against it, and says so once.
	var oldLog syncBuffer
	old := mustOpenAs(t, addr, clk, n-1, &oldLog)
	info, err := old.Schema(context.Background())
	if err != nil || info.Code != n-1 || info.Database != n || !info.Compatible() {
		t.Fatalf("older build's view of the schema: %+v, %v", info, err)
	}
	if c := strings.Count(oldLog.String(), "running against a database written by a newer version"); c != 1 {
		t.Fatalf("the older build said %d times that the database is newer:\n%s", c, oldLog.String())
	}

	// It sees the re-run but does not understand it, and leaves it alone.
	d, err := old.GetRun(re.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.NeedsNewerServer || len(d.NotUnderstood) == 0 || !strings.Contains(d.NotUnderstood[0], "step.reused") {
		t.Fatalf("older build's view of the re-run: needs newer %v, not understood %v", d.NeedsNewerServer, d.NotUnderstood)
	}
	if err := old.Cancel(re.ID, "ann"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("older build canceled a run it does not understand: %v", err)
	}
	if _, err := old.Rerun(RerunRequest{Run: orig, From: "a", Actor: "ann"}); err == nil {
		t.Fatal("older build made a re-run, which it does not know")
	}

	// A normal run, end to end, on the older build alone: created, every
	// step leased, started, logged and finished, outputs passed on. It
	// never gets the re-run's queued step.
	id := create(t, old, linear, Trigger{Event: "manual"})
	for _, step := range []string{"a", "b", "c"} {
		j := take(t, old)
		if j.RunID != id || j.StepID != step {
			t.Fatalf("older build leased %s/%s, want %s/%s", j.RunID, j.StepID, id, step)
		}
		if step == "b" && j.Env["LAUNCHWAY_NEEDS_A_OUT"] != "from a" {
			t.Fatalf("b did not receive a's output: %v", j.Env)
		}
		if err := old.AppendLogs(j.LeaseID, []LogLine{{Text: "working on " + step}}); err != nil {
			t.Fatal(err)
		}
		ok(t, old, j, map[string]string{"out": "from " + step})
	}
	none(t, old)
	want(t, statuses(t, old, id), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")
	if lines, _, _ := old.Logs(id, "c", 0, 0, 0); len(lines) != 1 {
		t.Fatalf("logs on the older build: %+v", lines)
	}
	// Inputs are a schema-N feature: the older build records none.
	if s := stepOf(t, old, id, "b"); len(s.Inputs) != 0 {
		t.Fatalf("older build recorded inputs: %+v", s.Inputs)
	}

	// Meanwhile the newer build carries the re-run on, and reads the older
	// build's run as it would its own.
	j := take(t, cur)
	if j.RunID != re.ID || j.StepID != "b" {
		t.Fatalf("newer build leased %s/%s", j.RunID, j.StepID)
	}
	ok(t, cur, j, nil)
	ok(t, cur, take(t, cur), nil)
	want(t, statuses(t, cur, re.ID), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")
	want(t, statuses(t, cur, id), "run", "succeeded")

	// Restarting the older build: it starts again, and says so again.
	old.Close()
	var again syncBuffer
	mustOpenAs(t, addr, clk, n-1, &again)
	if !strings.Contains(again.String(), "running against a database written by a newer version") {
		t.Fatalf("restarted older build: %s", again.String())
	}
}

// A build older than the database's minimum refuses to start, and says
// which schema it needs.
func TestBuildBelowTheMinimumRefuses(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	var buf syncBuffer
	cur := mustOpenAs(t, addr, clk, 0, &buf)
	n := len(migrations)
	minimum := minimumFor(n)

	_, err := openAs(t, addr, clk, minimum-1, &buf)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("needs a Launchway build that knows schema %d or newer; this build knows schema %d", minimum, minimum-1)) {
		t.Fatalf("a build below the minimum: %v", err)
	}

	// A future release applies a breaking migration N+1: this build now
	// refuses too, and a running one reports that it cannot carry on.
	if _, err := cur.db.Exec(`UPDATE meta SET value = ? WHERE key = 'schema_version'`, fmt.Sprint(n+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := cur.db.Exec(`UPDATE meta SET value = ? WHERE key = 'schema_min'`, fmt.Sprint(n+1)); err != nil {
		t.Fatal(err)
	}
	_, err = openAs(t, addr, clk, 0, &buf)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("schema %d or newer", n+1)) {
		t.Fatalf("this build against a breaking schema %d: %v", n+1, err)
	}
	info, err := cur.Schema(context.Background())
	if err != nil || info.Compatible() || !strings.Contains(info.Refusal(), "Run a newer version") {
		t.Fatalf("a running server's view after a breaking migration: %+v, %v", info, err)
	}
}

// A database migrated by builds before schema 8 has no minimum stored; it
// is worked out from the migrations' classes, and stored by the next
// migration.
func TestMinimumOfADatabaseFromBeforeSchema8(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	var buf syncBuffer
	old := mustOpenAs(t, addr, clk, 7, &buf)
	if _, err := old.db.Exec(`DELETE FROM meta WHERE key = 'schema_min'`); err != nil {
		t.Fatal(err)
	}
	if info, err := old.Schema(context.Background()); err != nil || info.Database != 7 || info.Minimum != 7 {
		t.Fatalf("schema 7 without a stored minimum: %+v, %v", info, err)
	}
	old.Close()
	cur := mustOpenAs(t, addr, clk, 0, &buf)
	var v string
	if err := cur.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_min'`).Scan(&v); err != nil || v != "7" {
		t.Fatalf("stored minimum %q, %v", v, err)
	}
}

// Servers starting at the same moment, on an empty database and on one an
// older release left: each migration is applied by exactly one of them,
// while the others wait, and all of them start.
func TestConcurrentStartupsMigrateOnce(t *testing.T) {
	for _, from := range []int{0, 5, len(migrations) - 1} {
		t.Run(fmt.Sprintf("from schema %d", from), func(t *testing.T) {
			clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
			addr := testdb.New(t)
			var buf syncBuffer
			switch {
			case from == len(migrations)-1:
				// The previous release, with a run in flight.
				e := mustOpenAs(t, addr, clk, from, &buf)
				create(t, e, linear, Trigger{Event: "manual"})
				e.Close()
			case from > 0:
				// An older release's schema (this code cannot act as one).
				d, _, err := openDB(addr, 0, from, slog.New(slog.NewTextHandler(&buf, nil)))
				if err != nil {
					t.Fatal(err)
				}
				d.Close()
			}
			const servers = 4
			logs := make([]syncBuffer, servers)
			engines := make([]*Engine, servers)
			errs := make([]error, servers)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range servers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					engines[i], errs[i] = openAs(t, addr, clk, 0, &logs[i])
				}()
			}
			close(start)
			wg.Wait()
			applied := map[string]int{}
			re := regexp.MustCompile(`applied a database migration" schema=(\d+)`)
			for i := range servers {
				if errs[i] != nil {
					t.Fatalf("server %d did not start: %v", i, errs[i])
				}
				defer engines[i].Close()
				for _, m := range re.FindAllStringSubmatch(logs[i].String(), -1) {
					applied[m[1]]++
				}
			}
			for v := from + 1; v <= len(migrations); v++ {
				if applied[fmt.Sprint(v)] != 1 {
					t.Errorf("migration %d was applied %d times, want once (all: %v)", v, applied[fmt.Sprint(v)], applied)
				}
			}
			if len(applied) != len(migrations)-from {
				t.Errorf("migrations applied: %v", applied)
			}
			// They all work: one creates a run, another runs it, and the
			// run left in flight by the older release, if any.
			id := create(t, engines[0], linear, Trigger{Event: "manual"})
			last := engines[servers-1]
			for j := leaseAs(t, last, runner); j != nil; j = leaseAs(t, last, runner) {
				ok(t, last, j, nil)
			}
			runs, err := engines[1].ListRuns(RunFilter{})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range runs {
				want(t, statuses(t, engines[1], r.ID), "run", "succeeded")
			}
			if len(runs) == 0 || runs[0].ID != id {
				t.Fatalf("runs: %+v", runs)
			}
		})
	}
}

// Events and pipelines a newer server wrote that this one does not
// understand: the run is marked as needing a newer server and left alone,
// and every other run carries on. Each case changes one run's log the way
// a newer server might.
func TestRunsThisServerDoesNotUnderstandAreLeftAlone(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(t *testing.T, e *Engine, runID string)
	}{
		{"an unknown event type", "event type step.teleported", func(t *testing.T, e *Engine, runID string) {
			appendRaw(t, e, runID, "step.teleported", `{"step":"a","to":"elsewhere"}`)
		}},
		{"an event that needs a newer schema", "needs schema 99", func(t *testing.T, e *Engine, runID string) {
			appendRaw(t, e, runID, "run.cancel_requested", `{"actor":"x","requires":99,"scope":"steps a"}`)
		}},
		{"an unknown pipeline key", `unknown field "matrix"`, func(t *testing.T, e *Engine, runID string) {
			rewriteCreated(t, e, runID, `"run":"a"`, `"run":"a","matrix":{"os":["linux","mac"]}`)
		}},
		{"an unknown step kind", `step kind "trigger"`, func(t *testing.T, e *Engine, runID string) {
			rewriteCreated(t, e, runID, `"id":"b",`, `"id":"b","kind":"trigger",`)
		}},
		{"an unknown run status", `run status "superseded"`, func(t *testing.T, e *Engine, runID string) {
			appendRaw(t, e, runID, "run.finished", `{"status":"superseded"}`)
		}},
		{"an unknown step outcome", `step outcome "flaky"`, func(t *testing.T, e *Engine, runID string) {
			appendRaw(t, e, runID, "step.finished", `{"step":"a","attempt":1,"outcome":"flaky"}`)
		}},
		{"a step the pipeline does not have", "step zz, which is not in the pipeline", func(t *testing.T, e *Engine, runID string) {
			appendRaw(t, e, runID, "step.queued", `{"step":"zz","attempt":1}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, clk := newEngine(t)
			strange := create(t, e, linear, Trigger{Event: "manual"}) // a is queued
			tc.change(t, e, strange)
			other := create(t, e, linear, Trigger{Event: "manual"})

			d, err := e.GetRun(strange)
			if err != nil {
				t.Fatal(err)
			}
			if !d.NeedsNewerServer || !strings.Contains(strings.Join(d.NotUnderstood, "; "), tc.want) {
				t.Fatalf("not understood: %v, want %q", d.NotUnderstood, tc.want)
			}
			// Nothing changes it...
			for name, err := range map[string]error{
				"cancel": e.Cancel(strange, "x"),
				"resume": e.Resume(strange, "x"),
			} {
				if !errors.Is(err, ErrNeedsNewer) {
					t.Fatalf("%s: %v", name, err)
				}
			}
			if _, err := e.Rerun(RerunRequest{Run: strange, From: "a", Actor: "x"}); !errors.Is(err, ErrNeedsNewer) {
				t.Fatalf("re-run: %v", err)
			}
			before, _ := e.Events(strange)
			// ...while the other run goes from start to end.
			for _, step := range []string{"a", "b", "c"} {
				j := take(t, e)
				if j.RunID != other || j.StepID != step {
					t.Fatalf("leased %s/%s, want %s/%s", j.RunID, j.StepID, other, step)
				}
				ok(t, e, j, nil)
			}
			none(t, e)
			clk.Add(time.Hour)
			e.reap()
			e.expireApprovals()
			want(t, statuses(t, e, other), "run", "succeeded")
			after, _ := e.Events(strange)
			if len(after) != len(before) {
				t.Fatalf("the run was changed: %d events, then %d", len(before), len(after))
			}
		})
	}
}

// A lease held on a run this server does not understand is left for a
// newer server to expire, and the runner's report is refused.
func TestLeasesOfRunsNotUnderstoodAreLeftAlone(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, linear, Trigger{Event: "manual"})
	j := take(t, e)
	appendRaw(t, e, id, "step.teleported", `{"step":"b"}`)
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded"}); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("finish: %v", err)
	}
	// The derived row says the run needs schema 99, as a newer server would
	// have written; the reaper then does not even look at it.
	if _, err := e.db.Exec(`UPDATE runs SET min_schema = 99 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Hour)
	e.reap()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM leases WHERE id = ?`, j.LeaseID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the lease was reaped: %d, %v", n, err)
	}
	runs, _ := e.ListRuns(RunFilter{})
	if len(runs) != 1 || !runs[0].NeedsNewerServer || runs[0].MinSchema != 99 {
		t.Fatalf("listed: %+v", runs[0])
	}
}

// appendRaw appends an event as another server would have written it.
func appendRaw(t *testing.T, e *Engine, runID, typ, data string) {
	t.Helper()
	_, last, err := loadEvents(e.db, runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO events(run_id, run_seq, type, at, data) VALUES(?,?,?,?,?)`,
		runID, last+1, typ, e.now().UnixMilli(), data); err != nil {
		t.Fatal(err)
	}
}

// rewriteCreated changes the pipeline stored in a run's run.created event,
// as if a newer server had written it.
func rewriteCreated(t *testing.T, e *Engine, runID, old, new string) {
	t.Helper()
	var data string
	if err := e.db.QueryRow(`SELECT data FROM events WHERE run_id = ? AND run_seq = 1`, runID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, old) {
		t.Fatalf("%s is not in %s", old, data)
	}
	if _, err := e.db.Exec(`UPDATE events SET data = ? WHERE run_id = ? AND run_seq = 1`, strings.Replace(data, old, new, 1), runID); err != nil {
		t.Fatal(err)
	}
}
