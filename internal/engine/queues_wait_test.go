package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const oneStepPipeline = "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n"

func waiting(t *testing.T, e *Engine, j *Job, why string) {
	t.Helper()
	if err := e.Finish(j.LeaseID, Result{Outcome: OutcomeWaiting, Cause: "infrastructure", Error: why, WaitCode: WaitQuotaFull}); err != nil {
		t.Fatal(err)
	}
}

func waitStepView(t *testing.T, e *Engine, run, step string) StepView {
	t.Helper()
	d, err := e.GetRun(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		if s.ID == step {
			return s
		}
	}
	t.Fatalf("no step %s", step)
	return StepView{}
}

// A step its runner could not start for lack of room stays queued, with the
// reason and a stable code, is not handed out before its pause is over, and
// then starts as the same attempt: the run never fails.
func TestStepWaitsForRoomInsteadOfFailing(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, oneStepPipeline, Trigger{})
	j := take(t, e)
	waiting(t, e, j, "waiting for room in the step namespace: 8 CPU of 8 CPU already requested")
	want(t, statuses(t, e, id), "a", "queued", "run", "running")
	s := waitStepView(t, e, id, "a")
	if s.WaitingCode != WaitQuotaFull || !strings.Contains(s.WaitingFor, "waiting for room in the step namespace") || !strings.Contains(s.WaitingFor, "this run is next") {
		t.Fatalf("waiting_for %q code %q", s.WaitingFor, s.WaitingCode)
	}
	none(t, e) // the pause is not over
	clk.Add(11 * time.Second)
	j2 := take(t, e)
	if j2.Attempt != j.Attempt {
		t.Fatalf("attempt %d, want %d: waiting for room is not a failed attempt", j2.Attempt, j.Attempt)
	}
	ok(t, e, j2, nil)
	want(t, statuses(t, e, id), "run", "succeeded")
	if s := waitStepView(t, e, id, "a"); s.WaitingFor != "" {
		t.Fatalf("a finished step still waits for %q", s.WaitingFor)
	}
}

// The pause grows, and a step that has waited longer than queues.max_wait
// fails with the reason and what to change.
func TestWaitForRoomIsBounded(t *testing.T) {
	clk := newClock()
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Queues: QueueSettings{MaxWait: 3 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	id := create(t, e, oneStepPipeline, Trigger{})
	for i := 0; i < 30; i++ {
		clk.Add(61 * time.Second)
		j, err := e.Lease(t.Context(), runner, 0)
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			break
		}
		if _, err := e.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		waiting(t, e, j, "waiting for room in the step namespace: full")
	}
	want(t, statuses(t, e, id), "a", "failed", "run", "failed")
	s := waitStepView(t, e, id, "a")
	for _, w := range []string{"gave up waiting for room after", "queues.max_wait", "full"} {
		if !strings.Contains(s.Reason+s.Error, w) {
			t.Fatalf("missing %q in %q", w, s.Reason+s.Error)
		}
	}
}

// Steps wait in order, and each says who is ahead of it.
func TestWaitingStepsSayWhoIsAhead(t *testing.T) {
	e, _ := newEngine(t)
	first := create(t, e, oneStepPipeline, Trigger{})
	second := create(t, e, oneStepPipeline, Trigger{})
	waiting(t, e, take(t, e), "waiting for room in the step namespace: full")
	waiting(t, e, take(t, e), "waiting for room in the step namespace: full")
	if s := waitStepView(t, e, first, "a"); !strings.Contains(s.WaitingFor, "this run is next") || strings.Contains(s.WaitingFor, "after") {
		t.Fatalf("first: %q", s.WaitingFor)
	}
	if s := waitStepView(t, e, second, "a"); !strings.Contains(s.WaitingFor, "this run is next after #1") {
		t.Fatalf("second: %q", s.WaitingFor)
	}
}

// Run briefs in the list say which steps each step needs.
func TestRunBriefsCarryNeeds(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n  b: {image: x, run: y, needs: a}\n", Trigger{})
	runs, err := e.ListRuns(RunFilter{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("%v %d", err, len(runs))
	}
	got := runs[0].Steps
	if len(got) != 2 || len(got[0].Needs) != 0 || len(got[1].Needs) != 1 || got[1].Needs[0] != "a" {
		t.Fatalf("%+v", got)
	}
}

func TestNumberRanges(t *testing.T) {
	if got := numberRanges([]int{43, 44, 45, 46, 47, 48, 49, 50, 52, 54, 55}); got != "#43–#50, #52, #54, #55" {
		t.Fatal(got)
	}
}

// The step that waited writes log lines on the lease it waited on and more on the next lease of the SAME
// attempt: the second lease's lines follow the first's, and a runner that sends them is not refused (the
// key of the log table is run, step, attempt and line number; a rehearsal on a cluster found the second
// lease's lines colliding with the first's and the result never accepted).
func TestLogLinesOfAStepThatWaitedFollowTheEarlierOnes(t *testing.T) {
	e, clk := newEngine(t)
	id := create(t, e, oneStepPipeline, Trigger{})
	j := take(t, e)
	if err := e.AppendLogsAt(j.LeaseID, 0, []LogLine{{Text: "Starting pod"}, {Text: "waiting for room"}}); err != nil {
		t.Fatal(err)
	}
	waiting(t, e, j, "waiting for room in the step namespace: 3 pods of 3 pods already requested")
	clk.Add(11 * time.Second)
	j2 := take(t, e)
	if err := e.AppendLogsAt(j2.LeaseID, 0, []LogLine{{Text: "Starting pod"}, {Text: "running"}}); err != nil {
		t.Fatalf("the second lease's first lines were refused: %v", err)
	}
	if err := e.AppendLogsAt(j2.LeaseID, 2, []LogLine{{Text: "done"}}); err != nil {
		t.Fatal(err)
	}
	ok(t, e, j2, nil)
	var seqs []int
	rows, err := e.db.Query(`SELECT seq FROM logs WHERE run_id = ? AND step_id = 'a' ORDER BY seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seqs) != 5 || seqs[0] != 1 || seqs[4] != 5 {
		t.Fatalf("the lines of the attempt are numbered %v, want 1 to 5", seqs)
	}
}
