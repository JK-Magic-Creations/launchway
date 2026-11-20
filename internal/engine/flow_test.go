package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const outputGate = `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.changed, equals: true}}
  done: {image: x, run: done, needs: fix, when: {upstream: always}}
`

// A step with when: { output: ... } runs when the output matches, and is
// skipped, saying why, when it differs or was never written.
func TestOutputConditionRunsAndSkips(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outputs map[string]string
		runs    bool
		reason  string
	}{
		{"matches", map[string]string{"changed": "true"}, true, ""},
		{"differs", map[string]string{"changed": "false"}, false, `scan.changed was "false", not "true"`},
		{"never written", nil, false, "scan.changed was not set"},
		{"another key written", map[string]string{"other": "true"}, false, "scan.changed was not set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEngine(t)
			id := create(t, e, outputGate, Trigger{Event: "manual"})
			ok(t, e, take(t, e), tc.outputs)
			if tc.runs {
				want(t, statuses(t, e, id), "fix", "queued")
				ok(t, e, take(t, e), nil)
			} else {
				want(t, statuses(t, e, id), "fix", "skipped")
				s := stepOf(t, e, id, "fix")
				if s.SkipKind != "condition" || s.Error != tc.reason {
					t.Fatalf("skip %q: %q, want condition: %q", s.SkipKind, s.Error, tc.reason)
				}
			}
			ok(t, e, take(t, e), nil) // done
			want(t, statuses(t, e, id), "run", "succeeded")
		})
	}
}

// Several tests on one step all have to hold, and a list of values matches
// any of them.
func TestOutputConditionWithOtherKeysAndLists(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.level, equals: [low, medium], branch: main}}
`, Trigger{Event: "push", Branch: "main"})
	ok(t, e, take(t, e), map[string]string{"level": "medium"})
	want(t, statuses(t, e, id), "fix", "queued")
	ok(t, e, take(t, e), nil)
	// On another branch the same output is not enough.
	id2 := create(t, e, `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.level, equals: [low, medium], branch: main}}
`, Trigger{Event: "push", Branch: "dev"})
	ok(t, e, take(t, e), map[string]string{"level": "low"})
	want(t, statuses(t, e, id2), "fix", "skipped", "run", "succeeded")
}

// A re-run treats a step skipped by an output condition as reusable.
func TestOutputConditionSkipIsReusedByARerun(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.changed, equals: true}}
  last: {image: x, run: last, needs: fix, when: {upstream: always}}
`, Trigger{Event: "manual"})
	ok(t, e, take(t, e), map[string]string{"changed": "false"})
	fail(t, e, take(t, e)) // last
	want(t, statuses(t, e, id), "fix", "skipped", "last", "failed")
	r, err := e.Rerun(RerunRequest{Run: id, From: "last", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(t, e, r.ID)
	want(t, got, "scan", "succeeded", "fix", "skipped", "last", "queued")
	if s := stepOf(t, e, r.ID, "fix"); s.SkipKind != "condition" || s.Error != "scan.changed was \"false\", not \"true\"" {
		t.Fatalf("%+v", s)
	}
}

const tolerant = `
pipeline: demo
steps:
  lint: {image: x, run: lint, on_failure: continue}
  build: {image: x, run: build, needs: lint}
  report: {image: x, run: report, needs: lint, when: {upstream: failed}}
`

// A failed step with on_failure: continue leaves the run succeeded, its
// dependents run, and the step is shown as failed (allowed).
func TestAllowedFailureLeavesTheRunSucceeded(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, tolerant, Trigger{Event: "manual"})
	fail(t, e, take(t, e))
	s := stepOf(t, e, id, "lint")
	if s.Status != StepFailed || !s.AllowedFailure || s.OnFailure != "continue" {
		t.Fatalf("%+v", s)
	}
	// build runs as if lint had succeeded; report asked for a failure, and
	// runs like after any failure.
	j1, j2 := take(t, e), take(t, e)
	if got := j1.StepID + "," + j2.StepID; got != "build,report" {
		t.Fatalf("leased %s", got)
	}
	ok(t, e, j1, nil)
	ok(t, e, j2, nil)
	d, _ := e.GetRun(id)
	if d.Status != RunSucceeded {
		t.Fatalf("run %s", d.Status)
	}
	want(t, statuses(t, e, id), "lint", "failed", "build", "succeeded", "report", "succeeded")
}

// A failure that is not tolerated still fails the run, even next to one
// that is, and a tolerant step that is retried is allowed only after its
// last attempt.
func TestAllowedFailureCannotHideARealOne(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  lint: {image: x, run: lint, on_failure: continue, retry: 1}
  test: {image: x, run: test}
  build: {image: x, run: build, needs: [lint, test]}
`, Trigger{Event: "manual"})
	j1, j2 := take(t, e), take(t, e)
	if j1.StepID != "lint" {
		j1, j2 = j2, j1
	}
	fail(t, e, j1) // retried: not allowed yet
	if s := stepOf(t, e, id, "lint"); s.AllowedFailure || s.Status != StepQueuedS {
		t.Fatalf("%+v", s)
	}
	fail(t, e, j2) // test fails for good
	fail(t, e, take(t, e))
	s := stepOf(t, e, id, "lint")
	if !s.AllowedFailure || s.Attempt != 2 {
		t.Fatalf("%+v", s)
	}
	want(t, statuses(t, e, id), "run", "failed", "test", "failed", "build", "skipped")
	if b := stepOf(t, e, id, "build"); !strings.Contains(b.Error, "test failed") {
		t.Fatalf("build was skipped because of: %q", b.Error)
	}
}

// Resume does not run an allowed failure again; a re-run reuses it.
func TestAllowedFailureSurvivesResumeAndRerun(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  lint: {image: x, run: lint, on_failure: continue}
  test: {image: x, run: test}
  build: {image: x, run: build, needs: [lint, test]}
`, Trigger{Event: "manual"})
	j1, j2 := take(t, e), take(t, e)
	if j1.StepID != "lint" {
		j1, j2 = j2, j1
	}
	fail(t, e, j1)
	fail(t, e, j2)
	want(t, statuses(t, e, id), "run", "failed", "lint", "failed", "test", "failed", "build", "skipped")

	plan, err := e.PlanRerun(id, "test")
	if err != nil || len(plan.Reuse) != 1 || plan.Reuse[0] != "lint" {
		t.Fatalf("%+v %v", plan, err)
	}
	re, err := e.Rerun(RerunRequest{Run: id, From: "test", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, re.ID, "lint"); s.Status != StepFailed || !s.AllowedFailure || s.ReusedFrom != id {
		t.Fatalf("a re-run must reuse the allowed failure: %+v", s)
	}
	ok(t, e, take(t, e), nil) // test, in the re-run
	ok(t, e, take(t, e), nil) // build
	if d, _ := e.GetRun(re.ID); d.Status != RunSucceeded {
		t.Fatalf("re-run %s", d.Status)
	}

	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "lint", "failed", "test", "queued")
	if s := stepOf(t, e, id, "lint"); !s.AllowedFailure {
		t.Fatalf("resume must not reset an allowed failure: %+v", s)
	}
	j := take(t, e)
	if j.StepID != "test" || j.RunID != id {
		t.Fatalf("resume ran %s of %s", j.StepID, j.RunID)
	}
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	if d, _ := e.GetRun(id); d.Status != RunSucceeded {
		t.Fatalf("resumed run %s", d.Status)
	}
}

// A server that does not know schema 24 leaves runs that use its keys alone,
// and refuses to create them.
func TestOlderServerLeavesFlowControlRunsAlone(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	cur := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	id := create(t, cur, tolerant, Trigger{Event: "manual"})
	if sum, err := cur.Summary(id); err != nil || sum.MinSchema != schemaFlow {
		t.Fatalf("min_schema %d %v", sum.MinSchema, err)
	}
	gated := create(t, cur, outputGate, Trigger{Event: "manual"})
	if sum, err := cur.Summary(gated); err != nil || sum.MinSchema != schemaFlow {
		t.Fatalf("min_schema %d %v", sum.MinSchema, err)
	}
	cur.Close()

	old := mustOpenAs(t, addr, clk, schemaResources, &syncBuffer{})
	none(t, old) // neither run's steps are leased
	if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d, err)
	}
	if err := old.Cancel(id, "ann"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("an older server changed the run: %v", err)
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, tolerant), Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/tmp"}}); err == nil || !strings.Contains(err.Error(), "flow control") {
		t.Fatalf("%v", err)
	}
	old.Close()

	again := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	if j := take(t, again); j.StepID != "lint" && j.StepID != "scan" {
		t.Fatalf("leased %s", j.StepID)
	}
}

// retryWait is a step that waits between its tries.
const retryWait = `
pipeline: demo
steps:
  flaky: {image: x, run: f, retry: {times: 2, wait: 10s}}
  after: {image: x, run: g, needs: flaky}
`

// A retry with a wait is queued at once but handed out only when its time
// has come, once for each retry.
func TestRetryWaitsBeforeEachRetry(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, retryWait, Trigger{Event: "manual"})
	fail(t, e, take(t, e))
	s := stepOf(t, e, id, "flaky")
	if s.Status != StepQueuedS || s.Attempt != 2 || s.NotBefore != clk.Now().Add(10*time.Second).UnixMilli() || s.FailureCause != "exit" {
		t.Fatalf("%+v", s)
	}
	none(t, e)
	clk.Add(9 * time.Second)
	none(t, e)
	clk.Add(time.Second + time.Millisecond)
	j := take(t, e)
	if j.StepID != "flaky" || j.Attempt != 2 {
		t.Fatalf("%+v", j)
	}
	if s := stepOf(t, e, id, "flaky"); s.NotBefore != 0 {
		t.Fatalf("a step handed out no longer waits: %+v", s)
	}
	fail(t, e, j)
	none(t, e) // another ten seconds
	clk.Add(10 * time.Second)
	fail(t, e, take(t, e)) // the third and last attempt
	want(t, statuses(t, e, id), "run", "failed", "flaky", "failed", "after", "skipped")
}

// A Lease that waits for work looks again when a retry's time comes, on
// SQLite as on PostgreSQL.
func TestWaitingLeaseWakesForARetryThatIsDue(t *testing.T) {
	addr := testdb.New(t)
	e, err := Open(Options{Database: addr, LeaseTTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	create(t, e, "pipeline: demo\nsteps:\n  flaky: {image: x, run: f, retry: {times: 1, wait: 1s}}\n", Trigger{Event: "manual"})
	fail(t, e, take(t, e))
	start := time.Now()
	j, err := e.Lease(context.Background(), runner, 5*time.Second)
	if err != nil || j == nil || j.Attempt != 2 {
		t.Fatalf("%+v %v", j, err)
	}
	if took := time.Since(start); took < 900*time.Millisecond || took > 3*time.Second {
		t.Fatalf("handed out after %v: it should come when the retry is due, 1s", took)
	}
}

func TestRetryBackoffMultipliesTheWait(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  flaky: {image: x, run: f, retry: {times: 3, wait: 10s, backoff: 2}}\n", Trigger{Event: "manual"})
	for i, wait := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second} {
		fail(t, e, take(t, e))
		s := stepOf(t, e, id, "flaky")
		if got := time.UnixMilli(s.NotBefore).Sub(clk.Now()); got != wait {
			t.Fatalf("wait before retry %d: %v, want %v", i+1, got, wait)
		}
		clk.Add(wait)
	}
	fail(t, e, take(t, e))
	want(t, statuses(t, e, id), "run", "failed")
}

// A canceled run does not wait for its retries.
func TestCancelDuringARetryWait(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, retryWait, Trigger{Event: "manual"})
	fail(t, e, take(t, e))
	if err := e.Cancel(id, "ann"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "canceled", "flaky", "canceled")
}

func finishWith(t *testing.T, e *Engine, res Result) {
	t.Helper()
	j := take(t, e)
	if err := e.Finish(j.LeaseID, res); err != nil {
		t.Fatal(err)
	}
}

// retry.on decides which failures are tried again; with none listed every
// cause is, as for a whole number.
func TestRetryOnCauses(t *testing.T) {
	one := 1
	exit := Result{Outcome: "failed", ExitCode: &one, Error: "exit code 1"}
	timeout := Result{Outcome: "failed", Error: "timed out after 1h0m0s"}
	infra := Result{Outcome: "failed", Error: "preparing the workspace: disk full"}
	refused := Result{Outcome: OutcomeRefused, Error: "its accept: section does not allow it"}
	for _, tc := range []struct {
		name    string
		retry   string
		res     Result
		retried bool
	}{
		{"timeout only: a non-zero exit is final", "{times: 2, on: [timeout]}", exit, false},
		{"timeout only: a timeout is retried", "{times: 2, on: [timeout]}", timeout, true},
		{"exit only: a timeout is final", "{times: 2, on: [exit]}", timeout, false},
		{"infrastructure only: the workspace is retried", "{times: 2, on: [infrastructure]}", infra, true},
		{"infrastructure only: an exit is final", "{times: 2, on: [infrastructure]}", exit, false},
		{"no causes: an exit is retried", "{times: 2, wait: 5s}", exit, true},
		{"no causes: a timeout is retried", "2", timeout, true},
		{"no causes: the workspace is retried", "2", infra, true},
		{"no causes: a refusal is final, as it always was", "2", refused, false},
		{"infrastructure listed: a refusal is retried", "{times: 2, on: [infrastructure]}", refused, true},
		{"exit and timeout: a refusal is final", "{times: 2, on: [exit, timeout]}", refused, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEngine(t)
			id := create(t, e, "pipeline: demo\nsteps:\n  s: {image: x, run: f, retry: "+tc.retry+"}\n", Trigger{Event: "manual"})
			finishWith(t, e, tc.res)
			s := stepOf(t, e, id, "s")
			if tc.retried != (s.Status == StepQueuedS) {
				t.Fatalf("status %s, retried should be %v", s.Status, tc.retried)
			}
			if !tc.retried && s.Status != StepFailed {
				t.Fatalf("%+v", s)
			}
		})
	}
}

// What the finish handler reads from a runner's report. Cases it does not
// know count as exit.
func TestFailureCauseTable(t *testing.T) {
	zero := 2
	for _, tc := range []struct {
		res  Result
		want string
	}{
		{Result{Outcome: "failed", ExitCode: &zero, Error: "exit code 2"}, "exit"},
		{Result{Outcome: "failed", Error: "timed out after 30m0s"}, "timeout"},
		{Result{Outcome: "failed", Error: "preparing the workspace: clone failed"}, "infrastructure"},
		{Result{Outcome: "failed", Error: "preparing to fetch artifacts: no space"}, "infrastructure"},
		{Result{Outcome: "failed", Error: "cannot pull alpine:nope: manifest unknown"}, "infrastructure"},
		{Result{Outcome: "failed", Error: "this runner has no image builders configured (images.builders)"}, "infrastructure"},
		{Result{Outcome: "failed", Error: "secret TOKEN (vault:ci/token): the store is unavailable"}, "infrastructure"},
		{Result{Outcome: OutcomeRefused, Error: "not allowed"}, "infrastructure"},
		{Result{Outcome: "failed", Error: "secret TOKEN (vault:ci/token): not found"}, "exit"},
		{Result{Outcome: "failed", Error: "Could not run the step: exec format error"}, "exit"},
		{Result{Outcome: "failed"}, "exit"},
		{Result{Outcome: "failed", Error: "something nobody has seen before"}, "exit"},
	} {
		if got := failureCause(tc.res); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.res, got, tc.want)
		}
	}
}

const timed = `
pipeline: demo
timeout: 1h
steps:
  a: {image: x, run: a}
  b: {image: x, run: b, needs: a}
`

// A run past its timeout is canceled, saying why; before it, nothing
// happens.
func TestRunTimeoutCancelsTheRun(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, timed, Trigger{Event: "manual"})
	sum, _ := e.Summary(id)
	if sum.DeadlineAt != clk.Now().Add(time.Hour).UnixMilli() {
		t.Fatalf("deadline %d", sum.DeadlineAt)
	}
	take(t, e) // a is running
	clk.Add(59 * time.Minute)
	e.expireRunTimeouts()
	want(t, statuses(t, e, id), "run", "running", "a", "running")
	clk.Add(2 * time.Minute)
	e.expireRunTimeouts()
	d, _ := e.GetRun(id)
	if !d.CancelRequested || d.CancelReason != "run timeout of 1h reached" {
		t.Fatalf("%+v", d.RunSummary)
	}
	// The running step is stopped by its runner, like for any cancel; b never starts.
	want(t, statuses(t, e, id), "b", "canceled")
	if cancel, err := e.Heartbeat(take2(t, e, id)); err != nil || !cancel {
		t.Fatalf("the runner should be told to stop: %v %v", cancel, err)
	}
}

// take2 finds the lease of the run's running step.
func take2(t *testing.T, e *Engine, run string) string {
	t.Helper()
	var lease string
	if err := e.db.QueryRow(`SELECT lease_id FROM steps WHERE run_id = ? AND status IN ('leased', 'running')`, run).Scan(&lease); err != nil {
		t.Fatal(err)
	}
	return lease
}

// Two servers on one database cancel an overdue run exactly once.
func TestRunTimeoutIsExpiredByExactlyOneServer(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	e1, e2 := open(t, addr, clk), open(t, addr, clk)
	defer e1.Close()
	defer e2.Close()
	id := create(t, e1, timed, Trigger{Event: "manual"})
	clk.Add(2 * time.Hour)
	var wg sync.WaitGroup
	for _, e := range []*Engine{e1, e2, e1, e2} {
		wg.Add(1)
		go func() { defer wg.Done(); e.expireRunTimeouts() }()
	}
	wg.Wait()
	if n := countEvents(t, e1, id, RunCancelRequested); n != 1 {
		t.Fatalf("%d cancel requests", n)
	}
	want(t, statuses(t, e2, id), "run", "canceled")
}

// Time spent waiting for an approval does not count against the timeout; a
// run that waits for a person is never timed out, and once they decide the
// clock carries on from where it stood.
func TestApprovalWaitDoesNotCountAgainstTheRunTimeout(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, `
pipeline: demo
timeout: 1h
steps:
  a: {image: x, run: a}
  gate: {kind: approval, needs: a, message: 'go?', timeout: 72h}
  b: {image: x, run: b, needs: gate}
`, Trigger{Event: "manual"})
	clk.Add(10 * time.Minute)
	ok(t, e, take(t, e), nil) // gate waits from minute 10
	if sum, _ := e.Summary(id); sum.DeadlineAt != 0 {
		t.Fatalf("a run waiting for a person has no deadline: %d", sum.DeadlineAt)
	}
	clk.Add(30 * time.Hour)
	e.expireRunTimeouts()
	want(t, statuses(t, e, id), "run", "running", "gate", "waiting")
	// A person decides at hour 30 and 10 minutes; the run has used 10 minutes.
	if err := e.Decide(id, "gate", true, "ann", ""); err != nil {
		t.Fatal(err)
	}
	sum, _ := e.Summary(id)
	if want := clk.Now().Add(50 * time.Minute).UnixMilli(); sum.DeadlineAt != want {
		t.Fatalf("deadline %d, want %d (50 minutes left)", sum.DeadlineAt, want)
	}
	clk.Add(49 * time.Minute)
	e.expireRunTimeouts()
	want(t, statuses(t, e, id), "run", "running")
	clk.Add(2 * time.Minute)
	e.expireRunTimeouts()
	want(t, statuses(t, e, id), "run", "canceled", "b", "canceled")
}

// A resumed run's timeout does not run while it was over.
func TestRunTimeoutIgnoresTimeBetweenEndAndResume(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, timed, Trigger{Event: "manual"})
	clk.Add(20 * time.Minute)
	fail(t, e, take(t, e))
	clk.Add(5 * time.Hour)
	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	sum, _ := e.Summary(id)
	if want := clk.Now().Add(40 * time.Minute).UnixMilli(); sum.DeadlineAt != want {
		t.Fatalf("deadline %d, want %d", sum.DeadlineAt, want)
	}
}

// A run its own timeout canceled gets a fresh budget when it is resumed:
// the clock restarts from the resume, so the next sweep does not cancel it
// again.
func TestResumeAfterRunTimeoutGetsAFreshBudget(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, timed, Trigger{Event: "manual"})
	j := take(t, e)
	clk.Add(2 * time.Hour)
	e.expireRunTimeouts()
	if err := e.Finish(j.LeaseID, Result{Outcome: "canceled"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.GetRun(id); d.Status != RunCanceled {
		t.Fatalf("status %s", d.Status)
	}
	clk.Add(3 * time.Hour)
	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	sum, _ := e.Summary(id)
	if want := clk.Now().Add(time.Hour).UnixMilli(); sum.DeadlineAt != want {
		t.Fatalf("deadline %d, want %d (a whole new hour)", sum.DeadlineAt, want)
	}
	e.expireRunTimeouts()
	if d, _ := e.GetRun(id); d.CancelRequested {
		t.Fatal("the resumed run was canceled again straight away")
	}
	clk.Add(61 * time.Minute)
	e.expireRunTimeouts()
	if d, _ := e.GetRun(id); !d.CancelRequested {
		t.Fatal("the fresh budget should run out too")
	}
}

// The installation can cap run timeouts; a pipeline that asks for more is
// refused, naming the limit.
func TestMaxRunTimeoutRefusesALongerTimeout(t *testing.T) {
	e, _ := newEngine(t)
	if l, err := e.RunLimits(); err != nil || l.MaxRunTimeout != "" {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, "pipeline: p\ntimeout: 5h\nsteps:\n  a: {image: x, run: a}\n"),
		Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
		t.Fatalf("no limit set: %v", err)
	}
	before, after, err := e.SetRunLimits(RunLimits{MaxRunTimeout: "4h"}, "ann")
	if err != nil || before.MaxRunTimeout != "" || after.MaxRunTimeout != "4h" || after.UpdatedBy != "ann" {
		t.Fatalf("%+v %+v %v", before, after, err)
	}
	_, err = e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, "pipeline: p\ntimeout: 5h\nsteps:\n  a: {image: x, run: a}\n"),
		Source: Source{Kind: "dir", Path: "/tmp"}})
	var ps pipeline.Problems
	if !errors.As(err, &ps) || !strings.Contains(err.Error(), "max_run_timeout 4h") || ps[0].Line != 2 {
		t.Fatalf("%v", err)
	}
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, "pipeline: p\ntimeout: 4h\nsteps:\n  a: {image: x, run: a}\n"),
		Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"soon", "10s", "800h"} {
		if _, _, err := e.SetRunLimits(RunLimits{MaxRunTimeout: bad}, "ann"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, after, err := e.SetRunLimits(RunLimits{}, "ann"); err != nil || after.MaxRunTimeout != "" {
		t.Fatalf("clearing: %+v %v", after, err)
	}
}

// Old pipelines, and servers that do not know the new keys.
func TestOlderServerLeavesRetryAndTimeoutRunsAlone(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	cur := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	waits := create(t, cur, retryWait, Trigger{Event: "manual"})
	timeout := create(t, cur, timed, Trigger{Event: "manual"})
	plain := create(t, cur, "pipeline: demo\nsteps:\n  a: {image: x, run: a, retry: 2}\n", Trigger{Event: "manual"})
	for id, want := range map[string]int{waits: schemaFlow, timeout: schemaFlow} {
		if sum, err := cur.Summary(id); err != nil || sum.MinSchema != want {
			t.Fatalf("%s: min_schema %d %v", id, sum.MinSchema, err)
		}
	}
	if sum, _ := cur.Summary(plain); sum.MinSchema >= schemaFlow {
		t.Fatalf("retry: 2 must not need schema 24: %d", sum.MinSchema)
	}
	cur.Close()
	old := mustOpenAs(t, addr, clk, schemaResources, &syncBuffer{})
	j := take(t, old)
	if j.RunID != plain {
		t.Fatalf("an older server leased %s of %s, want the plain run's step", j.StepID, j.RunID)
	}
	none(t, old)
	for _, id := range []string{waits, timeout} {
		if err := old.Cancel(id, "ann"); !errors.Is(err, ErrNeedsNewer) {
			t.Fatalf("%v", err)
		}
	}
	// A plain retry of an older server's is exactly as before: at once.
	fail(t, old, j)
	if again := take(t, old); again.RunID != plain || again.Attempt != 2 {
		t.Fatalf("%+v", again)
	}
}

const prInto = `
pipeline: demo
on: { pull_request: { into: [main] } }
steps:
  a: {image: x, run: a}
`

// A pull-request run of a pipeline that filters by target keeps the target
// on the run, exports it to its steps, and older servers leave it alone.
func TestPullRequestIntoOnTheRun(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	trig := prTrigger(12, "c12")
	trig.PullRequestInto = "main"
	id := create(t, e, prInto, trig)
	sum, _ := e.Summary(id)
	if sum.PullRequest != 12 || sum.PullRequestInto != "main" || sum.MinSchema != schemaFlow {
		t.Fatalf("%+v", sum)
	}
	d, _ := e.GetRun(id)
	if d.TriggerDetail == nil || d.TriggerDetail.PullRequestInto != "main" {
		t.Fatalf("%+v", d.TriggerDetail)
	}
	j := take(t, e)
	if j.Env["LAUNCHWAY_PULL_REQUEST_INTO"] != "main" || j.Env["LAUNCHWAY_PULL_REQUEST"] != "12" {
		t.Fatalf("%v", j.Env)
	}
	// Another pull-request run has no target to export.
	other := create(t, e, "pipeline: demo\non: { pull_request: {} }\nsteps:\n  a: {image: x, run: a}\n", prTrigger(13, "c13"))
	if j := take(t, e); j.RunID != other || j.Env["LAUNCHWAY_PULL_REQUEST_INTO"] != "" {
		t.Fatalf("%v", j.Env)
	}
	e.Close()
	old := mustOpenAs(t, addr, clk, schemaResources, &syncBuffer{})
	if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d, err)
	}
}
