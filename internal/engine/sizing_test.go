package engine

import (
	"testing"
)

func TestLearnMemory(t *testing.T) {
	const Gi = int64(1 << 30)
	cases := []struct {
		name            string
		peaks           []int64
		req, limit, cap int64
		wantOK          bool
		wantReq, wantLm int64
	}{
		{"too few runs", []int64{5 * Gi, 5 * Gi}, 2 * Gi, 6 * Gi, 12 * Gi, false, 0, 0},
		{"within the default: never lowers", []int64{Gi, Gi, Gi, Gi}, 2 * Gi, 6 * Gi, 12 * Gi, false, 0, 0},
		{"p95 plus a quarter", []int64{5 * Gi, 5 * Gi, 5 * Gi}, 2 * Gi, 6 * Gi, 12 * Gi, true, 5*Gi + Gi/4*5, 5*Gi + Gi/4*5},
		{"the limit keeps the default's when it is higher", []int64{3 * Gi, 3 * Gi, 3 * Gi}, 2 * Gi, 6 * Gi, 12 * Gi, true, 3*Gi + 3*Gi/4, 6 * Gi},
		{"capped", []int64{20 * Gi, 20 * Gi, 20 * Gi}, 2 * Gi, 6 * Gi, 12 * Gi, true, 12 * Gi, 12 * Gi},
		{"one spike does not decide a window of twenty", append([]int64{9 * Gi}, rep(3*Gi, 19)...), 2 * Gi, 6 * Gi, 12 * Gi, true, 3*Gi + 3*Gi/4, 6 * Gi},
	}
	for _, c := range cases {
		req, lim, ok := learnMemory(c.peaks, c.req, c.limit, c.cap)
		if ok != c.wantOK || req != c.wantReq || lim != c.wantLm {
			t.Errorf("%s: got %d %d %v, want %d %d %v", c.name, req, lim, ok, c.wantReq, c.wantLm, c.wantOK)
		}
	}
}

func rep(v int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func runOnce(t *testing.T, e *Engine, src string, peak int64) *Job {
	t.Helper()
	create(t, e, src, Trigger{Event: "push", Branch: "main"})
	j := take(t, e)
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Usage: &StepUsage{Measured: true, Method: "cgroup2", MemoryPeak: peak, ReservedMemory: 6 << 30}}); err != nil {
		t.Fatal(err)
	}
	return j
}

const sizingStep = "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n"

// A step that declares no memory is given what it used in earlier runs, with
// headroom, once there are enough of them; the run page says where the
// numbers came from.
func TestStepMemoryIsLearnedFromEarlierRuns(t *testing.T) {
	e, _ := newEngine(t)
	for i := 0; i < 2; i++ {
		j := runOnce(t, e, sizingStep, 5<<30)
		if j.Sizing == nil || j.Sizing.Source != SizingDefault || j.StepContainer.Memory != "2Gi" {
			t.Fatalf("run %d: %+v %+v", i+1, j.Sizing, j.StepContainer)
		}
	}
	runOnce(t, e, sizingStep, 5<<30)
	id := create(t, e, sizingStep, Trigger{Event: "push", Branch: "main"})
	j := take(t, e)
	if j.Sizing == nil || j.Sizing.Source != SizingLearned || j.Sizing.Runs != 3 {
		t.Fatalf("%+v", j.Sizing)
	}
	if got := j.StepContainer; got.Memory != "6400Mi" || got.MemoryLimit != "6400Mi" {
		t.Fatalf("learned memory: %+v", got)
	}
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if u := d.StepList[0].Usage; u == nil || u.Sizing != SizingLearned || u.SizingRuns != 3 || u.SizingText != "learned from 3 runs" {
		t.Fatalf("the run page must say where the numbers came from while the step runs: %+v", u)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Usage: &StepUsage{Measured: true, MemoryPeak: 5 << 30, ReservedMemory: 6400 << 20}}); err != nil {
		t.Fatal(err)
	}
	d, _ = e.GetRun(id)
	if u := d.StepList[0].Usage; u == nil || u.Sizing != SizingLearned || !u.Measured || u.MemoryPeak != 5<<30 {
		t.Fatalf("the source is kept when the attempt ends: %+v", u)
	}
	// A branch of another family has no history of its own.
	create(t, e, sizingStep, Trigger{Event: "push", Branch: "feat/x"})
	if j := take(t, e); j.Sizing.Source != SizingDefault {
		t.Fatalf("another branch family must not use main's history: %+v", j.Sizing)
	}
}

// Numbers written in a file always win, and are labelled so.
func TestDeclaredMemoryIsNeverLearnedOver(t *testing.T) {
	e, _ := newEngine(t)
	src := "pipeline: demo\nsteps:\n  a: {image: x, run: y, resources: {memory: 1Gi}}\n"
	for i := 0; i < 4; i++ {
		runOnce(t, e, src, 5<<30)
	}
	create(t, e, src, Trigger{Event: "push", Branch: "main"})
	j := take(t, e)
	if j.Sizing.Source != SizingDeclared || j.StepContainer.Memory != "1Gi" || j.StepContainer.MemoryLimit != "1Gi" {
		t.Fatalf("%+v %+v", j.Sizing, j.StepContainer)
	}
	// A memory under the container counts as written too.
	src = "pipeline: demo\ncontainers:\n  c: {image: x, memory: 1Gi}\nsteps:\n  a: {container: c, run: y}\n"
	create(t, e, src, Trigger{Event: "push", Branch: "main"})
	if j := take(t, e); j.Sizing.Source != SizingDeclared {
		t.Fatalf("%+v", j.Sizing)
	}
}

// After a kill for memory the next attempt has twice the memory and says so;
// a second kill ends the step; a step that wrote its memory is not retried.
func TestMemoryKillRaisesOnceAndSaysSo(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, sizingStep, Trigger{Event: "push", Branch: "main"})
	j := take(t, e)
	ec := 137
	kill := func(j *Job, had int64) Result {
		return Result{Outcome: "failed", ExitCode: &ec, Error: "the step was killed because it used more memory than its limit",
			Usage: &StepUsage{Measured: true, MemoryPeak: had, ReservedMemory: had}}
	}
	if err := e.Finish(j.LeaseID, kill(j, 6<<30)); err != nil {
		t.Fatal(err)
	}
	j2 := take(t, e)
	if j2.Attempt != 2 || j2.Sizing.Source != SizingRaised || j2.StepContainer.Memory != "12Gi" || j2.StepContainer.MemoryLimit != "12Gi" {
		t.Fatalf("%+v %+v attempt %d", j2.Sizing, j2.StepContainer, j2.Attempt)
	}
	if j2.Sizing.Text == "" {
		t.Fatal("the log line is empty")
	}
	if err := e.Finish(j2.LeaseID, kill(j2, 12<<30)); err != nil {
		t.Fatal(err)
	}
	d, _ := e.GetRun(id)
	if d.Status != "failed" {
		t.Fatalf("a second kill must end the step, not loop: %s", d.Status)
	}
	// A step with its own memory is not retried for memory.
	id = create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y, resources: {memory: 1Gi}}\n", Trigger{Event: "push", Branch: "main"})
	j = take(t, e)
	if err := e.Finish(j.LeaseID, kill(j, 1<<30)); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.GetRun(id); d.Status != "failed" {
		t.Fatalf("declared memory is the file's to change: %s", d.Status)
	}
}

// The services a step lists reach the Station with its job, and a step that says nothing leaves the Station to start every one.
func TestJobCarriesTheServicesAStepLists(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, "pipeline: demo\ncontainers:\n  db: {image: postgres, service: true}\n  go: {image: golang}\nsteps:\n  a: {container: go, run: y, services: []}\n  b: {container: go, run: y, services: [db], needs: a}\n  c: {container: go, run: y, needs: b}\n", Trigger{Event: "manual"})
	for _, want := range []struct {
		scoped bool
		list   []string
	}{{true, nil}, {true, []string{"db"}}, {false, nil}} {
		j := take(t, e)
		if j.ScopedServices != want.scoped || len(j.Services) != len(want.list) {
			t.Fatalf("%s: scoped %v services %v", j.StepID, j.ScopedServices, j.Services)
		}
		if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded"}); err != nil {
			t.Fatal(err)
		}
	}
}

// A runner that makes one pod for the whole run builds it from the run's containers: a container's memory is the
// largest of what its steps would each be given, so what is learned about one step reaches the pod too.
func TestLearnedMemoryReachesTheContainerOfAPodForTheWholeRun(t *testing.T) {
	e, _ := newEngine(t)
	for i := 0; i < 3; i++ {
		runOnce(t, e, sizingStep, 5<<30)
	}
	create(t, e, sizingStep, Trigger{Event: "push", Branch: "main"})
	j := take(t, e)
	if j.Sizing == nil || j.Sizing.Source != SizingLearned {
		t.Fatalf("%+v", j.Sizing)
	}
	var found bool
	for _, c := range j.Containers {
		if !c.Service {
			found = true
			if c.Memory != "6400Mi" || c.MemoryLimit != "6400Mi" {
				t.Errorf("the run's container did not get the learned memory: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("no container for the step")
	}
}
