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

// bound parses a pipeline and binds it to the given input values, as a
// server does before creating a run. It returns the graph and the values.
func bound(t *testing.T, src string, given map[string]string) (*pipeline.Pipeline, map[string]string) {
	t.Helper()
	p := parse(t, src)
	vals, err := pipeline.ResolveInputs(p, given)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pipeline.Bind(p, vals, pipeline.BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return b, vals
}

func createBound(t *testing.T, e *Engine, src string, given map[string]string, trig Trigger) string {
	t.Helper()
	p, vals := bound(t, src, given)
	trig.Inputs = vals
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: trig, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

const deployInputs = `
pipeline: deploy
inputs:
  environment: { type: choice, options: [dev, prod], default: dev }
  version: { required: true, pattern: "^v[0-9]+$" }
  dry_run: { type: boolean, default: true }
  regions: { type: list, default: [eu] }
steps:
  plan:
    image: alpine
    run: echo plan
  apply:
    image: alpine
    needs: plan
    when: { input: dry_run, equals: false }
    run: echo apply
  announce:
    image: alpine
    needs: plan
    when: { input: environment, equals: prod }
    run: echo announce
`

func TestInputsAreRecordedAndReachSteps(t *testing.T) {
	e, _ := newEngine(t)
	id := createBound(t, e, deployInputs, map[string]string{"version": "v7", "dry_run": "false", "regions": "eu, us"}, Trigger{Event: "manual", Actor: "ann"})
	j := take(t, e)
	for k, v := range map[string]string{"LAUNCHWAY_INPUT_VERSION": "v7", "LAUNCHWAY_INPUT_DRY_RUN": "false",
		"LAUNCHWAY_INPUT_ENVIRONMENT": "dev", "LAUNCHWAY_INPUT_REGIONS": `["eu","us"]`} {
		if j.Env[k] != v {
			t.Fatalf("%s = %q, want %q (env %v)", k, j.Env[k], v, j.Env)
		}
	}
	ok(t, e, j, nil)
	// apply runs (dry_run is false); announce does not (environment is dev).
	ok(t, e, take(t, e), nil)
	none(t, e)
	want(t, statuses(t, e, id), "run", "succeeded", "plan", "succeeded", "apply", "succeeded", "announce", "skipped")
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if d.TriggerDetail == nil || d.TriggerDetail.Inputs["version"] != "v7" || d.MinSchema != schemaTriggers {
		t.Fatalf("detail: %+v, min schema %d", d.TriggerDetail, d.MinSchema)
	}
	// What the step was given says where each input came from.
	var from string
	for _, v := range d.StepList[0].Inputs[0].Inputs.Env {
		if v.Name == "LAUNCHWAY_INPUT_VERSION" {
			from = v.From
		}
	}
	if from != FromInput {
		t.Fatalf("recorded origin of an input: %q", from)
	}
}

func TestCreateRunChecksInputValues(t *testing.T) {
	e, _ := newEngine(t)
	p, vals := bound(t, deployInputs, map[string]string{"version": "v1"})
	cases := map[string]map[string]string{
		"missing":       {"environment": "dev", "dry_run": "true", "regions": `["eu"]`},
		"not an option": {"environment": "qa", "version": "v1", "dry_run": "true", "regions": `["eu"]`},
		"pattern":       {"environment": "dev", "version": "1", "dry_run": "true", "regions": `["eu"]`},
		"extra":         {"environment": "dev", "version": "v1", "dry_run": "true", "regions": `["eu"]`, "x": "y"},
		"not canonical": {"environment": "dev", "version": "v1", "dry_run": "true", "regions": "eu"},
	}
	for name, v := range cases {
		_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "manual", Inputs: v}, Source: Source{Kind: "dir", Path: "/x"}})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A pipeline that was not bound is refused.
	raw := parse(t, loopPipeline)
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: raw, Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unbound: %v", err)
	}
	// A run of event schedule must be for a firing, and only it.
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "schedule", Inputs: vals}, Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("schedule without a firing: %v", err)
	}
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "manual", Inputs: vals}, Fire: &Firing{Key: "k", At: 1},
		Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a firing for a manual run: %v", err)
	}
}

// Resume keeps the inputs; a re-run copies them.
func TestResumeAndRerunKeepInputs(t *testing.T) {
	e, _ := newEngine(t)
	id := createBound(t, e, deployInputs, map[string]string{"version": "v3", "dry_run": "false", "environment": "prod"}, Trigger{Event: "manual"})
	ok(t, e, take(t, e), nil) // plan
	fail(t, e, take(t, e))    // apply
	ok(t, e, take(t, e), nil) // announce
	want(t, statuses(t, e, id), "run", "failed", "apply", "failed")
	if err := e.Resume(id, "bob"); err != nil {
		t.Fatal(err)
	}
	j := take(t, e)
	if j.StepID != "apply" || j.Env["LAUNCHWAY_INPUT_VERSION"] != "v3" {
		t.Fatalf("resumed: %s %v", j.StepID, j.Env)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "succeeded")
	re, err := e.Rerun(RerunRequest{Run: id, From: "apply", Actor: "cy"})
	if err != nil {
		t.Fatal(err)
	}
	j = take(t, e)
	if j.RunID != re.ID || j.StepID != "apply" || j.Env["LAUNCHWAY_INPUT_ENVIRONMENT"] != "prod" {
		t.Fatalf("re-run: %s %s %v", j.RunID, j.StepID, j.Env)
	}
	ok(t, e, j, nil)
	if tr, _ := e.RunTrigger(re.ID); tr.Inputs["version"] != "v3" || tr.Actor != "cy" {
		t.Fatalf("re-run's trigger: %+v", tr)
	}
}

const loopPipeline = `
pipeline: matrix
inputs:
  systems: { type: list, default: [debian, alpine] }
steps:
  test:
    for_each:
      python: ["3.12", "3.13"]
      os: { input: systems }
    max_parallel: 2
    fail_fast: false
    image: { each: python, format: "python:%s" }
    run: pytest
  report:
    needs: test
    image: alpine
    run: echo report
`

func TestLoopMembersRunWithinMaxParallel(t *testing.T) {
	e, _ := newEngine(t)
	id := createBound(t, e, loopPipeline, nil, Trigger{Event: "manual"})
	var held []*Job
	for range 2 {
		held = append(held, take(t, e))
	}
	none(t, e) // max_parallel: 2
	st := statuses(t, e, id)
	pending := 0
	for k, v := range st {
		if strings.HasPrefix(k, "test[") && v == "pending" {
			pending++
		}
	}
	if pending != 2 {
		t.Fatalf("members waiting their turn: %v", st)
	}
	j := held[0]
	if j.StepID != "test[python=3.12,os=debian]" || j.Image != "python:3.12" || j.Env["LAUNCHWAY_EACH_PYTHON"] != "3.12" || j.Env["LAUNCHWAY_EACH_OS"] != "debian" {
		t.Fatalf("first member: %s %s %v", j.StepID, j.Image, j.Env)
	}
	// One fails; fail_fast is off, so the others carry on, and the next
	// member takes its place.
	fail(t, e, held[0])
	next := take(t, e)
	ok(t, e, held[1], map[string]string{"cov": "81"})
	ok(t, e, next, nil)
	ok(t, e, take(t, e), nil)
	none(t, e)
	want(t, statuses(t, e, id), "run", "failed", "test[python=3.12,os=debian]", "failed", "test[python=3.13,os=alpine]", "succeeded", "report", "skipped")
	// Resume runs the failed member again, keeping the same IDs, and the
	// step that needs the loop receives each member's outputs by name.
	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil)
	j = take(t, e)
	if j.StepID != "report" || j.Env["LAUNCHWAY_NEEDS_TEST_PYTHON_3_12_OS_ALPINE_COV"] != "81" {
		t.Fatalf("report: %s %v", j.StepID, j.Env)
	}
	ok(t, e, j, nil)
	want(t, statuses(t, e, id), "run", "succeeded")
}

func TestLoopFailFastStopsTheOthers(t *testing.T) {
	e, _ := newEngine(t)
	src := strings.Replace(loopPipeline, "    fail_fast: false\n", "", 1)
	src = strings.Replace(src, "max_parallel: 2", "max_parallel: 3", 1)
	id := createBound(t, e, src, nil, Trigger{Event: "manual"})
	a, b, c := take(t, e), take(t, e), take(t, e)
	_ = c
	fail(t, e, a)
	st := statuses(t, e, id)
	if st[b.StepID] != "canceled" || st[c.StepID] != "canceled" || st["test[python=3.13,os=alpine]"] != "canceled" || st["report"] != "skipped" || st["run"] != "failed" {
		t.Fatalf("after a failure with fail_fast: %v", st)
	}
	// The runners holding the others learn at their next heartbeat, and
	// what they report afterwards is refused.
	if _, err := e.Heartbeat(b.LeaseID); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("heartbeat of a stopped member: %v", err)
	}
	if err := e.Finish(b.LeaseID, Result{Outcome: "succeeded"}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("finish of a stopped member: %v", err)
	}
	d, _ := e.GetRun(id)
	for _, s := range d.StepList {
		if s.ID == b.StepID && !strings.Contains(s.Error, "fail_fast") {
			t.Fatalf("reason: %q", s.Error)
		}
	}
	// Resume runs the failed and the stopped members again.
	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		ok(t, e, take(t, e), nil)
	}
	ok(t, e, take(t, e), nil) // report
	want(t, statuses(t, e, id), "run", "succeeded")
}

// A loop over a list too long for the server's limit fails to bind; the
// engine never sees it. A run with loops is left alone by an older server.
func TestOlderBuildLeavesTriggerRunsAlone(t *testing.T) {
	n := schemaTriggers
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	var newLog, oldLog syncBuffer
	cur := mustOpenAs(t, addr, clk, n, &newLog)
	looped := createBound(t, cur, loopPipeline, nil, Trigger{Event: "manual"})
	withInputs := createBound(t, cur, deployInputs, map[string]string{"version": "v1"}, Trigger{Event: "manual"})
	p, vals := bound(t, linear, nil)
	scheduled, err := cur.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "schedule", Inputs: vals, Schedule: "0 3 * * *"},
		Fire: &Firing{Key: "k1", At: 1}, Source: Source{Kind: "dir", Path: "/x"}})
	if err != nil {
		t.Fatal(err)
	}
	plain := create(t, cur, linear, Trigger{Event: "manual"})
	for _, id := range []string{looped, withInputs, scheduled.ID} {
		if sum, _ := cur.Summary(id); sum.MinSchema != n {
			t.Fatalf("run %s has min_schema %d, want %d", id, sum.MinSchema, n)
		}
	}
	if sum, _ := cur.Summary(plain); sum.MinSchema >= n {
		t.Fatalf("a plain run needs schema %d", sum.MinSchema)
	}

	old := mustOpenAs(t, addr, clk, n-1, &oldLog)
	if !strings.Contains(oldLog.String(), "running against a database written by a newer version") {
		t.Fatalf("older build's log: %s", oldLog.String())
	}
	for _, id := range []string{looped, withInputs, scheduled.ID} {
		d, err := old.GetRun(id)
		if err != nil {
			t.Fatal(err)
		}
		if !d.NeedsNewerServer {
			t.Fatalf("older build understands run %s: %v", id, d.NotUnderstood)
		}
		if err := old.Cancel(id, "x"); !errors.Is(err, ErrNeedsNewer) {
			t.Fatalf("older build cancelled %s: %v", id, err)
		}
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "manual", Inputs: map[string]string{}},
		Source: Source{Kind: "dir", Path: "/x"}}); err != nil {
		t.Fatalf("older build, plain run: %v", err)
	}
	ip, ivals := bound(t, deployInputs, map[string]string{"version": "v2"})
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: ip, Trigger: Trigger{Event: "manual", Inputs: ivals},
		Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("older build made a run with inputs: %v", err)
	}
	if _, _, err := old.ScheduleSeen("demo", "k", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("older build looked at schedules: %v", err)
	}
	// The older build runs the plain runs, never a step of the others.
	for range 6 {
		j := take(t, old)
		if j.RunID == looped || j.RunID == withInputs || j.RunID == scheduled.ID {
			t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
		}
		ok(t, old, j, nil)
	}
	none(t, old)
	// The newer build carries the others on.
	for {
		j, err := cur.Lease(context.Background(), runner, 0)
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		if _, err := cur.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		ok(t, cur, j, nil)
	}
	for _, id := range []string{looped, withInputs, scheduled.ID} {
		want(t, statuses(t, cur, id), "run", "succeeded")
	}
}

// Each firing of a schedule starts one run, however many servers try.
func TestScheduleFiresOnce(t *testing.T) {
	a, b := twoServers(t)
	p, vals := bound(t, linear, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	started, claimed := 0, 0
	for i := range 8 {
		e := a
		if i%2 == 1 {
			e = b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "schedule", Inputs: vals},
				Fire: &Firing{Key: "nightly", At: 1000}, Source: Source{Kind: "dir", Path: "/x"}})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started++
			case errors.Is(err, ErrAlreadyClaimed):
				claimed++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if started != 1 || claimed != 7 {
		t.Fatalf("started %d, already claimed %d", started, claimed)
	}
	// A firing that started nothing is claimed too, once.
	if ok, err := a.RecordFire("demo", Firing{Key: "nightly", At: 1000}, "error", "x"); ok || err != nil {
		t.Fatalf("recording a claimed firing: %v %v", ok, err)
	}
	if ok, err := b.RecordFire("demo", Firing{Key: "nightly", At: 2000}, "error", "the pipeline has problems"); !ok || err != nil {
		t.Fatalf("recording a failed firing: %v %v", ok, err)
	}
	if ok, _ := a.RecordFire("demo", Firing{Key: "nightly", At: 2000}, "error", "again"); ok {
		t.Fatal("recorded a firing twice")
	}
	first, last, err := a.ScheduleSeen("demo", "nightly", 500)
	if err != nil || first != 500 || last != 2000 {
		t.Fatalf("seen: %d %d %v", first, last, err)
	}
	if first, _, _ := b.ScheduleSeen("demo", "nightly", 900); first != 500 {
		t.Fatalf("first seen moved: %d", first)
	}
	fires, err := b.ScheduleFires("demo")
	if err != nil || len(fires) != 2 || fires[0].FireAt != 2000 || fires[1].RunID == "" || fires[1].Outcome != "started" {
		t.Fatalf("fires: %+v %v", fires, err)
	}
}

// A webhook call's idempotency key starts one run; a Git host's delivery
// is seen once.
func TestWebhookDeliveriesAreClaimedOnce(t *testing.T) {
	a, b := twoServers(t)
	p, vals := bound(t, linear, nil)
	mk := func(e *Engine, key string) (*RunSummary, error) {
		return e.CreateRun(NewRun{Project: "demo", Pipeline: p, Trigger: Trigger{Event: "webhook", Webhook: "deploy", Inputs: vals},
			Delivery: &Delivery{Hook: "deploy", Key: key, BodyHash: "h1"}, Source: Source{Kind: "dir", Path: "/x"}})
	}
	r1, err := mk(a, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mk(b, "key-1"); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("second call with the key: %v", err)
	}
	if run, hash, err := b.DeliveryRun("demo", "deploy", "key-1"); err != nil || run != r1.ID || hash != "h1" {
		t.Fatalf("delivery: %s %s %v", run, hash, err)
	}
	if _, _, err := b.DeliveryRun("demo", "deploy", "key-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unused key: %v", err)
	}
	if _, err := mk(b, "key-2"); err != nil {
		t.Fatal(err)
	}
	if fresh, err := a.SeeDelivery("demo", "github", "d-1"); !fresh || err != nil {
		t.Fatalf("first delivery: %v %v", fresh, err)
	}
	if fresh, err := b.SeeDelivery("demo", "github", "d-1"); fresh || err != nil {
		t.Fatalf("replayed delivery: %v %v", fresh, err)
	}
	if fresh, _ := b.SeeDelivery("demo", "gitea", "d-1"); !fresh {
		t.Fatal("deliveries of different hosts are different")
	}
}

// twoServers is two engines sharing a PostgreSQL database, or on SQLite,
// which one server uses alone, the same engine twice.
func twoServers(t *testing.T) (a, b *Engine) {
	t.Helper()
	if testdb.Postgres() {
		a, b, _ = pair(t)
		return a, b
	}
	e, _ := newEngine(t)
	return e, e
}
