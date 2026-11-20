package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// clock is a fake time source the tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const ttl = 30 * time.Second

// open opens an engine on a database from testdb.New. Opening the same
// address again is a restart, or, while the first is open, a second server.
func open(t *testing.T, addr string, clk *clock) *Engine {
	t.Helper()
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func newEngine(t *testing.T) (*Engine, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e := open(t, testdb.New(t), clk)
	t.Cleanup(func() { e.Close() })
	return e, clk
}

func parse(t *testing.T, src string) *pipeline.Pipeline {
	t.Helper()
	p, err := pipeline.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func create(t *testing.T, e *Engine, src string, trig Trigger) string {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, src), Trigger: trig, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

var runner = RunnerInfo{ID: "runner-1", Name: "test", Features: FeatureArtifacts + " " + FeatureCoverage}

// take leases one step (without waiting) and fails the test if there is none.
func take(t *testing.T, e *Engine) *Job {
	t.Helper()
	j, err := e.Lease(context.Background(), runner, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected a queued step, found none")
	}
	if _, err := e.Start(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	return j
}

func none(t *testing.T, e *Engine) {
	t.Helper()
	j, err := e.Lease(context.Background(), runner, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Fatalf("expected no queued step, got %s", j.StepID)
	}
}

func ok(t *testing.T, e *Engine, j *Job, outputs map[string]string) {
	t.Helper()
	zero := 0
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Outputs: outputs}); err != nil {
		t.Fatal(err)
	}
}

func fail(t *testing.T, e *Engine, j *Job) {
	t.Helper()
	one := 1
	if err := e.Finish(j.LeaseID, Result{Outcome: "failed", ExitCode: &one, Error: "exit status 1"}); err != nil {
		t.Fatal(err)
	}
}

// statuses returns "step=status" for every step, plus the run status, and
// checks that the stored tables agree with a fresh fold of the event log.
func statuses(t *testing.T, e *Engine, runID string) map[string]string {
	t.Helper()
	d, err := e.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.State(runID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{"run": string(d.Status)}
	if string(st.Status) != string(d.Status) {
		t.Fatalf("run status: table says %s, events say %s", d.Status, st.Status)
	}
	for _, s := range d.StepList {
		out[s.ID] = string(s.Status)
		fs := st.Steps[s.ID]
		if fs.Status != s.Status || fs.Attempt != s.Attempt {
			t.Fatalf("step %s: table says %s/%d, events say %s/%d", s.ID, s.Status, s.Attempt, fs.Status, fs.Attempt)
		}
	}
	return out
}

func want(t *testing.T, got map[string]string, pairs ...string) {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		if got[pairs[i]] != pairs[i+1] {
			t.Fatalf("%s is %q, want %q (all: %v)", pairs[i], got[pairs[i]], pairs[i+1], got)
		}
	}
}

const linear = `
pipeline: demo
steps:
  a: {image: x, run: a}
  b: {image: x, run: b, needs: a}
  c: {image: x, run: c, needs: b}
`

func TestLinearRunSucceeds(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, linear, Trigger{Event: "manual"})
	want(t, statuses(t, e, id), "run", "queued", "a", "queued", "b", "pending", "c", "pending")
	for _, step := range []string{"a", "b", "c"} {
		j := take(t, e)
		if j.StepID != step {
			t.Fatalf("leased %s, want %s", j.StepID, step)
		}
		none(t, e) // nothing else is ready until this one finishes
		ok(t, e, j, nil)
	}
	want(t, statuses(t, e, id), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")
}

func TestParallelStepsAndJoin(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  lint: {image: x, run: a}
  test: {image: x, run: b}
  build: {image: x, run: c, needs: [lint, test]}
`, Trigger{})
	j1, j2 := take(t, e), take(t, e)
	if got := []string{j1.StepID, j2.StepID}; !(got[0] == "lint" && got[1] == "test") {
		t.Fatalf("leased %v, want lint then test", got)
	}
	none(t, e)
	ok(t, e, j2, nil)
	none(t, e) // build still waits for lint
	ok(t, e, j1, nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded")
}

func TestFailureSkipsDependents(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, linear, Trigger{})
	fail(t, e, take(t, e))
	got := statuses(t, e, id)
	want(t, got, "run", "failed", "a", "failed", "b", "skipped", "c", "skipped")
	d, _ := e.GetRun(id)
	if d.StepList[1].SkipKind != "upstream" {
		t.Fatalf("skip kind %q", d.StepList[1].SkipKind)
	}
}

func TestRetry(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  flaky: {image: x, run: y, retry: 2}\n", Trigger{})
	fail(t, e, take(t, e))
	fail(t, e, take(t, e))
	j := take(t, e)
	if j.Attempt != 3 {
		t.Fatalf("attempt %d, want 3", j.Attempt)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "succeeded")

	id2 := create(t, e, "pipeline: demo\nsteps:\n  flaky: {image: x, run: y, retry: 1}\n", Trigger{})
	fail(t, e, take(t, e))
	fail(t, e, take(t, e))
	none(t, e)
	want(t, statuses(t, e, id2), "run", "failed")
}

func TestConditions(t *testing.T) {
	e, _ := newEngine(t)
	src := `
pipeline: demo
steps:
  build: {image: x, run: a}
  deploy: {image: x, run: b, needs: build, when: {branch: main}}
  notify: {image: x, run: c, needs: deploy}
`
	id := create(t, e, src, Trigger{Event: "push", Branch: "feature/x"})
	ok(t, e, take(t, e), nil)
	got := statuses(t, e, id)
	want(t, got, "run", "succeeded", "deploy", "skipped", "notify", "skipped")

	id = create(t, e, src, Trigger{Event: "push", Branch: "main"})
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded", "deploy", "succeeded", "notify", "succeeded")
}

func TestOutputsReachDependents(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, linear, Trigger{Event: "push", Branch: "main", SHA: "abc123"})
	ok(t, e, take(t, e), map[string]string{"image-digest": "sha256:1"})
	j := take(t, e)
	if j.Env["LAUNCHWAY_NEEDS_A_IMAGE_DIGEST"] != "sha256:1" {
		t.Fatalf("output not passed on: %v", j.Env)
	}
	if j.Env["LAUNCHWAY_COMMIT"] != "abc123" || j.Env["LAUNCHWAY_BRANCH"] != "main" || j.Env["CI"] != "true" {
		t.Fatalf("standard variables missing: %v", j.Env)
	}
}

func TestLostRunnerIsRequeuedThenGivenUp(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	for attempt := 1; attempt <= 4; attempt++ {
		j := take(t, e)
		if j.Attempt != attempt {
			t.Fatalf("attempt %d, want %d", j.Attempt, attempt)
		}
		// A heartbeat inside the lease period keeps it alive.
		clk.Add(ttl / 2)
		e.reap()
		if _, err := e.Heartbeat(j.LeaseID); err != nil {
			t.Fatalf("heartbeat inside the lease: %v", err)
		}
		clk.Add(ttl + time.Second)
		e.reap()
		if _, err := e.Heartbeat(j.LeaseID); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("heartbeat after expiry: %v", err)
		}
		if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded"}); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("a runner that lost its lease must not be able to finish the step: %v", err)
		}
	}
	none(t, e)
	got := statuses(t, e, id)
	want(t, got, "run", "failed", "a", "failed")
}

// The core durability test: the server stops in the middle of a run.
func TestCrashRecovery(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	path := testdb.New(t)

	e := open(t, path, clk)
	id := create(t, e, linear, Trigger{})
	ok(t, e, take(t, e), map[string]string{"k": "v"}) // a done
	jb := take(t, e)                                  // b running
	if err := e.AppendLogs(jb.LeaseID, []LogLine{{Text: "before the crash"}}); err != nil {
		t.Fatal(err)
	}
	e.Close() // the server goes away

	// Case 1: the runner was elsewhere and is still alive. It carries on with
	// the same lease, and its logs continue in sequence.
	e = open(t, path, clk)
	if _, err := e.Heartbeat(jb.LeaseID); err != nil {
		t.Fatalf("a live runner's lease should survive a server restart: %v", err)
	}
	if err := e.AppendLogs(jb.LeaseID, []LogLine{{Text: "after the restart"}}); err != nil {
		t.Fatal(err)
	}
	lines, _, _ := e.Logs(id, "b", 0, 0, 0)
	if len(lines) != 2 || lines[1].Seq != 2 {
		t.Fatalf("log sequence broken across restart: %+v", lines)
	}
	ok(t, e, jb, nil)
	jc := take(t, e) // c running
	e.Close()

	// Case 2: the runner died with the server. Its lease expires and the
	// step runs again; a and b are not repeated.
	e = open(t, path, clk)
	defer e.Close()
	none(t, e)
	clk.Add(ttl + time.Second)
	e.reap()
	j := take(t, e)
	if j.StepID != "c" || j.Attempt != 2 {
		t.Fatalf("got %s attempt %d, want c attempt 2", j.StepID, j.Attempt)
	}
	if j.Env["LAUNCHWAY_NEEDS_A_K"] != "" {
		t.Fatalf("c does not need a, so a's outputs should not be passed")
	}
	if err := e.Finish(jc.LeaseID, Result{Outcome: "succeeded"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("the old lease must be dead: %v", err)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "succeeded", "a", "succeeded", "b", "succeeded", "c", "succeeded")

	// a ran exactly once across all of that.
	evs, _ := e.Events(id)
	leasedA := 0
	for _, ev := range evs {
		if ev.Type == StepLeased && ev.Data.Step == "a" {
			leasedA++
		}
	}
	if leasedA != 1 {
		t.Fatalf("a was leased %d times, want 1", leasedA)
	}
}

func TestResumeFromFailedStep(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, linear, Trigger{})
	ok(t, e, take(t, e), nil)
	fail(t, e, take(t, e))
	want(t, statuses(t, e, id), "run", "failed", "a", "succeeded", "b", "failed", "c", "skipped")

	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "running", "a", "succeeded", "b", "queued", "c", "pending")
	j := take(t, e)
	if j.StepID != "b" || j.Attempt != 2 {
		t.Fatalf("got %s attempt %d, want b attempt 2", j.StepID, j.Attempt)
	}
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded")

	if err := e.Resume(id, "dana"); !errors.Is(err, ErrConflict) {
		t.Fatalf("resuming a succeeded run: %v", err)
	}
	if err := e.Resume("run_nope", "dana"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resuming a missing run: %v", err)
	}
}

func TestCancel(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  a: {image: x, run: a}
  b: {image: x, run: b}
  c: {image: x, run: c, needs: [a, b]}
`, Trigger{})
	ja := take(t, e) // a running, b queued
	if err := e.Cancel(id, "dana"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "running", "a", "running", "b", "canceled", "c", "canceled")
	none(t, e)
	cancel, err := e.Heartbeat(ja.LeaseID)
	if err != nil || !cancel {
		t.Fatalf("the runner should be told to stop: cancel=%v err=%v", cancel, err)
	}
	if err := e.Finish(ja.LeaseID, Result{Outcome: "canceled"}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "canceled", "a", "canceled")
	if err := e.Cancel(id, "dana"); !errors.Is(err, ErrConflict) {
		t.Fatalf("canceling a finished run: %v", err)
	}

	// A canceled run can be resumed.
	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded")
}

func TestRunnerStoppingWithoutCancelRequeues(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	j := take(t, e)
	if err := e.Finish(j.LeaseID, Result{Outcome: "canceled", Error: "runner shutting down"}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "a", "queued")
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded")
}

func TestLongPollWakesWhenWorkArrives(t *testing.T) {
	e, _ := newEngine(t)
	got := make(chan *Job)
	go func() {
		j, _ := e.Lease(context.Background(), runner, 5*time.Second)
		got <- j
	}()
	time.Sleep(50 * time.Millisecond)
	create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	select {
	case j := <-got:
		if j == nil || j.StepID != "a" {
			t.Fatalf("got %+v", j)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting runner was not woken")
	}
}

// Many runners competing for many steps: every step runs exactly once.
func TestConcurrentRunnersNeverShareAStep(t *testing.T) {
	e, _ := newEngine(t)
	const runs = 25
	for i := 0; i < runs; i++ {
		create(t, e, `
pipeline: demo
steps:
  a: {image: x, run: a}
  b: {image: x, run: b}
  c: {image: x, run: c, needs: [a, b]}
`, Trigger{})
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := RunnerInfo{ID: fmt.Sprintf("r%d", w)}
			for {
				j, err := e.Lease(context.Background(), r, 200*time.Millisecond)
				if err != nil {
					t.Error(err)
					return
				}
				if j == nil {
					return
				}
				mu.Lock()
				seen[j.RunID+"/"+j.StepID]++
				mu.Unlock()
				if _, err := e.Start(j.LeaseID); err != nil {
					t.Error(err)
					return
				}
				if err := e.AppendLogs(j.LeaseID, []LogLine{{Text: "hello"}}); err != nil {
					t.Error(err)
					return
				}
				if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded"}); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if len(seen) != runs*3 {
		t.Fatalf("%d steps ran, want %d", len(seen), runs*3)
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("%s ran %d times", k, n)
		}
	}
	list, _ := e.ListRuns(RunFilter{Limit: 100})
	for _, r := range list {
		if r.Status != RunSucceeded {
			t.Fatalf("run %d is %s", r.Number, r.Status)
		}
	}
	nums := make([]int, len(list))
	for i, r := range list {
		nums[i] = r.Number
	}
	sort.Ints(nums)
	for i, n := range nums {
		if n != i+1 {
			t.Fatalf("run numbers not sequential: %v", nums)
		}
	}
}

func TestLiveUpdatesArePublished(t *testing.T) {
	e, _ := newEngine(t)
	ch, stop := e.Hub().Subscribe(nil)
	defer stop()
	create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	j := take(t, e)
	_ = e.AppendLogs(j.LeaseID, []LogLine{{Text: "hi"}})
	ok(t, e, j, nil)
	types := map[string]int{}
	timeout := time.After(time.Second)
	for types["run"] == 0 || types["step"] == 0 || types["log"] == 0 {
		select {
		case m := <-ch:
			types[m.Type]++
		case <-timeout:
			t.Fatalf("missing live updates: %v", types)
		}
	}
}

func TestReleaseRequeuesSameAttempt(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	j, _ := e.Lease(context.Background(), runner, 0)
	if err := e.Release(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	j2 := take(t, e)
	if j2.Attempt != 1 {
		t.Fatalf("attempt %d, want 1: a handed-back step was never tried", j2.Attempt)
	}
	ok(t, e, j2, nil)
	want(t, statuses(t, e, id), "run", "succeeded")
}

func TestLeaseStopsWaitingWhenCallerGoesAway(t *testing.T) {
	e, _ := newEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { _, err := e.Lease(ctx, runner, time.Minute); done <- err }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Lease kept waiting after its caller left")
	}
}

func TestLostOutcomeRequeues(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: demo\ncontainers:\n  m: {image: mvn, env: {A: from-container, B: from-container}}\nsteps:\n  a: {image: x, run: y}\n  b: {container: m, run: y, needs: a, env: {B: from-step}}\n", Trigger{})
	j := take(t, e)
	if len(j.Containers) != 2 || j.Containers[0].Name != "m" || j.Containers[1].Name != "image-1" || j.Container != "image-1" {
		t.Fatalf("containers %+v, container %s", j.Containers, j.Container)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "lost", Error: "pod disappeared"}); err != nil {
		t.Fatal(err)
	}
	j2 := take(t, e)
	if j2.StepID != "a" || j2.Attempt != 2 {
		t.Fatalf("got %s attempt %d", j2.StepID, j2.Attempt)
	}
	ok(t, e, j2, nil)
	jb := take(t, e)
	if jb.Container != "m" || jb.Env["A"] != "from-container" || jb.Env["B"] != "from-step" {
		t.Fatalf("container env not merged under step env: %s %v", jb.Container, jb.Env)
	}
	infos, _ := e.RunInfos([]string{id, "run_missing"})
	if len(infos) != 1 || infos[0].ID != id || infos[0].Status != RunRunning {
		t.Fatalf("infos %+v", infos)
	}
}
