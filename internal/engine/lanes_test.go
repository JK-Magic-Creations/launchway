package engine

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func lanePipeline(lane string) string {
	return "pipeline: demo\nlane: " + lane + "\nsteps:\n  a: {image: x, run: a}\n  b: {image: x, run: b, needs: a}\n"
}

// laneOf reads a run's lane block.
func laneOf(t *testing.T, e *Engine, id string) *LaneInfo {
	t.Helper()
	sum, err := e.Summary(id)
	if err != nil {
		t.Fatal(err)
	}
	return sum.Lane
}

func mainPush(sha string) Trigger { return pushTo("main", sha) }

// With when_busy: wait and queue: all, runs go strictly one after another,
// in the order they were created, and a waiting run has no step queued.
func TestLaneWaitAllRunsInTurn(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{when_busy: wait, queue: all}")
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	r3 := create(t, e, src, mainPush("s3"))
	want(t, statuses(t, e, r1), "run", "queued", "a", "queued")
	want(t, statuses(t, e, r2), "run", "queued", "a", "pending")
	want(t, statuses(t, e, r3), "run", "queued", "a", "pending")
	if l := laneOf(t, e, r1); l.State != "holding" || l.Label != "demo" {
		t.Fatalf("%+v", l)
	}
	if l := laneOf(t, e, r2); l.State != "waiting" || l.Position != 1 || l.Behind == nil || l.Behind.Number != 1 ||
		l.Reason != "waiting for lane demo (run #1 is running)" {
		t.Fatalf("%+v", l)
	}
	if l := laneOf(t, e, r3); l.State != "waiting" || l.Position != 2 {
		t.Fatalf("%+v", l)
	}
	lanes, err := e.Lanes("demo")
	if err != nil || len(lanes) != 1 || lanes[0].Holder.ID != r1 || len(lanes[0].Waiting) != 2 || lanes[0].Waiting[0].ID != r2 || lanes[0].Waiting[1].Position != 2 {
		t.Fatalf("%+v %v", lanes, err)
	}
	for _, id := range []string{r1, r2, r3} {
		j := take(t, e)
		if j.RunID != id {
			t.Fatalf("leased a step of %s, want %s", j.RunID, id)
		}
		none(t, e) // only one run goes at a time
		ok(t, e, j, nil)
		j = take(t, e)
		ok(t, e, j, nil)
		want(t, statuses(t, e, id), "run", "succeeded")
	}
	if lanes, _ := e.Lanes("demo"); len(lanes) != 0 {
		t.Fatalf("a lane nobody holds or waits for is not listed: %+v", lanes)
	}
}

// With queue: newest only the newest waiting run starts after the holder
// ends, and the replaced ones say so.
func TestLaneWaitNewestReplacesWaitingRuns(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{when_busy: wait, queue: newest}")
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	r3 := create(t, e, src, mainPush("s3"))
	r4 := create(t, e, src, mainPush("s4"))
	for _, id := range []string{r2, r3} {
		sum, _ := e.Summary(id)
		if sum.Status != RunCanceled || sum.Lane.State != "replaced" || sum.CancelReason == "" || !strings.HasPrefix(sum.CancelReason, "replaced in lane demo by run #") {
			t.Fatalf("%+v %+v", sum, sum.Lane)
		}
	}
	if sum, _ := e.Summary(r3); sum.CancelReason != "replaced in lane demo by run #4" || sum.SupersededByNumber != 4 || sum.Lane.Behind.Number != 4 {
		t.Fatalf("%+v", sum)
	}
	if l := laneOf(t, e, r4); l.State != "waiting" || l.Position != 1 {
		t.Fatalf("%+v", l)
	}
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, r1), "run", "succeeded")
	j := take(t, e)
	if j.RunID != r4 {
		t.Fatalf("after the holder ended, run %s started, want the newest, %s", j.RunID, r4)
	}
	if l := laneOf(t, e, r4); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
}

// with when_busy: cancel the holder and the waiting runs are canceled and
// the new run takes the lane.
func TestLaneCancelTakesTheLane(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{when_busy: cancel}")
	r1 := create(t, e, src, mainPush("s1"))
	j := take(t, e) // r1's a runs
	r2 := create(t, e, src, mainPush("s2"))
	sum, _ := e.Summary(r1)
	if !sum.CancelRequested || sum.CancelReason != "replaced in lane demo by run #2" || sum.SupersededByNumber != 2 || sum.Lane.State != "replaced" {
		t.Fatalf("%+v %+v", sum, sum.Lane)
	}
	if l := laneOf(t, e, r2); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
	want(t, statuses(t, e, r2), "a", "queued")
	if cancel, err := e.Heartbeat(j.LeaseID); err != nil || !cancel {
		t.Fatalf("the runner should be told to stop: %v %v", cancel, err)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "canceled"}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, r1), "run", "canceled")
	// The canceled holder's end does not release the lane that r2 holds.
	if lanes, _ := e.Lanes("demo"); len(lanes) != 1 || lanes[0].Holder.ID != r2 {
		t.Fatalf("%+v", lanes)
	}
	// A third run replaces the second, whatever state it is in.
	r3 := create(t, e, src, mainPush("s3"))
	if sum, _ := e.Summary(r2); !sum.CancelRequested {
		t.Fatalf("%+v", sum)
	}
	if l := laneOf(t, e, r3); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
}

// when_busy: skip creates the run and cancels it at once: it appears in
// lists, says why, and never runs a step.
func TestLaneSkip(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{when_busy: skip}")
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	sum, _ := e.Summary(r2)
	if sum.Status != RunCanceled || sum.CancelReason != "skipped: lane busy (run #1)" || sum.Lane.State != "skipped" || sum.Lane.Behind.Number != 1 {
		t.Fatalf("%+v %+v", sum, sum.Lane)
	}
	want(t, statuses(t, e, r2), "a", "canceled", "b", "canceled")
	if n := countEvents(t, e, r2, StepLeased, StepStarted); n != 0 {
		t.Fatalf("a skipped run ran a step: %d", n)
	}
	// The holder is untouched, and once it is over the lane is free again.
	want(t, statuses(t, e, r1), "run", "queued", "a", "queued")
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	r3 := create(t, e, src, mainPush("s3"))
	if l := laneOf(t, e, r3); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
}

// Lanes are per project, and per ref when the lane says so: runs of other
// refs do not wait for each other.
func TestLanePerRefAndProject(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{per: [ref], when_busy: skip}")
	main := create(t, e, src, mainPush("s1"))
	dev := create(t, e, src, pushTo("dev", "s2"))
	if l := laneOf(t, e, dev); l.State != "holding" || l.Label != "demo/ref dev" {
		t.Fatalf("%+v", l)
	}
	if l := laneOf(t, e, main); l.Label != "demo/ref main" {
		t.Fatalf("%+v", l)
	}
	if sum, _ := e.Summary(create(t, e, src, mainPush("s3"))); sum.Lane.State != "skipped" {
		t.Fatalf("a second run of main: %+v", sum.Lane)
	}
	// The same lane in another project is another lane.
	other := createIn(t, e, "other", src, mainPush("s4"))
	if l := laneOf(t, e, other); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
}

// A lane can be split by an input, and by pull request.
func TestLanePerInputAndPullRequest(t *testing.T) {
	e, _ := newEngine(t)
	src := `
pipeline: demo
inputs:
  target: {type: choice, options: [a, b], default: a}
lane: {per: ["input:target"], when_busy: skip}
steps:
  s: {image: x, run: s}
`
	a := create(t, e, src, Trigger{Event: "manual", Inputs: map[string]string{"target": "a"}})
	b := create(t, e, src, Trigger{Event: "manual", Inputs: map[string]string{"target": "b"}})
	a2 := create(t, e, src, Trigger{Event: "manual", Inputs: map[string]string{"target": "a"}})
	if laneOf(t, e, a).State != "holding" || laneOf(t, e, b).State != "holding" || laneOf(t, e, a2).State != "skipped" {
		t.Fatalf("%+v %+v %+v", laneOf(t, e, a), laneOf(t, e, b), laneOf(t, e, a2))
	}
	pr := "pipeline: demo\non: {pull_request: {}}\nlane: {per: [pull_request], when_busy: skip}\nsteps:\n  s: {image: x, run: s}\n"
	p1 := createPR(t, e, pr, 7, "p1", false)
	p2 := createPR(t, e, pr, 8, "p2", false)
	p1b := createPR(t, e, pr, 7, "p3", false)
	if laneOf(t, e, p1).State != "holding" || laneOf(t, e, p2).State != "holding" || laneOf(t, e, p1b).State != "skipped" {
		t.Fatalf("%+v %+v %+v", laneOf(t, e, p1), laneOf(t, e, p2), laneOf(t, e, p1b))
	}
}

// Draft runs never join a lane, and a re-run neither.
func TestLaneDraftsAndRerunsDoNotJoin(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{when_busy: skip}")
	held := create(t, e, src, mainPush("s1"))
	_ = held
	draft := createDraft(t, e, src, false)
	if sum, _ := e.Summary(draft); sum.Lane != nil || sum.Status != RunQueued {
		t.Fatalf("%+v", sum)
	}
	done := create(t, e, "pipeline: other\nlane: {name: x2}\nsteps:\n  a: {image: x, run: a}\n", pushTo("dev", "d1"))
	ok(t, e, takeRun(t, e, done), nil)
	want(t, statuses(t, e, done), "run", "succeeded")
	// Another run takes the lane x2 while the re-run is made.
	busy := create(t, e, "pipeline: other\nlane: {name: x2}\nsteps:\n  a: {image: x, run: a}\n", pushTo("dev", "d2"))
	re, err := e.Rerun(RerunRequest{Run: done, From: "a", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if re.Lane != nil || re.Status != RunQueued {
		t.Fatalf("a re-run joined a lane: %+v", re.Lane)
	}
	_ = busy
}

// takeRun leases steps until it finds one of the run's.
func takeRun(t *testing.T, e *Engine, run string) *Job {
	t.Helper()
	var back []*Job
	defer func() {
		for _, j := range back {
			e.Release(j.LeaseID)
		}
	}()
	for range 20 {
		j := take(t, e)
		if j.RunID == run {
			return j
		}
		back = append(back, j)
	}
	t.Fatalf("no step of %s", run)
	return nil
}

// Canceling a waiting run takes it out of the line; the others keep their
// places.
func TestLaneCancelingAWaitingRunLeavesTheLine(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{queue: all}")
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	r3 := create(t, e, src, mainPush("s3"))
	if err := e.Cancel(r2, "ann"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, r2), "run", "canceled", "a", "canceled")
	if l := laneOf(t, e, r3); l.Position != 1 {
		t.Fatalf("%+v", l)
	}
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, r1), "run", "succeeded")
	if j := take(t, e); j.RunID != r3 {
		t.Fatalf("%s started, want %s", j.RunID, r3)
	}
}

// A holder that fails or is canceled releases the lane through the normal
// finish path.
func TestLaneIsReleasedWhenTheHolderFailsOrIsCanceled(t *testing.T) {
	for _, how := range []string{"fails", "is canceled"} {
		t.Run(how, func(t *testing.T) {
			e, _ := newEngine(t)
			src := lanePipeline("{queue: all}")
			r1 := create(t, e, src, mainPush("s1"))
			r2 := create(t, e, src, mainPush("s2"))
			if how == "fails" {
				fail(t, e, take(t, e))
			} else if err := e.Cancel(r1, "ann"); err != nil {
				t.Fatal(err)
			}
			want(t, statuses(t, e, r1), "run", map[bool]string{true: "failed", false: "canceled"}[how == "fails"])
			if l := laneOf(t, e, r2); l.State != "holding" {
				t.Fatalf("%+v", l)
			}
			if j := take(t, e); j.RunID != r2 {
				t.Fatalf("%s", j.RunID)
			}
		})
	}
}

// Resuming a run of a lane is refused while another run holds the lane,
// and takes the lane when it is free.
func TestLaneResume(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{queue: all}")
	r1 := create(t, e, src, mainPush("s1"))
	fail(t, e, take(t, e))
	want(t, statuses(t, e, r1), "run", "failed")
	r2 := create(t, e, src, mainPush("s2")) // takes the free lane
	if l := laneOf(t, e, r2); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
	if err := e.Resume(r1, "ann"); err == nil || !strings.Contains(err.Error(), "held by run #2") {
		t.Fatalf("%v", err)
	}
	want(t, statuses(t, e, r1), "run", "failed")
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, r2), "run", "succeeded")
	if err := e.Resume(r1, "ann"); err != nil {
		t.Fatal(err)
	}
	if l := laneOf(t, e, r1); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
	// While it goes, a new run waits for it.
	r3 := create(t, e, src, mainPush("s3"))
	if l := laneOf(t, e, r3); l.State != "waiting" || l.Behind.Number != 1 {
		t.Fatalf("%+v", l)
	}
}

// A lost promotion cannot wedge a lane: the holder is over, the next run
// still waits, and the reaper starts it.
func TestLaneRecoversFromALostPromotion(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{queue: all}")
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	r3 := create(t, e, src, mainPush("s3"))
	// The holder ended, but the transaction that would have started r2 was
	// lost: its row still says it holds the lane.
	if _, err := e.db.Exec(`UPDATE runs SET status = 'succeeded', finished_at = 1 WHERE id = ?`, r1); err != nil {
		t.Fatal(err)
	}
	if l := laneOf(t, e, r2); l.State != "waiting" {
		t.Fatalf("%+v", l)
	}
	e.recoverLanes()
	if l := laneOf(t, e, r2); l.State != "holding" {
		t.Fatalf("the lane stayed wedged: %+v", l)
	}
	if l := laneOf(t, e, r3); l.State != "waiting" || l.Position != 1 {
		t.Fatalf("%+v", l)
	}
	e.recoverLanes() // nothing more to do while r2 goes
	if l := laneOf(t, e, r3); l.State != "waiting" {
		t.Fatalf("%+v", l)
	}
}

// A run that arrives at a lane with runs waiting and no holder (the same
// loss) does not jump the queue.
func TestLaneNewRunDoesNotJumpAWedgedQueue(t *testing.T) {
	e, _ := newEngine(t)
	src := lanePipeline("{queue: all}")
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	if _, err := e.db.Exec(`UPDATE runs SET status = 'succeeded', finished_at = 1 WHERE id = ?`, r1); err != nil {
		t.Fatal(err)
	}
	r3 := create(t, e, src, mainPush("s3"))
	if l := laneOf(t, e, r2); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
	if l := laneOf(t, e, r3); l.State != "waiting" || l.Behind.Number != 2 {
		t.Fatalf("%+v", l)
	}
}

// The time a run waits for its lane does not count against its timeout.
func TestLaneWaitDoesNotCountAgainstTheTimeout(t *testing.T) {
	e, clk := newEngine(t)
	src := "pipeline: demo\ntimeout: 1h\nlane: {queue: all}\nsteps:\n  a: {image: x, run: a}\n"
	r1 := create(t, e, src, mainPush("s1"))
	r2 := create(t, e, src, mainPush("s2"))
	if sum, _ := e.Summary(r2); sum.DeadlineAt != 0 {
		t.Fatalf("a waiting run has no deadline: %d", sum.DeadlineAt)
	}
	clk.Add(3 * time.Hour)
	// r1 times out; r2 is not touched while it waits.
	e.expireRunTimeouts()
	want(t, statuses(t, e, r1), "run", "canceled")
	if l := laneOf(t, e, r2); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
	sum, _ := e.Summary(r2)
	if want := clk.Now().Add(time.Hour).UnixMilli(); sum.DeadlineAt != want {
		t.Fatalf("deadline %d, want %d: the wait is not counted", sum.DeadlineAt, want)
	}
}

// A schedule's firing while its lane is held says whether its run waits or
// was skipped.
func TestLaneScheduleFiringRecordsTheOutcome(t *testing.T) {
	e, _ := newEngine(t)
	for _, tc := range []struct{ lane, outcome, detail string }{
		{"{name: nightly, when_busy: skip}", "skipped", "skipped: lane nightly is busy (run #1)"},
		{"{name: nightly2, when_busy: wait}", "started", "waiting for lane nightly2 (run #3 is running)"},
	} {
		src := "pipeline: demo\nlane: " + tc.lane + "\nsteps:\n  a: {image: x, run: a}\n"
		var held *RunSummary
		var err error
		if held, err = e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, src), Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
			t.Fatal(err)
		}
		_ = held
		r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, src), Trigger: Trigger{Event: "schedule", Schedule: "0 3 * * *"},
			Source: Source{Kind: "dir", Path: "/tmp"}, Fire: &Firing{Key: "k-" + tc.outcome, At: 1000}})
		if err != nil {
			t.Fatal(err)
		}
		var outcome, detail string
		if err := e.db.QueryRow(`SELECT outcome, detail FROM schedule_fires WHERE run_id = ?`, r.ID).Scan(&outcome, &detail); err != nil {
			t.Fatal(err)
		}
		if outcome != tc.outcome || !strings.Contains(detail, tc.detail) {
			t.Fatalf("%s: outcome %q detail %q", tc.lane, outcome, detail)
		}
	}
}

// Two servers on one database take three runs strictly one after another,
// in order (PostgreSQL; one server on SQLite).
func TestLaneRunsInTurnAcrossServers(t *testing.T) {
	var servers []*Engine
	if a, b, _ := pairOrNot(t); b != nil {
		servers = []*Engine{a, b}
	} else {
		servers = []*Engine{a}
	}
	src := lanePipeline("{queue: all}")
	start := make(chan struct{})
	var wg sync.WaitGroup
	ids := make([]string, 6)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // all together
			r, err := servers[i%len(servers)].CreateRun(NewRun{Project: "demo", Pipeline: parse(t, src), Trigger: mainPush(fmt.Sprint(i)),
				Source: Source{Kind: "dir", Path: "/tmp"}})
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = r.ID
		}()
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		return
	}
	numbers := map[string]int{}
	for _, id := range ids {
		sum, _ := servers[0].Summary(id)
		numbers[id] = sum.Number
	}
	// Runs of the lane go one at a time, in order of their numbers: at any
	// moment exactly one run has steps being worked on.
	for want := 1; want <= len(ids); want++ {
		for _, step := range []string{"a", "b"} {
			var jobs []*Job
			for _, e := range servers {
				if j, err := e.Lease(t.Context(), runner, 0); err != nil {
					t.Fatal(err)
				} else if j != nil {
					jobs = append(jobs, j)
				}
			}
			if len(jobs) != 1 || numbers[jobs[0].RunID] != want || jobs[0].StepID != step {
				t.Fatalf("run #%d step %s: leased %d steps: %+v", want, step, len(jobs), jobs)
			}
			if err := servers[len(ids)%len(servers)].Finish(jobs[0].LeaseID, Result{Outcome: "succeeded"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, id := range ids {
		if sum, _ := servers[0].Summary(id); sum.Status != RunSucceeded {
			t.Fatalf("%+v", sum)
		}
	}
}

// An older server does not know lanes: it leaves runs of such pipelines
// alone.
func TestOlderServerLeavesLaneRunsAlone(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	cur := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	id := create(t, cur, lanePipeline("{queue: all}"), mainPush("s1"))
	if sum, _ := cur.Summary(id); sum.MinSchema != schemaFlow {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}
	cur.Close()
	old := mustOpenAs(t, addr, clk, schemaResources, &syncBuffer{})
	none(t, old)
	if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d, err)
	}
}

// With when_busy: cancel, a holder with an open deployment finishes it
// first, and the new run waits for the lane; runs that wait are replaced.
func TestLaneCancelSparesOpenDeployments(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	src := "lane: {when_busy: cancel}\non: { push: { branches: [main] } }\n" + shopPipeline
	a := createShop(t, e, src, 1)
	build(t, e, a, "1")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	waiting := createShop(t, e, src, 2) // a newer push: the deployment of a is open
	want(t, statuses(t, e, a), "run", "running", "deploy-prod", "waiting")
	if sum, _ := e.Summary(a); sum.CancelRequested || sum.Lane.State != "holding" {
		t.Fatalf("the run with an open deployment was canceled: %+v", sum)
	}
	if l := laneOf(t, e, waiting); l.State != "waiting" || l.Behind.Number != 1 {
		t.Fatalf("%+v", l)
	}
	// A third run replaces the waiting one, and waits too.
	third := createShop(t, e, src, 3)
	if sum, _ := e.Summary(waiting); !sum.CancelRequested || sum.CancelReason != "replaced in lane shop by run #3" && !strings.HasPrefix(sum.CancelReason, "replaced in lane") {
		t.Fatalf("%+v", sum)
	}
	if l := laneOf(t, e, third); l.State != "waiting" {
		t.Fatalf("%+v", l)
	}
	// The deployment finishes; the run ends and the lane goes to the third.
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), healthy(true))
	want(t, statuses(t, e, a), "deploy-prod", "succeeded")
	for statuses(t, e, a)["run"] != "succeeded" {
		ok(t, e, take(t, e), nil)
	}
	if l := laneOf(t, e, third); l.State != "holding" {
		t.Fatalf("%+v", l)
	}
}
