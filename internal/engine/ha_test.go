package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// pair opens two engines on one PostgreSQL database: two servers.
func pair(t *testing.T) (a, b *Engine, clk *clock) {
	t.Helper()
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	clk = &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	a, b = open(t, addr, clk), open(t, addr, clk)
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b, clk
}

const fanIn = `
pipeline: demo
steps:
  a: {image: x, run: a}
  b: {image: x, run: b}
  c: {image: x, run: c, needs: [a, b]}
`

// Runs are created through both servers at once, and runners on both
// servers report through either: every step runs exactly once, every run
// succeeds, and run numbers have no gaps.
func TestServersShareWork(t *testing.T) {
	a, b, _ := pair(t)
	servers := []*Engine{a, b}
	const runs = 60
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := servers[i%2].CreateRun(NewRun{Project: "demo", Pipeline: parse(t, fanIn), Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	var mu sync.Mutex
	seen := map[string]int{}
	var finished atomic.Int64
	deadline := time.Now().Add(60 * time.Second)
	for w := 0; w < 12; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			home, other := servers[w%2], servers[(w+1)%2]
			r := RunnerInfo{ID: fmt.Sprintf("r%d", w)}
			for finished.Load() < runs*3 {
				if time.Now().After(deadline) {
					t.Error("the runs did not finish in time")
					return
				}
				j, err := home.Lease(context.Background(), r, 200*time.Millisecond)
				if err != nil {
					t.Error(err)
					return
				}
				if j == nil {
					continue
				}
				mu.Lock()
				seen[j.RunID+"/"+j.StepID]++
				mu.Unlock()
				// Each call goes to whichever server the load balancer picks.
				if _, err := other.Start(j.LeaseID); err != nil {
					t.Error(err)
					return
				}
				if err := home.AppendLogs(j.LeaseID, []LogLine{{Text: "hello"}}); err != nil {
					t.Error(err)
					return
				}
				if err := other.AppendLogs(j.LeaseID, []LogLine{{Text: "again"}}); err != nil {
					t.Error(err)
					return
				}
				if err := other.Finish(j.LeaseID, Result{Outcome: "succeeded"}); err != nil {
					t.Error(err)
					return
				}
				finished.Add(1)
			}
		}(w)
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	if len(seen) != runs*3 {
		t.Fatalf("%d steps ran, want %d", len(seen), runs*3)
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("%s ran %d times", k, n)
		}
	}
	list, err := b.ListRuns(RunFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	nums := []int{}
	for _, r := range list {
		want(t, statuses(t, a, r.ID), "run", "succeeded")
		nums = append(nums, r.Number)
		lines, _, _ := a.Logs(r.ID, "c", 0, 0, 0)
		if len(lines) != 2 || lines[0].Seq != 1 || lines[1].Seq != 2 {
			t.Fatalf("log lines %+v", lines)
		}
	}
	sort.Ints(nums)
	if len(nums) != runs {
		t.Fatalf("%d runs, want %d", len(nums), runs)
	}
	for i, n := range nums {
		if n != i+1 {
			t.Fatalf("run numbers not sequential: %v", nums)
		}
	}
}

// A runner may talk to any server, and keeps working when the server that
// gave it the step goes away.
func TestLeaseWorksThroughAnyServer(t *testing.T) {
	a, b, _ := pair(t)
	id := create(t, a, linear, Trigger{})
	j := take(t, a)
	if _, err := b.Heartbeat(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := b.AppendLogs(j.LeaseID, []LogLine{{Text: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.AppendLogs(j.LeaseID, []LogLine{{Text: "two"}}); err != nil {
		t.Fatal(err)
	}
	ok(t, b, j, nil)
	lines, _, _ := a.Logs(id, "a", 0, 0, 0)
	if len(lines) != 2 || lines[0].Text != "one" || lines[1].Seq != 2 {
		t.Fatalf("logs %+v", lines)
	}

	jb := take(t, a)
	if jb.StepID != "b" {
		t.Fatalf("leased %s", jb.StepID)
	}
	if err := a.Close(); err != nil { // the server that leased the step goes away
		t.Fatal(err)
	}
	if _, err := b.Heartbeat(jb.LeaseID); err != nil {
		t.Fatalf("heartbeat through the surviving server: %v", err)
	}
	if err := b.AppendLogs(jb.LeaseID, []LogLine{{Text: "still here"}}); err != nil {
		t.Fatal(err)
	}
	ok(t, b, jb, nil)
	ok(t, b, take(t, b), nil)
	want(t, statuses(t, b, id), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")
}

// A step queued through one server wakes a runner waiting on another at
// once, not when its periodic recheck comes round.
func TestQueuedStepWakesRunnerOnOtherServer(t *testing.T) {
	a, b, _ := pair(t)
	type result struct {
		job *Job
		at  time.Time
	}
	got := make(chan result, 1)
	go func() {
		j, err := b.Lease(context.Background(), runner, 10*time.Second)
		if err != nil {
			t.Error(err)
		}
		got <- result{j, time.Now()}
	}()
	time.Sleep(200 * time.Millisecond) // b is now waiting
	queued := time.Now()
	create(t, a, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	select {
	case r := <-got:
		if r.job == nil || r.job.StepID != "a" {
			t.Fatalf("got %+v", r.job)
		}
		if d := r.at.Sub(queued); d > 750*time.Millisecond {
			t.Fatalf("the runner on the other server woke after %s", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the runner on the other server was not woken")
	}
}

// Live updates made through one server reach browsers connected to another.
func TestLiveUpdatesReachOtherServer(t *testing.T) {
	a, b, _ := pair(t)
	ch, stop := b.Hub().Subscribe(nil)
	defer stop()
	id := create(t, a, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	j := take(t, a)
	if err := a.AppendLogs(j.LeaseID, []LogLine{{Text: "hi"}}); err != nil {
		t.Fatal(err)
	}
	ok(t, a, j, nil)
	types := map[string]int{}
	var logText string
	var lastRun RunStatus
	timeout := time.After(5 * time.Second)
	for types["step"] == 0 || logText == "" || lastRun != RunSucceeded {
		select {
		case m := <-ch:
			if m.RunID != id {
				t.Fatalf("message for another run: %+v", m)
			}
			types[m.Type]++
			switch d := m.Data.(type) {
			case LogBatch:
				logText = d.Lines[0].Text
			case *RunSummary:
				lastRun = d.Status
			}
		case <-timeout:
			t.Fatalf("missing live updates: %v, log %q, run %s", types, logText, lastRun)
		}
	}
	if logText != "hi" {
		t.Fatalf("log line %q", logText)
	}
}

// Every server reaps; an expired lease is still lost exactly once.
func TestExpiredLeaseIsLostOnce(t *testing.T) {
	a, b, clk := pair(t)
	id := create(t, a, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	j := take(t, a)
	clk.Add(ttl + time.Second)
	var wg sync.WaitGroup
	for _, e := range []*Engine{a, b, a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); e.reap() }()
	}
	wg.Wait()
	evs, _ := a.Events(id)
	lost := 0
	for _, ev := range evs {
		if ev.Type == StepLost {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("%d step.lost events, want 1", lost)
	}
	if _, err := b.Heartbeat(j.LeaseID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("heartbeat on a reaped lease: %v", err)
	}
	want(t, statuses(t, a, id), "a", "queued")
}

// A server joining while another is running leaves leases alone.
func TestJoiningServerDoesNotExtendLeases(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	a := open(t, addr, clk)
	defer a.Close()
	id := create(t, a, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	take(t, a)
	clk.Add(ttl * 3 / 4)
	a.markAlive() // a's reaper ticks
	b := open(t, addr, clk)
	defer b.Close()
	clk.Add(ttl / 2) // past the lease granted by a
	b.reap()
	want(t, statuses(t, b, id), "a", "queued")
}

// After a restart with no server running in between, in-flight steps get a
// fresh lease so a runner that is still working can carry on.
func TestRestartGivesInFlightStepsAFreshLease(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	e := open(t, addr, clk)
	id := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	j := take(t, e)
	e.Close()
	clk.Add(2 * ttl) // the server was down for longer than a lease
	e = open(t, addr, clk)
	defer e.Close()
	e.reap()
	if _, err := e.Heartbeat(j.LeaseID); err != nil {
		t.Fatalf("the lease should survive the restart: %v", err)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "succeeded")
}

// Changes to refs are claimed by exactly one server, and a server whose
// view of the repository is older cannot move a ref back.
func TestPushClaims(t *testing.T) {
	e, _ := newEngine(t)
	const main = "refs/heads/main"
	if err := e.SeedRefs("demo", map[string]string{main: "s1"}, 100); err != nil {
		t.Fatal(err)
	}
	claim := func(ch RefChange) bool {
		t.Helper()
		claimed, err := e.RecordPush("demo", ch, PushDecision{Ref: ch.Ref, SHA: ch.New, Outcome: "ignored", Detail: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return claimed
	}
	if !claim(RefChange{Ref: main, Old: "s1", New: "s2", SeenAt: 200}) {
		t.Fatal("first claim refused")
	}
	if claim(RefChange{Ref: main, Old: "s1", New: "s2", SeenAt: 210}) {
		t.Fatal("the same change was claimed twice")
	}
	if claim(RefChange{Ref: main, Old: "s2", New: "s1", SeenAt: 150}) {
		t.Fatal("a server with an older view moved the ref back")
	}
	if !claim(RefChange{Ref: "refs/heads/new", New: "n1", SeenAt: 220}) {
		t.Fatal("a new ref was not claimed")
	}
	push := &RefChange{Ref: main, Old: "s2", New: "s3", SeenAt: 300}
	nr := NewRun{Project: "demo", Pipeline: parse(t, linear), Push: push}
	r, err := e.CreateRun(nr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateRun(nr); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("second run for the same push: %v", err)
	}
	if runs, _ := e.ListRuns(RunFilter{}); len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	ds, err := e.PushDecisions("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || ds[0].RunID != r.ID || ds[0].Detail != "started run #1" || ds[2].SHA != "s2" {
		t.Fatalf("decisions %+v", ds)
	}
	seen, _ := e.SeenRefs("demo")
	if seen[main] != "s3" || seen["refs/heads/new"] != "n1" {
		t.Fatalf("seen %v", seen)
	}
}

func TestSharedValueIsCreatedOnce(t *testing.T) {
	e, _ := newEngine(t)
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := e.SharedValue("token", fmt.Sprintf("proposal-%d", i))
			if err != nil {
				t.Error(err)
			}
			got[i] = v
		}()
	}
	wg.Wait()
	for _, v := range got {
		if v != got[0] || v == "" {
			t.Fatalf("servers disagree: %v", got)
		}
	}
}

// A database written by the previous version, with a step running, is
// upgraded in place and the run carries on.
func TestUpgradeFromSchema1(t *testing.T) {
	if testdb.Postgres() {
		t.Skip("builds a SQLite file by hand")
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`)
	for _, stmt := range strings.Split(render(false, migrations[0].sql), ";\n") {
		if strings.TrimSpace(stmt) != "" {
			exec(stmt)
		}
	}
	exec(`INSERT INTO meta(key, value) VALUES('schema_version', '1')`)

	// Run 1: step a is running on lease_old; b and c wait for it.
	at := clk.Now().UnixMilli()
	const runID, lease = "run_old", "lease_old"
	trig, src := Trigger{Event: "manual"}, Source{Kind: "dir", Path: "/tmp"}
	for i, ev := range []struct {
		typ EventType
		p   Payload
	}{
		{RunCreated, Payload{Project: "demo", Number: 1, Pipeline: parse(t, linear), Trigger: &trig, Source: &src}},
		{StepQueued, Payload{Step: "a", Attempt: 1}},
		{StepLeased, Payload{Step: "a", Attempt: 1, Lease: lease, Runner: "runner-1"}},
		{StepStarted, Payload{Step: "a", Attempt: 1, Lease: lease}},
	} {
		data, _ := json.Marshal(ev.p)
		exec(`INSERT INTO events(seq, run_id, type, at, data) VALUES(?,?,?,?,?)`, i+1, runID, ev.typ, at, string(data))
	}
	exec(`INSERT INTO runs(id, project, number, pipeline, status, created_at, started_at, last_seq)
		VALUES(?, 'demo', 1, 'demo', 'running', ?, ?, 4)`, runID, at, at)
	exec(`INSERT INTO steps(run_id, step_id, position, status, attempt, queued_at, started_at, runner, lease_id)
		VALUES(?, 'a', 0, 'running', 1, ?, ?, 'runner-1', ?)`, runID, at, at, lease)
	exec(`INSERT INTO steps(run_id, step_id, position, status, attempt) VALUES(?, 'b', 1, 'pending', 0), (?, 'c', 2, 'pending', 0)`, runID, runID)
	exec(`INSERT INTO logs(run_id, step_id, attempt, seq, at, stream, text) VALUES(?, 'a', 1, 1, ?, 'stdout', 'before the upgrade')`, runID, at)
	raw.Close()

	e := open(t, path, clk)
	defer e.Close()
	if _, err := e.Heartbeat(lease); err != nil {
		t.Fatalf("the running step's lease did not survive the upgrade: %v", err)
	}
	if err := e.AppendLogs(lease, []LogLine{{Text: "after the upgrade"}}); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := e.Logs(runID, "a", 0, 0, 0)
	if len(lines) != 2 || lines[1].Seq != 2 {
		t.Fatalf("log sequence broken across the upgrade: %+v", lines)
	}
	ok(t, e, &Job{LeaseID: lease}, nil)
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, runID), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")

	rows, err := e.db.Query(`SELECT run_seq FROM events WHERE run_id = ? ORDER BY seq`, runID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := int64(0)
	for rows.Next() {
		var s int64
		rows.Scan(&s)
		if n++; s != n {
			t.Fatalf("event %d has run_seq %d", n, s)
		}
	}
	if r, _ := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, linear)}); r == nil || r.Number != 2 {
		t.Fatalf("a new run after the upgrade: %+v", r)
	}
}
